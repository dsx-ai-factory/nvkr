# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("native_report", Path(__file__).with_name("report-native-toolchains.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def subjects(arch):
    digest = "sha256:" + "a" * 64
    plan = {
        "builds": [{"platform": f"linux/{arch}", "executionGraph": {"nodes": [
            {"toolchain": f"{MODULE.REGISTRY}/packer-native@{digest}"}, {},
        ]}}],
        "environment": {"diskInspectionProfiles": {f"linux/{arch}": {
            "image": f"{MODULE.REGISTRY}/inspector-native@{digest}",
        }}},
    }
    proposal = [{"arch": arch, "kind": kind, "manifestDigest": digest,
                 "imageID": "sha256:" + "b" * 64} for kind in ("packer", "inspector")]
    return plan, proposal


def results(amd64="false", arm64="false"):
    result = {job: {"result": "skipped"} for job in MODULE.JOBS}
    result["measure"] = {"result": "success", "outputs": {
        "changed_amd64": amd64, "changed_arm64": arm64,
    }}
    return result


def issue(number, title=MODULE.TITLE, labeled=False, **extra):
    return {"number": number, "title": title,
            "labels": [{"name": MODULE.LABEL}] if labeled else [], **extra}


class FakeGitHub:
    def __init__(self, issues=(), label_exists=True):
        self.issues = copy.deepcopy(list(issues))
        self.labels = [{"name": MODULE.LABEL}] if label_exists else []
        self.writes = []
        self.lookup_failure = False

    def gh(self, *args, payload=None):
        endpoint = args[-1]
        base = f"repos/{MODULE.REPOSITORY}"
        if payload is None:
            # Exercise the real pagination interface, including an empty first page.
            assert args[:3] == ("api", "--paginate", "--slurp"), args
            if self.lookup_failure:
                raise subprocess.CalledProcessError(1, args)
            if endpoint == f"{base}/labels?per_page=100":
                return [[], copy.deepcopy(self.labels)]
            if endpoint == f"{base}/issues?state=open&labels={MODULE.LABEL}&per_page=100":
                return [[], copy.deepcopy([i for i in self.issues if {"name": MODULE.LABEL} in i["labels"]])]
            if endpoint == f"{base}/issues?state=open&per_page=100":
                return [[], copy.deepcopy(self.issues)]
            raise AssertionError(args)
        assert args[:3] == ("api", "--method", "POST"), args
        self.writes.append((endpoint, payload))
        if endpoint == f"{base}/labels":
            self.labels.append({"name": payload["name"]})
        elif endpoint == f"{base}/issues":
            self.issues.append(issue(1000, payload["title"], True))
        elif endpoint.endswith("/labels"):
            number = int(endpoint.split("/")[-2])
            next(i for i in self.issues if i["number"] == number)["labels"].append({"name": MODULE.LABEL})
        elif not endpoint.endswith("/comments"):
            raise AssertionError(args)
        return {}


class ComparisonTest(unittest.TestCase):
    def test_manifest_comparison_uses_both_image_subjects(self):
        for arch in MODULE.ARCHES:
            for changed in (None, "packer", "inspector"):
                with self.subTest(arch=arch, changed=changed):
                    plan, proposal = subjects(arch)
                    # Repeated Packer consumers are one pin, not an ambiguity.
                    plan["builds"] *= 2
                    if changed:
                        next(image for image in proposal if image["kind"] == changed)["manifestDigest"] = "sha256:" + "c" * 64
                    self.assertEqual(MODULE.compare(plan, proposal, arch), changed is not None)

    def test_incomplete_or_ambiguous_subjects_fail_closed(self):
        for scenario in ("missing-packer", "ambiguous-packer", "missing-inspector", "wrong-repository",
                         "mutable-pin", "missing-measurement", "duplicate", "wrong-arch", "bad-digest"):
            with self.subTest(scenario=scenario):
                plan, proposal = subjects("amd64")
                nodes = plan["builds"][0]["executionGraph"]["nodes"]
                profile = plan["environment"]["diskInspectionProfiles"]["linux/amd64"]
                if scenario == "missing-packer":
                    nodes.clear()
                elif scenario == "ambiguous-packer":
                    nodes.append({"toolchain": f"{MODULE.REGISTRY}/packer-native@sha256:" + "c" * 64})
                elif scenario == "missing-inspector":
                    profile.clear()
                elif scenario == "wrong-repository":
                    profile["image"] = "ghcr.io/example/other@sha256:" + "a" * 64
                elif scenario == "mutable-pin":
                    profile["image"] = f"{MODULE.REGISTRY}/inspector-native@latest"
                elif scenario == "missing-measurement":
                    proposal.pop()
                elif scenario == "duplicate":
                    proposal.append(proposal[0])
                elif scenario == "wrong-arch":
                    proposal[0]["arch"] = "arm64"
                else:
                    proposal[0]["manifestDigest"] = "latest"
                with self.assertRaises((ValueError, KeyError)):
                    MODULE.compare(plan, proposal, "amd64")

    def test_compare_cli_outputs_distinct_matrix_keys(self):
        for arch in MODULE.ARCHES:
            with self.subTest(arch=arch), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                plan, proposal = subjects(arch)
                (root / "plan").write_text(json.dumps(plan))
                (root / "proposal").write_text(json.dumps(proposal))
                output = root / "output"
                with patch.dict(os.environ, {"GITHUB_OUTPUT": str(output)}), patch("sys.argv", [
                    "report-native-toolchains.py", "compare", "--arch", arch,
                    "--plan", str(root / "plan"), "--proposal", str(root / "proposal"),
                ]):
                    MODULE.main()
                self.assertEqual(output.read_text(), f"changed_{arch}=false\n")


