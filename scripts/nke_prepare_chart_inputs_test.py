# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Adapter contracts use bounded tool fixtures, not a real NKE deployment."""

import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("chart_inputs", Path(__file__).with_name("nke-prepare-chart-inputs.py"))
ADAPTER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ADAPTER)


class ChartInputsTest(unittest.TestCase):
    def test_assignments(self):
        self.assertEqual(ADAPTER.assignments(["chart=/some/path"], "test"), {"chart": "/some/path"})
        for values in (["chart="], ["=path"], ["missing"], ["chart=a", "chart=b"]):
            with self.subTest(values=values), self.assertRaises(ADAPTER.HandoffError):
                ADAPTER.assignments(values, "test")

    def test_preparation(self):
        for case in ("exact", "wrong source", "dirty source", "missing chart", "extra chart", "missing pin",
                     "duplicate items", "invalid interface", "symlink source", "extraction failure", "existing output", "output symlink",
                     "NKE output", "NKE parent symlink", "layout output"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                checkout = root / "nke"
                checkout.mkdir()
                tool = root / "nkdctl"
                tool.touch()
                layouts = {name: root / name for name in ("controller", "spire")}
                for layout in layouts.values():
                    layout.mkdir()
                charts = {name: str(path) for name, path in layouts.items()}
                pins = {name: "sha256:" + "a" * 64 for name in layouts}
                out = root / "prepared"
                sources = {"nke-deploy.yaml": "items:\n  - {name: controller, type: helm}\n  - {name: spire, type: helm}\n"}
                for name in layouts:
                    sources[f"{name}/release.yaml"] = (
                        f"chart:\n  name: {name}\n  version: 1.2.3\n  repository: https://example.test\n"
                        f"release:\n  name: {name}\n  namespace: test\nvalues:\n  - values.yaml\n")
                    sources[f"{name}/values.yaml"] = "preserve: exact\n"
                calls = []

                def run(argv, label, timeout=30, text=True):
                    calls.append(argv)
                    if argv[0] == "git":
                        command = argv[3]
                        if command == "rev-parse":
                            return "wrong" if case == "wrong source" else ADAPTER.INSTALLER_COMMIT
                        if command == "status":
                            return "modified" if case == "dirty source" else ""
                        if command == "ls-tree":
                            mode = "120000" if case == "symlink source" else "100644"
                            return "".join(f"{mode} blob {'b' * 40}\tdeploy/nico/{name}\0" for name in sources)
                        if command == "show":
                            return sources[argv[4].split(":deploy/nico/", 1)[1]].encode()
                    if argv[0] == str(tool):
                        if case == "extraction failure":
                            raise ADAPTER.HandoffError("extraction failed")
                        Path(argv[-1]).write_bytes(b"exact fixture chart bytes")
                        return "{}"
                    if argv[2] == "items":
                        return "controller\thelm\nspire\thelm\n" + ("controller\thelm\n" if case == "duplicate items" else "")
                    if argv[2] == "release":
                        if case == "invalid interface":
                            return "unsupported"
                        name = Path(argv[3]).parent.name
                        staged = Path(argv[3]).exists()
                        return "\x1f".join(["single", name, "1.2.3", "" if staged else "https://example.test",
                                             f"charts/{name}.tgz" if staged else "", name, "test", "values.yaml", ""])
                    self.fail(f"unexpected producer/tool invocation: {argv}")

                if case == "missing chart":
                    charts.pop("spire")
                if case == "extra chart":
                    charts["other"] = "unused"
                if case == "missing pin":
                    pins.pop("spire")
                if case == "existing output":
                    out.mkdir()
                if case == "output symlink":
                    out.symlink_to(root / "must-not-exist")
                if case == "NKE output":
                    out = checkout / "prepared"
                if case == "NKE parent symlink":
                    alias = root / "checkout-alias"
                    alias.symlink_to(checkout, target_is_directory=True)
                    out = alias / "prepared"
                if case == "layout output":
                    out = layouts["controller"] / "prepared"
                with patch.object(ADAPTER, "run", run):
                    if case == "exact":
                        deploy = ADAPTER.prepare(checkout, tool, charts, pins, out)
                        self.assertEqual(deploy, out / "deploy")
                        self.assertEqual((deploy / "controller/values.yaml").read_text(), "preserve: exact\n")
                        self.assertIn("  path: charts/controller.tgz\n", (deploy / "controller/release.yaml").read_text())
                        self.assertNotIn("repository:", (deploy / "controller/release.yaml").read_text())
                        self.assertEqual((deploy / "charts/spire.tgz").read_bytes(), b"exact fixture chart bytes")
                        self.assertEqual(out.stat().st_mode & 0o777, 0o700)
                    else:
                        with self.assertRaises(ADAPTER.HandoffError):
                            ADAPTER.prepare(checkout, tool, charts, pins, out)
                        if case not in ("existing output", "output symlink"):
                            self.assertFalse(out.exists())
                        self.assertFalse((root / "must-not-exist").exists())
                self.assertFalse(list(root.glob(".nkd-chart-inputs-*")))
                self.assertFalse(list(checkout.iterdir()), "adapter wrote into NKE checkout")
                for argv in calls:
                    self.assertNotIn("make", argv)
                    self.assertNotIn("build", argv)
                    self.assertNotIn("dependency", argv)


if __name__ == "__main__":
    unittest.main()
