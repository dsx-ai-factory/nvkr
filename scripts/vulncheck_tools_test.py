# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Exercise the real Make recipe with synthetic scanner exit codes, offline."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class ToolVulnerabilityReports(unittest.TestCase):
    def test_results_and_collection_failures(self):
        root = Path(__file__).resolve().parents[1]
        cases = [(0, 0, False), (3, 0, False), (0, 3, False),
                 (1, 0, True), (0, 2, True), (127, 3, True)]
        for first, second, incomplete in cases:
            with self.subTest(first=first, second=second), tempfile.TemporaryDirectory() as tmp:
                work = Path(tmp)
                (work / "bin").mkdir()
                (work / ".go-version").write_text((root / ".go-version").read_text())
                go = work / "bin/go"
                go.write_text("#!/bin/sh\nif [ \"$1\" = env ]; then printf 'go%s\\n' \"$(cat .go-version)\"; fi\n")
                go.chmod(0o755)
                scanner = work / "bin/govulncheck"
                scanner.write_text(
                    "#!/bin/sh\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$3\" >> calls\n"
                    f'case "$2" in toolchains/actionlint) exit {first};; *) exit {second};; esac\n'
                )
                scanner.chmod(0o755)
                result = subprocess.run(
                    ["make", "--no-print-directory", "-f", str(root / "Makefile"), "vulncheck-tools"],
                    cwd=work, env={**os.environ, "PATH": str(work / "bin") + os.pathsep + os.environ["PATH"]},
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode != 0, incomplete, result.stdout + result.stderr)
                self.assertEqual((work / "calls").read_text().splitlines(), [
                    "-C toolchains/actionlint github.com/rhysd/actionlint/cmd/actionlint",
                    "-C toolchains/govulncheck golang.org/x/vuln/cmd/govulncheck",
                ])
                self.assertEqual("INCOMPLETE" in result.stdout, incomplete)
                self.assertEqual("findings (report-only)" in result.stdout, 3 in (first, second))


if __name__ == "__main__":
    unittest.main()
