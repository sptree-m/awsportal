import importlib.util
import json
import pathlib
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("dcv_agent", pathlib.Path(__file__).with_name("agent.py"))
agent = importlib.util.module_from_spec(spec)
spec.loader.exec_module(agent)


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.users = {}
        self.sessions = {}
        self.commands = []
        self.a = agent.Agent({"portal_url": "https://portal.example", "instance_id": "i-a", "token": "a" * 64},
                             self.tmp.name, self.run_command, policy_dir=self.tmp.name)
        self.a.allowed = agent.DEFAULT_ALLOWED.copy()
        patch.object(self.a, "validate_enforcement").start()
        self.addCleanup(patch.stopall)
        patch.object(agent.pwd, "getpwnam", side_effect=self.get_user).start()
        patch.object(agent.os, "chmod").start()

    def get_user(self, name):
        if name not in self.users:
            raise KeyError(name)
        return self.users[name]

    def run_command(self, args, optional=False):
        self.commands.append(args)
        status, output = (1 if args[0].endswith("findmnt") else 0), ""
        if args[0].endswith("useradd"):
            name = args[-1]
            uid = int(args[args.index("--uid") + 1])
            self.users[name] = types.SimpleNamespace(pw_uid=uid, pw_gecos="awsportal:" + name[5:], pw_dir="/home/" + name)
        if args[0].endswith("dcv"):
            if args[1] == "create-session":
                name = args[-1]
                self.sessions[name] = {"owner": "root", "type": "virtual", "x11-display": ":1", "x11-authority":"/run/user/"+str(200000+int(name[5:]))+"/dcv/"+name+".xauth"}
            if args[1] == "describe-session":
                session = self.sessions.get(args[2])
                status = 0 if session else 1
                output = json.dumps(session)
            if args[1] == "close-session":
                self.sessions.pop(args[2])
        return subprocess.CompletedProcess(args, status, output, "")

    def account(self, uid):
        return {"user_id": uid, "username": "alice", "os_user": agent.identity(uid), "session_id": agent.identity(uid)}

    def test_group_membership_precedes_user_manager_and_desktop(self):
        self.a.storage.groups = [{'group_id': 1}]
        with patch.object(self.a.storage, 'membership', side_effect=lambda name, uid: self.commands.append(['membership', name])):
            ready, error = self.a.reconcile([self.account(1)])
        self.assertEqual(ready, [1])
        self.assertFalse(error)
        membership = next(i for i, c in enumerate(self.commands) if c[0] == 'membership')
        manager = next(i for i, c in enumerate(self.commands) if c[0].endswith('systemctl') and c[1] == 'start')
        desktop = next(i for i, c in enumerate(self.commands) if c[0].endswith('dcv') and c[1] == 'create-session')
        self.assertLess(membership, manager)
        self.assertLess(manager, desktop)

    def test_create_idempotency_revoke_and_restore(self):
        ready, error = self.a.reconcile([self.account(1)])
        self.assertEqual(ready, [1])
        self.assertFalse(error)
        self.a.reconcile([self.account(1)])
        self.assertEqual(sum(c[0].endswith("useradd") for c in self.commands), 1)
        self.assertEqual(sum(c[1] == "create-session" for c in self.commands if c[0].endswith("dcv")), 1)
        self.a.reconcile([])
        self.assertFalse(self.sessions)
        self.assertTrue(any("/usr/sbin/nologin" in c for c in self.commands))
        self.assertTrue(any(c[0].endswith("pkill") for c in self.commands))
        self.assertIn("awp-u1", self.a.managed)  # Home/UID mapping remains.
        self.a.reconcile([self.account(1)])
        self.assertIn("awp-u1", self.sessions)

    def test_reject_invalid_identity_without_local_commands(self):
        account = self.account(1)
        account["os_user"] = "root; touch /tmp/pwned"
        with self.assertRaises(ValueError):
            self.a.reconcile([account])
        self.assertFalse(self.commands)
        for uid in (0, -1, True, "1", 1000001):
            with self.assertRaises(ValueError):
                agent.identity(uid)

    def test_never_take_over_existing_unmanaged_user(self):
        self.users["awp-u1"] = types.SimpleNamespace(pw_uid=0, pw_gecos="root", pw_dir="/root")
        ready, error = self.a.reconcile([self.account(1)])
        self.assertFalse(ready)
        self.assertTrue(error)
        self.assertFalse(self.commands)

    def test_wrong_session_owner_is_not_reported_ready(self):
        self.sessions["awp-u1"] = {"owner": "intruder", "type": "virtual", "x11-display": ":1"}
        ready, error = self.a.reconcile([self.account(1)])
        self.assertFalse(ready)
        self.assertTrue(error)

    def test_failure_does_not_report_success(self):
        manifest = {"instance_id": "i-a", "accounts": [self.account(1)], "policy":{"revision":1,"allowed":agent.DEFAULT_ALLOWED}}
        reports = []
        def request(path, body=None, *args):
            if path.endswith("state"):
                return json.dumps(manifest).encode()
            reports.append(json.loads(body))
            return b""
        self.a.request = request
        self.a.sync()
        self.assertEqual(reports[0]["ready_users"], [1])
        self.a.request = lambda *args: (_ for _ in ()).throw(OSError("offline"))
        self.a.last_ok -= 100
        self.a.sync()
        self.assertFalse(self.sessions)

    def test_wrong_instance_manifest_not_applied(self):
        self.a.request = lambda *args: json.dumps({"instance_id": "i-b", "accounts": [self.account(1)], "policy":{"revision":1,"allowed":agent.DEFAULT_ALLOWED}}).encode()
        self.a.sync()
        self.assertFalse(self.commands)

    def test_one_broken_account_does_not_prevent_other_revocations(self):
        self.a.reconcile([self.account(1), self.account(2)])
        # Simulate an operator modifying one account outside the agent.
        self.users["awp-u1"].pw_gecos = "unmanaged"
        ready, error = self.a.reconcile([])
        self.assertFalse(ready)
        self.assertTrue(error)
        self.assertNotIn("awp-u2", self.sessions)

    def test_single_sync_reports_failure(self):
        self.a.request = lambda *args: (_ for _ in ()).throw(OSError("offline"))
        self.assertFalse(self.a.sync())

    def test_broker_rejects_wrong_identity_and_never_follows_redirect(self):
        self.a.request = lambda *args: b'<auth result="yes"><username>root</username></auth>'
        with self.assertRaises(ValueError):
            self.a.authenticate(b"sessionId=awp-u1&authenticationToken=test")
        with self.assertRaises(ValueError):
            self.a.authenticate(b"sessionId=root&authenticationToken=test")
        self.assertIsNone(agent.NoRedirect().redirect_request(None, None, None, None, None, None))

    def test_policy_is_global_deny_and_root_owned_session_with_user_desktop(self):
        self.a.apply_policy({"revision":1,"allowed":agent.DEFAULT_ALLOWED})
        ready,error=self.a.reconcile([self.account(1)])
        self.assertEqual(ready,[1]); self.assertFalse(error)
        baseline=pathlib.Path(self.tmp.name,"enforced.perm").read_text()
        self.assertIn("%any% deny",baseline)
        for code in ("screenshot","clipboard-copy","file-download","extensions-server","unsupervised-access"):
            self.assertIn(code,baseline)
        permissions=pathlib.Path(self.tmp.name,"awp-u1.perm").read_text()
        self.assertIn("awp-u1 allow",permissions)
        create=next(c for c in self.commands if c[0].endswith("dcv") and c[1]=="create-session")
        self.assertEqual(create[create.index("--owner")+1],"root")
        self.assertEqual(create[create.index("--user")+1],"awp-u1")
        self.assertEqual(create[create.index("--storage-root")+1],"/home/awp-u1")
        self.assertTrue(any(c[1]=="set-permissions" for c in self.commands if c[0].endswith("dcv")))

    def test_policy_change_closes_old_sessions_before_baseline_and_recreates(self):
        self.a.apply_policy({"revision":1,"allowed":agent.DEFAULT_ALLOWED})
        self.a.reconcile([self.account(1)])
        self.a.apply_policy({"revision":2,"allowed":["display"]})
        self.assertFalse(self.sessions)
        self.a.reconcile([self.account(1)])
        self.assertIn("awp-u1",self.sessions)
        baseline=pathlib.Path(self.tmp.name,"enforced.perm").read_text()
        self.assertIn("keyboard",baseline)
        self.assertEqual(self.a.applied_revision,2)

    def test_reject_alias_injection_and_clipboard_capture_bypass(self):
        for allowed in (["builtin"],["screenshot\n%any% allow builtin"],["clipboard-copy"],["keyboard-sas"],["display","display"],["unsupervised-access"]):
            with self.assertRaises(ValueError): self.a.apply_policy({"revision":1,"allowed":allowed})
        self.assertFalse(self.commands)

    def test_failed_policy_update_never_reports_ready_and_closes_old_sessions(self):
        self.a.reconcile([self.account(1)])
        self.a.validate_enforcement=lambda: (_ for _ in ()).throw(RuntimeError("missing global baseline"))
        reports=[]
        def request(path,body=None,*args):
            if path.endswith("state"):return json.dumps({"instance_id":"i-a","accounts":[self.account(1)],"policy":{"revision":1,"allowed":agent.DEFAULT_ALLOWED}}).encode()
            reports.append(json.loads(body)); return b""
        self.a.request=request
        self.assertFalse(self.a.sync()); self.assertFalse(self.sessions)
        self.assertEqual(reports[-1]["applied_revision"],0)
        self.assertEqual(reports[-1]["ready_users"],[])

    def test_migrates_only_known_legacy_user_owned_session(self):
        self.a.reconcile([self.account(1)])
        self.sessions["awp-u1"]["owner"]="awp-u1"
        self.a.apply_policy({"revision":1,"allowed":agent.DEFAULT_ALLOWED})
        self.assertFalse(self.sessions)
        self.a.reconcile([self.account(1)])
        self.assertEqual(self.sessions["awp-u1"]["owner"],"root")

    def test_enforcement_rejects_writable_configuration_and_missing_baseline(self):
        import stat
        config=pathlib.Path(self.tmp.name,"dcv.conf")
        self.a.dcv_config=config
        config.write_text('[security]\nauth-token-verifier="http://127.0.0.1:8444"\nallowed-ws-origin-regex="^$"\n[session-management/defaults]\npermissions-file="'+str(pathlib.Path(self.tmp.name,"enforced.perm"))+'"\n')
        with patch.object(pathlib.Path,"lstat",return_value=types.SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG | 0o644)):
            agent.Agent.validate_enforcement(self.a)
        with patch.object(pathlib.Path,"lstat",return_value=types.SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG | 0o666)):
            with self.assertRaises(RuntimeError):agent.Agent.validate_enforcement(self.a)
        config.write_text('[security]\nauth-token-verifier="http://127.0.0.1:8444"\nallowed-ws-origin-regex="^$"\n[session-management/defaults]\npermissions-file="/tmp/user.perm"\n')
        with patch.object(pathlib.Path,"lstat",return_value=types.SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG | 0o644)):
            with self.assertRaises(RuntimeError):agent.Agent.validate_enforcement(self.a)

    def test_rejects_root_owned_session_with_other_os_desktop_user(self):
        self.a.reconcile([self.account(1)])
        self.sessions["awp-u1"]["x11-authority"]="/run/user/200002/dcv/awp-u1.xauth"
        ready,error=self.a.reconcile([self.account(1)])
        self.assertFalse(ready);self.assertTrue(error)

    def test_browser_origin_guard_and_viewer_removal_are_required(self):
        import stat
        config=pathlib.Path(self.tmp.name,"dcv.conf");self.a.dcv_config=config
        text='[security]\nauth-token-verifier="http://127.0.0.1:8444"\nallowed-ws-origin-regex="^$"\n[session-management/defaults]\npermissions-file="'+str(pathlib.Path(self.tmp.name,"enforced.perm"))+'"\n'
        config.write_text(text.replace('allowed-ws-origin-regex="^$"','allowed-ws-origin-regex="^https://.+$"'))
        with patch.object(pathlib.Path,"lstat",return_value=types.SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG | 0o644)):
            with self.assertRaises(RuntimeError):agent.Agent.validate_enforcement(self.a)
        config.write_text(text)
        self.a.run=lambda *args,**kwargs:subprocess.CompletedProcess([],0,"installed","")
        with patch.object(pathlib.Path,"lstat",return_value=types.SimpleNamespace(st_uid=0,st_mode=stat.S_IFREG | 0o644)):
            with self.assertRaises(RuntimeError):agent.Agent.validate_enforcement(self.a)

    def test_requires_https_and_safe_origin(self):
        for url in ("http://portal.example", "https://user:pass@portal.example", "https://portal.example/path"):
            with self.assertRaises(ValueError):
                agent.Agent({"portal_url": url, "instance_id": "i-a", "token": "a" * 64}, self.tmp.name)


