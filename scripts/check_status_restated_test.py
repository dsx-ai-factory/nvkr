# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Contracts for the diff-scoped status restatement check."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("restated", Path(__file__).with_name("check-status-restated.py"))
restated = importlib.util.module_from_spec(spec)
spec.loader.exec_module(restated)

class Link(str):
    """A fixture value committed as a symbolic link to this target."""


LINK = "link its docs/adr/reconciliation/ row instead"
OWNERS = "link docs/adr/reconciliation/"
ROW = "| Owner | Implemented | Remaining |\n|---|---|---|\n"
BASE = {
    "docs/adr/reconciliation/README.md": "# Architecture implementation and evidence\n",
    "docs/adr/reconciliation/adr-01-03.md": ROW + "| 01–03 Identity | x | y |\n",
    "docs/adr/reconciliation/adr-21.md": ROW + "| 21 Generation | x | y |\n",
    "docs/adr/reconciliation/adr-17-cli-presentation-and-inspection.md": "## ADR-17 CLI presentation\n",
    "docs/guide.md": "Existing text remains unrecorded.\n",
    "docs/old.md": "Old text remains unproven.\n",
    "docs/moved-from.md": "Old text remains unproven.\nTwo.\nThree.\nFour.\nFive.\n",
    "docs/dash.md": "-- intro\n",
    "docs/straddle.md": "Intro.\nCompletion requires a recorded\nOld tail.\n",
    "docs/three.md": "It requires a\nrecorded\nOld tail.\n",
    "docs/split.md": "The release is\nunrecorded.\n",
    "docs/link.md": Link("old.md"),
    "docs/items.md": "- ADR-02 identity.\n",
    "docs/swap.md": "Plain.\n",
    "outside.txt": "Hosted acceptance remains unrecorded.\n",
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
         f'{OWNERS}adr-21.md instead']),
    "adr reference names a range row": (
        {"docs/identity.md": "Identity has not been exercised\nfor ADR-02.\n"},
        ['docs/identity.md:1: status restated ("has not been exercised"); '
         f'{OWNERS}adr-01-03.md instead']),
    "topic file is not an owner file": (
        {"docs/cli.md": "The ADR-17 viewer is untested.\n"},
        [f'docs/cli.md:1: status restated ("is untested"); {LINK}']),
    "renamed file reports only added lines": (
        {"docs/moved-from.md": None, "docs/moved.md": BASE["docs/moved-from.md"] + "Signing is untested.\n"},
        [f'docs/moved.md:6: status restated ("is untested"); {LINK}']),
    "added line that looks like a header": (
        {"docs/dash.md": "++ Hosted acceptance remains unrecorded.\n"},
        [f'docs/dash.md:1: status restated ("remains unrecorded"); {LINK}']),
    "validation subject makes unverified count": (
        {"docs/cycle.md": "The runtime cycle remains unverified; payloads remain unverified by design.\n"},
        [f'docs/cycle.md:1: status restated ("cycle remains unverified"); {LINK}']),
    "evidence category named, not claimed": (
        {"docs/category.md": "[Reconciliation](adr/reconciliation.md) owns externally exercised scope.\n"
                             "The release was not externally exercised.\n"},
        [f'docs/category.md:2: status restated ("not externally exercised"); {LINK}']),
    "phrase starting on an unchanged line": (
        {"docs/straddle.md": "Intro.\nCompletion requires a recorded\nrun of the workflow.\n"},
        [f'docs/straddle.md:3: status restated ("requires a recorded run"); {LINK}']),
    "phrase wrapping over several unchanged lines": (
        {"docs/three.md": "It requires a\nrecorded\nrun of the workflow.\n"},
        [f'docs/three.md:3: status restated ("requires a recorded run"); {LINK}']),
    "blank line ends a paragraph": ({"docs/split.md": "The release is\n\nunrecorded.\n"}, []),
    "adr in another paragraph is not the owner": (
        {"docs/neighbour.md": "ADR-02 owns identity.\n\nHosted signing remains unproven.\n"},
        [f'docs/neighbour.md:3: status restated ("remains unproven"); {LINK}']),
    "adr in a neighbouring list item is not the owner": (
        {"docs/items.md": BASE["docs/items.md"] + "- Hosted signing remains unproven.\n"},
        [f'docs/items.md:2: status restated ("remains unproven"); {LINK}']),
    "numbered item with a wrapped phrase": (
        {"docs/numbered.md": "1. ADR-02 identity.\n2. Hosted signing remains\n   unproven.\n  - ADR-21 note.\n"},
        [f'docs/numbered.md:2: status restated ("remains unproven"); {LINK}']),
    "file replaced by a symlink is its target path": ({"docs/swap.md": Link("../outside.txt")}, []),
    "symlink replaced by a file": (
        {"docs/link.md": "Hosted acceptance remains unrecorded.\n"},
        [f'docs/link.md:1: status restated ("remains unrecorded"); {LINK}']),
    "path with a space": (
        {"docs/my file.md": "Signing is untested.\n"},
        [f'docs/my file.md:1: status restated ("is untested"); {LINK}']),
    "path git quotes": (
        {'docs/naïve "q".md': "Signing is untested.\n"},
        [f'docs/naïve "q".md:1: status restated ("is untested"); {LINK}']),
    "ledger look-alike is checked": (
        {"docs/adr/reconciliation.md": "Hosted signing remains unproven.\n",
         "docs/adr/reconciliation-notes.md": "Hosted signing remains unproven.\n"},
        [f'docs/adr/reconciliation-notes.md:1: status restated ("remains unproven"); {LINK}',
         f'docs/adr/reconciliation.md:1: status restated ("remains unproven"); {LINK}']),
    "ledger and evidence": (
        {"docs/adr/reconciliation/adr-21.md": BASE["docs/adr/reconciliation/adr-21.md"] + "Hosted signing remains unproven.\n",
         "docs/adr/reconciliation/native-arc-disk-route.md": "Hosted signing remains unproven.\n",
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


# Variables such as GIT_DIR and GIT_INDEX_FILE override `git -C`, so an inherited one would point the
# fixture at the caller's repository.
LOCAL_GIT_ENV = subprocess.run(["git", "rev-parse", "--local-env-vars"], check=True, capture_output=True,
                               text=True).stdout.split()


def fixture_env():
    return {name: value for name, value in os.environ.items() if name not in LOCAL_GIT_ENV}


def git(repo, *args):
    return subprocess.run(
        ["git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
         "-c", "commit.gpgsign=false", *args],
        check=True, capture_output=True, text=True, env=fixture_env()).stdout.strip()


def commit(repo, files):
    for name, text in files.items():
        path = Path(repo, name)
        if path.is_symlink() or (path.exists() and (text is None or isinstance(text, Link))):
            path.unlink()
        if text is None:
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(text, Link):
            path.symlink_to(text)
        else:
            path.write_text(text)
    git(repo, "add", "-A")
    git(repo, "commit", "-q", "-m", "change")
    return git(repo, "rev-parse", "HEAD")


class StatusRestatedTest(unittest.TestCase):
    """Check which added Markdown lines restate status and how they are reported."""

    @classmethod
    def setUpClass(cls):
        environment = mock.patch.dict(os.environ, fixture_env(), clear=True)  # covers the checker's git calls
        environment.start()
        cls.addClassCleanup(environment.stop)
        tmp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(tmp.cleanup)
        cls.repo = tmp.name
        git(cls.repo, "init", "-q", "-b", "main")
        cls.base = commit(cls.repo, BASE)
        cls.head = commit(cls.repo, {k: v for files, _ in CASES.values() for k, v in files.items()})
        _, cls.findings = restated.check(cls.repo, cls.base, cls.head)

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
        self.assertIn("STATUS_BASE", str(raised.exception.code))

    def test_textconv_ignored(self):
        """A local Markdown textconv driver cannot hide added lines from the check."""
        attributes = Path(self.repo, ".git/info/attributes")
        attributes.parent.mkdir(exist_ok=True)
        attributes.write_text("*.md diff=hide\n")
        git(self.repo, "config", "diff.hide.textconv", "true")
        try:
            self.assertEqual(restated.check(self.repo, self.base, self.head)[1], self.findings)
        finally:
            attributes.write_text("")
            git(self.repo, "config", "--unset", "diff.hide.textconv")

    def test_fixture_leaves_caller_index_alone(self):
        """An inherited GIT_INDEX_FILE cannot make the fixture or the check write another repository's index."""
        with tempfile.TemporaryDirectory() as outside, tempfile.TemporaryDirectory() as repo:
            git(outside, "init", "-q")
            Path(outside, "sentinel").write_text("keep\n")
            git(outside, "add", "sentinel")
            index = Path(outside, ".git/index")
            before = index.read_bytes()
            with mock.patch.dict(os.environ, {"GIT_INDEX_FILE": str(index)}):
                git(repo, "init", "-q", "-b", "main")
                first = commit(repo, {"docs/a.md": "Intro.\n"})
                second = commit(repo, {"docs/a.md": "Signing is untested.\n"})
                self.assertEqual(len(restated.check(repo, first, second)[1]), 1)
            self.assertEqual(index.read_bytes(), before)

    def test_missing_local_ledger(self):
        """Without a local ledger, naming an owner row fails closed with a concise message."""
        with tempfile.TemporaryDirectory() as empty, self.assertRaises(SystemExit) as raised:
            restated.owner_files(empty, None)
        self.assertIn("cannot list docs/adr/reconciliation/", str(raised.exception.code))
        self.assertIn("contains the per-section ledger", str(raised.exception.code))
        for name, files in {"empty ledger": [], "topic files only": ["README.md", "adr-17-cli.md"]}.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as checkout:
                ledger_dir = Path(checkout, "docs/adr/reconciliation")
                ledger_dir.mkdir(parents=True)
                for file in files:
                    Path(ledger_dir, file).write_text("# topic\n")
                with self.assertRaises(SystemExit) as raised:
                    restated.owner_files(checkout, None)
                self.assertIn("has no adr-NN.md owner files", str(raised.exception.code))
        with self.assertRaises(SystemExit) as raised:
            restated.owner_files(self.repo, "4b825dc642cb6eb9a060e54bf8d69288fbee4904")  # git's empty tree
        self.assertIn("predates the per-section ledger; merge main", str(raised.exception.code))

    def test_working_tree(self):
        """Without a head, check uncommitted and untracked Markdown since the merge base."""
        Path(self.repo, "docs/old.md").write_text(BASE["docs/old.md"] + "Signing is unproven.\n")
        Path(self.repo, "docs/new.md").write_text("Publication has not been recorded.\n")
        Path(self.repo, "docs/guide.md").unlink()
        Path(self.repo, "docs/guide.md").symlink_to("../outside.txt")  # local mode must not follow it
        Path(self.repo, "docs/untracked-link.md").symlink_to("../outside.txt")
        Path(self.repo, "docs/notes.md").write_text(
            "Intro.\n\nRelease requires a\nrecorded run.\n\nThe release is\n\nunrecorded.\n")
        _, findings = restated.check(self.repo, self.head)
        self.assertEqual(findings, [
            f'docs/new.md:1: status restated ("has not been recorded"); {LINK}',
            f'docs/notes.md:3: status restated ("requires a recorded run"); {LINK}',
            f'docs/old.md:2: status restated ("is unproven"); {LINK}',
        ])


if __name__ == "__main__":
    unittest.main()
