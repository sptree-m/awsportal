import importlib.machinery
import importlib.util
import json
import pathlib
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

from job_metrics import JobLedger


class JobMetricsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.proc, self.cg = self.root / "proc", self.root / "cg"
        self.proc.mkdir()
        self.cg.mkdir()
        self.job_id = "a" * 32
        self.group = "/user.slice/user-200001.slice/user@200001.service/app.slice/awsportal-job-" + self.job_id + ".scope"
        self.scope = self.cg / self.group.lstrip("/")
        self.scope.mkdir(parents=True)
        peer = self.proc / "42"
        peer.mkdir()
        (peer / "cgroup").write_text("0::" + self.group + "\n")
        self.write_counters(100, 1)
        self.ledger = self.new_ledger()

    def new_ledger(self, boot="boot-test-0001"):
        return JobLedger(self.root, boot, lambda p, text: p.write_text(text), self.proc, self.cg)

    def write_counters(self, cpu, populated):
        (self.scope / "cpu.stat").write_text("usage_usec " + str(cpu) + "\n")
        (self.scope / "memory.current").write_text("4096\n")
        (self.scope / "io.stat").write_text("8:0 rbytes=100 wbytes=50 rios=1 wios=1\n8:16 rbytes=25 wbytes=75\n")
        (self.scope / "cgroup.events").write_text("populated " + str(populated) + "\nfrozen 0\n")

    def start(self):
        self.ledger.event(42, 200001, {"event": "start", "job_id": self.job_id}, True)

    def test_short_job_is_persisted_before_first_poll_and_ack(self):
        self.start()
        self.write_counters(900, 1)
        self.ledger.event(42, 200001, {"event": "finish", "job_id": self.job_id, "exit_code": 7}, False)
        shutil.rmtree(self.scope)
        recovered = self.new_ledger()
        samples = recovered.sample()
        self.assertEqual(samples[0]["state"], "FAILED")
        self.assertEqual(samples[0]["cpu_usec"], 900)
        self.assertEqual(samples[0]["read_bytes"], 125)
        self.assertEqual(samples[0]["write_bytes"], 125)
        self.assertEqual(samples[0]["quality"], "scope_removed")
        self.assertEqual(len(self.new_ledger().sample()), 1)  # Portal outage/restart retains it.
        recovered.acknowledge(samples)
        self.assertEqual(self.new_ledger().sample(), [])

    def test_detached_child_keeps_job_running_after_command_exit(self):
        self.start()
        self.ledger.event(42, 200001, {"event": "finish", "job_id": self.job_id, "exit_code": 0}, True)
        self.assertEqual(self.ledger.sample()[0]["state"], "RUNNING")
        self.write_counters(500, 0)
        self.assertEqual(self.ledger.sample()[0]["state"], "SUCCEEDED")

    def test_cross_user_scope_and_unassigned_registration_rejected(self):
        for uid, permitted in [(200002, True), (200001, False), (0, True)]:
            with self.assertRaises(ValueError):
                self.ledger.event(42, uid, {"event": "start", "job_id": self.job_id}, permitted)
        self.assertFalse(self.ledger.jobs)

    def test_missing_counter_is_unknown_and_reset_is_explicit(self):
        self.start()
        (self.scope / "cpu.stat").unlink()
        self.assertEqual(self.ledger.sample()[0]["state"], "UNKNOWN")
        self.write_counters(20, 1)
        sample = self.ledger.sample()[0]
        self.assertEqual(sample["state"], "RUNNING")
        self.assertEqual(sample["quality"], "counter_reset")

    def test_os_reboot_marks_interruption_without_claiming_success(self):
        self.start()
        j = self.new_ledger("boot-test-0002").sample()[0]
        self.assertEqual(j["state"], "INTERRUPTED")
        self.assertEqual(j["quality"], "boot_changed")
        self.assertIsNone(j["exit_code"])

    def test_cgroup_symlink_escape_and_changed_completion_rejected(self):
        with self.assertRaises(ValueError):
            self.ledger.counters("/../../outside")
        self.start()
        self.ledger.event(42, 200001, {"event": "finish", "job_id": self.job_id, "exit_code": 0}, True)
        with self.assertRaises(ValueError):
            self.ledger.event(42, 200001, {"event": "finish", "job_id": self.job_id, "exit_code": 1}, True)

    def test_wrapper_does_not_execute_when_registration_fails(self):
        path = pathlib.Path(__file__).with_name("awsportal-job")
        loader = importlib.machinery.SourceFileLoader("job_wrapper", str(path))
        module = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(module)
        with patch.object(module.os, "geteuid", return_value=200001), patch.object(sys, "argv", [str(path), "--worker", self.job_id, "sleep", "1"]), patch.object(module, "notify", side_effect=RuntimeError("offline")), patch.object(module.subprocess, "call") as command:
            with self.assertRaises(RuntimeError):
                module.main()
            command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
