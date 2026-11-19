# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Contract tests against a CRD read from the supplied pinned NVKE Git source."""

import argparse
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from jsonschema.exceptions import ValidationError
import yaml

import render_candidate as candidate


def fixture():
    disks = [{"output": "disk-" + arch, "platform": "linux/" + arch} for arch in ("amd64", "arm64")]
    imagers = [{"output": "imager-" + arch, "platform": "linux/" + arch,
                "member": "imager.efi", "profile": "synthetic-strict-v1"} for arch in ("amd64", "arm64")]
    assets = []
    for kind, selections in (("disk-image", disks), ("executable", imagers)):
        for selected in selections:
            digest = "a" * 64 if kind == "disk-image" else "b" * 64
            binding = dict(selected, kind=kind, sha256="sha256:" + digest, size=1234)
            assets.append({"binding": binding, "url": "https://boot.example.test/" + selected["output"] + "/" + digest})
    publication = {"selection": {"schemaVersion": "nvkr.dev/boot-selection/v1", "disks": disks, "imagers": imagers},
                   "destination": {"name": "primary"}, "assets": assets}
    metadata = [dict(d, family="ubuntu", version="24.04", variant="", buildDate="2026-11-19") for d in disks]
    return publication, metadata


class RendererTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.validator = candidate.load_crd(SOURCE, COMMIT)
        cls.template = candidate.read(candidate.HERE / "machineimage.yaml.tmpl")

    def render(self, publications=None, metadata=None, template=None):
        publication, defaults = fixture()
        return candidate.render(publications if publications is not None else [publication],
                                metadata if metadata is not None else defaults, self.validator,
                                template if template is not None else self.template)

    def test_both_architectures_and_downstream_placeholders(self):
        documents = list(yaml.safe_load_all(self.render()))
        self.assertEqual(len(documents), 2)
        for document in documents:
            arch = document["spec"]["architecture"]
            self.validator.validate(document)
            self.assertRegex(document["metadata"]["name"], r"^mi-[a-z2-7]{52}$")
            script = document["spec"]["nico"]["ipxeScript"]
            self.assertIn("image_sha=" + "a" * 64, script)
            self.assertIn("disk_strategy={{ .DiskSelectionStrategy }}", script)
            self.assertIn("${cloudinit-url}", script)
            self.assertNotIn("sha256:", script)
            self.assertIn("ttyS1" if arch == "amd64" else "ttyAMA0", script)
            self.assertEqual("intel_iommu" in script, arch == "amd64")
            self.assertNotIn("variant", document["spec"])

    def test_complete_set_and_order_independent_bytes(self):
        one, metadata = fixture()
        two = copy.deepcopy(one)
        two["destination"]["name"] = "secondary"
        forward = self.render([one, two], metadata)
        one["assets"].reverse()
        one["selection"]["disks"].reverse()
        self.assertEqual(forward, self.render([two, one], list(reversed(metadata))))
        self.assertEqual(len(list(yaml.safe_load_all(forward))), 4)

    def test_crd_schema_rejects_metadata_and_template_changes(self):
        for field, bad in (("family", "bad family"), ("version", "latest"), ("variant", "bad variant"),
                           ("buildDate", "2026-02-30")):
            with self.subTest(field=field):
                _, metadata = fixture()
                metadata[1][field] = bad
                with self.assertRaises(ValidationError):
                    self.render(metadata=metadata)
        with self.assertRaises(ValidationError):
            self.render(template=self.template.replace("architecture: ${architecture}", "architecture: []"))

    def test_complete_selection_and_metadata_required(self):
        mutations = [lambda p: p["assets"].pop(), lambda p: p["assets"].append(p["assets"][0]),
                     lambda p: p["selection"]["imagers"].append(p["selection"]["imagers"][0]),
                     lambda p: p["assets"][0]["binding"].update(sha256="latest"),
                     lambda p: p["assets"][0]["binding"].update(platform="linux/riscv64")]
        for mutate in mutations:
            publication, metadata = fixture()
            mutate(publication)
            with self.assertRaises(ValueError):
                self.render([publication], metadata)
        publication, metadata = fixture()
        with self.assertRaises(ValueError):
            self.render([publication, publication], metadata)
        with self.assertRaises(KeyError):
            self.render([publication], metadata[:1])
        with self.assertRaises(ValueError):
            self.render([publication], metadata + [dict(metadata[0], output="extra")])

    def test_url_grammar_rejects_script_injection(self):
        for url in ("http://host/image", "https://u:p@host/image", "https://host/image?token=x",
                    "https://host/image#x", "https://host/../image", "https://host//image",
                    "https://host/image;reboot", "https://host/image\nboot", "https://host/${inject}",
                    "https://host/{{inject}}", "https://host/%20", "https://host:65536/image",
                    "https://[2001:db8::1]junk/image", "https://host:/image"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                candidate.literal_url(url)
        self.assertEqual(candidate.literal_url("https://[2001:db8::1]/image"), "https://[2001:db8::1]/image")

    def test_exact_source_pin_required(self):
        with self.assertRaises(ValueError):
            candidate.load_crd(SOURCE, "main")
        with self.assertRaises(subprocess.CalledProcessError):
            candidate.load_crd(SOURCE, "0" * 40)

    def test_changed_exact_bytes_change_candidate_name(self):
        baseline = list(yaml.safe_load_all(self.render()))[0]["metadata"]["name"]
        publication, metadata = fixture()
        publication["assets"][0]["url"] += "/changed"
        changed = list(yaml.safe_load_all(self.render([publication], metadata)))[0]["metadata"]["name"]
        self.assertNotEqual(baseline, changed)

    def test_cli_failure_has_no_partial_stdout(self):
        publication, metadata = fixture()
        metadata[1]["version"] = "invalid"
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "publication.json").write_text(json.dumps(publication))
            (root / "metadata.json").write_text(json.dumps(metadata))
            result = subprocess.run([sys.executable, str(candidate.HERE / "render_candidate.py"),
                                     "--nvke-source", str(SOURCE), "--nvke-commit", COMMIT,
                                     "--publication", str(root / "publication.json"),
                                     "--metadata", str(root / "metadata.json")], capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, b"")
            self.assertIn(b"verify-failed", result.stderr)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nvke-source", required=True, type=Path)
    parser.add_argument("--nvke-commit", required=True)
    options = parser.parse_args()
    SOURCE, COMMIT = options.nvke_source, options.nvke_commit
    unittest.main(argv=[sys.argv[0]], verbosity=2)
