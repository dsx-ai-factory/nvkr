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
