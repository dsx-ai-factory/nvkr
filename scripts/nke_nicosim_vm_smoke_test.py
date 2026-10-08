# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Diagnostic probe contracts. Fixtures do not establish real VM acceptance."""

import importlib.util
import json
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("vm_smoke", Path(__file__).with_name("nke-nicosim-vm-smoke.py"))
ADAPTER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ADAPTER)


def record(name, kind, digest):
    binding = {"name": name, "kind": kind, "buildDigest": "sha256:" + "b" * 64, "components": ["component"]}
    return {"status": "complete", "failure": None, "expected": [binding],
            "observed": [{**binding, "digest": digest}]}


class VMSmokeTest(unittest.TestCase):
    def test_timeout_retains_partial_private_diagnostics(self):
        for stdout, stderr in ((b"architecture\ncloud-init\n", b"waiting\n"),
                               ("architecture\ncloud-init\n", "waiting\n"), (None, None)):
            with self.subTest(stdout=stdout), tempfile.TemporaryDirectory() as temporary:
                log = Path(temporary) / "guest-check.log"
                timeout = subprocess.TimeoutExpired("ssh", 180, output=stdout, stderr=stderr)
                with patch.object(ADAPTER.subprocess, "run", side_effect=timeout):
                    with self.assertRaisesRegex(ADAPTER.SmokeError, "guest: timed out"):
                        ADAPTER.run(["ssh"], "guest", diagnostic=log)
                self.assertEqual(log.read_text(), "architecture\ncloud-init\nwaiting\n" if stdout else "")

    def test_completed_run_admission(self):
        cases = ("complete", "failed", "partial", "empty", "duplicate expected", "duplicate observed",
                 "different name", "different kind", "different build", "different components", "missing output")
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                data = record("tools", "executable", "sha256:" + "a" * 64)
                if case == "failed":
                    data["status"] = "failed"
                elif case == "partial":
                    data["failure"] = {"message": "failed"}
                elif case == "empty":
                    data["expected"] = data["observed"] = []
                elif case == "duplicate expected":
                    data["expected"] *= 2
                elif case == "duplicate observed":
                    data["observed"] *= 2
                elif case.startswith("different "):
                    key = {"name": "name", "kind": "kind", "build": "buildDigest", "components": "components"}[case.split()[1]]
                    data["observed"][0][key] = "different"
                (root / "lifecycle.json").write_text(json.dumps(data))
                selected = "missing" if case == "missing output" else "tools"
                if case == "complete":
                    self.assertEqual(ADAPTER.complete_output(root, selected, "executable"), data["observed"][0])
                else:
                    with self.assertRaises(ADAPTER.SmokeError):
                        ADAPTER.complete_output(root, selected, "executable")

    def test_exact_inputs_and_no_output_on_rejection(self):
        for case in ("exact", "wrong disk bytes", "wrong size", "FDE", "wrong format", "wrong manifest",
                     "disk symlink", "output exists", "output symlink", "output in run"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                sim, image = root / "sim", root / "image"
                sim.mkdir()
                (image / "outputs").mkdir(parents=True)
                layout = sim / "packages/nicosim-tools-arm64"
                layout.mkdir(parents=True)
                disk = image / "outputs/nke-node-image-arm64.qcow2"
                disk.write_bytes(b"fixture, not a real disk")
                observed = ADAPTER.digest(disk)
                sim_record = record(ADAPTER.BUNDLE, "executable", "sha256:" + "a" * 64)
                os_record = record(ADAPTER.DISK, "disk-image", observed)
                os_record["observed"][0]["disk"] = {"payloadDigest": observed, "size": disk.stat().st_size,
                    "format": "qcow2", "encryption": "none", "outcome": "succeeded"}
                disk_fields = os_record["observed"][0]["disk"]
                manifest = sim_record["observed"][0]["digest"]
                out = root / "probe"
                if case == "wrong disk bytes":
                    disk.write_bytes(b"different")
                if case == "wrong size":
                    disk_fields["size"] += 1
                if case == "FDE":
                    disk_fields["encryption"] = "luks2"
                if case == "wrong format":
                    disk_fields["format"] = "raw"
                if case == "wrong manifest":
                    manifest = "sha256:" + "c" * 64
                if case == "disk symlink":
                    held = disk.with_suffix(".held")
                    disk.rename(held)
                    disk.symlink_to(held)
                if case == "output exists":
                    out.mkdir()
                if case == "output symlink":
                    out.symlink_to(root / "absent")
                if case == "output in run":
                    out = image / "probe"
                (sim / "lifecycle.json").write_text(json.dumps(sim_record))
                (image / "lifecycle.json").write_text(json.dumps(os_record))
                (layout / "index.json").write_text(json.dumps({"manifests": [{"digest": manifest}]}))
                if case == "exact":
                    admitted = ADAPTER.inputs(sim, image, out)
                    self.assertEqual(admitted[:3], (layout, disk, out))
                else:
                    with self.assertRaises(ADAPTER.SmokeError):
                        ADAPTER.inputs(sim, image, out)
                if case not in ("output exists", "output symlink"):
                    self.assertFalse(out.exists())

    def test_stop_and_join(self):
        for exited, timeout in ((True, False), (False, False), (False, True)):
            with self.subTest(exited=exited, timeout=timeout):
                process = Mock(pid=123)
                process.poll.return_value = 0 if exited else None
                process.wait.side_effect = [subprocess.TimeoutExpired("server", 40), 0] if timeout else [0]
                with patch.object(ADAPTER.os, "killpg") as kill:
                    ADAPTER.stop(process)
                if exited:
                    kill.assert_not_called()
                else:
                    self.assertEqual(kill.call_args_list[0].args, (123, signal.SIGTERM))
                    if timeout:
                        self.assertEqual(kill.call_args_list[1].args, (123, signal.SIGKILL))
                self.assertEqual(process.wait.call_count, 2 if timeout else 1)

    def test_cleanup_only_exact_owned_domain_even_when_api_delete_fails(self):
        identity = "a1b2c3d4-9999-4000-8000-000000000001"
        domain = "nico-vm-" + identity
        for leftover, api_failure in ((False, False), (True, False), (True, True)):
            with self.subTest(leftover=leftover, api_failure=api_failure), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                commands, deleted = [], False
                def run(argv, _label, timeout=30):
                    nonlocal deleted
                    commands.append(argv)
                    command = argv[3]
                    if command == "list":
                        return "unrelated\n" + (domain + "\n" if not deleted else "")
                    if command == "dumpxml":
                        return (f"<domain><name>{domain}</name><uuid>{identity}</uuid><devices><disk>"
                                f'<source file="{work}/var/instances/{identity}/disk.qcow2"/>'
                                "</disk></devices></domain>")
                    if command == "domstate":
                        return "running"
                    if command == "undefine":
                        deleted = True
                    return ""
                def cli(*args):
                    nonlocal deleted
                    self.assertEqual(args, ("instance", "delete", "--instance-id", identity))
                    if api_failure:
                        raise ADAPTER.SmokeError("API failed")
                    deleted = not leftover
                with patch.object(ADAPTER, "run", run), patch.object(ADAPTER.time, "monotonic", side_effect=[0, 31]):
                    if api_failure:
                        with self.assertRaises(ADAPTER.SmokeError):
                            ADAPTER.cleanup_vm(cli, identity, work)
                    else:
                        ADAPTER.cleanup_vm(cli, identity, work)
                self.assertTrue(deleted)
                for argv in commands:
                    if argv[3] in ("destroy", "undefine"):
                        self.assertEqual(argv[4], identity)
                self.assertEqual(any(argv[3] == "undefine" for argv in commands), leftover)

    def test_process_lookup_race_still_joins(self):
        process = Mock(pid=123)
        process.poll.return_value = None
        with patch.object(ADAPTER.os, "killpg", side_effect=ProcessLookupError):
            ADAPTER.stop(process)
        process.wait.assert_called_once_with(timeout=40)

    def test_cleanup_diagnostic_failure_still_deletes(self):
        identity = "a1b2c3d4-9999-4000-8000-000000000001"
        domain = "nico-vm-" + identity
        with tempfile.TemporaryDirectory() as temporary:
            state = {"exists": True}
            def run(argv, label, timeout=30):
                if argv[3] == "list":
                    return domain if state["exists"] else ""
                if argv[3] == "dumpxml":
                    raise ADAPTER.SmokeError("diagnostic failed")
                self.fail(argv)
            def cli(*_arguments):
                state["exists"] = False
            with patch.object(ADAPTER, "run", run):
                ADAPTER.cleanup_vm(cli, identity, Path(temporary))
            self.assertFalse(state["exists"])

    def test_ambiguous_create_stops_reconciliation_before_owned_sweep(self):
        events = []
        with patch.object(ADAPTER, "stop", side_effect=lambda _p: events.append("joined")), \
                patch.object(ADAPTER, "sweep_owned_domains", side_effect=lambda _w: events.append("owned sweep")), \
                patch.object(ADAPTER, "cleanup_vm") as cleanup:
            ADAPTER.finish(Mock(), Mock(), None, Path("/private/probe"))
        cleanup.assert_not_called()
        self.assertEqual(events, ["joined", "owned sweep"])

    def test_stop_failure_does_not_sweep_a_running_producer(self):
        with patch.object(ADAPTER, "stop", side_effect=ADAPTER.SmokeError("join failed")), \
                patch.object(ADAPTER, "sweep_owned_domains") as sweep:
            with self.assertRaises(ADAPTER.SmokeError):
                ADAPTER.finish(Mock(), Mock(), None, Path("/private/probe"))
        sweep.assert_not_called()

    def test_ambiguous_create_domain_is_removed_but_foreign_domain_is_not(self):
        identity = "a1b2c3d4-9999-4000-8000-000000000001"
        foreign = "a1b2c3d4-9999-4000-8000-000000000002"
        work = Path("/private/probe")
        commands = []
        def run(argv, label, timeout=30):
            commands.append(argv)
            if argv[3] == "list":
                return "nico-vm-" + identity + "\nnico-vm-" + foreign
            if argv[3] == "dumpxml":
                selected = argv[4].removeprefix("nico-vm-")
                root = work if selected == identity else Path("/someone/else")
                return (f"<domain><name>nico-vm-{selected}</name><uuid>{selected}</uuid><devices><disk>"
                        f'<source file="{root}/var/instances/{selected}/disk.qcow2"/>'
                        "</disk></devices></domain>")
            if argv[3] == "domstate":
                return "shut off"
            return ""
        with patch.object(ADAPTER, "run", run):
            ADAPTER.sweep_owned_domains(work)
        mutations = [argv for argv in commands if argv[3] in ("destroy", "undefine")]
        self.assertEqual(len(mutations), 1)
        self.assertEqual(mutations[0][4], identity)

    def test_inherited_nico_controls_are_not_forwarded(self):
        with patch.dict(ADAPTER.os.environ, {"NICO_VMS_QEMU_IMG_PATH": "/untrusted", "NICO_VMS_PROMTAIL_TARGETS_FILE": "/outside",
                                            "NICO_VMS_QEMU_SYSTEM_PATH": "/untrusted", "NICO_SIM_LOG_FORMAT": "text"}), \
                patch.object(ADAPTER.shutil, "which", side_effect=lambda name: "/usr/bin/" + name):
            env = ADAPTER.environment(Path("/private/probe"), Path("/private/probe/base.qcow2"), "nkdvm0", "172.30.0.0/24")
        self.assertEqual(env["NICO_VMS_QEMU_IMG_PATH"], "/usr/bin/qemu-img")
        self.assertEqual(env["NICO_VMS_PROMTAIL_TARGETS_FILE"], "/private/probe/promtail.json")
        self.assertEqual(env["NICO_SIM_LOG_FORMAT"], "json")
        self.assertNotIn("NICO_VMS_QEMU_SYSTEM_PATH", env)

    def test_mac_admission_does_not_run_tools(self):
        with patch.object(ADAPTER.platform, "system", return_value="Darwin"), patch.object(ADAPTER, "run") as run:
            with self.assertRaises(ADAPTER.SmokeError):
                ADAPTER.host("nkdvm0", "172.30.0.0/24")
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
