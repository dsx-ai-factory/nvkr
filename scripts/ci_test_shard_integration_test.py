# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Keep the real Go discovery, selection and execution contract in the CI gate."""

from collections import Counter
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
RUNNER = ROOT / "scripts" / "ci-test-shard.py"
MODULE = "example.com/shardfixture"

# Eleven targets suffice to keep all eleven shards nonempty. The prefix pair
# deliberately lands in different factory shards, detecting unanchored -run.
FACTORY = '''package fixture

import (
    "fmt"
    "os"
    "testing"
)

func TestExact(t *testing.T) {
    t.Parallel()
    t.Run("child", func(t *testing.T) {
        t.Parallel()
        if os.Getenv("NVKR_SHARD_FIXTURE_FAIL") == "1" {
            t.Fatal("injected shard failure")
        }
    })
}

func TestExactSuffix(t *testing.T) {}

func Example() {
    fmt.Println("example ran")
    // Output: example ran
}

func FuzzDecode0(f *testing.F) {
    f.Add("seed")
    f.Fuzz(func(t *testing.T, value string) {
        if value != "seed" { t.Fatal("unexpected seed") }
    })
}
'''

LIFECYCLE = '''package fixture
import "testing"
func TestExact(t *testing.T) {}
func TestParallel(t *testing.T) { t.Parallel() }
'''

ORDINARY = '''package fixture
import "testing"
func TestExact(t *testing.T) {}
'''


def terminal_tests(output):
    return Counter((event["Package"], event["Test"], event["Action"])
                   for event in map(json.loads, output.splitlines())
                   if "Test" in event and "/" not in event["Test"]
                   and event["Action"] in {"pass", "fail", "skip"})


class RealGoShardTest(unittest.TestCase):
    def run_command(self, directory, environment, *command, check=True):
        # Preserve the caller's compilation cache, but prohibit dependency or
        # toolchain downloads. A missing Go executable is a failure, not a skip.
        with subprocess.Popen(command, cwd=directory, env=environment,
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, text=True,
                              start_new_session=True) as process:
            try:
                stdout, stderr = process.communicate(timeout=90)
            except subprocess.TimeoutExpired:
                # The shard runner owns a separate Go process group. Let its
                # SIGTERM handler stop and join that group before forced cleanup.
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
                raise
            finally:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)
        result = subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
        if check:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def test_real_matrix_matches_unsharded_suite_and_propagates_failure(self):
        environment = dict(os.environ, GOTOOLCHAIN="local", GOWORK="off",
                           GOPROXY="off", GOSUMDB="off", GOFLAGS="",
                           NVKR_SHARD_FIXTURE_FAIL="0")
        with tempfile.TemporaryDirectory(prefix="nvkr-shard-contract-") as directory:
            root = Path(directory)
            version = (ROOT / ".go-version").read_text().strip()
            (root / "go.mod").write_text(f"module {MODULE}\n\ngo {version}\n")
            fixtures = {"pkg/nvkr/factory": FACTORY, "pkg/nvkr/lifecycle": LIFECYCLE}
            fixtures.update({f"ordinary/package{index}": ORDINARY for index in (0, 1, 2, 3, 23)})
            for package, source in fixtures.items():
                path = root / package / "contract_test.go"
                path.parent.mkdir(parents=True)
                path.write_text(source)

            baseline = self.run_command(directory, environment, "go", "test", "-json",
                                        "-count=1", "-timeout=10s", "./...")
            expected = terminal_tests(baseline.stdout)
            self.assertEqual(sum(expected.values()), 11)
            self.assertTrue(all(action == "pass" and count == 1
                                for (_, _, action), count in expected.items()))
            self.assertIn('"Test":"TestExact/child"', baseline.stdout)
            self.assertIn('"Test":"FuzzDecode0/seed#0"', baseline.stdout)

            matrix = json.loads(self.run_command(directory, environment, sys.executable,
                                                 str(RUNNER), "--matrix").stdout)["include"]
            self.assertEqual(len(matrix), 11)
            actual, failure_command = Counter(), None
            for entry in matrix:
                command = (sys.executable, str(RUNNER), "--group", entry["group"],
                           "--index", str(entry["index"]), "--timeout=10s")
                with self.subTest(shard=entry):
                    result = self.run_command(directory, environment, *command)
                    completed = terminal_tests(result.stdout)
                    self.assertTrue(completed, "every fixture shard must exercise a target")
                    actual.update(completed)
                    if (MODULE + "/pkg/nvkr/factory", "TestExact", "pass") in completed:
                        failure_command = command
            self.assertEqual(actual, expected, "every unsharded target must run exactly once")
            self.assertIsNotNone(failure_command)

            failed = self.run_command(directory, dict(environment, NVKR_SHARD_FIXTURE_FAIL="1"),
                                      *failure_command, check=False)
            self.assertNotEqual(failed.returncode, 0)
            self.assertEqual(terminal_tests(failed.stdout),
                             Counter({(MODULE + "/pkg/nvkr/factory", "TestExact", "fail"): 1}))
            self.assertIn("injected shard failure", failed.stdout)


if __name__ == "__main__":
    unittest.main()