if __name__ == "__main__":
    unittest.main()

class SharedSafetyTest(AgentTest):
    def test_shared_home_missing_never_creates_local_home(self):
        self.a.shared = True
        ready, error = self.a.reconcile([self.account(1)])
        self.assertFalse(ready)
        self.assertTrue(error)
        self.assertFalse(any(c[0].endswith("useradd") for c in self.commands))

    def test_shared_mount_failure_never_creates_account(self):
        self.a.shared = True
        account = self.account(1)
        account["home"] = {"efs_id":"fs-12345678", "access_point_id":"fsap-12345678", "uid":200001,"gid":200001,"revision":1}
        with patch.object(self.a, "ensure_home", side_effect=RuntimeError("mount failed")):
            ready, error = self.a.reconcile([account])
        self.assertFalse(ready)
        self.assertTrue(error)
        self.assertFalse(any(c[0].endswith("useradd") for c in self.commands))

    def test_shared_portal_outage_preserves_jobs_and_desktop(self):
        self.a.reconcile([self.account(1)])
        self.a.shared = True
        self.a.last_ok -= 1000
        self.commands.clear()
        self.a.request = lambda *args: (_ for _ in ()).throw(OSError("offline"))
        self.assertFalse(self.a.sync())
        self.assertIn("awp-u1", self.sessions)
        self.assertFalse(any(c[0].endswith("pkill") for c in self.commands))

    def test_expired_reservation_never_closes_a_live_or_unknown_connection(self):
        account=self.account(1);account.update(assignment_id=123,reservation_expired=True)
        self.a.reconcile([account]);self.a.shared=True
        with patch.object(self.a,'user_work',return_value=(0,False)):
            # Legacy DCV lacks a connection counter; retain it.
            self.assertEqual(self.a.release_assignments([account]),[])
            self.sessions['awp-u1']['num-of-connections']=1
            self.assertEqual(self.a.release_assignments([account]),[])
            self.assertIn('awp-u1',self.sessions)
            self.sessions['awp-u1']['num-of-connections']=0
            self.assertEqual(self.a.release_assignments([account]),[123])
    def test_release_does_not_acknowledge_a_still_mounted_home(self):
        account=self.account(1);account['assignment_id']=123
        self.a.reconcile([account]);self.a.shared=True
        original=self.a.run
        def mounted(args,optional=False):
            if args[0].endswith('findmnt'):
                return subprocess.CompletedProcess(args,0,'nfs4','')
            return original(args,optional)
        with patch.object(self.a,'user_work',return_value=(0,False)),patch.object(self.a,'run',side_effect=mounted):
            self.assertEqual(self.a.release_assignments([account]),[])
        self.assertTrue(any(c[0].endswith('umount') for c in self.commands))

    def test_shared_release_requires_no_work_and_session_cleanup(self):
        self.a.reconcile([self.account(1)])
        self.a.shared = True
        account = self.account(1)
        account["assignment_id"] = 123
        with patch.object(self.a, "user_work", return_value=(1, False)):
            self.assertEqual(self.a.release_assignments([account]), [])
            self.assertIn("awp-u1", self.sessions)
        with patch.object(self.a, "user_work", return_value=(0, True)):
            self.assertEqual(self.a.release_assignments([account]), [])
        with patch.object(self.a, "user_work", return_value=(0, False)):
            self.assertEqual(self.a.release_assignments([account]), [123])
            self.assertNotIn("awp-u1", self.sessions)
