#!/usr/bin/env python3
"""Root-only Linux DCV reconciler and loopback external authentication broker.

No portal passwords, AWS credentials, arbitrary commands or shell interpolation.
Only accounts marked as created by this agent may be changed. Homes are retained.
"""
import argparse
import fcntl
import http.server
import json
import logging
import os
import pathlib
import pwd
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def identity(user_id):
    if type(user_id) is not int or not 1 <= user_id <= 1000000:
        raise ValueError("invalid user ID")
    return "awp-u" + str(user_id)


def command(args, optional=False):
    result = subprocess.run(args, capture_output=True, text=True, timeout=60)
    if result.returncode and not optional:
        # Never include command arguments (which may eventually contain secrets).
        raise RuntimeError("local provisioning command failed: " + pathlib.Path(args[0]).name)
    return result


class Agent:
    def __init__(self, config, state_dir, run=command):
        url = urllib.parse.urlsplit(config["portal_url"])
        if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
            raise ValueError("portal_url must be an HTTPS origin")
        token = config["token"]
        if len(token) != 64 or any(c not in "0123456789abcdef" for c in token):
            raise ValueError("invalid instance credential")
        self.base = config["portal_url"].rstrip("/")
        self.token = token
        self.instance_id = config["instance_id"]
        self.client = urllib.request.build_opener(
            NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=config.get("ca_file")))
        )
        self.root = pathlib.Path(state_dir)
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.state_path = self.root / "accounts.json"
        self.managed = json.loads(self.state_path.read_text()) if self.state_path.exists() else {}
        for key, value in self.managed.items():
            if key != identity(value["user_id"]):
                raise ValueError("invalid local state")
        self.run = run
        self.last_ok = time.monotonic()
        self.lock = threading.Lock()

    def request(self, path, body=None, content_type="application/json"):
        req = urllib.request.Request(self.base + path, data=body, headers={
            "Authorization": "Bearer " + self.token, "Content-Type": content_type,
        })
        with self.client.open(req, timeout=15) as response:
            data = response.read(1024 * 1024 + 1)
            if len(data) > 1024 * 1024:
                raise ValueError("response too large")
            return data

    def save(self):
        temporary = self.root / "accounts.tmp"
        with temporary.open("w") as f:
            os.chmod(temporary, 0o600)
            json.dump(self.managed, f)
            f.flush()
            os.fsync(f.fileno())
        temporary.replace(self.state_path)

    def check_account(self, name, user_id):
        account = pwd.getpwnam(name)
        if account.pw_uid != 200000 + user_id or account.pw_gecos != "awsportal:" + str(user_id):
            raise RuntimeError("refusing to modify an unmanaged OS account")
        return account

    def ensure_account(self, name, user_id):
        try:
            self.check_account(name, user_id)
        except KeyError:
            self.run(["/usr/sbin/useradd", "--create-home", "--user-group", "--uid", str(200000 + user_id),
                      "--shell", "/bin/bash", "--comment", "awsportal:" + str(user_id), name])
            self.check_account(name, user_id)
        # Persist ownership before the next command can fail or the process can exit.
        self.managed[name] = {"user_id": user_id}
        self.save()
        self.run(["/usr/sbin/usermod", "--lock", "--shell", "/bin/bash", name])
        account = self.check_account(name, user_id)
        # Do not follow a user-controlled home symlink while running as root.
        if account.pw_dir != "/home/" + name or pathlib.Path(account.pw_dir).is_symlink():
            raise RuntimeError("unexpected managed home path")
        os.chmod(account.pw_dir, 0o700)

    def describe(self, name):
        result = self.run(["/usr/bin/dcv", "describe-session", name, "--json"], optional=True)
        if result.returncode:
            return None
        session = json.loads(result.stdout)
        if session.get("owner") != name or session.get("type") != "virtual":
            raise RuntimeError("unexpected session owner or type")
        return session

    def revoke(self, name, user_id):
        self.check_account(name, user_id)
        if self.describe(name) is not None:
            self.run(["/usr/bin/dcv", "close-session", name])
        self.run(["/usr/sbin/usermod", "--lock", "--shell", "/usr/sbin/nologin", name])
        # Stop desktop and other processes of this managed UID, including detached tasks.
        result = self.run(["/usr/bin/pkill", "-KILL", "-u", str(200000 + user_id)], optional=True)
        if result.returncode not in (0, 1):
            raise RuntimeError("could not stop managed user processes")

    def reconcile(self, accounts):
        desired = {}
        for account in accounts:
            name = identity(account["user_id"])
            if account["os_user"] != name or account["session_id"] != name or name in desired:
                raise ValueError("invalid or duplicate account manifest")
            desired[name] = account["user_id"]
        # Revocation precedes provisioning. Never delete the home or the UID mapping.
        for name, account in list(self.managed.items()):
            if name not in desired:
                self.revoke(name, account["user_id"])
        ready = []
        failures = 0
        for name, user_id in desired.items():
            try:
                self.ensure_account(name, user_id)
                if self.describe(name) is None:
                    self.run(["/usr/bin/dcv", "create-session", "--type", "virtual", "--name", name,
                              "--owner", name, "--user", name, "--gl", "off", "--permissions-file",
                              "/etc/awsportal-dcv/user.perm", "--init", "/usr/local/libexec/awsportal-dcv-desktop", name])
                session = self.describe(name)
                if session is None or not session.get("x11-display"):
                    raise RuntimeError("virtual desktop is not ready")
                ready.append(user_id)
            except (RuntimeError, ValueError, KeyError, OSError, subprocess.TimeoutExpired):
                failures += 1
                logging.error("DCV provisioning failed for user ID %s", user_id)
        return ready, ("%d account(s) failed provisioning" % failures if failures else "")

    def sync(self):
        with self.lock:
            try:
                manifest = json.loads(self.request("/api/dcv/agent/state"))
                if manifest["instance_id"] != self.instance_id:
                    raise ValueError("manifest belongs to a different instance")
                ready, error = self.reconcile(manifest["accounts"])
                self.request("/api/dcv/agent/heartbeat", json.dumps({"ready_users": ready, "error": error}).encode())
                self.last_ok = time.monotonic()
            except Exception:
                # A network outage never creates users or grants local access.
                # Existing managed sessions are closed after a bounded 90-second grace.
                logging.error("DCV synchronization failed")
                if time.monotonic() - self.last_ok >= 90:
                    for name, account in self.managed.items():
                        self.revoke(name, account["user_id"])

    def authenticate(self, body):
        form = urllib.parse.parse_qs(body.decode(), strict_parsing=True)
        session_id = form.get("sessionId", [""])[0]
        user_id = int(session_id.removeprefix("awp-u"))
        if identity(user_id) != session_id:
            raise ValueError("invalid managed session")
        result = self.request("/api/dcv/agent/auth", body, "application/x-www-form-urlencoded")
        auth = ET.fromstring(result)
        if auth.get("result") != "yes" or auth.findtext("username") != session_id:
            raise ValueError("authentication rejected")
        return result


def broker(agent):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass  # Never log authentication tokens or request bodies.

        def do_POST(self):
            self.connection.settimeout(20)
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 16384:
                    raise ValueError("invalid request size")
                result = agent.authenticate(self.rfile.read(size))
                status = 200
            except Exception:
                status, result = 401, b'<auth result="no"><message>unauthorized</message></auth>'
            self.send_response(status)
            self.send_header("Content-Type", "application/xml")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(result)))
            self.end_headers()
            self.wfile.write(result)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 8444), Handler)
    server.daemon_threads = True
    return server


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", default="/etc/awsportal-dcv/config.json")
    parser.add_argument("--state-dir", default="/var/lib/awsportal-dcv")
    parser.add_argument("--once", action="store_true")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("agent must run as root")
    metadata = os.stat(args.config)
    if metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise SystemExit("config must be owned by root with mode 0600")
    logging.basicConfig(level=logging.INFO)
    agent = Agent(json.loads(pathlib.Path(args.config).read_text()), args.state_dir)
    with (agent.root / "agent.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.once:
            agent.sync()
            return
        server = broker(agent)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        while True:
            agent.sync()
            time.sleep(30)


if __name__ == "__main__":
    main()
