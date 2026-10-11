"""Root-owned job ledger. Counters come from cgroup v2, never from the user."""
import json
import os
import pathlib
import re
import socket
import socketserver
import struct
import threading
import time

JOB_ID = re.compile(r"^[a-f0-9]{32}$")
TERMINAL = {"SUCCEEDED", "FAILED", "INTERRUPTED"}


class JobLedger:
    def __init__(self, state, boot_id, write, proc="/proc", cgroup="/sys/fs/cgroup"):
        self.path = pathlib.Path(state) / "jobs.json"
        self.boot_id, self.write = boot_id, write
        self.proc, self.cgroup = pathlib.Path(proc), pathlib.Path(cgroup)
        self.lock = threading.RLock()
        self.jobs = json.loads(self.path.read_text()) if self.path.exists() else {}
        for job in self.jobs.values():
            if job["boot_id"] != boot_id and job["state"] not in TERMINAL:
                job.update(state="INTERRUPTED", ended_at=int(time.time()), quality="boot_changed")

    def save(self):
        self.write(self.path, json.dumps(self.jobs, sort_keys=True))

    def group_for_peer(self, pid, uid, job_id):
        if not JOB_ID.fullmatch(job_id) or not 200001 <= uid <= 1200000:
            raise ValueError("invalid job identity")
        lines = (self.proc / str(pid) / "cgroup").read_text().splitlines()
        group = next((line[3:] for line in lines if line.startswith("0::")), None)
        expected = "awsportal-job-" + job_id + ".scope"
        if group is None:
            raise ValueError("cgroup v2 required")
        parts = pathlib.PurePosixPath(group).parts
        if expected not in parts or "user-" + str(uid) + ".slice" not in parts:
            raise ValueError("job is not in the caller's scope")
        group = str(pathlib.PurePosixPath(*parts[:parts.index(expected) + 1]))
        return group

    def counters(self, group):
        path = (self.cgroup / group.lstrip("/")).resolve()
        path.relative_to(self.cgroup.resolve())
        cpu = dict(line.split() for line in (path / "cpu.stat").read_text().splitlines())
        events = dict(line.split() for line in (path / "cgroup.events").read_text().splitlines())
        read, written = 0, 0
        for line in (path / "io.stat").read_text().splitlines():
            values = dict(v.split("=", 1) for v in line.split()[1:])
            read += int(values.get("rbytes", 0))
            written += int(values.get("wbytes", 0))
        result = {"cpu_usec": int(cpu["usage_usec"]), "memory_bytes": int((path / "memory.current").read_text()),
                  "read_bytes": read, "write_bytes": written, "counter_epoch": self.boot_id + ":" + str(path.stat().st_ino)}
        if any(result[k] < 0 for k in ("cpu_usec", "memory_bytes", "read_bytes", "write_bytes")):
            raise ValueError("invalid cgroup counter")
        return result, int(events["populated"]) == 1

    def event(self, pid, uid, payload, permitted):
        job_id = payload.get("job_id", "")
        group = self.group_for_peer(pid, uid, job_id)
        with self.lock:
            job = self.jobs.get(job_id)
            if payload.get("event") == "start":
                if not permitted or job is not None or len(self.jobs) >= 128:
                    raise ValueError("job registration unavailable")
                counters, _ = self.counters(group)
                job = {"job_id": job_id, "user_id": uid - 200000, "boot_id": self.boot_id, "group": group,
                       "state": "RUNNING", "started_at": int(time.time()), "ended_at": 0, "exit_code": None,
                       "quality": "ok", **counters}
                self.jobs[job_id] = job
            elif payload.get("event") == "finish":
                code = payload.get("exit_code")
                if job is None or job["user_id"] != uid - 200000 or job["boot_id"] != self.boot_id or job["group"] != group or type(code) is not int or not -255 <= code <= 255:
                    raise ValueError("invalid job completion")
                if job["exit_code"] is not None and job["exit_code"] != code:
                    raise ValueError("changed job completion")
                counters, _ = self.counters(group)
                reset = counters["counter_epoch"] != job["counter_epoch"] or any(counters[k] < job[k] for k in ("cpu_usec", "read_bytes", "write_bytes"))
                job.update(counters, exit_code=code, quality="counter_reset" if reset or job["quality"] == "counter_reset" else "ok")
                # Wrapper is still in the scope; detached children must finish too.
            else:
                raise ValueError("invalid job event")
            self.save()

    def sample(self):
        with self.lock:
            for job in self.jobs.values():
                if job["state"] in TERMINAL:
                    continue
                try:
                    counters, populated = self.counters(job["group"])
                    reset = counters["counter_epoch"] != job["counter_epoch"] or any(counters[k] < job[k] for k in ("cpu_usec", "read_bytes", "write_bytes"))
                    job.update(counters, quality="counter_reset" if reset or job["quality"] == "counter_reset" else "ok")
                    if populated:
                        job["state"] = "RUNNING"
                    else:
                        self.finish(job)
                except FileNotFoundError:
                    # A missing counter file is unknown; only a removed scope
                    # proves no process can still live in that scope.
                    if not (self.cgroup / job["group"].lstrip("/")).exists():
                        self.finish(job)
                        job["quality"] = "scope_removed"
                    else:
                        job.update(state="UNKNOWN", quality="unavailable")
                except (OSError, ValueError, KeyError):
                    job.update(state="UNKNOWN", quality="unavailable")
            self.save()
            return [{k: v for k, v in job.items() if k != "group"} for job in self.jobs.values()]

    @staticmethod
    def finish(job):
        code = job["exit_code"]
        job.update(state="INTERRUPTED" if code is None else "SUCCEEDED" if code == 0 else "FAILED", ended_at=int(time.time()))

    def acknowledge(self, samples):
        with self.lock:
            for sample in samples:
                job = self.jobs.get(sample["job_id"])
                if job and job == {**sample, "group": job["group"]}:
                    if job["state"] in TERMINAL:
                        del self.jobs[sample["job_id"]]
                    elif job["quality"] == "counter_reset":
                        job["quality"] = "ok"  # Only a successfully sent reset is acknowledged.
            self.save()


def job_broker(agent, path="/run/awsportal-dcv/jobs.sock"):
    class Handler(socketserver.StreamRequestHandler):
        def handle(self):
            self.connection.settimeout(20)
            try:
                pid, uid, _ = struct.unpack("3i", self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
                data = self.rfile.readline(4097)
                if len(data) > 4096 or not data.endswith(b"\n"):
                    raise ValueError("invalid job request")
                payload = json.loads(data)
                agent.jobs.event(pid, uid, payload, agent.shared and uid - 200000 in agent.job_users)
                self.wfile.write(b'{"ok":true}\n')
            except Exception:
                self.wfile.write(b'{"ok":false}\n')

    class Server(socketserver.ThreadingUnixStreamServer):
        daemon_threads = True

    directory = pathlib.Path(path).parent
    directory.mkdir(mode=0o755, parents=True, exist_ok=True)
    st = directory.lstat()
    if directory.is_symlink() or st.st_uid != 0 or st.st_mode & 0o022:
        raise RuntimeError("job socket directory is not root protected")
    if os.path.lexists(path):
        os.unlink(path)  # Only after the exclusive agent lock is acquired.
    server = Server(path, Handler)
    os.chmod(path, 0o666)  # SO_PEERCRED/cgroup identify the caller, not this mode.
    return server
