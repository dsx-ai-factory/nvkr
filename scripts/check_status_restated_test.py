# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Contracts for the diff-scoped status restatement check."""

import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("restated", Path(__file__).with_name("check-status-restated.py"))
restated = importlib.util.module_from_spec(spec)
spec.loader.exec_module(restated)

LINK = "link docs/adr/reconciliation.md instead"
LEDGER = "| Owner | Implemented | Remaining |\n|---|---|---|\n| 01–03 Identity | x | y |\n| 21 Generation | x | y |\n"
BASE = {
    "docs/adr/reconciliation.md": LEDGER,
    "docs/guide.md": "Existing text remains unrecorded.\n",
    "docs/old.md": "Old text remains unproven.\n",
}
# Each case owns distinct paths, so one base...head diff exercises them all.
CASES = {
    "added restatement": (
        {"docs/guide.md": BASE["docs/guide.md"] + "Hosted acceptance remains unrecorded.\n"},
        [f'docs/guide.md:2: status restated ("remains unrecorded"); {LINK}']),
    "wrapped phrase": (
        {"README.md": "Intro.\nCompletion requires a recorded\nrun of the workflow.\n"},
        [f'README.md:2: status restated ("requires a recorded run"); {LINK}']),
    "adr file names its owner row": (
        {"docs/adr/0021-generation.md": "Its hosted acceptance is unrecorded.\n"},
        ['docs/adr/0021-generation.md:1: status restated ("is unrecorded"); '
         "link docs/adr/reconciliation.md:4 (Support by owner, 21 Generation) instead"]),
    "adr reference names a range row": (
        {"docs/identity.md": "ADR-02 identity has not been exercised.\n"},
        ['docs/identity.md:1: status restated ("has not been exercised"); '
         "link docs/adr/reconciliation.md:3 (Support by owner, 01 Identity) instead"]),
    "ledger and evidence": (
        {"docs/adr/reconciliation.md": LEDGER + "Hosted signing remains unproven.\n",
         "docs/evidence/run.md": "Hosted signing remains unproven.\n"}, []),
    "lane names": ({"docs/lanes.md": "Dry-run stays static-unqualified; local runs remain unqualified.\n"}, []),
    "unchanged text": ({"docs/old.md": BASE["docs/old.md"] + "Unrelated.\n"}, []),
    "not markdown": ({"pkg/x.go": "// remains unrecorded\n"}, []),
    "escape with reason": (
        {"docs/escaped.md": "Hosted publication was exercised. <!-- status-ok: dated log -->\n"}, []),
    "escape on a wrapped line": (
        {"docs/wrapped.md": "It requires a recorded\nrun. <!-- status-ok: quoted policy -->\n"}, []),
    "escape without reason": (
        {"docs/bare.md": "Hosted publication was exercised. <!-- status-ok: -->\n"},
        [f'docs/bare.md:1: status restated ("Hosted publication was exercised"); {LINK}',
         "docs/bare.md:1: status-ok escape needs a reason: <!-- status-ok: why -->"]),
}


def git(repo, *args):
    return subprocess.run(
        ["git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
         "-c", "commit.gpgsign=false", *args],
        check=True, capture_output=True, text=True).stdout.strip()


def commit(repo, files):
    for name, text in files.items():
        path = Path(repo, name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    git(repo, "add", "-A")
    git(repo, "commit", "-q", "-m", "change")
    return git(repo, "rev-parse", "HEAD")


class StatusRestatedTest(unittest.TestCase):
    """Check which added Markdown lines restate status and how they are reported."""

    @classmethod
    def setUpClass(cls):
        tmp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(tmp.cleanup)
        cls.repo = tmp.name
        git(cls.repo, "init", "-q", "-b", "main")
        cls.base = commit(cls.repo, BASE)
        cls.head = commit(cls.repo, {k: v for files, _ in CASES.values() for k, v in files.items()})
        cls.findings = restated.check(cls.repo, cls.base, cls.head)

    def test_findings(self):
        """Flag added restatements, including wrapped ones; skip the ledger, evidence, lanes and code."""
        for name, (files, want) in CASES.items():
            with self.subTest(name):
                got = [f for f in self.findings if f.split(":", 1)[0] in files]
                self.assertEqual(got, want)
        self.assertEqual(len(self.findings), sum(len(want) for _, want in CASES.values()))

    def test_exit_status(self):
        """Exit 1 with findings, 0 without, and fail closed on an unknown revision."""
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(restated.main(["--repo", self.repo, self.base, self.head]), 1)
            self.assertEqual(restated.main(["--repo", self.repo, self.head, self.head]), 0)
            with self.assertRaises(SystemExit) as raised:
                restated.main(["--repo", self.repo, "no-such-revision", self.head])
        self.assertIn("no-such-revision", str(raised.exception.code))


if __name__ == "__main__":
    unittest.main()
