# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Check notice metadata independently of Go's module cache or network."""
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name("gen_notices.py")


class LinkedModuleNotices(unittest.TestCase):
    def module(self, root, name, license_text="MIT License\nSynthetic license text."):
        directory = root / name
        directory.mkdir(parents=True)
        (directory / "LICENSE").write_text(license_text, encoding="utf-8")
        return directory

    def generate(self, root, records):
        metadata = root / "linked.tsv"
        metadata.write_text("".join(
            "\t".join(map(str, record)) + "\n" for record in records
        ), encoding="utf-8")
        return subprocess.run(
            [sys.executable, "-B", str(SCRIPT), str(metadata)],
            cwd=root, capture_output=True, text=True, timeout=10,
        )

    def test_nested_only_dependency_uses_supplied_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = self.module(root, "demos/nvke/module cache/nested-only",
                                    "Apache License\nSynthetic copyright — preserved.")
            (directory / "NOTICE").write_text("Synthetic nested NOTICE.\n", encoding="utf-8")
            result = self.generate(root, [("example.test/nested-only", "v1.2.3", directory)])
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stderr, "")
            self.assertIn("| `example.test/nested-only` | v1.2.3 | Apache-2.0 |", result.stdout)
            self.assertIn("Synthetic copyright — preserved.", result.stdout)
            self.assertIn("NOTICE:\n\n```\nSynthetic nested NOTICE.\n```", result.stdout)

    def test_identical_duplicates_and_input_order_do_not_change_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            first = ("example.test/a", "v1.0.0", self.module(root, "a"))
            second = ("example.test/z", "v2.0.0", self.module(root, "z"))
            baseline = self.generate(root, [first, second])
            self.assertEqual(baseline.returncode, 0, baseline.stderr)
            for records in ([second, first], [first, second, first], [second, second, first]):
                with self.subTest(records=records):
                    result = self.generate(root, records)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, baseline.stdout)
            self.assertEqual(baseline.stdout.count("## example.test/a\n"), 1)
            self.assertLess(baseline.stdout.index("## example.test/a\n"),
                            baseline.stdout.index("## example.test/z\n"))

    def test_conflicting_metadata_fails_before_emitting_notices(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = self.module(root, "first")
            other = self.module(root, "other")
            for name, version, location in (("version", "v2.0.0", directory),
                                            ("directory", "v1.0.0", other)):
                with self.subTest(conflict=name):
                    result = self.generate(root, [
                        ("example.test/m", "v1.0.0", directory),
                        ("example.test/m", version, location),
                    ])
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")
                    self.assertIn("conflicting linked versions for example.test/m", result.stderr)

    def test_malformed_tsv_fails_before_emitting_notices(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for record in (("example.test/m", "v1.0.0"),
                           ("example.test/m", "v1.0.0", "/unused", "extra")):
                with self.subTest(record=record):
                    result = self.generate(root, [record])
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
