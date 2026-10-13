# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Regression contracts for the non-authoritative artifact diagnostic."""

import csv
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("artifact_tests", Path(__file__).with_name("test-artifacts.py"))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)
ROOT = Path(__file__).resolve().parents[1]


class ArtifactAdapterTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="artifact space:comma,")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        plan = json.loads((ROOT / ".github/nvkr-run/plan.json").read_text())
        build = next(b for b in plan["builds"] if "nvkectl" in b["components"])
        self.plan = {"schemaVersion": plan["schemaVersion"], "builds": [build]}
        self.component = build["catalogSnapshot"]["Components"]["nvkectl"]

    def test_go_input_projection(self):
        inputs = {key: self.component[key] for key in ("repo", "commit", "treeHash")}
        inputs.update(toolchainImage="mirror.example/go@sha256:" + "a" * 64,
                      expectedVersion="v1", platform="linux/arm64", output="renamed-cli",
                      buildDigest="sha256:" + "b" * 64)
        with patch.object(adapter.subprocess, "check_output", return_value=json.dumps(inputs)) as command:
            component, platform, output, digest = adapter.load_cli_inputs(self.root / "plan.json", "renamed-cli", "/tools/nvkr")
        command.assert_called_once_with(
            ["/tools/nvkr", "run", "artifact-test-inputs", "--plan", str(self.root / "plan.json"), "--output", "renamed-cli"],
            text=True, timeout=30)
        self.assertEqual(component["toolchainImage"], inputs["toolchainImage"])
        self.assertEqual(adapter.expected_version(component), "v1")
        self.assertEqual((platform, output, digest), (inputs["platform"], inputs["output"], inputs["buildDigest"]))

    def test_go_rejection_propagates(self):
        with patch.object(adapter.subprocess, "check_output", side_effect=subprocess.CalledProcessError(2, "nvkr")):
            with self.assertRaises(subprocess.CalledProcessError):
                adapter.load_cli_inputs(self.root / "plan.json")

    def test_version_alias(self):
        self.assertEqual(adapter.expected_version({"versionStamp": {"NKECTL_VERSION": "v1"}}), "v1")
        self.assertEqual(adapter.expected_version({"versionStamp": {"NVKECTL_VERSION": "v1", "NKECTL_VERSION": "v1"}}), "v1")

    def test_structured_mounts_and_offline_suites(self):
        for suite, target in (("contract", "test-artifact"), ("cli", "test")):
            with self.subTest(suite=suite):
                workspace = self.root / suite
                workspace.mkdir()
                artifact = workspace / "binary space:debug,quote\""
                calls = []

                def container(command, _name, **kwargs):
                    calls.append(command)
                    if "stdout" in kwargs:
                        package = "command-line-arguments" if suite == "contract" else "github.com/dsx-ai-factory/nvke/cli/cmd/nvkectl"
                        for event in self.events(package):
                            kwargs["stdout"].write(json.dumps(event) + "\n")

                with patch.object(adapter, "run_container", side_effect=container):
                    counts = adapter.test_binary(workspace, artifact, self.component, "linux/arm64", suite)
                self.assertEqual(counts["topLevelTests"], 1)
                acquire, offline = calls
                self.assertNotIn("/artifact/nvkectl", " ".join(acquire))
                self.assertIn("none", offline)
                self.assertIn("GOPROXY=off", offline)
                self.assertIn("GO=go", offline)
                self.assertIn(target, offline)
                self.assertEqual(offline[offline.index("--platform") + 1], "linux/arm64")
                mounts = [next(csv.reader([offline[i + 1]])) for i, arg in enumerate(offline) if arg == "--mount"]
                self.assertIn(["type=bind", f"src={artifact}", "dst=/artifact/nvkectl", "readonly"], mounts)
                self.assertIn(["type=bind", f"src={workspace / 'nvke'}", "dst=/src", "readonly"], mounts)

    @staticmethod
    def events(package="command-line-arguments"):
        return [{"Action": "run", "Package": package, "Test": "TestContract"},
                {"Action": "pass", "Package": package, "Test": "TestContract"},
                {"Action": "pass", "Package": package}]

    def test_result_rejections(self):
        path = self.root / "tests.jsonl"
        for events in ([], self.events()[:-1], self.events()[1:], self.events("unrelated"),
                       [{"Action": "skip", "Package": "p"}], [{"Action": "fail", "Package": "p"}]):
            with self.subTest(events=events):
                path.write_text("\n".join(json.dumps(event) for event in events))
                with self.assertRaises(ValueError):
                    adapter.summarize_tests(path, "contract")

    def test_source_import_and_rejections(self):
        repo = self.root / "upstream"
        repo.mkdir()
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        (repo / "cli").mkdir()
        makefile = repo / "cli/Makefile"
        makefile.write_text("test-artifact:\n\t@true\n")
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c", "user.email=test@example.com",
                        "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"], check=True)
        component = dict(self.component, repo=str(repo))
        for key, rev in (("commit", "HEAD"), ("treeHash", "HEAD^{tree}")):
            component[key] = subprocess.check_output(["git", "-C", str(repo), "rev-parse", rev], text=True).strip()
        checkout = self.root / "checkout"
        adapter.import_test_source(checkout, component)
        self.assertTrue((checkout / ".git/shallow").exists())
        with self.assertRaisesRegex(ValueError, "tree does not match"):
            adapter.import_test_source(self.root / "wrong-tree", dict(component, treeHash="a" * 40))
        makefile.write_text("test:\n\t@true\n")
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c", "user.email=test@example.com",
                        "-c", "commit.gpgsign=false", "commit", "-qm", "no interface"], check=True)
        component["commit"] = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
        component["treeHash"] = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD^{tree}"], text=True).strip()
        with self.assertRaisesRegex(ValueError, "no test-artifact target"):
            adapter.import_test_source(self.root / "no-target", component)

    def test_cleanup_preserves_failure(self):
        for error in (subprocess.TimeoutExpired("docker", 1), KeyboardInterrupt(), adapter.Interrupted(signal.SIGTERM)):
            with self.subTest(error=type(error).__name__):
                with patch.object(adapter, "run_command", side_effect=error), patch.object(adapter.subprocess, "run", side_effect=OSError("cleanup")) as cleanup:
                    with self.assertRaises(type(error)):
                        adapter.run_container(["docker", "run"], "owned")
                    cleanup.assert_called_once()

    def test_cli_exit_codes(self):
        for error, expected in ((KeyboardInterrupt(), 130), (adapter.Interrupted(signal.SIGTERM), 143),
                                (subprocess.CalledProcessError(7, "docker"), 7),
                                (ValueError("invalid plan"), 1), (OSError("missing"), 1)):
            with self.subTest(error=type(error).__name__), patch.object(adapter, "main", side_effect=error):
                self.assertEqual(adapter.cli(), expected)
        result = subprocess.run([sys.executable, "-B", adapter.__file__, "--artifact", "unused"],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 2)
        self.assertIn("--plan", result.stderr)

    def test_actual_sigterm_cleanup(self):
        ready, cleaned = self.root / "ready", self.root / "cleaned"
        worker = self.root / "worker.py"
        worker.write_text(f'''import importlib.util, pathlib, subprocess, sys
spec = importlib.util.spec_from_file_location("adapter", {str(Path(adapter.__file__))!r})
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)
original = subprocess.run
def run(command, **kwargs):
    if command[:2] == ["docker", "rm"]:
        pathlib.Path({str(cleaned)!r}).write_text("cleaned")
        return
    return original(command, **kwargs)
a.subprocess.run = run
def main():
    a.run_container([sys.executable, "-c", "from pathlib import Path; import time; Path({str(ready)!r}).write_text('ready'); time.sleep(60)"], "owned")
a.main = main
sys.exit(a.cli())
''')
        process = subprocess.Popen([sys.executable, str(worker)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 10
            while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(ready.exists())
            process.send_signal(signal.SIGTERM)
            _, stderr = process.communicate(timeout=10)
            self.assertEqual(process.returncode, 143, stderr)
            self.assertTrue(cleaned.exists())
        finally:
            if process.poll() is None:
                process.kill()
            process.communicate(timeout=10)

    def test_staging_and_workspace_cleanup(self):
        artifact = self.root / "original"
        artifact.write_bytes(b"original")
        artifact.chmod(0o755)
        plan = self.root / "plan.json"
        plan.write_text(json.dumps(self.plan))
        workspaces = []

        def test(workspace, staged, *_args):
            workspaces.append(workspace)
            artifact.write_bytes(b"swapped")
            self.assertEqual(staged.read_bytes(), b"original")
            return {"topLevelTests": 6, "packages": 1}

        with patch.object(adapter, "load_cli_inputs", return_value=(self.component, "linux/amd64", "nvkectl", "sha256:" + "a" * 64)), patch.object(adapter, "REPOSITORY", self.root), patch.object(adapter, "import_test_source"), patch.object(adapter, "test_binary", side_effect=test), patch.object(sys, "argv", ["adapter", "--artifact", str(artifact), "--plan", str(plan)]):
            adapter.main()
        self.assertFalse(workspaces[0].exists())
        with patch.object(adapter, "load_cli_inputs", return_value=(self.component, "linux/amd64", "nvkectl", "sha256:" + "a" * 64)), patch.object(adapter, "REPOSITORY", self.root), patch.object(adapter, "import_test_source", side_effect=ValueError("no interface")), patch.object(sys, "argv", ["adapter", "--artifact", str(artifact), "--plan", str(plan)]):
            with self.assertRaisesRegex(ValueError, "no interface"):
                adapter.main()
        self.assertEqual(list((self.root / ".nvkr/artifact-tests").iterdir()), [])

    def test_changed_staged_bytes_and_keep(self):
        artifact = self.root / "original"
        artifact.write_bytes(b"original")
        artifact.chmod(0o755)
        plan = self.root / "plan.json"
        plan.write_text(json.dumps(self.plan))

        def change(_workspace, staged, *_args):
            staged.chmod(0o755)
            staged.write_bytes(b"changed")
            return {"topLevelTests": 6, "packages": 1}

        with patch.object(adapter, "load_cli_inputs", return_value=(self.component, "linux/amd64", "nvkectl", "sha256:" + "a" * 64)), patch.object(adapter, "REPOSITORY", self.root), patch.object(adapter, "import_test_source"), patch.object(adapter, "test_binary", side_effect=change), patch.object(sys, "argv", ["adapter", "--artifact", str(artifact), "--plan", str(plan), "--keep"]):
            with self.assertRaisesRegex(ValueError, "staged artifact bytes changed"):
                adapter.main()
        workspaces = list((self.root / ".nvkr/artifact-tests").iterdir())
        self.assertEqual(len(workspaces), 1)
        self.assertFalse((workspaces[0] / "result.json").exists())


if __name__ == "__main__":
    unittest.main()
