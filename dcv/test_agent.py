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
                             self.tmp.name, self.run_command)
        self.addCleanup(patch.stopall)
        patch.object(agent.pwd, "getpwnam", side_effect=self.get_user).start()
        patch.object(agent.os, "chmod").start()

    def get_user(self, name):
        if name not in self.users:
            raise KeyError(name)
        return self.users[name]

    def run_command(self, args, optional=False):
        self.commands.append(args)
        status, output = 0, ""
        if args[0].endswith("useradd"):
            name = args[-1]
            uid = int(args[args.index("--uid") + 1])
            self.users[name] = types.SimpleNamespace(pw_uid=uid, pw_gecos="awsportal:" + name[5:], pw_dir="/home/" + name)
        if args[0].endswith("dcv"):
            if args[1] == "create-session":
                name = args[-1]
                self.sessions[name] = {"owner": name, "type": "virtual", "x11-display": ":1"}
            if args[1] == "describe-session":
                session = self.sessions.get(args[2])
                status = 0 if session else 1
                output = json.dumps(session)
            if args[1] == "close-session":
                self.sessions.pop(args[2])
        return subprocess.CompletedProcess(args, status, output, "")

    def account(self, uid):
        return {"user_id": uid, "username": "alice", "os_user": agent.identity(uid), "session_id": agent.identity(uid)}

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
        self.sessions["awp-u1"] = {"owner": "root", "type": "virtual", "x11-display": ":1"}
        ready, error = self.a.reconcile([self.account(1)])
        self.assertFalse(ready)
        self.assertTrue(error)

    def test_failure_does_not_report_success(self):
        manifest = {"instance_id": "i-a", "accounts": [self.account(1)]}
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
        self.a.request = lambda *args: json.dumps({"instance_id": "i-b", "accounts": [self.account(1)]}).encode()
        self.a.sync()
        self.assertFalse(self.commands)

    def test_broker_rejects_wrong_identity_and_never_follows_redirect(self):
        self.a.request = lambda *args: b'<auth result="yes"><username>root</username></auth>'
        with self.assertRaises(ValueError):
            self.a.authenticate(b"sessionId=awp-u1&authenticationToken=test")
        with self.assertRaises(ValueError):
            self.a.authenticate(b"sessionId=root&authenticationToken=test")
        self.assertIsNone(agent.NoRedirect().redirect_request(None, None, None, None, None, None))

    def test_requires_https_and_safe_origin(self):
        for url in ("http://portal.example", "https://user:pass@portal.example", "https://portal.example/path"):
            with self.assertRaises(ValueError):
                agent.Agent({"portal_url": url, "instance_id": "i-a", "token": "a" * 64}, self.tmp.name)


if __name__ == "__main__":
    unittest.main()