class RoutingTest(unittest.TestCase):
    def test_existing_open_issue_gets_comment_without_create(self):
        for issues, expected in (
            ([issue(9, "Renamed by owner", True), issue(1)], 9),
            ([issue(5)], 5),
            ([issue(8, "Native toolchain refresh 123"), issue(3, "Native toolchain refresh 456")], 3),
            ([issue(1, labeled=True, pull_request={}), issue(4)], 4),
        ):
            with self.subTest(expected=expected):
                fake = FakeGitHub(issues, label_exists=False)
                with patch.object(MODULE, "gh", fake.gh):
                    MODULE.post("actionable report")
                self.assertTrue(fake.writes[-1][0].endswith(f"/issues/{expected}/comments"))
                self.assertEqual(fake.writes[-1][1], {"body": "actionable report"})
                self.assertFalse(any(endpoint.endswith("/issues") for endpoint, _ in fake.writes))

    def test_no_open_issue_creates_once_then_comments(self):
        fake = FakeGitHub([issue(1, "Unrelated title")], label_exists=False)
        with patch.object(MODULE, "gh", fake.gh):
            MODULE.post("first report")
            MODULE.post("second report")
        creations = [body for endpoint, body in fake.writes if endpoint.endswith("/issues")]
        self.assertEqual(creations, [{"title": MODULE.TITLE, "labels": [MODULE.LABEL],
                                     "assignees": ["njhensley"], "body": "first report"}])
        self.assertTrue(fake.writes[-1][0].endswith("/issues/1000/comments"))

    def test_lookup_failure_never_creates(self):
        fake = FakeGitHub()
        fake.lookup_failure = True
        with patch.object(MODULE, "gh", fake.gh), self.assertRaises(subprocess.CalledProcessError):
            MODULE.post("report")
        self.assertEqual(fake.writes, [])

    def test_only_actionable_runs_post(self):
        approval = {job: {"result": "success"} for job in MODULE.JOBS}
        approval["measure"]["result"] = "skipped"
        cases = [("unchanged", results(), "", False),
                 ("amd64", results("true"), "", True),
                 ("arm64", results(arm64="true"), "", True),
                 ("approved-success", approval, "123", False)]
        for job in MODULE.JOBS:
            failure = results()
            failure[job]["result"] = "failure"
            cases.append((f"failure-{job}", failure, "", True))
        cancelled = results()
        cancelled["measure"]["result"] = "cancelled"
        cases.append(("cancelled", cancelled, "", True))
        for name, jobs, run, actionable in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                env = {"GITHUB_REPOSITORY": MODULE.REPOSITORY, "GITHUB_REF": "refs/heads/main",
                       "RESULTS": json.dumps(jobs), "APPROVAL_RUN": run,
                       "RUN_URL": f"https://github.com/{MODULE.REPOSITORY}/actions/runs/123",
                       "GITHUB_STEP_SUMMARY": str(Path(temp) / "summary")}
                fake = FakeGitHub()
                with patch.dict(os.environ, env), patch.object(MODULE, "gh", fake.gh), contextlib.redirect_stdout(io.StringIO()):
                    MODULE.report()
                self.assertEqual(bool(fake.writes), actionable)
                if not actionable:
                    self.assertFalse((Path(temp) / "summary").exists())

    def test_missing_matrix_comparison_is_not_unchanged(self):
        for value in (None, "", "unknown"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                MODULE.reason(results(arm64=value), "")


if __name__ == "__main__":
    unittest.main()
