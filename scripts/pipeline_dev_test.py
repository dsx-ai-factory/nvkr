# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Dispatch preflight contracts with no GitHub calls or repository mutations."""

import importlib.util
import io
import json
from pathlib import Path
import shlex
import subprocess
import unittest
from urllib.parse import quote


spec = importlib.util.spec_from_file_location("pipeline_dev", Path(__file__).with_name("pipeline-dev.py"))
pipeline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pipeline)

REPO = "example/nvkr"
BRANCH = "feature/pipeline;$shell"
COMMIT = "a" * 40


class FakeCommands:
    def __init__(self, failure=None):
        self.failure = failure
        self.calls = []
        self.branch_reads = 0

    def __call__(self, argv, **kwargs):
        self.calls.append((argv, kwargs))
        stdout, code = "", 0
        if argv[:2] == ["git", "status"]:
            if self.failure in {"dirty", "untracked"}:
                stdout = " M tracked.go" if self.failure == "dirty" else "?? untracked.go"
        elif argv[:2] == ["git", "symbolic-ref"]:
            self.branch_reads += 1
            stdout = "different" if self.failure == "local moved" and self.branch_reads > 1 else BRANCH
            code = int(self.failure == "detached")
        elif argv[:2] == ["git", "rev-parse"]:
            stdout = COMMIT
        elif argv[:2] == ["gh", "api"]:
            if self.failure == "timeout":
                raise subprocess.TimeoutExpired(argv, kwargs["timeout"])
            if argv[-1].endswith("/actions/workflows/nvkr-dev.yml"):
                code = int(self.failure == "missing workflow")
                stdout = json.dumps({"path": ".github/workflows/nvkr-dev.yml",
                                     "state": "disabled_manually" if self.failure == "disabled workflow" else "active"})
            elif argv[-1] == f"repos/{REPO}/git/ref/heads/{quote(BRANCH, safe='')}":
                value = {"ref": "refs/heads/" + BRANCH,
                         "object": {"type": "commit", "sha": "b" * 40 if self.failure == "remote mismatch" else COMMIT}}
                if self.failure == "malformed response":
                    stdout = "{"
                elif self.failure == "ambiguous response":
                    stdout = json.dumps([value])
                elif self.failure == "missing object":
                    stdout = json.dumps({"ref": "refs/heads/" + BRANCH})
                elif self.failure == "wrong ref":
                    value["ref"] = "refs/tags/" + BRANCH
                    stdout = json.dumps(value)
                else:
                    stdout = json.dumps(value)
            else:
                raise AssertionError(f"Unexpected API request: {argv}")
        elif argv[:3] == ["gh", "workflow", "run"]:
            if self.failure == "dispatch timeout":
                raise subprocess.TimeoutExpired(argv, kwargs["timeout"])
            code = int(self.failure == "dispatch rejected")
        else:
            raise AssertionError(f"Unexpected command: {argv}")
        return subprocess.CompletedProcess(argv, code, stdout, "")

    def dispatches(self):
        return [argv for argv, _ in self.calls if argv[:3] == ["gh", "workflow", "run"]]


class PipelineDevTest(unittest.TestCase):
    def test_dispatch_and_check(self):
        for check in (True, False):
            with self.subTest(check=check):
                commands, output = FakeCommands(), io.StringIO()
                pipeline.dispatch(REPO, check=check, runner=commands, output=output, root="/repo")
                self.assertEqual(commands.dispatches(), [] if check else [
                    ["gh", "workflow", "run", "nvkr-dev.yml", "--ref", BRANCH, "--repo", REPO]])
                for _, options in commands.calls:
                    self.assertEqual(options["cwd"], Path("/repo"))
                    self.assertEqual(options["timeout"], 30)
                    self.assertEqual(options["stdin"], subprocess.DEVNULL)
                    self.assertEqual(options["env"]["GH_HOST"], "github.com")
                    self.assertEqual(options["env"]["GH_PROMPT_DISABLED"], "1")
                    self.assertFalse(options.get("shell", False))
                self.assertIn(COMMIT, output.getvalue())
                if check:
                    self.assertIn("no workflow dispatched", output.getvalue())
                else:
                    line = next(line for line in output.getvalue().splitlines() if line.startswith("Inspect matching runs: "))
                    status = shlex.split(line.removeprefix("Inspect matching runs: "))
                    self.assertEqual(status[status.index("--branch") + 1], BRANCH)
                    self.assertEqual(status[status.index("--commit") + 1], COMMIT)
                    self.assertEqual(status[status.index("--repo") + 1], REPO)
                    self.assertIn("Dispatch request accepted", output.getvalue())
                    self.assertNotIn("succeeded", output.getvalue())

    def test_preflight_rejections_do_not_dispatch(self):
        for failure, code, message in [
            ("dirty", "NVKR-E-USAGE", "tracked and untracked"),
            ("untracked", "NVKR-E-USAGE", "tracked and untracked"),
            ("detached", "NVKR-E-FETCH-FAILED", "named branch"),
            ("remote mismatch", "NVKR-E-USAGE", "does not match local HEAD"),
            ("missing workflow", "NVKR-E-FETCH-FAILED", "default branch first"),
            ("disabled workflow", "NVKR-E-USAGE", "registered and active"),
            ("timeout", "NVKR-E-TIMEOUT", "No workflow was dispatched"),
            ("malformed response", "NVKR-E-FETCH-FAILED", "malformed JSON"),
            ("ambiguous response", "NVKR-E-FETCH-FAILED", "ambiguous response"),
            ("missing object", "NVKR-E-USAGE", "does not match local HEAD"),
            ("wrong ref", "NVKR-E-USAGE", "does not match local HEAD"),
            ("local moved", "NVKR-E-USAGE", "changed during preflight"),
        ]:
            with self.subTest(failure=failure):
                commands = FakeCommands(failure)
                with self.assertRaises(pipeline.PipelineDevError) as raised:
                    pipeline.dispatch(REPO, runner=commands, output=io.StringIO())
                self.assertIn(code, str(raised.exception))
                self.assertIn(message, str(raised.exception))
                self.assertEqual(commands.dispatches(), [])

    def test_invalid_repo_does_not_start_commands(self):
        for repo in ("", "nvkr", "https://github.com/example/nvkr", "example/../nvkr", "example/..", "example/nvkr\n", "--flag/repo"):
            with self.subTest(repo=repo):
                commands = FakeCommands()
                with self.assertRaisesRegex(pipeline.PipelineDevError, "NVKR-E-USAGE"):
                    pipeline.dispatch(repo, runner=commands)
                self.assertEqual(commands.calls, [])

    def test_failed_dispatch_is_not_retried_or_reported_successful(self):
        for failure, code, detail in [
            ("dispatch timeout", "NVKR-E-TIMEOUT", "outcome is unknown"),
            ("dispatch rejected", "NVKR-E-FETCH-FAILED", "inspect matching runs before retrying"),
        ]:
            with self.subTest(failure=failure):
                commands, output = FakeCommands(failure), io.StringIO()
                with self.assertRaises(pipeline.PipelineDevError) as raised:
                    pipeline.dispatch(REPO, runner=commands, output=output)
                self.assertIn(code, str(raised.exception))
                self.assertIn(detail, str(raised.exception))
                self.assertEqual(len(commands.dispatches()), 1)
                self.assertIn("Inspect matching runs:", output.getvalue())
                self.assertNotIn("Dispatch request accepted", output.getvalue())


if __name__ == "__main__":
    unittest.main()
