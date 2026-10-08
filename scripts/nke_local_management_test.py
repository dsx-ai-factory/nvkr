# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Management consumer contracts, not external Kind/NKE acceptance."""

import importlib.util
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location("management", Path(__file__).with_name("nke-local-management.py"))
ADAPTER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ADAPTER)


class ManagementTest(unittest.TestCase):
    def test_native_environment_does_not_adopt_other_targets(self):
        foreign = {"PATH": "/usr/bin", "DOCKER_CONTEXT": "shared", "DOCKER_HOST": "tcp://remote",
                   "KUBECONFIG": "shared", "KIND_EXPERIMENTAL_PROVIDER": "podman", "CAPI_VERSION": "foreign",
                   "SITE_KUBECONFIG": "shared", "RELEASE_REF": "foreign", "HOME": "/foreign"}
        with patch.dict(os.environ, foreign, clear=True):
            env = ADAPTER.native_environment()
        self.assertEqual(env, {"PATH": "/usr/bin", "DOCKER_HOST": "unix:///var/run/docker.sock",
                               "KIND_EXPERIMENTAL_PROVIDER": "docker"})

    def test_platform_rejected_before_output_or_docker(self):
        with patch.object(ADAPTER.platform, "system", return_value="Darwin"), patch.object(ADAPTER, "inspect") as inspect:
            with self.assertRaisesRegex(ADAPTER.VM.SmokeError, "native Linux ARM64"):
                ADAPTER.admit(Mock(), {})
        inspect.assert_not_called()

    def test_pinned_projection_ignores_hidden_working_tree_stage(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            checkout, run, tool = root / "checkout", root / "run", root / "nkdctl"
            checkout.mkdir()
            (checkout / "deploy/stages").mkdir(parents=True)
            # Simulates skip-worktree/assume-unchanged: status reports clean,
            # but the executable working-tree content must never be used.
            (checkout / "deploy/stages/04-install-cluster-api.sh").write_text("hidden modification")
            layout = run / "packages" / ADAPTER.BUNDLE
            layout.mkdir(parents=True)
            binding = {"name": ADAPTER.BUNDLE, "kind": "executable", "buildDigest": "sha256:" + "b" * 64,
                       "components": ADAPTER.MEMBERS}
            observed = {**binding, "digest": "sha256:" + "a" * 64}
            (run / "lifecycle.json").write_text(json.dumps({"status": "complete", "expected": [binding], "observed": [observed]}))
            (layout / "index.json").write_text(json.dumps({"manifests": [{"digest": observed["digest"]}]}))
            tool.write_text("fixture")
            tool.chmod(0o700)
            config = "fixture\n" + ADAPTER.SOCKET_MOUNT
            source = {path: "pinned " + path for path in ADAPTER.SOURCE_FILES}
            def inspect(argv, _label, _env):
                if argv[0] == "git":
                    if "rev-parse" in argv:
                        return ADAPTER.CHART.INSTALLER_COMMIT
                    if "status" in argv:
                        return ""
                    relative = argv[-1].split(":", 1)[1]
                    return config if relative == ADAPTER.CONFIG_PATH else source[relative]
                if argv[1] == "info":
                    return json.dumps({"OSType": "linux", "Architecture": "aarch64", "NCPU": 8, "MemTotal": 24 * 1024**3})
                return json.dumps([{"Id": ADAPTER.NODE_ID, "Architecture": "arm64"}])
            args = SimpleNamespace(nke_checkout=checkout, nkdctl=tool, tools_run=run, out=root / "fresh")
            with patch.object(ADAPTER.platform, "system", return_value="Linux"), \
                 patch.object(ADAPTER.platform, "machine", return_value="aarch64"), \
                 patch.object(ADAPTER.sys, "version_info", (3, 12)), patch.object(ADAPTER.shutil, "which", return_value="fixture"), \
                 patch.object(ADAPTER.shutil, "disk_usage", return_value=SimpleNamespace(free=64 * 1024**3)), \
                 patch.object(ADAPTER, "inspect", inspect), \
                 patch.object(ADAPTER, "CONFIG_HASH", ADAPTER.hashlib.sha256(config.encode()).hexdigest()), \
                 patch.object(ADAPTER, "VERSIONS_HASH", ADAPTER.hashlib.sha256(source["make/tool-versions.mk"].encode()).hexdigest()):
                admitted = ADAPTER.admit(args, {})
            self.assertEqual(admitted[-1], source)
            self.assertNotIn("hidden modification", admitted[-1].values())
            self.assertNotIn(ADAPTER.SOCKET_MOUNT, admitted[5])
            self.assertFalse(args.out.exists())

    def test_ownership_checks(self):
        for case in ("exact", "different cluster", "different role", "different image", "different name", "missing"):
            with self.subTest(case=case):
                row = {"Id": "captured-id", "Name": "/nker-demo-test-control-plane", "Image": ADAPTER.NODE_ID,
                       "Config": {"Labels": {"io.x-k8s.kind.cluster": "nker-demo-test",
                                              "io.x-k8s.kind.role": "control-plane"}}}
                if case == "different cluster":
                    row["Config"]["Labels"]["io.x-k8s.kind.cluster"] = "shared"
                elif case == "different role":
                    row["Config"]["Labels"]["io.x-k8s.kind.role"] = "worker"
                elif case == "different image":
                    row["Image"] = "different"
                elif case == "different name":
                    row["Name"] = "/shared-control-plane"
                with patch.object(ADAPTER, "inspect", side_effect=["captured-id", json.dumps([] if case == "missing" else [row])]):
                    if case == "exact":
                        self.assertEqual(ADAPTER.owned_nodes("nker-demo-test", {}), ["captured-id"])
                    else:
                        with self.assertRaises(ADAPTER.VM.SmokeError):
                            ADAPTER.owned_nodes("nker-demo-test", {})

    def test_cleanup_uses_ids_and_rechecks(self):
        for remaining in (False, True):
            with self.subTest(remaining=remaining), tempfile.TemporaryDirectory() as temporary, \
                 patch.object(ADAPTER, "owned_nodes", side_effect=[
                    ["exact-id"], ["exact-id"] if remaining else []]), patch.object(ADAPTER, "execute") as execute, \
                 patch.object(ADAPTER, "volumes_for_nodes", return_value=["owned-volume"]), \
                 patch.object(ADAPTER, "inspect", return_value=""):
                if remaining:
                    with self.assertRaisesRegex(ADAPTER.VM.SmokeError, "remain"):
                        ADAPTER.cleanup("nker-demo-test", {}, Path(temporary), set())
                else:
                    ADAPTER.cleanup("nker-demo-test", {}, Path(temporary), set())
                self.assertEqual(execute.call_args.args[0], ["docker", "rm", "--force", "--volumes", "exact-id"])

    def test_empty_inventory_does_not_delete(self):
        with patch.object(ADAPTER, "owned_nodes", return_value=[]), patch.object(ADAPTER, "execute") as execute:
            ADAPTER.cleanup("nker-demo-test", {}, Path("private"), set())
        execute.assert_not_called()

    def test_cleanup_diagnostics_failure_does_not_prevent_removal(self):
        with patch.object(ADAPTER, "owned_nodes", side_effect=[["exact-id"], []]), \
             patch.object(ADAPTER, "volumes_for_nodes", return_value=["owned-volume"]), \
             patch.object(Path, "write_text", side_effect=OSError("disk full")), \
             patch.object(ADAPTER, "inspect", return_value=""), patch.object(ADAPTER, "execute") as execute:
            ADAPTER.cleanup("nker-demo-test", {}, Path("private"), set())
            self.assertEqual(execute.call_args.args[0], ["docker", "rm", "--force", "--volumes", "exact-id"])
            self.assertTrue(execute.call_args.kwargs["diagnostic_optional"])

    def test_cleanup_phase_log_failure_uses_null_sink_and_still_joins(self):
        process = Mock(returncode=0)
        with patch.object(Path, "open", side_effect=OSError("disk full")), \
             patch.object(ADAPTER.subprocess, "Popen", return_value=process) as popen, \
             patch.object(ADAPTER, "stop_group") as stop:
            ADAPTER.execute(["docker", "rm", "--force", "--volumes", "exact-id"],
                            "owned-node-cleanup", Path("private"), {}, 60, diagnostic_optional=True)
            self.assertEqual(popen.call_args.kwargs["stdout"].name, os.devnull)
            stop.assert_called_once_with(process)

    def test_all_subprocess_outcomes_stop_and_join(self):
        for case in ("success", "failure", "timeout", "interrupt", "join failed"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                process = Mock(returncode=1 if case == "failure" else 0)
                if case == "timeout":
                    process.wait.side_effect = subprocess.TimeoutExpired("kind", 600)
                if case == "interrupt":
                    process.wait.side_effect = ADAPTER.VM.SmokeError("interrupted")
                with patch.object(ADAPTER.subprocess, "Popen", return_value=process) as popen, patch.object(ADAPTER, "stop_group") as stop:
                    if case == "join failed":
                        stop.side_effect = ADAPTER.VM.SmokeError("not joined")
                    if case == "success":
                        ADAPTER.execute(["kind"], "phase", Path(temporary), {}, 600)
                    else:
                        with self.assertRaises(ADAPTER.UnjoinedProcess if case == "join failed" else ADAPTER.VM.SmokeError):
                            ADAPTER.execute(["kind"], "phase", Path(temporary), {}, 600)
                    stop.assert_called_once_with(process)
                    self.assertTrue(popen.call_args.kwargs["start_new_session"])
                self.assertTrue((Path(temporary) / "phase.log").is_file())

    def test_actual_process_timeout_has_no_surviving_child(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            pidfile = work / "pid"
            script = "import os,time; from pathlib import Path; Path(%r).write_text(str(os.getpid())); print('started',flush=True); time.sleep(60)" % str(pidfile)
            with self.assertRaisesRegex(ADAPTER.VM.SmokeError, "timed out"):
                ADAPTER.execute([os.sys.executable, "-c", script], "timeout", work, os.environ.copy(), .5)
            self.assertEqual((work / "timeout.log").read_text(), "started\n")
            with self.assertRaises(ProcessLookupError):
                os.kill(int(pidfile.read_text()), 0)

    @unittest.skipUnless(platform.system() == "Linux", "real orphan-group execution is Linux-only, as admitted by the consumer")
    def test_descendant_outliving_leader_is_stopped(self):
        for ignore_term in (False, True):
            with self.subTest(ignore_term=ignore_term), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                child = ("import os,signal,time; from pathlib import Path; "
                         "signal.signal(signal.SIGTERM,signal.SIG_IGN); " if ignore_term else
                         "import os,time; from pathlib import Path; ")
                child += "Path(%r).write_text(str(os.getpid())); time.sleep(60)" % str(work / "child-pid")
                parent = ("import subprocess,sys,time; from pathlib import Path; "
                          "subprocess.Popen([sys.executable,'-c',%r]); "
                          "p=Path(%r); "
                          "\nwhile not p.exists(): time.sleep(.01)\n") % (child, str(work / "child-pid"))
                process = subprocess.Popen([os.sys.executable, "-c", parent], start_new_session=True)
                try:
                    process.wait(timeout=5)
                    self.assertTrue(ADAPTER.group_alive(process.pid))
                    ADAPTER.stop_group(process, grace=.2, kill_grace=2)
                    self.assertFalse(ADAPTER.group_alive(process.pid))
                    with self.assertRaises(ProcessLookupError):
                        os.kill(int((work / "child-pid").read_text()), 0)
                finally:
                    if ADAPTER.group_alive(process.pid):
                        os.killpg(process.pid, signal.SIGKILL)

    def test_join_failure_skips_node_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / "fresh"
            admitted = (Path("checkout"), Path("nkdctl"), Path("run"), Path("layout"), work, "config", {}, {})
            tools = work / "tools"
            def execute(argv, phase, *_args):
                if phase == "extract-tools":
                    tools.mkdir()
                    for name in ADAPTER.MEMBERS:
                        file = tools / name
                        file.write_text("fixture")
                        file.chmod(0o700)
                if phase == "create-management":
                    raise ADAPTER.UnjoinedProcess("group remains")
            old_mask = os.umask(0o077)
            try:
                with patch.object(ADAPTER, "admit", return_value=admitted), patch.object(ADAPTER, "owned_nodes", return_value=[]), \
                     patch.object(ADAPTER, "execute", side_effect=execute), patch.object(ADAPTER, "cleanup") as cleanup, \
                     patch.object(ADAPTER, "verify_versions"), patch.object(ADAPTER, "inspect", return_value=""):
                    with self.assertRaises(ADAPTER.UnjoinedProcess):
                        ADAPTER.probe(Mock())
                    cleanup.assert_not_called()
            finally:
                os.umask(old_mask)

    def test_volume_ownership_rejects_foreign_mounts(self):
        for case in ("exact", "predates attempt", "shared", "named", "wrong destination", "extra volume", "foreign id"):
            with self.subTest(case=case):
                mount = {"Type": "volume", "Destination": "/var", "Driver": "local", "RW": True, "Name": "owned-volume"}
                row = {"Id": "exact-id", "Mounts": [mount], "Config": {"Volumes": {"/var": {}}}}
                volume = {"Name": "owned-volume", "Driver": "local", "Labels": {"com.docker.volume.anonymous": ""}}
                existing = {"owned-volume"} if case == "predates attempt" else set()
                if case == "named":
                    volume["Labels"] = {}
                if case == "wrong destination":
                    mount["Destination"] = "/foreign"
                if case == "extra volume":
                    row["Mounts"] *= 2
                if case == "foreign id":
                    row["Id"] = "foreign-id"
                consumers = "foreign-id" if case == "shared" else "exact-id"
                with patch.object(ADAPTER, "inspect", side_effect=[json.dumps([row]), json.dumps([volume]), consumers]):
                    if case == "exact":
                        self.assertEqual(ADAPTER.volumes_for_nodes(["exact-id"], {}, existing), ["owned-volume"])
                    else:
                        with self.assertRaises(ADAPTER.VM.SmokeError):
                            ADAPTER.volumes_for_nodes(["exact-id"], {}, existing)

    def test_tool_versions_and_platforms(self):
        for case in ("exact", "foreign kind", "foreign clusterctl", "foreign helm", "foreign kubectl", "foreign oras", "wrong platform"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                work = Path(temporary)
                data = {"version-kind": "0.32.0", "version-clusterctl": "v1.12.3", "version-helm": "v4.2.4",
                        "version-kubectl": json.dumps({"clientVersion": {"gitVersion": "v1.36.0", "platform": "linux/arm64"}}),
                        "version-oras": "Version: 1.3.3\nOS/Arch: linux/arm64\n"}
                if case.startswith("foreign "):
                    data["version-" + case.split()[1]] = "foreign"
                if case == "wrong platform":
                    data["version-kubectl"] = json.dumps({"clientVersion": {"gitVersion": "v1.36.0", "platform": "linux/amd64"}})
                def execute(_argv, phase, *_args):
                    (work / (phase + ".log")).write_text(data[phase])
                with patch.object(ADAPTER, "execute", execute):
                    if case == "exact":
                        ADAPTER.verify_versions(Path("tools"), work, {})
                    else:
                        with self.assertRaises((ADAPTER.VM.SmokeError, ValueError)):
                            ADAPTER.verify_versions(Path("tools"), work, {})


if __name__ == "__main__":
    unittest.main()
