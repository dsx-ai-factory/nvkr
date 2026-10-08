# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Deploy-only orchestration negatives, not real tenant acceptance."""

import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("demo", Path(__file__).with_name("nke-local-demo.py"))
DEMO = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEMO)


class DemoTest(unittest.TestCase):
    def test_host_admission_precedes_output_network_and_process_work(self):
        with patch.object(DEMO.SITE, "admit_materials", side_effect=DEMO.VM.SmokeError("host")), \
             patch.object(DEMO.VM, "inputs") as inputs, patch.object(DEMO.NET, "OwnedNetwork") as guard, \
             patch.object(DEMO.subprocess, "Popen") as process:
            with self.assertRaises(DEMO.VM.SmokeError):
                DEMO.probe(Mock())
            inputs.assert_not_called()
            guard.assert_not_called()
            process.assert_not_called()

    def test_all_owners_share_unjoined_failure_identity(self):
        self.assertIs(DEMO.NET.MGMT.UnjoinedProcess, DEMO.MGMT.UnjoinedProcess)
        self.assertIs(DEMO.NET.VM, DEMO.VM)

    def test_guest_route_binds_owned_node_and_only_pinned_spire_ports(self):
        guard = Mock()
        row = {"Id": "owned", "NetworkSettings": {"Networks": {"kind": {"IPAddress": "172.18.0.2"}}}}
        with patch.object(DEMO.MGMT, "owned_nodes", return_value=["owned"]), \
             patch.object(DEMO.MGMT, "inspect", return_value=json.dumps([row])):
            DEMO.admit_guest_management(guard, "cluster", {})
        guard.allow_guest_tcp.assert_called_once_with("172.18.0.2", (30440, 30443))
        for nodes, rows in (([], []), (["one", "two"], []), (["owned"], []), (["owned"], [dict(row, Id="foreign")])):
            with self.subTest(nodes=nodes, rows=rows), patch.object(DEMO.MGMT, "owned_nodes", return_value=nodes), \
                 patch.object(DEMO.MGMT, "inspect", return_value=json.dumps(rows)):
                guard.reset_mock()
                with self.assertRaises(DEMO.VM.SmokeError):
                    DEMO.admit_guest_management(guard, "cluster", {})
                guard.allow_guest_tcp.assert_not_called()

    def test_guest_rules_are_revoked_before_returning_to_management_cleanup(self):
        for phase in ("success", "admission", "run", "close", "unjoined", "unjoined-close", "revoke"):
            with self.subTest(phase=phase):
                events = []
                guard, tenant = Mock(), Mock()
                def step(name):
                    events.append(name)
                    if phase == name:
                        raise DEMO.VM.SmokeError(name)
                    if phase == "unjoined" and name == "run":
                        raise DEMO.MGMT.UnjoinedProcess("producer still running")
                    if phase == "unjoined-close" and name == "close":
                        raise DEMO.MGMT.UnjoinedProcess("cleanup child still running")
                tenant.run.side_effect = lambda: step("run")
                tenant.close.side_effect = lambda: step("close")
                guard.revoke_guest_tcp.side_effect = lambda: step("revoke")
                with patch.object(DEMO, "Tenant", return_value=tenant), \
                     patch.object(DEMO, "admit_guest_management", side_effect=lambda *args: step("admission")):
                    if phase == "success":
                        DEMO.consume_tenant(guard, "owned", None, None, {}, 300, None, {}, None)
                    else:
                        expected = DEMO.MGMT.UnjoinedProcess if phase in ("unjoined", "unjoined-close", "revoke") else DEMO.VM.SmokeError
                        with self.assertRaises(expected):
                            DEMO.consume_tenant(guard, "owned", None, None, {}, 300, None, {}, None)
                if phase == "unjoined":
                    self.assertEqual(events, ["admission", "run"])
                elif phase == "unjoined-close":
                    self.assertEqual(events, ["admission", "run", "close"])
                else:
                    self.assertEqual(events[-2:], ["close", "revoke"])

    def test_request_uses_current_control_plane_contract_and_real_vm_seed(self):
        request = DEMO.tenant_request("nkd-tenant-owned")
        self.assertEqual(request["nico"]["siteID"], DEMO.VM.SITE)
        self.assertEqual(request["controlPlaneConfig"]["desiredCount"], 1)
        self.assertEqual(request["controlPlaneConfig"]["nico"]["instanceTypePreference"], [DEMO.TYPE])
        self.assertEqual(len(request["nodePools"]), 1)
        self.assertFalse(request["nodePools"][0]["controlPlane"])
        self.assertEqual(request["nodePools"][0]["nico"]["osProfile"], DEMO.PROFILE)

    def test_tenant_inventory_rejects_foreign_duplicate_and_changed_identity(self):
        identifier = "00000000-0000-4000-8000-000000000001"
        row = {"id": identifier, "name": "owned", "nico": DEMO.tenant_request("owned")["nico"]}
        for case in ("exact", "empty", "null", "wrong shape", "duplicate", "wrong provider", "changed ID"):
            with self.subTest(case=case):
                rows = [dict(row)]
                expected = identifier
                if case == "empty":
                    rows = []
                elif case == "null":
                    rows = None
                elif case == "wrong shape":
                    rows = {}
                elif case == "duplicate":
                    rows *= 2
                elif case == "wrong provider":
                    rows[0]["nico"] = {"orgID": "foreign"}
                elif case == "changed ID":
                    expected = "00000000-0000-4000-8000-000000000002"
                if case in ("exact", "empty", "null"):
                    value = DEMO.selected_tenant(rows, "owned", expected)
                    self.assertEqual(value, row if case == "exact" else None)
                else:
                    with self.assertRaises(DEMO.VM.SmokeError):
                        DEMO.selected_tenant(rows, "owned", expected)

    def test_cleanup_never_deletes_substituted_tenant(self):
        with tempfile.TemporaryDirectory() as temporary:
            tenant = DEMO.Tenant("owned", Path("tools"), Path(temporary), {}, 300, Mock(), {})
            tenant.intent = True
            tenant.id = "00000000-0000-4000-8000-000000000001"
            row = {"id": "00000000-0000-4000-8000-000000000002", "name": "owned",
                   "nico": DEMO.tenant_request("owned")["nico"]}
            with patch.object(tenant, "capture", return_value=json.dumps([row])) as capture:
                with self.assertRaises(DEMO.VM.SmokeError):
                    tenant.close()
                self.assertEqual(len(capture.call_args_list), 1)
                self.assertNotIn("delete", capture.call_args.args[0])

    def test_bgp_cleanup_rejects_foreign_route_before_any_delete(self):
        for row in ({"dst": "172.31.0.0/24", "dev": "foreign"},
                    {"dst": DEMO.VIP, "gateway": "172.30.0.2", "dev": "foreign"},
                    {"dst": DEMO.VIP, "gateway": "8.8.8.8", "dev": DEMO.NET.BRIDGE},
                    {"dst": DEMO.NET.VIP_CIDR, "type": "blackhole", "protocol": "static", "metric": 123},
                    {"dst": DEMO.VIP, "gateway": "172.30.0.2", "dev": DEMO.NET.BRIDGE, "metric": 123},
                    {"dst": "172.31.0.11", "gateway": "172.30.0.2", "dev": DEMO.NET.BRIDGE}):
            with self.subTest(row=row):
                guard = Mock()
                guard.command.return_value = json.dumps([row])
                with self.assertRaises(DEMO.VM.SmokeError):
                    DEMO.restore_bgp(guard)
                self.assertEqual(len(guard.command.call_args_list), 1)

    def test_bgp_cleanup_removes_only_exact_covering_and_bound_vip_routes(self):
        routes = [{"dst": DEMO.NET.VIP_CIDR, "type": "blackhole", "flags": []},
                  {"dst": DEMO.VIP, "nexthops": [{"dev": DEMO.NET.BRIDGE, "gateway": "172.30.0.2", "weight": 1}]}]
        guard = Mock()
        guard.command.side_effect = [json.dumps(routes), json.dumps(routes), "", json.dumps(routes[1:]), "", "[]"]
        DEMO.restore_bgp(guard)
        deleted = [call.args[0] for call in guard.command.call_args_list if "delete" in call.args[0]]
        self.assertEqual(deleted, [["ip", "route", "delete", "blackhole", DEMO.NET.VIP_CIDR, "table", "main"],
                                   ["ip", "route", "delete", DEMO.VIP, "table", "main"]])

    def test_bgp_route_replacement_during_cleanup_fails_closed(self):
        guard = Mock()
        guard.command.side_effect = [json.dumps([{"dst": DEMO.NET.VIP_CIDR, "type": "blackhole"}]), "[]"]
        with self.assertRaises(DEMO.VM.SmokeError):
            DEMO.restore_bgp(guard)
        self.assertFalse(any("delete" in call.args[0] for call in guard.command.call_args_list))

    def test_tenant_cleanup_uses_volatile_capture_and_suppresses_repeated_interrupts(self):
        with tempfile.TemporaryDirectory() as temporary:
            tenant = DEMO.Tenant("owned", Path("tools"), Path(temporary), {}, 300, Mock(), {})
            tenant.intent = True
            with patch.object(DEMO.MGMT, "capture", return_value="[]") as capture, \
                 patch.object(DEMO.signal, "signal", return_value="old") as signals:
                tenant.close()
            self.assertTrue(capture.call_args.kwargs["volatile"])
            self.assertEqual([call.args[1] for call in signals.call_args_list],
                             [DEMO.signal.SIG_IGN, DEMO.signal.SIG_IGN, "old", "old"])

    def test_bgp_cleanup_sets_volatile_mode_before_any_command(self):
        guard = Mock(cleaning=False)
        def command(_argv, *_args):
            self.assertTrue(guard.cleaning)
            return "[]"
        guard.command.side_effect = command
        DEMO.restore_bgp(guard)

    def test_console_retention_precedes_cleanup_and_storage_failure_does_not_block_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / "site"
            work.mkdir()
            identifier = "00000000-0000-4000-8000-000000000001"
            source = work.parent / "simulator/var/instances" / identifier
            source.mkdir(parents=True)
            (source / "disk.qcow2.ttyS0.log").write_bytes(b"x" * (4 * 1024**2) + b"tail")
            (source / "disk.qcow2.ttyS1.log").write_bytes(b"")
            tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {})
            tenant.intent = True
            with patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-" + identifier]), \
                 patch.object(DEMO.VM, "owned_domain", return_value=(identifier, "xml")), \
                 patch.object(tenant, "restore") as restore:
                tenant.close()
                saved = work / (identifier + ".ttyS0.log")
                self.assertEqual(saved.stat().st_size, 4 * 1024**2)
                self.assertTrue(saved.read_bytes().endswith(b"tail"))
                restore.assert_called_once()
                # Exclusive output collision is a diagnostic failure, not an
                # excuse to strand the exact owned tenant.
                tenant.close()
                self.assertEqual(restore.call_count, 2)
            with patch.object(DEMO.VM, "domain_inventory", side_effect=DEMO.MGMT.UnjoinedProcess("unjoined")), \
                 patch.object(tenant, "restore") as restore:
                with self.assertRaises(DEMO.MGMT.UnjoinedProcess):
                    tenant.close()
                restore.assert_not_called()

    def test_simulator_env_rejects_ambient_loader_home_and_runtime_settings(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            with patch.dict(DEMO.os.environ, {"HOME": "/foreign", "XDG_RUNTIME_DIR": "/foreign", "LD_PRELOAD": "/foreign",
                                               "NICO_VMS_VM_BACKEND": "mock"}):
                env = DEMO.simulator_environment(work, work / "disk", work, "172.18.0.1",
                    {"HOME": "/admitted-account", "XDG_RUNTIME_DIR": "/run/user/" + str(os.getuid())})
            self.assertEqual(env["HOME"], "/admitted-account")
            self.assertEqual(env["XDG_RUNTIME_DIR"], "/run/user/" + str(os.getuid()))
            self.assertEqual(env["NICO_VMS_VM_BACKEND"], "libvirt")
            self.assertEqual(env["NICO_VMS_DIAG_SSH_ENABLED"], "true")
            self.assertEqual(env["NICO_VMS_DIAG_SSH_USER"], "nkd-demo")
            self.assertEqual(len(env["NICO_VMS_DIAG_SSH_PASSWORD"]), 48)
            self.assertNotEqual(env["NICO_VMS_DIAG_SSH_PASSWORD"], "nico-diag")
            self.assertNotIn("LD_PRELOAD", env)

    def test_guest_diagnostics_are_bounded_private_and_owned(self):
        identifier = "00000000-0000-4000-8000-000000000001"
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / "site"
            work.mkdir()
            tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {},
                                 diagnostic_url="http://172.18.0.1:18089")
            tenant.intent = True
            with patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-" + identifier]), \
                 patch.object(DEMO.VM, "owned_domain", return_value=(identifier, "xml")), \
                 patch.object(tenant, "capture", return_value="private journal") as capture:
                tenant.preserve_guest_diagnostics()
            self.assertEqual(len(capture.call_args_list), 3)
            for call in capture.call_args_list:
                self.assertEqual(call.args[2], 12)
                self.assertEqual(call.args[0][:9], ["curl", "--noproxy", "*", "--max-time", "10", "--max-filesize", "1048576", "--fail", "--silent"])
                self.assertTrue(call.args[0][-1].startswith(tenant.diagnostic_url + "/v1/orgs/novxwn552ssc/instances/" + identifier))
                self.assertNotIn("follow", call.args[0][-1])
            for path in work.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_failed_or_foreign_guest_diagnostics_do_not_block_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            tenant = DEMO.Tenant("owned", Path("tools"), Path(temporary), {}, 300, Mock(), {},
                                 diagnostic_url="http://172.18.0.1:18089")
            tenant.intent = True
            for case in ("foreign", "transport", "storage", "oversize", "unjoined"):
                with self.subTest(case=case), \
                     patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-00000000-0000-4000-8000-000000000001"]), \
                     patch.object(DEMO.VM, "owned_domain", return_value=None if case == "foreign" else ("id", "xml")), \
                     patch.object(tenant, "capture") as capture, \
                     patch.object(DEMO.Path, "open", side_effect=OSError("read-only")), \
                     patch.object(tenant, "preserve_consoles"), patch.object(tenant, "restore") as restore:
                    if case == "transport":
                        capture.side_effect = DEMO.VM.SmokeError("unavailable")
                    elif case == "unjoined":
                        capture.side_effect = DEMO.MGMT.UnjoinedProcess("child did not join")
                    else:
                        capture.return_value = "x" * (1024**2 + 1) if case == "oversize" else "logs"
                    if case == "unjoined":
                        with self.assertRaises(DEMO.MGMT.UnjoinedProcess):
                            tenant.close()
                        restore.assert_not_called()
                    else:
                        tenant.close()
                        restore.assert_called_once()
                    if case == "foreign":
                        capture.assert_not_called()

    def test_console_fifo_is_rejected_without_blocking_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / "site"
            work.mkdir()
            identifier = "00000000-0000-4000-8000-000000000001"
            source = work.parent / "simulator/var/instances" / identifier
            source.mkdir(parents=True)
            os.mkfifo(source / "disk.qcow2.ttyS0.log", 0o600)
            tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {})
            tenant.intent = True
            with patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-" + identifier]), \
                 patch.object(DEMO.VM, "owned_domain", return_value=(identifier, "xml")), \
                 patch.object(tenant, "restore") as restore:
                tenant.close()
                restore.assert_called_once()
            self.assertFalse((work / (identifier + ".ttyS0.log")).exists())

    def test_simulator_env_rejects_foreign_runtime_ownership(self):
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(DEMO.Path, "lstat", return_value=Mock(st_mode=0o40700, st_uid=os.getuid() + 1)):
            with self.assertRaises(DEMO.VM.SmokeError):
                DEMO.admit_libvirt_environment({})

    def test_runtime_and_daemon_paths_admitted_before_privileged_setup(self):
        outputs = ({}, "172.18.0.1", "registry")
        with tempfile.TemporaryDirectory() as temporary, patch.object(DEMO.SITE, "admit_materials", return_value=outputs), \
             patch.object(DEMO.VM, "inputs", return_value=(Path("layout"), Path("disk"), Path(temporary), {}, {})), \
             patch.object(DEMO.shutil, "which", return_value="tool"), \
             patch.object(DEMO.shutil, "disk_usage", return_value=Mock(free=100*1024**3)), \
             patch.object(DEMO.Path, "stat", return_value=Mock(st_size=1)), \
             patch.object(DEMO, "admit_libvirt_environment", side_effect=DEMO.VM.SmokeError("foreign runtime")), \
             patch.object(DEMO.NET, "OwnedNetwork") as network:
            args = Mock(timeout=300)
            with self.assertRaisesRegex(DEMO.VM.SmokeError, "foreign runtime"):
                DEMO.probe(args)
            network.assert_not_called()

    def test_daemon_path_binding_accepts_defaults_and_rejects_foreign_namespace(self):
        for case in ("standard", "foreign", "duplicate", "oversize"):
            with self.subTest(case=case):
                raw = b"HOME=/native-account\0XDG_RUNTIME_DIR=/run/user/" + str(os.getuid()).encode() + b"\0"
                if case == "foreign":
                    raw += b"XDG_CONFIG_HOME=/foreign\0"
                elif case == "duplicate":
                    raw += b"HOME=/native-account\0"
                elif case == "oversize":
                    raw += b"x" * (1024**2)
                with patch.object(DEMO.pwd, "getpwuid", return_value=Mock(pw_dir="/native-account")), \
                     patch.object(DEMO.Path, "lstat", return_value=Mock(st_mode=0o40700, st_uid=os.getuid())), \
                     patch.object(DEMO.Path, "stat", return_value=Mock(st_uid=os.getuid())), \
                     patch.object(DEMO.Path, "open", return_value=io.BytesIO(raw)), \
                     patch.object(DEMO.MGMT, "inspect", return_value="7 libvirtd\n8 other\n"):
                    if case == "standard":
                        self.assertEqual(DEMO.admit_libvirt_environment({})["XDG_CONFIG_HOME"], "/native-account/.config")
                    else:
                        with self.assertRaises(DEMO.VM.SmokeError):
                            DEMO.admit_libvirt_environment({})


if __name__ == "__main__":
    unittest.main()
