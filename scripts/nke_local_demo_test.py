# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Deploy-only orchestration negatives, not real tenant acceptance."""

import importlib.util
import io
import copy
import ast
import inspect
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("demo", Path(__file__).with_name("nke-local-demo.py"))
DEMO = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEMO)


class DemoTest(unittest.TestCase):
    def test_restore_cli_only_restores_recorded_bgp_intent_and_never_deploys(self):
        for bgp, finished in ((False, False), (True, False), (False, True)):
            with self.subTest(bgp=bgp, finished=finished):
                guard = Mock(finished=finished, bgp_intent=bgp)
                with patch.object(DEMO.sys, "argv", ["demo", "restore", "--work", "/original-work"]), \
                     patch.object(DEMO.MGMT, "native_environment", return_value={}), \
                     patch.object(DEMO, "admit_libvirt_environment", return_value={"HOME": "/native-home"}), \
                     patch.object(DEMO.NET.OwnedNetwork, "recover", return_value=guard) as recover, \
                     patch.object(DEMO, "restore_bgp") as routes, patch.object(DEMO, "probe") as deploy, \
                     patch.object(DEMO.sys, "stdout", io.StringIO()):
                    self.assertEqual(DEMO.main(), 0)
                recover.assert_called_once_with("/original-work", {"HOME": "/native-home"})
                deploy.assert_not_called()
                self.assertEqual(routes.call_count, int(bgp and not finished))
                self.assertEqual(guard.close.call_count, int(not finished))
                guard.lock.close.assert_called_once_with()

    def test_restore_cli_active_guest_prevents_route_and_control_mutation(self):
        guard = Mock(finished=False, bgp_intent=True)
        guard.require_idle.side_effect = DEMO.VM.SmokeError("unknown live guest")
        with patch.object(DEMO.sys, "argv", ["demo", "restore", "--work", "/original-work"]), \
             patch.object(DEMO.MGMT, "native_environment", return_value={}), \
             patch.object(DEMO, "admit_libvirt_environment", return_value={}), \
             patch.object(DEMO.NET.OwnedNetwork, "recover", return_value=guard), \
             patch.object(DEMO, "restore_bgp") as routes, patch.object(DEMO.sys, "stderr", io.StringIO()):
            self.assertEqual(DEMO.main(), 17)
        routes.assert_not_called()
        guard.close.assert_not_called()
        guard.lock.close.assert_called_once_with()

    def test_cli_default_allows_measured_native_tcg_provisioning_budget(self):
        arguments = ["demo"]
        for name in ("nke-checkout", "nkdctl", "tools-run", "site-run", "components-run", "profiles-run",
                     "provider-run", "simulator-run", "os-run", "out"):
            arguments.extend(["--" + name, "/unused"])
        with patch.object(DEMO.sys, "argv", arguments), patch.object(DEMO, "probe") as probe, \
             patch.object(DEMO.signal, "signal"):
            self.assertEqual(DEMO.main(), 0)
        self.assertEqual(probe.call_args.args[0].timeout, 3600)

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

    def test_only_final_demo_pass_follows_cleanup_and_subject_recheck(self):
        # Source-order contract complements the executable cleanup matrix below;
        # it is not a substitute for real tenant/workload acceptance.
        body = ast.parse(inspect.getsource(DEMO.probe)).body[0].body
        passes = [node for statement in body for node in ast.walk(statement)
                  if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "print"
                  and node.args and isinstance(node.args[0], ast.Constant)
                  and str(node.args[0].value).startswith("PASS:")]
        self.assertEqual(len(passes), 1)
        positions = [index for index, statement in enumerate(body)
                     if isinstance(statement, ast.Expr) and statement.value is passes[0]]
        self.assertEqual(len(positions), 1)
        cleanup = [node for statement in body for node in ast.walk(statement)
                   if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "cleanup_runtime"]
        self.assertEqual(len(cleanup), 1)
        self.assertLess(cleanup[0].lineno, passes[0].lineno)
        recheck = body[positions[0] - 1]
        self.assertIsInstance(recheck, ast.If)
        self.assertTrue(any(isinstance(node, ast.Raise) for node in ast.walk(recheck)))
        self.assertLess(cleanup[0].lineno, recheck.lineno)

    def test_seed_projection_preserves_source_and_changes_only_selected_memory(self):
        with tempfile.TemporaryDirectory() as temporary:
            simulator = Path(temporary)
            source = simulator / "internal/seed/default-seed.yaml"
            source.parent.mkdir(parents=True)
            # Byte-projection contract only; NICoSim owns full YAML validation.
            raw = b"# unchanged prefix\n" + DEMO.LAPTOP_INSTANCE + b"# unchanged suffix\n"
            source.write_bytes(raw)
            digest = DEMO.hashlib.sha256(raw).hexdigest()
            with patch.object(DEMO, "SEED_SHA256", digest):
                record = DEMO.project_laptop_seed(simulator)
            self.assertEqual(source.read_bytes(), raw)
            projected = (simulator / "laptop-seed.yaml").read_bytes()
            self.assertEqual(projected, raw.replace(b"mem6gb", b"mem8gb").replace(b"memory_gib: 6", b"memory_gib: 8"))
            self.assertEqual(record["sourceDigest"], "sha256:" + digest)
            self.assertEqual(record["runtimeDigest"], "sha256:" + DEMO.hashlib.sha256(projected).hexdigest())
            self.assertEqual(record["memoryGiB"], 8)
            self.assertEqual((simulator / "laptop-seed.yaml").stat().st_mode & 0o777, 0o600)
            with patch.object(DEMO, "SEED_SHA256", digest), self.assertRaises(FileExistsError):
                DEMO.project_laptop_seed(simulator)

    def test_seed_projection_rejects_changed_or_ambiguous_source_before_output(self):
        for case in ("changed", "duplicate", "missing", "symlink", "fifo", "oversize", "foreign"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                simulator = Path(temporary)
                source = simulator / "internal/seed/default-seed.yaml"
                source.parent.mkdir(parents=True)
                raw = DEMO.LAPTOP_INSTANCE
                if case == "duplicate":
                    raw *= 2
                elif case == "missing":
                    raw = b"# missing\n"
                elif case == "oversize":
                    raw += b"x" * 65536
                if case == "symlink":
                    source.symlink_to(simulator)
                elif case == "fifo":
                    os.mkfifo(source, 0o600)
                else:
                    source.write_bytes(raw)
                real_stat = DEMO.os.fstat
                def observed(fd):
                    value = real_stat(fd)
                    return Mock(st_mode=value.st_mode, st_uid=os.getuid()+1, st_size=value.st_size) if case == "foreign" else value
                with patch.object(DEMO.os, "fstat", side_effect=observed), \
                     patch.object(DEMO, "SEED_SHA256", "f" * 64 if case == "changed" else DEMO.hashlib.sha256(raw).hexdigest()):
                    with self.assertRaises((DEMO.VM.SmokeError, OSError)):
                        DEMO.project_laptop_seed(simulator)
                self.assertFalse((simulator / "laptop-seed.yaml").exists())

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
                        expected = (DEMO.MGMT.UnjoinedProcess if phase in ("unjoined", "unjoined-close") else
                                    DEMO.MGMT.RetainedControls if phase == "revoke" else DEMO.VM.SmokeError)
                        with self.assertRaises(expected):
                            DEMO.consume_tenant(guard, "owned", None, None, {}, 300, None, {}, None)
                if phase == "unjoined":
                    self.assertEqual(events, ["admission", "run"])
                elif phase == "unjoined-close":
                    self.assertEqual(events, ["admission", "run", "close"])
                else:
                    self.assertEqual(events[-2:], ["close", "revoke"])

    def test_daemons_are_joined_before_any_host_restoration_even_when_refused(self):
        for phase in ("success", "retained", "foreign-domain", "bgp", "server", "http", "dns"):
            with self.subTest(phase=phase):
                events = []
                server, http, guard = Mock(), Mock(), Mock()
                def step(name):
                    events.append(name)
                    if phase == name:
                        raise DEMO.MGMT.UnjoinedProcess(name) if name in ("server", "http", "dns") else DEMO.VM.SmokeError(name)
                guard.stop_dns.side_effect = lambda: step("dns")
                guard.close.side_effect = lambda: step("close")
                with patch.object(DEMO.MGMT, "stop_group", side_effect=lambda process: step("server" if process is server else "http")), \
                     patch.object(DEMO.VM, "sweep_owned_domains", side_effect=lambda *_args: step("sweep")), \
                     patch.object(DEMO.VM, "domain_inventory", return_value=["foreign"] if phase == "foreign-domain" else []), \
                     patch.object(DEMO, "restore_bgp", side_effect=lambda *_args: step("bgp")):
                    if phase in ("success", "retained"):
                        DEMO.cleanup_runtime(server, http, guard, Path("owned"), Mock(), True, phase != "retained")
                    else:
                        with self.assertRaises(DEMO.MGMT.UnjoinedProcess if phase in ("server", "http", "dns") else DEMO.VM.SmokeError):
                            DEMO.cleanup_runtime(server, http, guard, Path("owned"), Mock(), True, True)
                self.assertEqual(events[:3], ["server", "http", "dns"])
                if phase in ("retained", "server", "http", "dns"):
                    self.assertEqual(events, ["server", "http", "dns"])
                elif phase == "success":
                    self.assertEqual(events, ["server", "http", "dns", "sweep", "bgp", "close"])
                else:
                    self.assertNotIn("close", events)

    def test_refused_host_restoration_still_joins_real_daemon_groups(self):
        processes = []
        for _ in range(3):
            process = DEMO.subprocess.Popen([DEMO.sys.executable, "-c", "import time; time.sleep(60)"],
                                           start_new_session=True)
            processes.append(process)
            self.addCleanup(DEMO.MGMT.stop_group, process)
        guard = Mock()
        guard.stop_dns.side_effect = lambda: DEMO.MGMT.stop_group(processes[2])
        with patch.object(DEMO.VM, "sweep_owned_domains"), patch.object(DEMO.VM, "domain_inventory", return_value=[]), \
             patch.object(DEMO, "restore_bgp", side_effect=DEMO.VM.SmokeError("foreign route")):
            with self.assertRaisesRegex(DEMO.VM.SmokeError, "foreign route"):
                DEMO.cleanup_runtime(processes[0], processes[1], guard, Path("owned"), Mock(), True, True)
        guard.close.assert_not_called()
        for process in processes:
            self.assertIsNotNone(process.returncode)
            self.assertFalse(DEMO.MGMT.group_alive(process.pid))

    def test_request_uses_current_control_plane_contract_and_real_vm_seed(self):
        request = DEMO.tenant_request("nkd-tenant-owned")
        self.assertEqual(request["nico"]["siteID"], DEMO.VM.SITE)
        self.assertEqual(request["controlPlaneConfig"]["desiredCount"], 1)
        self.assertEqual(request["controlPlaneConfig"]["nico"]["instanceTypePreference"], [DEMO.TYPE])
        self.assertEqual(len(request["nodePools"]), 1)
        self.assertFalse(request["nodePools"][0]["controlPlane"])
        self.assertEqual(request["nodePools"][0]["nico"]["osProfile"], DEMO.PROFILE)

    def test_tenant_success_gates_reject_wrong_nodes_vip_and_workload(self):
        identifier = "00000000-0000-4000-8000-000000000001"
        cases = ("success", "extra-node", "missing-node", "not-ready", "arch", "version",
                 "no-control-plane", "two-control-planes", "vip", "workload-log", "image-id", "extra-container")
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                simulator = Mock()
                simulator.poll.return_value = None
                tenant = DEMO.Tenant("owned", Path("tools"), work, {"SITE_KUBECONFIG": "site"}, 300, simulator, {})
                row = {"id": identifier, "name": "owned", "nico": DEMO.tenant_request("owned")["nico"]}
                node = {"metadata": {"labels": {}}, "status": {"nodeInfo": {
                    "architecture": "arm64", "kubeletVersion": "v1.34.2"},
                    "conditions": [{"type": "Ready", "status": "True"}]}}
                nodes = [copy.deepcopy(node), copy.deepcopy(node)]
                nodes[0]["metadata"]["labels"]["node-role.kubernetes.io/control-plane"] = ""
                if case == "extra-node":
                    nodes.append(copy.deepcopy(node))
                elif case == "missing-node":
                    nodes.pop()
                elif case == "not-ready":
                    nodes[1]["status"]["conditions"][0]["status"] = "False"
                elif case in ("arch", "version"):
                    nodes[1]["status"]["nodeInfo"]["architecture" if case == "arch" else "kubeletVersion"] = "wrong"
                elif case == "no-control-plane":
                    nodes[0]["metadata"]["labels"] = {}
                elif case == "two-control-planes":
                    nodes[1]["metadata"]["labels"] = dict(nodes[0]["metadata"]["labels"])
                config = {"clusters": [{"cluster": {"server": "https://" + DEMO.VIP + ":6443"}}]}
                if case == "vip":
                    config["clusters"][0]["cluster"]["server"] = "https://foreign:6443"
                states = [{"imageID": DEMO.WORKLOAD_IMAGE if case != "image-id" else "wrong"}]
                if case == "extra-container":
                    states *= 2
                pods = {"items": [{"status": {"containerStatuses": states}}]}
                inventory_calls = 0
                def capture(_argv, phase, *_args):
                    nonlocal inventory_calls
                    if phase == "inventory":
                        inventory_calls += 1
                        return json.dumps([] if inventory_calls == 1 else [row])
                    if phase == "kubeconfig-secret":
                        return '{"metadata": {"name": "held"}}'
                    if phase == "tenant-config":
                        return json.dumps(config)
                    if phase == "nodes":
                        return json.dumps({"items": nodes})
                    if phase == "workload-log":
                        return "wrong" if case == "workload-log" else "NKE_LOCAL_WORKLOAD_PASS\n"
                    if phase == "workload-pod":
                        return json.dumps(pods)
                    return ""
                with patch.object(tenant, "capture", side_effect=capture), \
                     patch.object(DEMO.time, "monotonic", side_effect=[0, 1, 2, 1000]), \
                     patch.object(DEMO.time, "sleep"), patch.object(DEMO, "vm_evidence", return_value=["two held domains"]):
                    if case == "success":
                        tenant.run()
                        result = json.loads((work / "tenant-result.json").read_text())
                        self.assertEqual(result["tenantID"], identifier)
                        self.assertEqual(result["nodes"], nodes)
                        self.assertEqual(result["workload"], pods)
                    else:
                        with self.assertRaises(DEMO.VM.SmokeError):
                            tenant.run()
                        self.assertFalse((work / "tenant-result.json").exists())

    def test_vm_evidence_rejects_unowned_or_substituted_backing_disk(self):
        names = ["nico-vm-00000000-0000-4000-8000-000000000001", "nico-vm-00000000-0000-4000-8000-000000000002"]
        for case in ("success", "count", "foreign", "tpm", "os", "path", "symlink", "size", "digest"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary).resolve()
                cache = work / "cache"
                cache.mkdir()
                base = cache / "base.qcow2"
                base.write_bytes(b"held")
                image = {"digest": DEMO.VM.digest(base), "disk": {"size": 4}}
                original = base
                if case == "path":
                    original = work / "foreign.qcow2"
                    original.write_bytes(b"held")
                elif case == "symlink":
                    original = cache / "link"
                    original.symlink_to(base)
                elif case == "size":
                    image["disk"]["size"] = 5
                elif case == "digest":
                    image["digest"] = "sha256:" + "0" * 64
                xml = "<domain><os>" + ("" if case == "os" else "<type>hvm</type>") + "</os><devices>" + (
                    "" if case == "tpm" else "<tpm/>") + "</devices></domain>"
                runner = Mock(return_value=json.dumps({"full-backing-filename": str(original)}))
                with patch.object(DEMO.VM, "domain_inventory", return_value=names[:1] if case == "count" else names), \
                     patch.object(DEMO.VM, "owned_domain", return_value=None if case == "foreign" else ("id", xml)):
                    if case == "success":
                        result = DEMO.vm_evidence(work, image, runner)
                        self.assertEqual(len(result), 2)
                        self.assertTrue(all(row["backingDigest"] == image["digest"] for row in result))
                    else:
                        with self.assertRaises(DEMO.VM.SmokeError):
                            DEMO.vm_evidence(work, image, runner)

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
            self.assertEqual(env["NICO_SIM_SEED_PATH"], str(work / "laptop-seed.yaml"))
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
            DEMO.write_nico_header(work.parent, "private-test-token")
            tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {},
                                 diagnostic_url="http://172.18.0.1:18089")
            tenant.intent = True
            with patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-" + identifier]), \
                 patch.object(DEMO.VM, "owned_domain", return_value=(identifier, "xml")), \
                 patch.object(tenant, "capture", return_value="private journal") as capture:
                tenant.preserve_guest_diagnostics()
            self.assertEqual(len(capture.call_args_list), 3)
            for call in capture.call_args_list:
                self.assertEqual(call.args[2], 32)
                self.assertEqual(call.args[0][:10], ["curl", "-q", "--noproxy", "*", "--max-time", "30", "--max-filesize", "1048576", "--fail", "--silent"])
                self.assertTrue(call.args[0][-1].startswith(tenant.diagnostic_url + "/v1/orgs/novxwn552ssc/instances/" + identifier))
                self.assertNotIn("follow", call.args[0][-1])
                self.assertIn("@" + str(work.parent / "nico-api-header"), call.args[0])
                self.assertNotIn("private-test-token", " ".join(call.args[0]))
            for path in work.iterdir():
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_failed_or_foreign_guest_diagnostics_do_not_block_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / "site"
            work.mkdir()
            DEMO.write_nico_header(work.parent, "private-test-token")
            tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {},
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

    def test_guest_diagnostics_use_bearer_header_with_real_http_transport(self):
        identifier = "00000000-0000-4000-8000-000000000001"
        requests = []
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                accepted = self.headers.get("Authorization") == "Bearer private-test-token"
                requests.append((self.path, accepted))
                self.send_response(200 if accepted else 401)
                self.end_headers()
                self.wfile.write(b"private journal" if accepted else b"missing bearer token")

            def log_message(self, *_args):
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary) / "site"
                work.mkdir()
                DEMO.write_nico_header(work.parent, "private-test-token")
                url = "http://127.0.0.1:" + str(server.server_port)
                config = work.parent / "curl-home"
                config.mkdir()
                (config / ".curlrc").write_text('url = "' + url + '/unexpected"\nverbose\n')
                tenant = DEMO.Tenant("owned", Path("tools"), work, {"CURL_HOME": str(config)}, 300, Mock(), {},
                    diagnostic_url=url)
                tenant.intent = True
                with self.assertRaises(DEMO.VM.SmokeError) as rejected:
                    tenant.capture(["curl", "-q", "--noproxy", "*", "--max-time", "2", "--fail", "--silent",
                                    tenant.diagnostic_url], "missing-auth", 4)
                self.assertEqual(rejected.exception.returncode, 22)
                with patch.object(DEMO.VM, "domain_inventory", return_value=["nico-vm-" + identifier]), \
                     patch.object(DEMO.VM, "owned_domain", return_value=(identifier, "xml")), \
                     patch.object(tenant, "capture", wraps=tenant.capture) as capture:
                    tenant.preserve_guest_diagnostics()
                self.assertEqual(len(capture.call_args_list), 3)
                self.assertFalse(any("private-test-token" in " ".join(call.args[0]) for call in capture.call_args_list))
                for item in ("agent-logs", "kubelet-logs", "containerd-logs"):
                    path = work / (identifier + "." + item + ".log")
                    self.assertEqual(path.read_text(), "private journal")
                    self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertEqual([accepted for _, accepted in requests], [False, True, True, True])
                self.assertFalse(any(path == "/unexpected" for path, _ in requests))
                for path in work.glob("tenant-*.log"):
                    self.assertNotIn("private-test-token", path.read_text())
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive())

    def test_diagnostic_header_rejects_unsafe_files_without_blocking_cleanup(self):
        for case in ("missing", "symlink", "fifo", "public", "oversize", "foreign", "hardlink"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary) / "site"
                work.mkdir()
                header = work.parent / "nico-api-header"
                if case == "symlink":
                    header.symlink_to(work)
                elif case == "fifo":
                    os.mkfifo(header, 0o600)
                elif case != "missing":
                    DEMO.write_nico_header(work.parent, "x" * 300 if case == "oversize" else "private-test-token")
                    if case == "public":
                        header.chmod(0o644)
                    elif case == "hardlink":
                        os.link(header, work / "alias")
                tenant = DEMO.Tenant("owned", Path("tools"), work, {}, 300, Mock(), {},
                                     diagnostic_url="http://127.0.0.1:18089")
                tenant.intent = True
                real_stat = DEMO.os.fstat
                def observed(fd):
                    value = real_stat(fd)
                    return Mock(st_mode=value.st_mode, st_uid=os.getuid() + 1, st_nlink=value.st_nlink,
                                st_size=value.st_size) if case == "foreign" else value
                with patch.object(DEMO.os, "fstat", side_effect=observed), \
                     patch.object(tenant, "preserve_consoles"), patch.object(tenant, "capture") as capture, \
                     patch.object(tenant, "restore") as restore:
                    tenant.close()
                restore.assert_called_once()
                capture.assert_not_called()

    def test_diagnostic_header_writer_never_overwrites_existing_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            DEMO.write_nico_header(work, "private-test-token")
            with self.assertRaises(FileExistsError):
                DEMO.write_nico_header(work, "replacement")
            self.assertEqual((work / "nico-api-header").read_text(), "Authorization: Bearer private-test-token\n")

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
