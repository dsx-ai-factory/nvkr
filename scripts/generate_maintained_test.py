# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("generate_maintained", Path(__file__).with_name("generate-maintained.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class MaintainedGenerationInventoryTest(unittest.TestCase):
    def test_inventory_admission(self):
        selection = {"bundle": "nvkr-run", "group": "main", "definition": "definition.yaml", "environment": "environment.yaml"}
        for name, inventory, valid in [
            ("valid", [selection], True),
            ("new named bundle", [selection, dict(selection, bundle="nvkr-run-new")], True),
            ("empty", [], False),
            ("duplicate", [selection, selection], False),
            ("unknown field", [dict(selection, ignored=True)], False),
            ("unknown group", [dict(selection, group="typo")], False),
            ("missing input", [dict(selection, definition="missing.yaml")], False),
            ("invalid input type", [dict(selection, definition=None)], False),
            ("traversal", [dict(selection, environment="../environment.yaml")], False),
            ("invalid runtime", [dict(selection, runtime="false")], False),
        ]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "inputs/policies").mkdir(parents=True)
                (root / "definition.yaml").touch()
                (root / "environment.yaml").touch()
                (root / "inputs/policies/maintained-generation.json").write_text(json.dumps(inventory))
                if valid:
                    self.assertEqual(MODULE.load_inventory(root), inventory)
                else:
                    with self.assertRaises(ValueError):
                        MODULE.load_inventory(root)


if __name__ == "__main__":
    unittest.main()
