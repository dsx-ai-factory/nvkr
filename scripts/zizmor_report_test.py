# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("zizmor_report", Path(__file__).with_name("zizmor-report.py"))
REPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPORT)


class ZizmorReportTests(unittest.TestCase):
    def test_inventory_includes_generated_and_actions_and_rejects_missing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            names = [".github/workflows/authored.yaml", ".github/workflows/nvkr-run.yml",
                     ".github/nvkr-run/actions/bootstrap/action.yml"]
            for name in names:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("fixture")
            subprocess.run(["git", "-C", str(root), "add", ".github"], check=True)
            self.assertEqual(REPORT.collect(root), sorted(names))
            (root / names[0]).rename(root / "moved.yaml")
            with self.assertRaisesRegex(ValueError, "missing or non-regular"):
                REPORT.collect(root)

    def test_checksum_failure_prevents_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            def download(*args, **kwargs):
                (work / "zizmor.tar.gz").write_bytes(b"wrong archive")
            with patch.object(REPORT.platform, "system", return_value="Linux"), \
                 patch.object(REPORT.platform, "machine", return_value="x86_64"), \
                 patch.object(REPORT.subprocess, "run", side_effect=download), \
                 patch.object(REPORT.tarfile, "open") as extract:
                with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                    REPORT.install(work)
                extract.assert_not_called()
                self.assertFalse((work / "zizmor").exists())

    def test_install_failure_is_incomplete(self):
        for failure in (OSError("download failed"), ValueError("checksum mismatch"),
                        subprocess.TimeoutExpired("curl", 70)):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                with patch.object(REPORT, "collect", return_value=[".github/workflows/test.yml"]), \
                     patch.object(REPORT, "install", side_effect=failure), \
                     patch.dict(REPORT.os.environ, {"GITHUB_STEP_SUMMARY": ""}):
                    self.assertEqual(REPORT.report(root, root / "reports"), 1)
                attempt, = (root / "reports").iterdir()
                self.assertIn("INCOMPLETE", (attempt / "summary.txt").read_text())

    def test_report_verdicts(self):
        finding = {"ident": "unpinned-uses", "desc": "unpinned action",
                   "determinations": {"severity": "High"}, "ignored": False, "locations": []}
        cases = [
            ("clean", "[]", "", 0, "NO FINDINGS", 0),
            ("findings", json.dumps([finding]), "", 0, "FINDINGS", 0),
            ("bad JSON", "{", "", 0, "INCOMPLETE", 1),
            ("bad schema", "{}", "", 0, "INCOMPLETE", 1),
            ("tool failure", "[]", "failed collection", 1, "INCOMPLETE", 1),
            ("audit warning", "[]", " WARN audit: could not parse expression", 0, "INCOMPLETE", 1),
        ]
        for name, raw, stderr, code, verdict, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                def scan(args, **kwargs):
                    for flag in ("--offline", "--strict-collection", "--no-exit-codes"):
                        self.assertIn(flag, args)
                    kwargs["stdout"].write(raw.encode())
                    kwargs["stderr"].write(stderr.encode())
                    return subprocess.CompletedProcess(args, code)
                with patch.object(REPORT, "collect", return_value=[".github/workflows/test.yml"]), \
                     patch.object(REPORT, "install", return_value=root / "zizmor"), \
                     patch.object(REPORT.subprocess, "run", side_effect=scan) as invoke, \
                     patch.dict(REPORT.os.environ, {"GITHUB_STEP_SUMMARY": ""}):
                    self.assertEqual(REPORT.report(root, root / "reports"), expected)
                    invoke.assert_called_once()
                attempt, = (root / "reports").iterdir()
                self.assertIn(f": {verdict}\n", (attempt / "summary.txt").read_text())
                self.assertEqual((attempt / "findings.json").read_text(), raw)


if __name__ == "__main__":
    unittest.main()
