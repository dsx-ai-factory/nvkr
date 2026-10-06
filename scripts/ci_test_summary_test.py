"""Contracts for diagnostics; go test's pipeline exit status remains the gate."""

import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("summary", Path(__file__).with_name("ci-test-summary.py"))
summary = importlib.util.module_from_spec(spec)
spec.loader.exec_module(summary)


class SummaryTest(unittest.TestCase):
    def test_results(self):
        for action in ("pass", "fail", "skip"):
            with self.subTest(action=action):
                events = [
                    {"Action": "output", "Package": "p", "Output": "ignored"},
                    {"Action": action, "Package": "p", "Test": "Test<unsafe>|/child", "Elapsed": 2},
                    {"Action": action, "Package": "p", "Elapsed": 3},
                    {"Action": "pass", "Package": "slow", "Elapsed": 10},
                ]
                text = summary.summarize(["go test -json\n"] + [json.dumps(e) for e in events] + ["total: 80%\n"])
                self.assertIn("Test&lt;unsafe&gt;&#124;/child", text)
                self.assertLess(text.index("<code>slow</code>"), text.index("<code>p</code>"))
                self.assertIn(f"failed packages: {int(action == 'fail')}", text)

    def test_missing_or_malformed_results(self):
        for lines in ([], ["make: build failed\n"], ['{"Action":']):
            with self.subTest(lines=lines):
                with self.assertRaises(ValueError):
                    summary.summarize(lines)


if __name__ == "__main__":
    unittest.main()
