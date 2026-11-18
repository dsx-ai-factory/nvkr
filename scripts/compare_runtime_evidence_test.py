# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("compare_runtime", Path(__file__).with_name("compare-runtime-evidence.py"))
COMPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COMPARE)


class RuntimeComparisonTest(unittest.TestCase):
    def test_comparison(self):
        for change in ("none", "order", "content", "identity", "unbound", "duplicate"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as temp:
                paths = [Path(temp) / "first", Path(temp) / "second"]
                for index, directory in enumerate(paths):
                    directory.mkdir()
                    production = {field: "same" for field in
                                  ("buildDigest", "platform", "component", "digest", "layerDigest", "layoutDigest", "size")}
                    entries = [{"path": "a", "sha256": "same"}, {"path": "b", "sha256": "same"}]
                    inventory = {"layerDigest": "same", "entries": entries}
                    if index == 1:
                        if change == "order":
                            entries.reverse()
                        elif change == "content":
                            entries[0]["sha256"] = "changed"
                        elif change == "identity":
                            production["buildDigest"] = "changed"
                        elif change == "unbound":
                            inventory["layerDigest"] = "changed"
                        elif change == "duplicate":
                            entries.append(entries[0])
                    (directory / "disk-result.json").write_text(json.dumps({"runtimeUse": {"production": production}}))
                    (directory / "runtime-tar-entries.json").write_text(json.dumps(inventory))
                if change in ("unbound", "duplicate"):
                    with self.assertRaises(ValueError):
                        COMPARE.compare(*paths)
                else:
                    result = COMPARE.compare(*paths)
                    self.assertEqual(result["equal"], change == "none")
                    if change == "content":
                        self.assertEqual(result["entries"][0]["path"], "a")
                        self.assertIn("sha256", result["entries"][0]["fields"])


if __name__ == "__main__":
    unittest.main()
