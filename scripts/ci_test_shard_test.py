# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""The sharded gate must run the same complete suite as ordinary go test."""

from collections import Counter
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest


spec = importlib.util.spec_from_file_location("shard", Path(__file__).with_name("ci-test-shard.py"))
shard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shard)


def events(*rows):
    return "\n".join(json.dumps(row) for row in rows)


class ShardTest(unittest.TestCase):
    def test_workflow_matrix_covers_every_declared_shard(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            shard.main(["--matrix"])
        actual = json.loads(output.getvalue())["include"]
        expected = [{"group": group, "index": index}
                    for group, (_, count) in shard.GROUPS.items() for index in range(count)]
        self.assertEqual(actual, expected)
        self.assertEqual(len(actual), 11)

    def test_child_failure_and_timeout_preserve_output_and_stop_descendants(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(subprocess.CalledProcessError) as failure:
                shard.captured([sys.executable, "-c", "print('failure output'); raise SystemExit(3)"], emit=True)
        self.assertEqual(failure.exception.returncode, 3)
        self.assertIn("failure output", output.getvalue())

        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "escaped-child"
            # The descendant would write after its parent times out if only the
            # direct process were killed. It holds stdout open independently.
            child = "import time,pathlib,sys; time.sleep(.4); pathlib.Path(sys.argv[1]).touch()"
            parent = ("import subprocess,sys,time; "
                      "subprocess.Popen([sys.executable,'-c',sys.argv[1],sys.argv[2]]); "
                      "print('running',flush=True); time.sleep(30)")
            output = io.StringIO()
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(subprocess.TimeoutExpired):
                    shard.captured([sys.executable, "-c", parent, child, str(marker)], timeout=.2, emit=True)
            self.assertIn("running", output.getvalue())
            time.sleep(.45)
            self.assertFalse(marker.exists(), "timed-out descendant escaped its owner's cleanup")

    def test_complete_partition_including_future_tests(self):
        names = [f"TestCase{i}" for i in range(1000)] + ["Example_contract", "FuzzDecoder", "TestCase"]
        for count in (1, 2, 4, 5, 11):
            with self.subTest(count=count):
                assigned = [shard.partition(names, index, count) for index in range(count)]
                self.assertEqual(Counter(name for group in assigned for name in group), Counter(names))
                for index, selection in enumerate(assigned):
                    self.assertEqual(selection, shard.partition(list(reversed(names)), index, count))
                    expanded = shard.partition(names + ["TestFutureContract"], index, count)
                    self.assertEqual(set(selection), set(expanded) - {"TestFutureContract"})

    def test_every_package_assigned_exactly_once_or_split(self):
        packages = ["m/pkg/nvkr/factory", "m/pkg/nvkr/lifecycle", "m/cmd/nvkr"]
        packages += [f"m/new/package{i}" for i in range(100)]
        counts = Counter()
        for group, (_, count) in shard.GROUPS.items():
            for index in range(count):
                counts.update(shard.packages_for_group(packages, "m", group, index))
        self.assertEqual(counts, Counter({package: 4 if package.endswith("/factory") else
                                         2 if package.endswith("/lifecycle") else 1 for package in packages}))

    def test_invalid_selection_fails_closed(self):
        for names, index, count in [(["x"], -1, 2), (["x"], 2, 2), (["x"], 0, 0), (["x", "x"], 0, 2)]:
            with self.subTest(names=names, index=index, count=count), self.assertRaises(ValueError):
                shard.partition(names, index, count)
        for packages, group, index in [([], "factory", 0), (["m/pkg/nvkr/factory"], "packages", 0),
                                        ([], "unknown", 0)]:
            with self.subTest(packages=packages, group=group), self.assertRaises(ValueError):
                shard.packages_for_group(packages, "m", group, index)

    def test_discovery_retains_tests_examples_and_fuzz_seeds(self):
        names = ["TestExact", "TestExactSuffix", "Example_contract", "FuzzDecode"]
        rows = [{"Action": "output", "Package": "p", "Output": name + "\n"} for name in names]
        rows += [{"Action": "output", "Package": "p", "Output": "BenchmarkDecode\n"},
                 {"Action": "pass", "Package": "p"}]
        self.assertEqual(shard.listed_tests(events(*rows), "p"), names)
        for bad in ["", "not-json", events(*rows[:-1]),
                    events(*rows[:-1], {"Action": "fail", "Package": "p"}),
                    events(*rows, rows[0]), events(*rows, {"Action": "pass", "Package": "other"})]:
            with self.subTest(log=bad), self.assertRaises(ValueError):
                shard.listed_tests(bad, "p")

    def test_results_require_complete_exact_selection(self):
        rows = [{"Action": "pass", "Package": "p", "Test": "TestA/child"},
                {"Action": "pass", "Package": "p", "Test": "TestA"},
                {"Action": "skip", "Package": "p", "Test": "FuzzB"},
                {"Action": "pass", "Package": "p"}]
        shard.validate_results(events(*rows), ["p"], ["TestA", "FuzzB"])
        # Whole-package shards may contain the same test name in distinct packages.
        shard.validate_results(events(*rows, {"Action": "pass", "Package": "q", "Test": "TestA"},
                                      {"Action": "pass", "Package": "q"}), ["p", "q"])
        for bad, selection in [(rows[:-1], ["TestA", "FuzzB"]), (rows, ["TestA"]),
                               (rows, ["TestA", "FuzzB", "TestOmitted"]),
                               (rows + [rows[-1]], ["TestA", "FuzzB"]),
                               (rows + [rows[1]], ["TestA", "FuzzB"]),
                               (rows[:-1] + [{"Action": "fail", "Package": "p"}], ["TestA", "FuzzB"]),
                               (rows + [{"Action": "pass", "Package": "other"}], ["TestA", "FuzzB"])]:
            with self.subTest(log=bad, selection=selection), self.assertRaises(ValueError):
                shard.validate_results(events(*bad), ["p"], selection)


if __name__ == "__main__":
    unittest.main()
