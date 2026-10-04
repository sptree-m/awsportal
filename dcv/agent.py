#!/usr/bin/env python3
"""Root-only Linux DCV reconciler and loopback external authentication broker.

No portal passwords, AWS credentials, arbitrary commands or shell interpolation.
Only accounts marked as created by this agent may be changed. Homes are retained.
"""
import argparse
import configparser
import hashlib
import fcntl
import http.server
import json
import logging
import os
import pathlib
import pwd
import re
import uuid
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


FEATURES = frozenset("display keyboard mouse pointer audio-out audio-in clipboard-copy clipboard-paste file-download file-upload screenshot printer usb smartcard webcam gamepad stylus touch keyboard-sas webauthn-redirection extensions-client extensions-server unsupervised-access".split())
DEFAULT_ALLOWED = ["display", "keyboard", "mouse", "pointer", "audio-out"]


def permission_text(allowed, actor=None):
    blocked = sorted(FEATURES - set(allowed))
    lines = ["[permissions]"]
    if actor and allowed:
        lines.append(actor + " allow " + " ".join(sorted(allowed)))
    if blocked:
        lines.append("%any% deny " + " ".join(blocked))
    return "\n".join(lines) + "\n"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def identity(user_id):
    if type(user_id) is not int or not 1 <= user_id <= 1000000:
        raise ValueError("invalid user ID")
    return "awp-u" + str(user_id)


def identity_id(name):
    user_id = int(name.removeprefix("awp-u"))
    if identity(user_id) != name:
        raise ValueError("invalid managed identity")
    return user_id


def command(args, optional=False):
    result = subprocess.run(args, capture_output=True, text=True, timeout=60)
    if result.returncode and not optional:
        # Never include command arguments (which may eventually contain secrets).
        raise RuntimeError("local provisioning command failed: " + pathlib.Path(args[0]).name)
    return result


