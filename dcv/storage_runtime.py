"""Root Agent storage integration; downloads run independently of heartbeats."""
import grp
import json
import os
import pathlib
import threading
from storage import cache_dataset, scratch_quota

class StorageRuntime:
    def __init__(self, agent):
        self.agent = agent
        self.lock = threading.Lock()
        self.results = {}
        self.active = set()
        self.groups = []
        self.du = {}
        self.du_active = set()
        state = agent.root / 'cache-results.json'
        if state.exists():
            self.results = json.loads(state.read_text())

    @property
    def busy(self):
        with self.lock:
            return bool(self.active or self.du_active)

    def snapshot(self):
        with self.lock:
            return list(self.results.values())

    def apply(self, groups, jobs, accounts):
        self.groups = groups
        run = self.agent.run
        for g in groups:
            gid = 2000000 + g['group_id']
            if not 1 <= g['group_id'] <= 1000000 or g['gid'] != gid:
                raise ValueError('invalid group GID')
            name = 'awp-g' + str(g['group_id'])
            try:
                local = grp.getgrnam(name)
                if local.gr_gid != gid:
                    raise RuntimeError('group GID collision')
            except KeyError:
                run(['/usr/sbin/groupadd','--gid',str(gid),name])
            mount = pathlib.Path('/srv/projects') / name
            mount.mkdir(parents=True, exist_ok=True)
            if mount.is_symlink():
                raise RuntimeError('group mount symlink')
            marker = self.agent.root / (name+'.mount.json')
            identity = {k:g[k] for k in ('efs_id','access_point_id','revision','gid')}
            found = run(['/usr/bin/findmnt','--mountpoint',str(mount),'-n','-o','FSTYPE'],optional=True)
            if found.returncode == 0:
                if found.stdout.strip() != 'nfs4' or not marker.exists() or json.loads(marker.read_text()) != identity:
                    raise RuntimeError('group mount identity changed; drain required')
            else:
                import re
                if not re.fullmatch(r'fs-[a-f0-9]{8,17}',g['efs_id']) or not re.fullmatch(r'fsap-[a-f0-9]{8,17}',g['access_point_id']):
                    raise ValueError('invalid group EFS identity')
                run(['/usr/bin/mount','-t','efs','-o','tls,iam,accesspoint='+g['access_point_id'],g['efs_id']+':/',str(mount)])
                self.agent.write_policy(marker,json.dumps(identity,sort_keys=True))
            if mount.stat().st_gid != gid or mount.stat().st_mode & 7:
                raise RuntimeError('group mount permissions mismatch')
            for directory in ('projects','common','small-datasets'):
                run(['/usr/bin/setpriv','--reuid',str(gid),'--regid',str(gid),'--clear-groups','/usr/bin/mkdir','-p',str(mount/directory)])
                run(['/usr/bin/setpriv','--reuid',str(gid),'--regid',str(gid),'--clear-groups','/usr/bin/chmod','2770',str(mount/directory)])
        wanted = {a['user_id']:[] for a in accounts}
        for g in groups:
            for uid in g['members']:
                if uid in wanted:
                    wanted[uid].append('awp-g'+str(g['group_id']))
        for name,a in self.agent.managed.items():
            self.agent.check_account(name,a['user_id'])
            keep = [g.gr_name for g in grp.getgrall() if name in g.gr_mem and not g.gr_name.startswith('awp-g')]
            run(['/usr/sbin/usermod','--groups',','.join(keep+wanted.get(a['user_id'],[])),name])
        for a in accounts:
            if groups:
                scratch_quota('/work-local',a['user_id'],min(g['scratch_gib'] for g in groups),run=lambda args,**kw:run(args))
        for job in jobs:
            with self.lock:
                if job['id'] in self.active or self.results.get(job['id'],{}).get('state') in ('AVAILABLE','FAILED'):
                    continue
                self.active.add(job['id'])
            threading.Thread(target=self.download,args=(job,),daemon=True).start()

    def acknowledge(self, results):
        with self.lock:
            for result in results:
                if result['state'] in ('AVAILABLE','FAILED') and self.results.get(result['id'])==result:
                    del self.results[result['id']]
            self.agent.write_policy(self.agent.root/'cache-results.json',json.dumps(self.results,sort_keys=True))

    def measure_homes(self, accounts):
        import time
        for a in accounts:
            uid=a['user_id']
            if a.get('home') is None:
                continue
            with self.lock:
                if uid in self.du_active or self.du.get(uid,{}).get('observed_at',0)>time.time()-3600:
                    continue
                self.du_active.add(uid)
            threading.Thread(target=self.measure_home,args=(uid,),daemon=True).start()

    def measure_home(self, uid):
        import time
        value={'observed_at':int(time.time()),'quality':'unavailable'}
        try:
            result=self.agent.run(['/usr/bin/setpriv','--reuid',str(200000+uid),'--regid',str(200000+uid),'--clear-groups','/usr/bin/du','-sx','--block-size=1','/home/awp-u'+str(uid)])
            size=int(result.stdout.split()[0])
            if size<0:raise ValueError('invalid HOME du')
            value['du_bytes']=size
            value['quality']='ok'
        except Exception:
            pass
        finally:
            with self.lock:
                self.du[uid]=value
                self.du_active.discard(uid)

    def home_measurement(self, uid):
        with self.lock:
            return dict(self.du.get(uid,{'quality':'unavailable'}))

    def membership(self, name, user_id):
        wanted=['awp-g'+str(g['group_id']) for g in self.groups if user_id in g['members']]
        keep=[g.gr_name for g in grp.getgrall() if name in g.gr_mem and not g.gr_name.startswith('awp-g')]
        self.agent.run(['/usr/sbin/usermod','--groups',','.join(keep+wanted),name])
        if self.groups:
            scratch_quota('/work-local',user_id,min(g['scratch_gib'] for g in self.groups),run=lambda args,**kw:self.agent.run(args))

    def download(self, job):
        result = {'id':job['id'],'state':'FAILED','error':'cache preparation failed; immutable source retained'}
        try:
            import re
            if not re.fullmatch(r'[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]',job['bucket']) or '..' in job['prefix']:
                raise ValueError('invalid dataset S3 source')
            def fetch(file,path):
                import subprocess
                subprocess.run(['/usr/bin/aws','s3api','get-object','--bucket',job['bucket'],'--key',job['prefix']+file['path'],'--version-id',file['version_id'],str(path)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=24*3600)
            cache_dataset('/var/cache/awsportal-datasets',job['dataset_id'],job['manifest'],fetch)
            result={'id':job['id'],'state':'AVAILABLE','error':''}
        except Exception:
            pass
        finally:
            with self.lock:
                self.results[job['id']]=result
                self.active.discard(job['id'])
                self.agent.write_policy(self.agent.root/'cache-results.json',json.dumps(self.results,sort_keys=True))

    def drain(self):
        if self.busy:
            return False
        for marker in self.agent.root.glob('awp-g*.mount.json'):
            mount='/srv/projects/'+marker.name.removesuffix('.mount.json')
            found=self.agent.run(['/usr/bin/findmnt','--mountpoint',mount],optional=True)
            if found.returncode==0:
                self.agent.run(['/usr/bin/sync','-f',mount])
                self.agent.run(['/usr/bin/umount',mount])
            if self.agent.run(['/usr/bin/findmnt','--mountpoint',mount],optional=True).returncode==0:
                return False
        return True
