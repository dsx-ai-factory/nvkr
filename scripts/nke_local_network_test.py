# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Host-guard negatives, not DHCP/BGP/tenant acceptance."""

import importlib.util
import os
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("network", Path(__file__).with_name("nke-local-network.py"))
NET = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NET)


class NetworkTest(unittest.TestCase):
    def test_platform_fails_before_lock_or_subprocess(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(NET.platform, "system", return_value="Darwin"), \
             patch.object(NET.os, "open") as opened, patch.object(NET.OwnedNetwork, "command") as command:
            with self.assertRaises(NET.VM.SmokeError):
                NET.OwnedNetwork(temporary, {}, "172.18.0.0/16").admit()
            opened.assert_not_called()
            command.assert_not_called()

    def test_privileged_helpers_have_shorter_root_deadline(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(NET.MGMT, "capture", return_value="ok") as capture:
            guard = NET.OwnedNetwork(temporary, {"PATH": "owned"}, "172.18.0.0/16")
            self.assertEqual(guard.command(["ip", "link", "show"], True), "ok")
            self.assertEqual(capture.call_args.args[0],
                             ["sudo", "-n", "timeout", "--signal=TERM", "--kill-after=5", "25", "ip", "link", "show"])
            self.assertEqual(capture.call_args.args[-1], 40)

    def test_rule_absence_does_not_hide_permission_or_unjoined_failures(self):
        for code in (1, 2, None):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
                failure = NET.VM.SmokeError("failed", code) if code else NET.MGMT.UnjoinedProcess("still running")
                with patch.object(guard, "command", side_effect=failure):
                    if code == 1:
                        self.assertFalse(guard.rule_exists(NET.rules("owner", guard.kind_cidr)[0]))
                    else:
                        with self.assertRaises(NET.VM.SmokeError):
                            guard.rule_exists(NET.rules("owner", guard.kind_cidr)[0])

    def test_each_rule_is_exactly_owner_bound_and_redirects_are_gateway_scoped(self):
        rows = NET.rules("private-owner", "172.18.0.0/16")
        self.assertEqual(len(rows), 12)
        for _table, _chain, argv in rows:
            self.assertEqual(argv[:4], ["-m", "comment", "--comment", "private-owner"])
        redirects = [argv for _, _, argv in rows if "REDIRECT" in argv]
        self.assertEqual(len(redirects), 2)
        for argv in redirects:
            self.assertEqual(argv[argv.index("-d") + 1], NET.GATEWAY)
            self.assertEqual(argv[argv.index("-i") + 1], NET.BRIDGE)

    def test_active_simulator_or_domain_prevents_restoration(self):
        for active in ("simulator", "domain"):
            with self.subTest(active=active), tempfile.TemporaryDirectory() as temporary:
                guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
                values = ["nico-sim"] if active == "simulator" else ["", "nico-vm-exact"]
                with patch.object(guard, "command", side_effect=values) as command:
                    with self.assertRaisesRegex(NET.VM.SmokeError, "refusing network"):
                        guard.close()
                    self.assertTrue(all(argv.args[0][0] in ("ps", "virsh") for argv in command.call_args_list))

    def test_guest_tcp_exception_is_exact_and_insertion_is_observed(self):
        with tempfile.TemporaryDirectory() as temporary:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            guard.bridge_intent = True
            with patch.object(guard, "bridge_binding", return_value="42"), \
                 patch.object(guard, "rule_exists", side_effect=[False, True, False, True]), \
                 patch.object(guard, "command") as command:
                guard.allow_guest_tcp("172.18.0.2", (30443, 30440))
            self.assertEqual(len(guard.rule_intents), 2)
            for index, port in enumerate((30440, 30443)):
                row = guard.rule_intents[index]
                self.assertEqual(row, ("raw", "PREROUTING", ["-m", "comment", "--comment", guard.owner,
                    "-i", NET.BRIDGE, "-s", NET.CIDR, "-d", "172.18.0.2", "-p", "tcp", "--dport", str(port), "-j", "ACCEPT"]))
                self.assertEqual(command.call_args_list[index].args[0], ["iptables", "-w", "5", "-t", "raw", "-I", "PREROUTING", *row[2]])

    def test_guest_tcp_exception_rejects_foreign_hosts_and_invalid_ports_before_work(self):
        for address, ports in (("8.8.8.8", (30440,)), ("172.18.0.0", (30440,)),
                ("172.18.255.255", (30440,)), ("172.18.0.2", ()), ("172.18.0.2", (True,)),
                ("172.18.0.2", (0,)), ("172.18.0.2", (65536,)), ("172.18.0.2", (30440, 30440))):
            with self.subTest(address=address, ports=ports), tempfile.TemporaryDirectory() as temporary:
                guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
                with patch.object(guard, "command") as command, patch.object(guard, "bridge_binding") as bridge:
                    with self.assertRaises(NET.VM.SmokeError):
                        guard.allow_guest_tcp(address, ports)
                    command.assert_not_called()
                    bridge.assert_not_called()
                    self.assertEqual(guard.rule_intents, [])

    def test_guest_tcp_exception_retains_ambiguous_insert_for_rollback(self):
        with tempfile.TemporaryDirectory() as temporary:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            guard.bridge_intent = True
            with patch.object(guard, "bridge_binding", return_value="42"), \
                 patch.object(guard, "rule_exists", return_value=False), \
                 patch.object(guard, "command", side_effect=NET.VM.SmokeError("insert failed")):
                with self.assertRaises(NET.VM.SmokeError):
                    guard.allow_guest_tcp("172.18.0.2", (30440,))
            self.assertEqual(len(guard.rule_intents), 1)
            with patch.object(guard, "bridge_binding", return_value=None), \
                 patch.object(guard, "rule_exists", side_effect=[True, False]), \
                 patch.object(guard, "command", return_value="") as command:
                guard.close()
            self.assertIn(["iptables", "-w", "5", "-t", "raw", "-D", "PREROUTING", *guard.rule_intents[0][2]],
                          [call.args[0] for call in command.call_args_list])

    def test_guest_tcp_revocation_only_withdraws_raw_rows_and_retains_intents(self):
        with tempfile.TemporaryDirectory() as temporary:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            row = ("raw", "PREROUTING", ["-m", "comment", "--comment", guard.owner,
                "-i", NET.BRIDGE, "-s", NET.CIDR, "-d", "172.18.0.2", "-p", "tcp", "--dport", "30440", "-j", "ACCEPT"])
            guard.rule_intents = [*NET.rules(guard.owner, guard.kind_cidr), row]
            intents = list(guard.rule_intents)
            with patch.object(guard, "rule_exists", side_effect=[True, False]), patch.object(guard, "command") as command:
                guard.revoke_guest_tcp()
            command.assert_called_once_with(["iptables", "-w", "5", "-t", "raw", "-D", "PREROUTING", *row[2]], True)
            self.assertEqual(guard.rule_intents, intents)
            self.assertFalse(guard.cleaning)
            with patch.object(guard, "rule_exists", return_value=True), patch.object(guard, "command"):
                with self.assertRaises(NET.VM.SmokeError):
                    guard.revoke_guest_tcp()
            self.assertFalse(guard.cleaning)

    def test_created_bridge_is_recovered_after_missing_initial_readback(self):
        for changed in (False, True):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
                guard.bridge_intent = True
                guard.bridge_id = None
                row = [{"ifname": NET.BRIDGE, "ifindex": 42, "address": "foreign" if changed else guard.bridge_mac,
                        "linkinfo": {"info_kind": "bridge"}}]
                values = ["", "", json.dumps(row), json.dumps(row), "", "[]"]
                with patch.object(guard, "command", side_effect=values) as command:
                    if changed:
                        with self.assertRaises(NET.VM.SmokeError):
                            guard.close()
                        self.assertFalse(any("delete" in call.args[0] for call in command.call_args_list))
                    else:
                        guard.close()
                        self.assertTrue(any("delete" in call.args[0] for call in command.call_args_list))

    def test_failed_bridge_inventory_is_never_absence(self):
        with tempfile.TemporaryDirectory() as temporary:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            guard.bridge_intent = True
            with patch.object(guard, "command", side_effect=["", "", NET.VM.SmokeError("inventory failed", 1)]) as command:
                with self.assertRaises(NET.VM.SmokeError):
                    guard.close()
                self.assertFalse(any("delete" in call.args[0] for call in command.call_args_list))

    def test_joined_ambiguous_capability_grant_is_verified_before_revoke(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            file = root / "tool"
            file.write_text("private")
            file.chmod(0o700)
            guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
            with patch.object(guard, "command", side_effect=["", "", NET.VM.SmokeError("readback failed")]):
                with self.assertRaises(NET.VM.SmokeError):
                    guard.grant(file, "cap_net_admin=ep")
            values = ["", "", "opaque-present", "", "", ""]
            with patch.object(guard, "command", side_effect=values) as command:
                guard.close()
                self.assertIn(["setcap", "-v", "cap_net_admin=ep", str(file)], [call.args[0] for call in command.call_args_list])
                self.assertIn(["setcap", "-r", str(file)], [call.args[0] for call in command.call_args_list])

    @unittest.skipUnless(hasattr(os, "memfd_create"), "cleanup RAM capture is native Linux-only")
    def test_cleanup_capture_survives_denied_diagnostic_writes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            guard = NET.OwnedNetwork(root, os.environ.copy(), "172.18.0.0/16")
            guard.cleaning = True
            with patch.object(Path, "open", side_effect=PermissionError("no log writes")):
                value = guard.command([os.sys.executable, "-c", "print('exact-readback')"])
                self.assertEqual(value.strip(), "exact-readback")

    def test_unjoined_dns_prevents_all_host_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(NET.MGMT, "stop_group", side_effect=NET.MGMT.UnjoinedProcess("not joined")):
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            guard.dns = Mock()
            with patch.object(guard, "command") as command:
                with self.assertRaises(NET.MGMT.UnjoinedProcess):
                    guard.close()
                command.assert_not_called()

    def test_joined_dns_stop_is_idempotent_without_host_mutations(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(NET.MGMT, "stop_group") as stop:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            daemon = guard.dns = Mock()
            with patch.object(guard, "command") as command:
                guard.stop_dns()
                guard.stop_dns()
            stop.assert_called_once_with(daemon)
            command.assert_not_called()

    def test_capability_target_must_be_private_regular_non_symlink(self):
        with tempfile.TemporaryDirectory() as temporary, tempfile.TemporaryDirectory() as foreign:
            root = Path(temporary).resolve()
            outside = Path(foreign) / "tool"
            outside.write_text("foreign")
            symlink = root / "tool"
            symlink.symlink_to(outside)
            guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
            with patch.object(guard, "command") as command:
                for path in (outside, symlink):
                    with self.subTest(path=path), self.assertRaises(NET.VM.SmokeError):
                        guard.grant(path, "cap_net_admin=ep")
                command.assert_not_called()

    def test_file_identity_rejects_symlinks_and_directories(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            file = root / "regular"
            file.write_text("owned")
            self.assertEqual(NET.identity(file)[3], os.getuid())
            symlink = root / "symlink"
            symlink.symlink_to(file)
            for path in (root, symlink):
                with self.subTest(path=path), self.assertRaises(NET.VM.SmokeError):
                    NET.identity(path)


    def test_capability_target_rejects_hard_links_or_shared_mode_before_mutation(self):
        with tempfile.TemporaryDirectory() as temporary, tempfile.TemporaryDirectory() as foreign:
            root = Path(temporary).resolve()
            outside = Path(foreign) / "tool"
            outside.write_text("foreign")
            outside.chmod(0o700)
            hardlink = root / "linked-tool"
            os.link(outside, hardlink)
            shared = root / "shared-tool"
            shared.write_text("shared")
            shared.chmod(0o755)
            guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
            with patch.object(guard, "command") as command:
                for path in (hardlink, shared):
                    with self.subTest(path=path), self.assertRaises(NET.VM.SmokeError):
                        guard.grant(path, "cap_net_admin=ep")
                command.assert_not_called()

    def test_capability_target_rejects_added_alias_during_restore(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            file = root / "tool"
            file.write_text("private")
            file.chmod(0o700)
            guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
            with patch.object(guard, "command", return_value=""):
                guard.grant(file, "cap_net_admin=ep")
            os.link(file, root / "alias")
            with patch.object(guard, "command", return_value="") as command:
                with self.assertRaises(NET.VM.SmokeError):
                    guard.close()
                self.assertFalse(any(call.args[0][0] == "setcap" for call in command.call_args_list))


if __name__ == "__main__":
    unittest.main()