class Agent:
    def __init__(self, config, state_dir, run=command, policy_dir="/etc/dcv/awsportal-policy", dcv_config="/etc/dcv/dcv.conf"):
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
        self.policy_dir = pathlib.Path(policy_dir)
        self.dcv_config = pathlib.Path(dcv_config)
        self.policy_state = self.root / "policy.json"
        previous = json.loads(self.policy_state.read_text()) if self.policy_state.exists() else {}
        self.applied_revision = previous.get("revision", 0)
        self.policy_fingerprint = previous.get("fingerprint", "")
        self.allowed = []
        self.run = run
        previous_environment = json.loads((self.root / "environment.json").read_text()) if (self.root / "environment.json").exists() else {}
        self.shared = previous_environment.get("shared", False)
        self.generation = previous_environment.get("generation", 0)
        self.sequence = 0
        self.boot_id = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        self.previous_cpu = None
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

    def ensure_account(self, name, user_id, home=None):
        if self.shared and home is None:
            raise RuntimeError("Shared HOME storage required")
        if home is not None:
            self.ensure_home(name, user_id, home)
        try:
            self.check_account(name, user_id)
        except KeyError:
            if home is not None:
                self.run(["/usr/sbin/groupadd", "--gid", str(200000 + user_id), name], optional=True)
                self.run(["/usr/sbin/useradd", "--no-create-home", "--gid", str(200000 + user_id), "--uid", str(200000 + user_id),
                          "--shell", "/bin/bash", "--comment", "awsportal:" + str(user_id), name])
            else:
                self.run(["/usr/sbin/useradd", "--create-home", "--user-group", "--uid", str(200000 + user_id),
                          "--shell", "/bin/bash", "--comment", "awsportal:" + str(user_id), name])
            self.check_account(name, user_id)
        # Persist ownership before the next command can fail or the process can exit.
        self.managed[name] = {"user_id": user_id}
        self.save()
        self.run(["/usr/sbin/usermod", "--lock", "--shell", "/bin/bash", name])
        account = self.check_account(name, user_id)
        if home is not None and account.pw_gid != 200000 + user_id:
            raise RuntimeError("unexpected Shared OS group identity")
        # Do not follow a user-controlled home symlink while running as root.
        if account.pw_dir != "/home/" + name or pathlib.Path(account.pw_dir).is_symlink():
            raise RuntimeError("unexpected managed home path")
        if home is None:
            os.chmod(account.pw_dir, 0o700)

    def ensure_home(self, name, user_id, home):
        uid = 200000 + user_id
        efs, ap = home["efs_id"], home["access_point_id"]
        if not re.fullmatch(r"fs-[a-f0-9]{8,17}", efs) or not re.fullmatch(r"fsap-[a-f0-9]{8,17}", ap):
            raise ValueError("invalid EFS storage identity")
        if home["uid"] != uid or home["gid"] != uid or type(home["revision"]) is not int:
            raise ValueError("unexpected storage POSIX identity")
        path = pathlib.Path("/home") / name
        if path.is_symlink():
            raise RuntimeError("refusing HOME symlink")
        marker = self.root / (name + ".efs.json")
        mounted = self.run(["/usr/bin/findmnt", "--mountpoint", str(path), "--noheadings", "--output", "FSTYPE"], optional=True)
        if mounted.returncode == 0:
            if mounted.stdout.strip() != "nfs4" or not marker.exists() or json.loads(marker.read_text()) != home:
                raise RuntimeError("unexpected HOME mount; manual migration required")
        else:
            if path.exists() and any(path.iterdir()):
                raise RuntimeError("local HOME is not empty; manual migration required")
            path.mkdir(mode=0o700, exist_ok=True)
            self.run(["/usr/bin/mount", "-t", "efs", "-o", "tls,iam,accesspoint=" + ap, efs + ":/", str(path)])
            mounted = self.run(["/usr/bin/findmnt", "--mountpoint", str(path), "--noheadings", "--output", "FSTYPE"])
            if mounted.stdout.strip() != "nfs4":
                raise RuntimeError("EFS HOME mount not verified")
            self.write_policy(marker, json.dumps(home, sort_keys=True))
        st = path.stat()
        if st.st_uid != uid or st.st_gid != uid or st.st_mode & 0o077:
            raise RuntimeError("EFS access point ownership or permissions mismatch")

    def user_work(self, user_id):
        # Unknown processes, inaccessible /proc entries, and cgroup ambiguity hold
        # the seat. No low-CPU heuristic is used to decide whether work is safe.
        jobs, unknown = set(), False
        desktop = {"Xdcv", "dcvagent", "dcvsession", "xfce4-session", "xfwm4", "xfdesktop", "xfce4-panel",
                   "xfsettingsd", "dbus-daemon", "dbus-broker", "systemd", "(sd-pam)", "at-spi-bus-laun", "at-spi2-registr",
                   "pulseaudio", "pipewire", "wireplumber", "gvfsd", "gvfsd-fuse", "dconf-service", "ssh-agent", "gpg-agent"}
        for proc in pathlib.Path("/proc").iterdir():
            if not proc.name.isdecimal():
                continue
            try:
                if proc.stat().st_uid != 200000 + user_id:
                    continue
                group = (proc / "cgroup").read_text()
                match = re.search(r"awsportal-job-[a-f0-9]+\.scope", group)
                if match:
                    jobs.add(match.group())
                elif (proc / "comm").read_text().strip() not in desktop:
                    unknown = True
            except FileNotFoundError:
                pass  # Process exited during sampling.
            except (PermissionError, OSError):
                unknown = True
        return len(jobs), unknown

    def release_assignments(self, releases):
        closed = []
        for account in releases:
            name = identity(account["user_id"])
            jobs, unknown = self.user_work(account["user_id"])
            if jobs or unknown:
                continue
            if self.describe(name) is not None:
                self.run(["/usr/bin/dcv", "close-session", name])
            jobs, unknown = self.user_work(account["user_id"])
            if self.describe(name) is None and not jobs and not unknown:
                closed.append(account["assignment_id"])
        return closed

    def environment_report(self, accounts, releases, closed):
        cpu_values = [int(x) for x in pathlib.Path("/proc/stat").read_text().splitlines()[0].split()[1:9]]
        total, idle = sum(cpu_values), cpu_values[3] + cpu_values[4]
        valid = self.previous_cpu is not None and total > self.previous_cpu[0]
        cpu = 100 * (1 - (idle - self.previous_cpu[1]) / (total - self.previous_cpu[0])) if valid else 100
        self.previous_cpu = (total, idle)
        mem = {line.split(":")[0]: int(line.split()[1]) for line in pathlib.Path("/proc/meminfo").read_text().splitlines()}
        work = []
        for a in accounts + releases:
            name, uid = identity(a["user_id"]), a["user_id"]
            jobs, unknown = self.user_work(uid)
            session = self.describe(name)
            count = session.get("num-of-connections") if session else 0
            # An older DCV that cannot report real connections is unsuitable for
            # Shared. The error is surfaced and no usable sample is emitted.
            if type(count) is not int or count < 0:
                raise RuntimeError("DCV connection count unavailable")
            marker = self.root / (name + ".efs.json")
            mounted = self.run(["/usr/bin/findmnt", "--mountpoint", "/home/" + name, "--noheadings", "--output", "FSTYPE"], optional=True)
            home = json.loads(marker.read_text()) if marker.exists() else {}
            work.append({"user_id": uid, "jobs": jobs, "unclassified": unknown, "session_present": session is not None,
                         "connected": count > 0, "home_mounted": mounted.returncode == 0 and mounted.stdout.strip() == "nfs4",
                         "storage_revision": home.get("revision", 0)})
        # Sequence is persisted, so an agent restart within the same OS boot never
        # reuses a (boot_id, sequence) sample identity.
        sequence_path = self.root / "sequence.json"
        previous = json.loads(sequence_path.read_text()) if sequence_path.exists() else {}
        sequence = previous.get("sequence", 0) + 1 if previous.get("boot_id") == self.boot_id else 1
        self.write_policy(sequence_path, json.dumps({"boot_id": self.boot_id, "sequence": sequence}))
        return {"agent_version": 2, "generation": self.generation, "boot_id": self.boot_id, "sequence": sequence,
                "observed_at": int(time.time()), "cpu": max(0, min(100, cpu)), "memory": 100 * (1 - mem["MemAvailable"] / mem["MemTotal"]),
                "metrics_valid": valid, "storage_busy": False, "work": work, "closed_assignments": closed}

    def validate_enforcement(self):
        # The global default is merged into every session, even if a local user
        # creates a new session or supplies their own permissions file.
        for path in (self.dcv_config, self.policy_dir):
            st = path.lstat()
            if path.is_symlink() or st.st_uid != 0 or st.st_mode & 0o022:
                raise RuntimeError("DCV enforcement path is not root protected")
        config = configparser.ConfigParser(interpolation=None)
        config.read(self.dcv_config)
        if config.get("session-management/defaults", "permissions-file").strip('"') != str(self.policy_dir / "enforced.perm"):
            raise RuntimeError("server-wide DCV enforcement is not installed")
        if config.get("security", "auth-token-verifier").strip('"') != "http://127.0.0.1:8444":
            raise RuntimeError("unexpected external authentication verifier")
        if config.get("security", "allowed-ws-origin-regex", fallback="").strip('"') != "^$":
            raise RuntimeError("browser WebSocket rejection is not installed")
        viewer = self.run(["/usr/bin/dpkg-query", "-W", "-f=${db:Status-Status}", "nice-dcv-web-viewer"], optional=True)
        if viewer.returncode == 0 and viewer.stdout.strip() == "installed":
            raise RuntimeError("browser viewer package must be removed")

    def write_policy(self, path, text):
        if path.is_symlink():
            raise RuntimeError("refusing policy symlink")
        temporary = path.with_suffix(".tmp")
        # Root-protected directory prevents users replacing the temporary file.
        with temporary.open("w") as f:
            os.chmod(temporary, 0o600)
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        temporary.replace(path)
        os.chmod(path, 0o644)

    def describe(self, name, allow_legacy=False):
        result = self.run(["/usr/bin/dcv", "describe-session", name, "--json"], optional=True)
        if result.returncode:
            return None
        session = json.loads(result.stdout)
        owners = ("root", name) if allow_legacy and name in self.managed else ("root",)
        if session.get("owner") not in owners or session.get("type") != "virtual":
            raise RuntimeError("unexpected session owner or type")
        # Owner controls DCV management; Xauthority identifies the OS desktop UID.
        uid = 200000 + identity_id(name)
        if not str(session.get("x11-authority", "")).startswith("/run/user/" + str(uid) + "/dcv/"):
            raise RuntimeError("unexpected virtual desktop OS identity")
        return session

    def apply_policy(self, policy):
        self.validate_enforcement()
        revision, allowed = policy["revision"], policy["allowed"]
        if type(revision) is not int or revision < 1 or not isinstance(allowed, list):
            raise ValueError("invalid policy")
        if any(type(code) is not str or code not in FEATURES or code == "unsupervised-access" for code in allowed) or len(set(allowed)) != len(allowed):
            raise ValueError("invalid feature")
        if ("screenshot" not in allowed and "clipboard-copy" in allowed) or ("keyboard-sas" in allowed and "keyboard" not in allowed):
            raise ValueError("unsafe feature combination")
        text = permission_text(allowed)
        fingerprint = hashlib.sha256(text.encode()).hexdigest()
        path = self.policy_dir / "enforced.perm"
        changed = revision != self.applied_revision or fingerprint != self.policy_fingerprint or not path.exists() or path.read_text() != text
        if changed:
            if self.shared and self.applied_revision and any(self.describe(name) is not None for name in self.managed):
                raise RuntimeError("Shared policy update requires released desktops")
            # Defaults are loaded at session creation: close old desktops before
            # replacing the global baseline. Never report a partially applied policy.
            for name in self.managed:
                if self.describe(name, allow_legacy=True) is not None:
                    self.run(["/usr/bin/dcv", "close-session", name])
            self.write_policy(path, text)
        self.allowed = sorted(allowed)
        self.applied_revision, self.policy_fingerprint = revision, fingerprint
        self.write_policy(self.policy_state, json.dumps({"revision": revision, "fingerprint": fingerprint}))

    def revoke(self, name, user_id):
        self.check_account(name, user_id)
        if self.describe(name, allow_legacy=True) is not None:
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
            desired[name] = account
        # Revocation precedes provisioning. Never delete the home or the UID mapping.
        failures = 0
        for name, account in list(self.managed.items()):
            if name not in desired and name not in getattr(self, "releasing_names", set()):
                try:
                    self.revoke(name, account["user_id"])
                except (RuntimeError, ValueError, KeyError, OSError, subprocess.TimeoutExpired):
                    failures += 1
                    logging.error("DCV revocation failed for user ID %s", account["user_id"])
        ready = []
        for name, account in desired.items():
            user_id = account["user_id"]
            try:
                self.ensure_account(name, user_id, account.get("home"))
                permissions = self.policy_dir / (name + ".perm")
                self.write_policy(permissions, permission_text(self.allowed, name))
                if self.describe(name) is None:
                    self.run(["/usr/bin/dcv", "create-session", "--type", "virtual", "--name", name,
                              "--owner", "root", "--user", name, "--gl", "off", "--permissions-file",
                              str(permissions), "--storage-root", "/home/" + name, "--init", "/usr/local/libexec/awsportal-dcv-desktop", name])
                # Reapply the administrator's exact permissions on every successful sync.
                self.run(["/usr/bin/dcv", "set-permissions", "--session", name, "--file", str(permissions)])
                session = self.describe(name)
                if session is None or not session.get("x11-display"):
                    raise RuntimeError("virtual desktop is not ready")
                ready.append(user_id)
            except (RuntimeError, ValueError, KeyError, OSError, subprocess.TimeoutExpired):
                failures += 1
                logging.error("DCV provisioning failed for user ID %s", user_id)
        return ready, ("%d account(s) failed synchronization" % failures if failures else "")

    def sync(self):
        with self.lock:
            fetched = False
            try:
                manifest = json.loads(self.request("/api/dcv/agent/state"))
                fetched = True
                if manifest["instance_id"] != self.instance_id:
                    raise ValueError("manifest belongs to a different instance")
                environment = manifest.get("environment", {})
                self.shared = environment.get("shared", False)
                self.generation = environment.get("generation", 0)
                self.write_policy(self.root / "environment.json", json.dumps({"shared": self.shared,"generation":self.generation}))
                releases = environment.get("release", [])
                self.releasing_names = {identity(a["user_id"]) for a in releases}
                self.apply_policy(manifest["policy"])
                ready, error = self.reconcile(manifest["accounts"])
                report = {"ready_users": ready, "error": error, "applied_revision":self.applied_revision,"browser_blocked":True}
                if self.shared:
                    closed = self.release_assignments(releases)
                    report.update(self.environment_report(manifest["accounts"], releases, closed))
                self.request("/api/dcv/agent/heartbeat", json.dumps(report).encode())
                self.last_ok = time.monotonic()
                return not bool(error)
            except Exception:
                # A network outage never creates users or grants local access.
                # Existing managed sessions are closed after a bounded 90-second grace.
                logging.error("DCV synchronization failed")
                if not self.shared and (fetched or time.monotonic() - self.last_ok >= 90):
                    for name, account in self.managed.items():
                        try:
                            self.revoke(name, account["user_id"])
                        except Exception:
                            logging.error("Emergency DCV revocation failed for user ID %s", account["user_id"])
                try:
                    self.request("/api/dcv/agent/heartbeat", json.dumps({"ready_users": [], "error": "DCV synchronization or policy enforcement failed", "applied_revision": 0,"browser_blocked":False}).encode())
                except Exception:
                    pass
                return False

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
            if not agent.sync():
                raise SystemExit("DCV synchronization failed")
            return
        server = broker(agent)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        while True:
            agent.sync()
            time.sleep(30)


if __name__ == "__main__":
    main()
