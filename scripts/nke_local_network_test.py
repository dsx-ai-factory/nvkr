# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Host-guard negatives, not DHCP/BGP/tenant acceptance."""

import importlib.util
import os
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("network", Path(__file__).with_name("nke-local-network.py"))
NET = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NET)


class NetworkTest(unittest.TestCase):
    def journal(self, root):
        guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
        guard.journal = True
        guard.origin_boot = "a14e7158-1737-451e-abd5-3bc3f61955e3"
        guard.origin_pid = 2147483647
        guard.helper_id, guard.helper_hash = (1, 2, 0o755, 0, 0), "sha256:" + "a" * 64
        guard.sysctls = {key: "0" for key in NET.SYSCTLS}
        guard.checkpoint()
        return guard

    def recover_patches(self, same_boot=False):
        stack = ExitStack()
        stack.enter_context(patch.object(NET.platform, "system", return_value="Linux"))
        stack.enter_context(patch.object(NET.platform, "machine", return_value="aarch64"))
        stack.enter_context(patch.object(NET.shutil, "which", return_value="/usr/bin/pinned-test-tool"))
        stack.enter_context(patch.object(NET.OwnedNetwork, "acquire_lock"))
        stack.enter_context(patch.object(NET, "boot_id", return_value=(
            "a14e7158-1737-451e-abd5-3bc3f61955e3" if same_boot else "c9370fb9-c6c0-4dc9-9c6b-b4501d4fe0b4")))
        stack.enter_context(patch.object(NET.Path, "iterdir", return_value=[]))
        stack.enter_context(patch.object(NET.socket, "socket"))
        return stack

    def test_recovery_digest_matches_actual_checkpoint_blob_spelling(self):
        for target in ("helper", "capability"):
            for mutation in ("valid", "bare-hex", "wrong-family", "uppercase", "truncated"):
                with self.subTest(target=target, mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary).resolve()
                    guard = self.journal(root)
                    payload = root / "dnsmasq"
                    payload.write_bytes(b"exact private executable fixture")
                    payload.chmod(0o700)
                    hashed = NET.VM.digest(payload)
                    self.assertTrue(hashed.startswith("sha256:"))
                    guard.helper_hash = hashed
                    guard.caps = [(payload, NET.private_identity(payload), hashed, NET.CAPABILITIES["dnsmasq"])]
                    guard.checkpoint()
                    row = NET.read_state(root)
                    invalid = {"valid": hashed, "bare-hex": hashed[7:], "wrong-family": "sha512:" + hashed[7:],
                               "uppercase": hashed.upper(), "truncated": hashed[:-1]}[mutation]
                    if target == "helper":
                        row["helperHash"] = invalid
                    else:
                        row["caps"][0][2] = invalid
                    (root / NET.STATE).write_text(json.dumps(row))
                    with self.recover_patches(), patch.object(NET.OwnedNetwork, "command") as command:
                        if mutation == "valid":
                            restored = NET.OwnedNetwork.recover(root, {})
                            self.assertEqual(restored.helper_hash, hashed)
                            self.assertEqual(restored.caps, guard.caps)
                        else:
                            with self.assertRaisesRegex(NET.VM.SmokeError, "exact-byte digest"):
                                NET.OwnedNetwork.recover(root, {})
                        command.assert_not_called()

    def test_protected_same_user_producer_requires_authenticated_metadata(self):
        for case in ("foreign-executable", "owned-executable", "malformed", "unavailable"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                guard = self.journal(root)
                payload = root / "dnsmasq"
                payload.write_bytes(b"private capability fixture")
                payload.chmod(0o700)
                held = NET.private_identity(payload)
                guard.caps = [(payload, held, NET.VM.digest(payload), NET.CAPABILITIES["dnsmasq"])]
                guard.checkpoint()
                process = Mock(name="protected-process")
                process.name = "1234"
                process.stat.return_value.st_uid = os.getuid()
                executable = Mock()
                executable.stat.side_effect = PermissionError("kernel non-dumpable process")
                executable.__str__ = Mock(return_value="/proc/1234/exe")
                process.__truediv__ = Mock(return_value=executable)
                values = {"foreign-executable": "123 456\n", "owned-executable": "%d %d\n" % held[:2],
                          "malformed": "not metadata\n", "unavailable": ""}
                with self.recover_patches(), patch.object(NET.Path, "iterdir", return_value=[process]), \
                     patch.object(NET.MGMT, "capture", return_value=values[case]) as capture:
                    if case == "unavailable":
                        capture.side_effect = NET.VM.SmokeError("privileged stat failed", 1)
                    if case == "foreign-executable":
                        NET.OwnedNetwork.recover(root, {})
                    else:
                        with self.assertRaises(NET.VM.SmokeError):
                            NET.OwnedNetwork.recover(root, {})
                    capture.assert_called_once()
                    self.assertEqual(capture.call_args.args[0], ["sudo", "-n", "timeout", "--signal=TERM",
                        "--kill-after=5", "25", "stat", "--dereference", "--format=%d %i", "--", "/proc/1234/exe"])
                    self.assertTrue(capture.call_args.kwargs["volatile"])

    def test_checkpoint_is_private_and_failure_precedes_bridge_mutation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            guard = self.journal(root)
            value = NET.read_state(root)
            self.assertEqual(value["owner"], guard.owner)
            self.assertFalse(value["bridgeIntent"])
            self.assertEqual(NET.private_identity(root / NET.STATE)[2], 0o600)
            with patch.object(NET.os, "fsync", side_effect=OSError("full")), patch.object(guard, "command") as command:
                with self.assertRaises(OSError):
                    guard.start()
            command.assert_not_called()
            self.assertFalse(NET.read_state(root)["bridgeIntent"])
            self.assertFalse(list(root.glob(".network-state-*")))

    def test_recovery_accepts_only_exact_supported_intent_closure(self):
        for mutation in ("valid", "unknown-field", "foreign-work", "foreign-uid", "arbitrary-rule", "foreign-cap", "bad-sysctl"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                original = self.journal(root)
                original.bridge_intent = True
                original.rule_intents = NET.rules(original.owner, original.kind_cidr)
                original.rule_intents.append(("raw", "PREROUTING", ["-m", "comment", "--comment", original.owner,
                    "-i", NET.BRIDGE, "-s", NET.CIDR, "-d", "172.18.0.2", "-p", "tcp", "--dport", "30440", "-j", "ACCEPT"]))
                original.checkpoint()
                row = NET.read_state(root)
                if mutation == "unknown-field":
                    row["argv"] = ["sudo", "arbitrary"]
                elif mutation == "foreign-work":
                    row["work"] += "-foreign"
                elif mutation == "foreign-uid":
                    row["uid"] += 1
                elif mutation == "arbitrary-rule":
                    row["rules"][0][2][-1] = "DROP"
                elif mutation == "foreign-cap":
                    row["caps"] = [["/usr/bin/foreign", [1, 2, 0o700, os.getuid(), os.getgid()], "sha256:" + "a" * 64, "cap_net_admin=ep"]]
                elif mutation == "bad-sysctl":
                    row["sysctlIntents"] = ["kernel.unrelated"]
                (root / NET.STATE).write_text(json.dumps(row))
                with self.recover_patches(), patch.object(NET.OwnedNetwork, "command") as command:
                    if mutation != "valid":
                        with self.assertRaises((NET.VM.SmokeError, ValueError)):
                            NET.OwnedNetwork.recover(root, {})
                    else:
                        restored = NET.OwnedNetwork.recover(root, {})
                        self.assertEqual(restored.owner, original.owner)
                        self.assertEqual(restored.rule_intents, original.rule_intents)
                        self.assertTrue(restored.journal)
                    command.assert_not_called()

    def test_recovery_does_not_kill_serialized_pid_and_waits_out_root_child(self):
        for present in (False, True):
            with self.subTest(present=present), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                original = self.journal(root)
                with self.recover_patches(same_boot=True), patch.object(NET.time, "sleep") as sleep, \
                     patch.object(NET.os, "kill", side_effect=None if present else ProcessLookupError()) as kill:
                    if present:
                        with self.assertRaisesRegex(NET.VM.SmokeError, "original PID"):
                            NET.OwnedNetwork.recover(root, {})
                        sleep.assert_not_called()
                    else:
                        NET.OwnedNetwork.recover(root, {})
                        sleep.assert_called_once_with(40)
                    kill.assert_called_once_with(original.origin_pid, 0)

    def test_recovery_rejects_shared_symlink_or_duplicate_journal(self):
        for invalid in ("shared", "symlink", "hardlink", "duplicate"):
            with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                self.journal(root)
                path = root / NET.STATE
                if invalid == "shared":
                    path.chmod(0o644)
                elif invalid == "symlink":
                    target = root / "other"
                    path.rename(target)
                    path.symlink_to(target)
                elif invalid == "hardlink":
                    os.link(path, root / "other")
                else:
                    path.write_text('{"version":1,"version":1}')
                with self.assertRaises((NET.VM.SmokeError, OSError)):
                    NET.read_state(root)

    def test_context_manager_admits_and_closes_on_consumer_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            guard = NET.OwnedNetwork(temporary, {}, "172.18.0.0/16")
            with patch.object(guard, "admit") as admit, patch.object(guard, "close") as close:
                with self.assertRaisesRegex(RuntimeError, "consumer failed"):
                    with guard as admitted:
                        self.assertIs(admitted, guard)
                        raise RuntimeError("consumer failed")
            admit.assert_called_once_with()
            close.assert_called_once_with()

    def test_policy_restore_replays_after_unlink_before_checkpoint(self):
        for changed in (False, True):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                policy = root / "policy"
                policy.write_text("allow " + NET.BRIDGE + "\n")
                guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
                guard.policy_id = NET.identity(policy)
                if changed:
                    policy.write_text("allow foreign\n")
                else:
                    policy.unlink()
                with patch.object(NET, "POLICY", policy), patch.object(guard, "command", return_value="") as command:
                    if changed:
                        with self.assertRaisesRegex(NET.VM.SmokeError, "policy changed"):
                            guard.close()
                    else:
                        guard.close()
                        self.assertTrue(guard.finished)
                    self.assertTrue(all(call.args[0][0] in ("ps", "virsh") for call in command.call_args_list))

    def test_root_creation_intent_survives_copy_before_identity_checkpoint(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            guard = self.journal(root)
            policy = root / "bridge.conf"
            stage = policy.parent / (".nkd-policy-" + guard.owner.removeprefix("nkd-demo-"))
            guard.staging = stage, None
            guard.checkpoint()  # Intent precedes the ambiguous privileged copy.
            stage.write_text("allow " + NET.BRIDGE + "\n")
            stage.chmod(0o600)
            expected = NET.hashlib.sha256(stage.read_bytes()).hexdigest()
            def command(argv, _privileged=False):
                if argv[0] == "sha256sum":
                    return expected + "  " + str(stage) + "\n"
                if argv[0] == "unlink":
                    Path(argv[1]).unlink()
                return ""
            with self.recover_patches(), patch.object(NET, "POLICY", policy):
                restored = NET.OwnedNetwork.recover(root, {})
                with patch.object(NET, "identity", return_value=(1, 2, 0o600, 0, 0)), \
                     patch.object(restored, "command", side_effect=command):
                    restored.close()
            self.assertFalse(stage.exists())
            self.assertTrue(NET.read_state(root)["finished"])

    def test_root_staging_substitution_refuses_unlink_and_success(self):
        for case in ("uid", "gid", "mode", "symlink", "hardlink", "inode", "empty", "partial", "foreign-bytes"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                guard = self.journal(root)
                stage = root / (".nkd-policy-" + guard.owner.removeprefix("nkd-demo-"))
                stage.write_text("allow " + NET.BRIDGE + "\n")
                stage.chmod(0o600)
                current = (1, 2, 0o600, 0, 0)
                guard.staging = stage, current if case == "inode" else None
                if case == "uid":
                    current = (1, 2, 0o600, 42, 0)
                elif case == "gid":
                    current = (1, 2, 0o600, 0, 42)
                elif case == "mode":
                    current = (1, 2, 0o666, 0, 0)
                elif case == "inode":
                    current = (1, 3, 0o600, 0, 0)
                elif case == "symlink":
                    other = root / "other"
                    stage.rename(other)
                    stage.symlink_to(other)
                elif case == "hardlink":
                    os.link(stage, root / "other")
                elif case in ("empty", "partial", "foreign-bytes"):
                    stage.write_text({"empty": "", "partial": "allow ", "foreign-bytes": "allow foreign\n"}[case])
                real_identity = NET.identity
                def identity(path):
                    return real_identity(path) if case == "symlink" else current
                def command(argv, _privileged=False):
                    if argv[0] == "sha256sum":
                        return NET.hashlib.sha256(stage.read_bytes()).hexdigest() + "  " + str(stage) + "\n"
                    raise AssertionError("unexpected mutation while rejecting substituted staging: " + argv[0])
                with patch.object(NET, "identity", side_effect=identity), \
                     patch.object(guard, "require_idle"), patch.object(guard, "command", side_effect=command) as calls:
                    with self.assertRaises(NET.VM.SmokeError):
                        guard.close()
                self.assertTrue(stage.exists() or stage.is_symlink())
                self.assertFalse(guard.finished)
                self.assertFalse(NET.read_state(root)["finished"])
                self.assertFalse(any(call.args[0][0] == "unlink" for call in calls.call_args_list))

    def test_cleanup_continues_after_enospc_but_does_not_report_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            guard = NET.OwnedNetwork(root, {}, "172.18.0.0/16")
            capability = root / "dnsmasq"
            capability.write_text("owned private bytes")
            capability.chmod(0o700)
            guard.caps = [(capability, NET.private_identity(capability), NET.VM.digest(capability), NET.CAPABILITIES["dnsmasq"])]
            guard.rule_intents = NET.rules(guard.owner, guard.kind_cidr)[:1]
            guard.bridge_intent, guard.helper_granted = True, True
            guard.helper_id, guard.helper_hash = (1, 2, 0o755, 0, 0), "sha256:" + "a" * 64
            guard.sysctls, guard.sysctl_intents = {NET.SYSCTLS[0]: "0"}, [NET.SYSCTLS[0]]
            helper, policy = root / "helper", root / "policy"
            policy.write_text("allow " + NET.BRIDGE + "\n")
            guard.policy_id = NET.identity(policy)
            original_identity = NET.identity
            state = {"cap": True, "helper": 0o4755, "sysctl": "1"}
            def identity(path):
                return (1, 2, state["helper"], 0, 0) if path == helper else original_identity(path)
            def command(argv, _privileged=False):
                if argv[0] == "getcap":
                    return "present" if state["cap"] else ""
                if argv[:2] == ["setcap", "-r"]:
                    state["cap"] = False
                if argv[:2] == ["chmod", "0755"]:
                    state["helper"] = 0o755
                if argv[:2] == ["sysctl", "-n"]:
                    return state["sysctl"]
                if argv[:2] == ["sysctl", "-w"]:
                    state["sysctl"] = "0"
                if argv[0] == "unlink":
                    Path(argv[1]).unlink()
                return ""
            original_digest = NET.VM.digest
            with patch.object(NET, "HELPER", helper), patch.object(NET, "POLICY", policy), \
                 patch.object(NET, "identity", side_effect=identity), \
                 patch.object(NET.VM, "digest", side_effect=lambda path: "sha256:" + "a" * 64 if path == helper else original_digest(path)), \
                 patch.object(guard, "checkpoint", side_effect=OSError("ENOSPC")), \
                 patch.object(guard, "bridge_binding", side_effect=["42", "42", None]), \
                 patch.object(guard, "rule_exists", side_effect=[True, False]), \
                 patch.object(guard, "command", side_effect=command) as calls:
                with self.assertRaisesRegex(NET.VM.SmokeError, "persistence failed"):
                    guard.close()
            self.assertEqual(state, {"cap": False, "helper": 0o755, "sysctl": "0"})
            self.assertFalse(policy.exists())
            self.assertIn(["ip", "link", "delete", NET.BRIDGE, "type", "bridge"], [call.args[0] for call in calls.call_args_list])
            self.assertTrue(any(call.args[0][:6] == ["iptables", "-w", "5", "-t", "filter", "-D"]
                                for call in calls.call_args_list))

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
