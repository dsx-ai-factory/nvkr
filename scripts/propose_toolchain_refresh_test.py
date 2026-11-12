# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("toolchain_proposal", Path(__file__).with_name("propose-toolchain-refresh.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ProposalTest(unittest.TestCase):
    def test_path_admission(self):
        cases = {
            "inputs/catalogs/node-images/release/registry.yaml": True,
            "inputs/environments/github-release.yaml": True,
            ".github/nvkr-run/plan.json": True,
            ".github/workflows/nvkr-run.yml": True,
            "inputs/catalogs/node-images/release/pins.lock.yaml": False,
            "inputs/policies/toolchain-maintenance/123.json": False,
            "inputs/policies/ovn-producer-overlays.json": True,
            "inputs/policies/other-producer-overlays.json": False,
            "toolchains/nvkr-packer/local-bindings.json": True,
            "toolchains/nvkr-packer/toolchain.lock.json": False,
            ".github/workflows/merge-gate.yaml": False,
            "inputs/catalogs/../../secret.json": False,
            "/tmp/registry.yaml": False,
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(MODULE.allowed_path(path), expected)

    def test_proposal_never_force_updates_or_reopens(self):
        for scenario in ("create", "retry", "closed", "conflict", "main-advanced", "unexpected-file"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                file = root / "inputs/environments/test.json"
                file.parent.mkdir(parents=True)
                file.write_text('{"image":"synthetic"}')
                record = root / "inputs/policies/toolchain-maintenance/123.json"
                record.parent.mkdir(parents=True)
                record.write_text(json.dumps({"sourceCommit": "a" * 40, "images": []}))
                calls = []
                sha = "b" * 40
                env = {"GITHUB_SHA": sha, "APPROVAL_RUN": "123", "APP_SLUG": "synthetic-review-bot",
                       "GITHUB_REPOSITORY": MODULE.REPOSITORY, "GITHUB_REF": "refs/heads/main",
                       "GITHUB_WORKFLOW_REF": f"{MODULE.REPOSITORY}/.github/workflows/nvkr-native-toolchains.yml@refs/heads/main",
                       "GITHUB_RUN_ID": "456"}

                def invoke(args, payload=None):
                    if args[:2] == ["gh", "api"]:
                        return b'{"id":1234}'
                    if args[1:3] == ["rev-parse", "HEAD"]:
                        return sha.encode()
                    if args[1:3] == ["rev-parse", "HEAD^{tree}"]:
                        return b"base-tree"
                    if args[1] == "diff":
                        return b"inputs/environments/test.json\0"
                    if args[1] == "ls-files":
                        return b"secret.json\0" if scenario == "unexpected-file" else b".nvkr-inputs/source/\0"
                    raise AssertionError(args)

                def api(endpoint, payload=None):
                    calls.append((endpoint, payload))
                    if endpoint == "git/ref/heads/main":
                        return {"object": {"sha": "c" * 40 if scenario == "main-advanced" else sha}}
                    if endpoint.startswith("git/matching-refs/"):
                        return [] if scenario == "create" else [{"ref": "refs/heads/automation/toolchain-refresh-123", "object": {"sha": "candidate"}}]
                    if endpoint == "git/blobs":
                        return {"sha": "blob"}
                    if endpoint == "git/trees":
                        return {"sha": "candidate-tree"}
                    if endpoint == "git/commits/candidate":
                        return {"tree": {"sha": "different" if scenario == "conflict" else "candidate-tree"}}
                    if endpoint == "git/commits":
                        self.assertIn("Signed-off-by: synthetic-review-bot[bot]", payload["message"])
                        self.assertEqual(payload["parents"], [sha])
                        return {"sha": "candidate"}
                    if endpoint == "git/refs":
                        self.assertEqual(payload["ref"], "refs/heads/automation/toolchain-refresh-123")
                        return {}
                    if endpoint.startswith("pulls?"):
                        return [] if scenario == "create" else [{"head": {"repo": {"full_name": MODULE.REPOSITORY}}, "html_url": "https://github.com/dsx-ai-factory/nvkr/pull/999999", "state": "closed" if scenario == "closed" else "open"}]
                    if endpoint == "pulls":
                        self.assertFalse(payload["draft"])
                        self.assertEqual(payload["base"], "main")
                        return {"html_url": "https://github.com/dsx-ai-factory/nvkr/pull/999999"}
                    raise AssertionError((endpoint, payload))

                with contextlib.chdir(root), patch.dict(os.environ, env), patch.object(MODULE, "invoke", invoke), patch.object(MODULE, "api", api), contextlib.redirect_stdout(io.StringIO()):
                    if scenario in ("conflict", "main-advanced", "unexpected-file"):
                        with self.assertRaises(RuntimeError):
                            MODULE.propose()
                    else:
                        MODULE.propose()
                writes = [(endpoint, body) for endpoint, body in calls if body is not None]
                self.assertFalse(any("force" in body for _, body in writes))
                if scenario != "create":
                    self.assertFalse(any(endpoint in ("pulls", "git/refs", "git/commits") for endpoint, _ in writes))
                if scenario in ("main-advanced", "unexpected-file"):
                    self.assertEqual(writes, [])


if __name__ == "__main__":
    unittest.main()
