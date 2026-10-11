#!/usr/bin/env python3
"""Golden AMI root bootstrap. UserData carries only a configuration parameter name."""
import argparse
import json
import os
import pathlib
import subprocess
import time
import urllib.request

def aws_parameter(name):
    p=subprocess.run(['/usr/bin/aws','ssm','get-parameter','--name',name,'--with-decryption','--output','json'],check=True,capture_output=True,text=True,timeout=20)
    return json.loads(p.stdout)['Parameter']['Value']

def main():
    if os.geteuid()!=0:
        raise RuntimeError('root required')
    parser=argparse.ArgumentParser();parser.add_argument('--config-parameter',required=True);args=parser.parse_args()
    # IMDSv2 is available only to root through the Golden AMI firewall.
    req=urllib.request.Request('http://169.254.169.254/latest/api/token',method='PUT',headers={'X-aws-ec2-metadata-token-ttl-seconds':'60'})
    with urllib.request.urlopen(req,timeout=3) as r:token=r.read().decode()
    req=urllib.request.Request('http://169.254.169.254/latest/meta-data/instance-id',headers={'X-aws-ec2-metadata-token':token})
    with urllib.request.urlopen(req,timeout=3) as r:iid=r.read().decode()
    req=urllib.request.Request('http://169.254.169.254/latest/dynamic/instance-identity/document',headers={'X-aws-ec2-metadata-token':token})
    with urllib.request.urlopen(req,timeout=3) as r:os.environ['AWS_DEFAULT_REGION']=json.load(r)['region']
    config=json.loads(aws_parameter(args.config_parameter))
    if not config['portal_url'].startswith('https://') or not config['credential_prefix'].startswith('/'):
        raise ValueError('approved HTTPS configuration required')
    instance=subprocess.run(['/usr/bin/aws','ec2','describe-instances','--instance-ids',iid,'--output','json'],check=True,capture_output=True,text=True,timeout=20)
    data=json.loads(instance.stdout)['Reservations'][0]['Instances'][0]
    work=next((b for b in data.get('BlockDeviceMappings',[]) if b['DeviceName']=='/dev/sdf'),None)
    if work is not None:
        if not work['Ebs']['DeleteOnTermination']:
            raise RuntimeError('scratch must be transient')
        volume=work['Ebs']['VolumeId'].replace('-','')
        device=pathlib.Path('/dev/disk/by-id')/('nvme-Amazon_Elastic_Block_Store_'+volume)
        if not device.exists():raise RuntimeError('approved scratch device unavailable')
        probe=subprocess.run(['/usr/sbin/blkid','-o','value','-s','TYPE',str(device)],capture_output=True,text=True)
        if probe.returncode==2:subprocess.run(['/usr/sbin/mkfs.xfs',str(device)],check=True,capture_output=True)
        elif probe.returncode!=0 or probe.stdout.strip()!='xfs':raise RuntimeError('unexpected scratch filesystem')
        pathlib.Path('/work-local').mkdir(exist_ok=True)
        mounted=subprocess.run(['/usr/bin/findmnt','--mountpoint','/work-local'],capture_output=True)
        if mounted.returncode!=0:subprocess.run(['/usr/bin/mount','-o','nodev,nosuid,prjquota',str(device),'/work-local'],check=True)
        cache=pathlib.Path('/work-local/.dataset-cache');cache.mkdir(mode=0o755,exist_ok=True)
        pathlib.Path('/var/cache/awsportal-datasets').mkdir(mode=0o755,exist_ok=True)
        if subprocess.run(['/usr/bin/findmnt','--mountpoint','/var/cache/awsportal-datasets'],capture_output=True).returncode!=0:
            subprocess.run(['/usr/bin/mount','--bind',str(cache),'/var/cache/awsportal-datasets'],check=True)
    root=pathlib.Path('/etc/awsportal-dcv');root.mkdir(mode=0o700,exist_ok=True)
    for attempt in range(240):
        try:
            credential=aws_parameter(config['credential_prefix']+'/'+iid+'/credential')
            if len(credential)!=64 or any(c not in '0123456789abcdef' for c in credential):
                raise ValueError('invalid machine credential')
            value={'portal_url':config['portal_url'],'instance_id':iid,'token':credential,'ca_file':config.get('ca_file')}
            temporary=root/'config.pending';fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
            with os.fdopen(fd,'w') as f:json.dump(value,f);f.flush();os.fsync(f.fileno())
            os.replace(temporary,root/'config.json')
            subprocess.run(['/usr/bin/systemctl','enable','--now','awsportal-dcv-agent'],check=True)
            return
        except subprocess.SubprocessError:
            time.sleep(5)
    raise RuntimeError('machine enrollment timed out; resource retained')
if __name__=='__main__':main()
