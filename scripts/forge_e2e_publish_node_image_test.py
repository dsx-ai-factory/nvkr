# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Contracts for publishing an NVKR-built node image for the Forge hardware lane.

The pull and the object store are replaced by fake executables, run as real child processes, so the
streaming, the deadlines and the exit handling are the code that ships. A directory stands in for the
bucket. These say what the publisher requires and refuses; they say nothing about S3, the HTTPS front
end, a registry or Forge, and a run against them is evidence of that run only.
"""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
import stat
import struct
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).parent
spec = importlib.util.spec_from_file_location("publisher", HERE / "forge-e2e-publish-node-image.py")
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)

REFERENCE = "oci://ghcr.io/example/nvkr-release@sha256:" + "a" * 64
REGISTRY_REFERENCE = REFERENCE[len("oci://"):]
OUTPUT = "node-image-amd64"
VERSION = "v0.24.0-beta.12"
BUCKET = "example-assets"
ROOT = "pkgs"
PREFIX = "nvkr/os-image"
DOMAIN = "cdn.example.test"
RUN = "https://github.com/example/nvkr/actions/runs/123456/attempts/1"


def qcow2(version=3, backing=0, bits=16, size=12 << 30, crypt=0, magic=b"QFI\xfb"):
    head = struct.pack(">4sIQIIQI", magic, version, backing, 0, bits, size, crypt)
    return head.ljust(publisher.checker.QCOW2_HEADER_BYTES, b"\0")


IMAGE = qcow2() + b"synthetic payload " * 64
DIGEST = hashlib.sha256(IMAGE).hexdigest()
KEY = f"{ROOT}/{PREFIX}/{VERSION}/amd64/{DIGEST}/{OUTPUT}.qcow2"


def pull_result(image=IMAGE, arch="amd64"):
    """What `nvkr release pull --format json` prints for a disk output."""
    return {
        "schemaVersion": "nvkr.dev/release-pull-result/v1",
        "reference": REGISTRY_REFERENCE,
        "output": {"name": OUTPUT, "kind": "disk-image", "digest": "sha256:" + "b" * 64,
                   "buildDigest": "sha256:" + "c" * 64, "os": "linux", "arch": arch},
        "destination": "unused",
        "payload": "files",
        "files": [{"path": f"{OUTPUT}.qcow2", "sha256": hashlib.sha256(image).hexdigest(), "size": len(image)}],
    }

FAKE_NVKR = """#!{python}
import json, os, sys
cfg = json.load(open({config!r}))
with open(cfg["log"], "a") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
args = sys.argv[1:]
flags = dict(zip(args[2::2], args[3::2]))
if cfg.get("exit"):
    sys.stderr.write(cfg.get("stderr", "pull failed"))
    sys.exit(cfg["exit"])
out = flags["--out"]
os.mkdir(out)
name = cfg["fileName"]
if cfg.get("symlink"):
    os.symlink("/etc/hosts", os.path.join(out, name))
else:
    with open(os.path.join(out, name), "wb") as handle:
        handle.write(bytes.fromhex(cfg["imageHex"]))
sys.stdout.write(json.dumps(cfg["result"]))
"""

FAKE_AWS = """#!{python}
import json, os, sys, time
cfg = json.load(open({config!r}))
args = sys.argv[1:]
with open(cfg["log"], "a") as log:
    log.write(json.dumps(args) + "\\n")
line = " ".join(args)
for rule in cfg.get("fail", []):
    if rule["match"] in line:
        sys.stderr.write(rule["stderr"])
        sys.exit(rule["code"])

def path(bucket, key):
    return os.path.join(cfg["store"], bucket, *key.split("/"))

def split(uri):
    bucket, _, key = uri[len("s3://"):].partition("/")
    return bucket, key

if args[:2] == ["s3api", "head-object"]:
    flags = dict(zip(args[2::2], args[3::2]))
    target = path(flags["--bucket"], flags["--key"])
    if not os.path.isfile(target):
        sys.stderr.write("An error occurred (404) when calling the HeadObject operation: Not Found")
        sys.exit(254)
    sys.stdout.write(json.dumps({{"ContentLength": os.path.getsize(target)}}))
elif args[:2] == ["s3", "cp"]:
    src, dst = args[2], args[3]
    if src.startswith("s3://") and dst == "-":
        target = path(*split(src))
        if not os.path.isfile(target):
            sys.stderr.write("An error occurred (NoSuchKey) when calling the GetObject operation")
            sys.exit(1)
        if cfg.get("slow") and not target.endswith(".sha256"):
            time.sleep(cfg["slow"])
        with open(target, "rb") as handle:
            sys.stdout.buffer.write(handle.read())
    else:
        bucket, key = split(dst)
        data = sys.stdin.buffer.read() if src == "-" else open(src, "rb").read()
        if cfg.get("corrupt") and not key.endswith(".sha256"):
            data = data[:-1] + bytes([data[-1] ^ 1])
        target = path(bucket, key)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "wb") as handle:
            handle.write(data)
else:
    sys.stderr.write("unexpected aws call: " + line)
    sys.exit(99)
"""


class Harness:
    """One test's fake tools, bucket, scratch space and call logs."""

    def __init__(self, test, image=IMAGE, pull=None, aws=None):
        self.root = Path(tempfile.mkdtemp(prefix="publish-node-image-"))
        test.addCleanup(self.cleanup)
        self.store = self.root / "bucket"
        self.store.mkdir()
        self.nvkr_log, self.aws_log = self.root / "nvkr.log", self.root / "aws.log"
        self.pull = {"log": str(self.nvkr_log), "fileName": f"{OUTPUT}.qcow2", "imageHex": image.hex(), "result": pull_result(image)}
        self.pull.update(pull or {})
        self.aws_config = {"log": str(self.aws_log), "store": str(self.store)}
        self.aws_config.update(aws or {})
        self.nvkr = self.tool("nvkr", FAKE_NVKR, self.pull)
        self.aws = self.tool("aws", FAKE_AWS, self.aws_config)

    def cleanup(self):
        import shutil

        shutil.rmtree(self.root, ignore_errors=True)

    def tool(self, name, template, config):
        config_path = self.root / f"{name}.json"
        config_path.write_text(json.dumps(config))
        path = self.root / name
        path.write_text(template.format(python=sys.executable, config=str(config_path)))
        path.chmod(path.stat().st_mode | stat.S_IXUSR)
        return str(path)

    def seed(self, key, data):
        target = self.store / BUCKET / Path(*key.split("/"))
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data if isinstance(data, bytes) else data.encode("ascii"))

    def stored(self, key):
        target = self.store / BUCKET / Path(*key.split("/"))
        return target.read_bytes() if target.is_file() else None

    def calls(self, log):
        return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []

    def aws_ops(self):
        """Each object store call as head, put-file, put-text or get."""
        ops = []
        for call in self.calls(self.aws_log):
            if call[:2] == ["s3api", "head-object"]:
                ops.append("head")
            elif call[:2] == ["s3", "cp"] and call[2] == "-":
                ops.append("put-text")
            elif call[:2] == ["s3", "cp"] and call[3] == "-":
                ops.append("get")
            elif call[:2] == ["s3", "cp"]:
                ops.append("put-file")
            else:
                ops.append(" ".join(call[:2]))
        return ops

    def argv(self, **overrides):
        values = {
            "--slot": "os-image-amd64", "--reference": REFERENCE, "--output": OUTPUT, "--version": VERSION,
            "--s3-uri": f"s3://{BUCKET}/{ROOT}", "--prefix": PREFIX, "--cdn-domain": DOMAIN, "--region": "us-west-2",
            "--work-dir": str(self.root / "pull"), "--out": str(self.root / "record"),
            "--run-url": RUN, "--nvkr": self.nvkr, "--aws": self.aws,
        }
        flags = []
        for name, value in {**values, **overrides}.items():
            if value is None:
                continue
            if value is True:
                flags.append(name)
            else:
                flags += [name, value]
        return flags

    def run(self, **overrides):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                code = publisher.main(self.argv(**overrides))
            except SystemExit as exit_:
                code = exit_.code
        return code, out.getvalue(), err.getvalue()

    def record(self, slot="os-image-amd64"):
        return json.loads((self.root / "record" / f"{slot}.json").read_text())

    def uploaded(self):
        return [op for op in self.aws_ops() if op.startswith("put")]

    def untouched(self, test):
        """Nothing was written to the object store and no record exists."""
        test.assertEqual(self.uploaded(), [])
        test.assertFalse((self.root / "record").exists())


class ConfigurationTest(unittest.TestCase):
    CASES = [
        ("a tag in place of a digest", {"--reference": "oci://ghcr.io/example/nvkr-release:latest"}, 13, "pinned by digest"),
        ("a reference with no digest at all", {"--reference": "oci://ghcr.io/example/nvkr-release"}, 13, "pinned by digest"),
        ("a reference with a short digest", {"--reference": "oci://ghcr.io/example/nvkr-release@sha256:" + "a" * 63}, 13, "pinned by digest"),
        ("a reference without oci://", {"--reference": REGISTRY_REFERENCE}, 13, "oci://"),
        ("an output name that climbs", {"--output": "../node-image"}, 13, "plain output name"),
        ("an output name with a slash", {"--output": "nested/node-image"}, 13, "plain output name"),
        ("a version without a v", {"--version": "0.24.0"}, 13, "version"),
        ("a prefix that is not under nvkr/", {"--prefix": "os-image"}, 13, "nvkr/"),
        ("a prefix that borrows the name", {"--prefix": "nvkrx/os-image"}, 13, "nvkr/"),
        ("a prefix that climbs out", {"--prefix": "nvkr/../os-image"}, 13, "nvkr/"),
        ("a prefix with an empty segment", {"--prefix": "nvkr//os-image"}, 13, "nvkr/"),
        ("an absolute prefix", {"--prefix": "/nvkr/os-image"}, 13, "nvkr/"),
        ("a root that is not s3", {"--s3-uri": "https://example-assets/pkgs"}, 17, "s3://"),
        ("a root with a trailing slash", {"--s3-uri": f"s3://{BUCKET}/{ROOT}/"}, 17, "trailing slash"),
        ("a domain with a scheme", {"--cdn-domain": "https://cdn.example.test"}, 17, "host name"),
        ("an upper-case domain", {"--cdn-domain": "CDN.example.test"}, 17, "host name"),
        ("a region that is not one", {"--region": "west"}, 17, "region"),
        ("no run to name", {"--run-url": None}, 13, "run-url"),
        ("a run URL that is not a run", {"--run-url": "https://github.com/example/nvkr/pull/1"}, 13, "run-url"),
        ("a run URL with text before it", {"--run-url": "see " + RUN}, 13, "run-url"),
        ("a run URL with text after it", {"--run-url": RUN + " and more"}, 13, "run-url"),
        ("a run URL with a query", {"--run-url": RUN + "?check_suite_focus=true"}, 13, "run-url"),
        ("a run URL over http", {"--run-url": RUN.replace("https", "http")}, 13, "run-url"),
        ("a run URL on another host", {"--run-url": RUN.replace("github.com", "ci.example.test")}, 13, "run-url"),
        ("a run URL of run zero", {"--run-url": "https://github.com/example/nvkr/actions/runs/0"}, 13, "run-url"),
        ("a run URL of attempt zero", {"--run-url": "https://github.com/example/nvkr/actions/runs/5/attempts/0"}, 13, "run-url"),
    ]

    def test_refuses_each_request_it_cannot_publish_before_running_anything(self):
        for name, overrides, exit_code, text in self.CASES:
            with self.subTest(name):
                harness = Harness(self)
                code, _, err = harness.run(**overrides)
                self.assertEqual(code, exit_code, err)
                self.assertIn(text, err)
                self.assertEqual(harness.calls(harness.nvkr_log), [])
                self.assertEqual(harness.calls(harness.aws_log), [])
                self.assertFalse((harness.root / "pull").exists())

    def test_an_unknown_slot_is_a_usage_error(self):
        code, _, err = Harness(self).run(**{"--slot": "os-image-riscv"})
        self.assertEqual(code, 2)
        self.assertIn("NVKR-E-USAGE", err)

    def test_a_missing_tool_is_a_failed_precondition(self):
        for tool in ("--nvkr", "--aws"):
            with self.subTest(tool):
                code, _, err = Harness(self).run(**{tool: "/nonexistent/tool"})
                self.assertEqual(code, 17, err)
                self.assertIn("is not installed", err)

    def test_an_existing_record_directory_is_refused_before_the_pull(self):
        harness = Harness(self)
        (harness.root / "record").mkdir()
        code, _, err = harness.run()
        self.assertEqual(code, 47, err)
        self.assertEqual(harness.calls(harness.nvkr_log), [])

    def test_an_existing_work_directory_is_refused_before_the_pull(self):
        harness = Harness(self)
        (harness.root / "pull").mkdir()
        code, _, err = harness.run()
        self.assertEqual(code, 17, err)
        self.assertEqual(harness.calls(harness.nvkr_log), [])

    def test_pull_is_asked_for_exactly_the_digest_pinned_output(self):
        harness = Harness(self)
        self.assertEqual(harness.run()[0], 0)
        [call] = harness.calls(harness.nvkr_log)
        flags = dict(zip(call[2::2], call[3::2]))
        self.assertEqual(call[:2], ["release", "pull"])
        self.assertEqual(flags["--reference"], REGISTRY_REFERENCE)
        self.assertEqual(flags["--output"], OUTPUT)
        self.assertEqual(flags["--format"], "json")
        self.assertEqual(flags["--out"], str(harness.root / "pull"))


class PullTest(unittest.TestCase):
    @staticmethod
    def mutate(edit):
        result = pull_result()
        edit(result)
        return {"result": result}

    def test_a_failed_pull_publishes_nothing(self):
        harness = Harness(self, pull={"exit": 21, "stderr": "boom"})
        code, _, err = harness.run()
        self.assertEqual(code, 20, err)
        self.assertIn("release pull exited 21: boom", err)
        self.assertEqual(harness.calls(harness.aws_log), [])

    def test_a_result_it_does_not_recognise_is_refused(self):
        cases = [
            ("another schema", lambda r: r.update(schemaVersion="nvkr.dev/release-pull-result/v2"), 22, "schema"),
            ("another reference", lambda r: r.update(reference="ghcr.io/example/other@sha256:" + "d" * 64), 22, "different reference"),
            ("another output", lambda r: r["output"].update(name="other"), 22, "different reference or output"),
            ("not a disk", lambda r: r["output"].update(kind="executable"), 13, "not a disk-image"),
            ("the wrong architecture", lambda r: r["output"].update(arch="arm64"), 22, "built for arm64"),
            ("two files", lambda r: r["files"].append(dict(r["files"][0], path="extra.qcow2")), 22, "expected only"),
            ("another file name", lambda r: r["files"][0].update(path="other.qcow2"), 22, "expected only"),
            ("no digest", lambda r: r["files"][0].pop("sha256"), 22, "no usable sha256"),
            ("a boolean size", lambda r: r["files"][0].update(size=True), 22, "no usable sha256"),
            ("a size of zero", lambda r: r["files"][0].update(size=0), 22, "no usable sha256"),
            ("a negative size", lambda r: r["files"][0].update(size=-1), 22, "no usable sha256"),
            ("a size that is text", lambda r: r["files"][0].update(size=str(len(IMAGE))), 22, "no usable sha256"),
            ("an upper-case digest", lambda r: r["files"][0].update(sha256=DIGEST.upper()), 22, "no usable sha256"),
            ("a short digest", lambda r: r["files"][0].update(sha256=DIGEST[:-1]), 22, "no usable sha256"),
            ("no files", lambda r: r.pop("files"), 22, "understands"),
        ]
        for name, edit, exit_code, text in cases:
            with self.subTest(name):
                harness = Harness(self, pull=self.mutate(edit))
                code, _, err = harness.run()
                self.assertEqual(code, exit_code, err)
                self.assertIn(text, err)
                harness.untouched(self)

    def test_an_architecture_the_output_does_not_declare_is_recorded_as_undeclared(self):
        harness = Harness(self, pull=self.mutate(lambda r: r["output"].pop("arch")))
        self.assertEqual(harness.run()[0], 0)
        self.assertIsNone(harness.record()["pulled"]["declaredArch"])

    def test_the_bytes_on_disk_must_be_the_bytes_the_pull_reported(self):
        wrong_sha, wrong_size = pull_result(), pull_result()
        wrong_sha["files"][0]["sha256"] = "e" * 64
        wrong_size["files"][0]["size"] += 1
        for name, result, text in [("digest", wrong_sha, "hashes to"), ("size", wrong_size, "bytes; release pull reported")]:
            with self.subTest(name):
                harness = Harness(self, pull={"result": result})
                code, _, err = harness.run()
                self.assertEqual(code, 22, err)
                self.assertIn(text, err)
                harness.untouched(self)

    def test_a_symlink_in_place_of_the_image_is_refused(self):
        harness = Harness(self, pull={"symlink": True})
        code, _, err = harness.run()
        self.assertEqual(code, 22, err)
        harness.untouched(self)

    def test_only_a_plain_qcow2_is_published(self):
        cases = [
            ("not a qcow2", b"not a disk image at all, only text " * 8, "qcow2 magic"),
            ("a backing file", qcow2(backing=4096) + b"x" * 64, "backing file"),
            ("an encrypted image", qcow2(crypt=2) + b"x" * 64, "encrypted"),
            ("an unknown version", qcow2(version=4) + b"x" * 64, "version 4"),
            ("too small", b"QFI\xfb", "too small"),
        ]
        for name, image, text in cases:
            with self.subTest(name):
                harness = Harness(self, image=image)
                code, _, err = harness.run()
                self.assertEqual(code, 13, err)
                self.assertIn(text, err)
                harness.untouched(self)


class PublishTest(unittest.TestCase):
    def test_uploads_then_reads_back_before_it_records_anything(self):
        harness = Harness(self)
        code, out, err = harness.run()
        self.assertEqual(code, 0, err)
        self.assertEqual(harness.stored(KEY), IMAGE)
        self.assertEqual(harness.stored(KEY + ".sha256"), DIGEST.encode("ascii"))
        self.assertEqual(harness.aws_ops(), ["head", "put-file", "head", "get", "head", "put-text", "get"])
        self.assertIn(f"sha256 {DIGEST}", out)

    def test_the_record_names_what_was_checked_and_claims_no_authority(self):
        harness = Harness(self)
        self.assertEqual(harness.run()[0], 0)
        record = harness.record()
        url = f"https://{DOMAIN}/{KEY}"
        self.assertTrue(publisher.checker.URL.match(url))
        self.assertEqual(record, {
            "schema": "nvkr.dev/forge-e2e-node-image-publication/v1",
            "slot": "os-image-amd64",
            "qualificationAuthority": "none",
            "signedProjection": False,
            "source": {"kind": "nvkr-release", "reference": REFERENCE, "version": VERSION},
            "pulled": {"output": OUTPUT, "declaredArch": "amd64", "digest": "sha256:" + "b" * 64, "buildDigest": "sha256:" + "c" * 64},
            "store": {"bucket": BUCKET, "key": KEY, "sidecarKey": KEY + ".sha256", "region": "us-west-2",
                      "contentLength": len(IMAGE), "reusedExistingObject": False, "reusedExistingSidecar": False},
            "qcow2": {"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
            "manifestEntry": {
                "url": url, "sha256": DIGEST,
                "source": {"kind": "nvkr-release", "reference": REFERENCE, "version": VERSION},
                "verification": {"method": "s3-readback", "run": RUN, "bytes": len(IMAGE), "qcow2Version": 3, "virtualSizeBytes": 12 << 30},
            },
        })

    def test_the_manifest_entry_is_exactly_what_the_lane_accepts_and_records(self):
        # The lane's node image check reads the same entry the maintainer pastes into the outputs
        # manifest. If the two ever disagree, a published image would be refused at dispatch.
        for slot, arch in (("os-image-amd64", "amd64"), ("os-image-arm64", "arm64")):
            with self.subTest(slot=slot):
                harness = Harness(self, pull={"result": pull_result(arch=arch)})
                self.assertEqual(harness.run(**{"--slot": slot})[0], 0)
                entry = harness.record(slot)["manifestEntry"]
                self.assertEqual(sorted(entry), ["sha256", "source", "url", "verification"])
                self.assertEqual(publisher.checker.parse_verification(entry["verification"]), entry["verification"])
                path = harness.root / f"{slot}-substitutions.json"
                path.write_text(json.dumps({
                    "schema": "nvkr.dev/forge-e2e-substitutions/v1", "artifactSource": "nvkr-outputs",
                    "replaced": [{"slot": slot, "from": "https://nvcr.example.test/old.qcow2", "to": entry["url"],
                                  "sha256": entry["sha256"], "source": entry["source"], "verification": entry["verification"]}],
                }))
                url, sha256, verification = publisher.checker.named_in(path, slot)
                self.assertEqual((url, sha256, verification), (entry["url"], entry["sha256"], entry["verification"]))
                recorded = publisher.checker.recorded(slot, url, sha256, verification)
                self.assertEqual(recorded["run"], RUN)
                self.assertEqual(recorded["bytes"], len(IMAGE))
                self.assertFalse(recorded["observedByThisRun"])

    def test_the_key_holds_the_digest_so_another_image_cannot_land_on_it(self):
        other = qcow2() + b"another payload " * 64
        first, second = Harness(self), Harness(self, image=other)
        self.assertEqual(first.run()[0], 0)
        self.assertEqual(second.run()[0], 0)
        self.assertNotEqual(first.record()["store"]["key"], second.record()["store"]["key"])
        self.assertIn(hashlib.sha256(other).hexdigest(), second.record()["store"]["key"])

    def test_the_arm64_slot_publishes_under_its_own_architecture(self):
        harness = Harness(self, pull={"result": pull_result(arch="arm64")})
        self.assertEqual(harness.run(**{"--slot": "os-image-arm64"})[0], 0)
        self.assertIn(f"/{VERSION}/arm64/{DIGEST}/", harness.record("os-image-arm64")["store"]["key"])

    def test_a_second_publication_of_the_same_image_reuses_the_object(self):
        harness = Harness(self)
        harness.seed(KEY, IMAGE)
        harness.seed(KEY + ".sha256", DIGEST)
        code, out, err = harness.run()
        self.assertEqual(code, 0, err)
        self.assertEqual(harness.uploaded(), [])
        store = harness.record()["store"]
        self.assertTrue(store["reusedExistingObject"] and store["reusedExistingSidecar"])
        self.assertIn("reused the existing object", out)

    def test_an_object_without_its_sidecar_gets_the_sidecar_once_it_reads_back_right(self):
        harness = Harness(self)
        harness.seed(KEY, IMAGE)
        self.assertEqual(harness.run()[0], 0, harness.aws_ops())
        self.assertEqual(harness.stored(KEY + ".sha256"), DIGEST.encode("ascii"))
        store = harness.record()["store"]
        self.assertTrue(store["reusedExistingObject"])
        self.assertFalse(store["reusedExistingSidecar"])

    def test_an_existing_object_that_differs_is_never_replaced(self):
        same_length = IMAGE[:-1] + bytes([IMAGE[-1] ^ 1])
        for name, existing, text in [("different bytes", same_length, "reads back as sha256"), ("another length", IMAGE + b"x", "bytes in the object store")]:
            with self.subTest(name):
                harness = Harness(self)
                harness.seed(KEY, existing)
                code, _, err = harness.run()
                self.assertEqual(code, 47, err)
                self.assertIn(text, err)
                self.assertEqual(harness.stored(KEY), existing)
                harness.untouched(self)

    def test_an_existing_sidecar_that_differs_is_never_replaced(self):
        harness = Harness(self)
        harness.seed(KEY, IMAGE)
        harness.seed(KEY + ".sha256", "f" * 64)
        code, _, err = harness.run()
        self.assertEqual(code, 47, err)
        self.assertEqual(harness.stored(KEY + ".sha256"), b"f" * 64)
        self.assertFalse((harness.root / "record").exists())

    def test_a_store_that_does_not_hold_the_uploaded_bytes_is_a_failure(self):
        harness = Harness(self, aws={"corrupt": True})
        code, _, err = harness.run()
        self.assertEqual(code, 22, err)
        self.assertIn("reads back as sha256", err)
        self.assertFalse((harness.root / "record").exists())

    def test_the_store_failing_is_coded_and_leaves_no_record(self):
        for name, match, text in [("the first head", "head-object", "head-object for"), ("the upload", "s3 cp ", "uploading")]:
            with self.subTest(name):
                harness = Harness(self, aws={"fail": [{"match": match, "code": 1, "stderr": "boom"}]})
                code, _, err = harness.run()
                self.assertEqual(code, 30, err)
                self.assertIn(text, err)
                self.assertIn("boom", err)
                self.assertFalse((harness.root / "record").exists())

    def test_a_failed_read_back_is_a_store_failure_and_not_a_digest_mismatch(self):
        harness = Harness(self, aws={"fail": [{"match": " - --only-show-errors", "code": 1, "stderr": "connection reset"}]})
        code, _, err = harness.run()
        self.assertEqual(code, 30, err)
        self.assertIn("connection reset", err)
        self.assertFalse((harness.root / "record").exists())

    def test_a_forbidden_head_says_the_role_may_lack_list_permission(self):
        harness = Harness(self, aws={"fail": [{"match": "head-object", "code": 254,
                                               "stderr": "An error occurred (403) when calling the HeadObject operation: Forbidden"}]})
        code, _, err = harness.run()
        self.assertEqual(code, 30, err)
        self.assertIn("s3:ListBucket", err)
        self.assertEqual(harness.aws_ops(), ["head"])

    def test_a_failed_sidecar_write_leaves_no_record(self):
        harness = Harness(self, aws={"fail": [{"match": ".sha256 --only-show-errors", "code": 1, "stderr": "denied"}]})
        code, _, err = harness.run()
        self.assertEqual(code, 30, err)
        self.assertFalse((harness.root / "record").exists())

    def test_a_read_back_that_outlives_its_deadline_is_stopped(self):
        harness = Harness(self, aws={"slow": 30})
        original = publisher.STORE_SECONDS
        publisher.STORE_SECONDS = 1
        self.addCleanup(setattr, publisher, "STORE_SECONDS", original)
        code, _, err = harness.run()
        self.assertEqual(code, 31, err)
        self.assertIn("still running", err)
        self.assertFalse((harness.root / "record").exists())

    def test_a_dry_run_pulls_and_checks_but_writes_nothing(self):
        harness = Harness(self)
        code, out, err = harness.run(**{"--dry-run": True, "--aws": "/nonexistent/aws", "--run-url": None})
        self.assertEqual(code, 0, err)
        report = json.loads(out)
        self.assertEqual((report["dryRun"], report["sha256"], report["key"]), (True, DIGEST, KEY))
        self.assertEqual(report["url"], f"https://{DOMAIN}/{KEY}")
        self.assertEqual(harness.calls(harness.aws_log), [])
        self.assertFalse((harness.root / "record").exists())

    def test_a_dry_run_still_refuses_an_image_it_could_not_publish(self):
        harness = Harness(self, image=qcow2(backing=4096) + b"x" * 64)
        code, _, err = harness.run(**{"--dry-run": True})
        self.assertEqual(code, 13, err)
        self.assertIn("backing file", err)

    def test_the_record_is_private_and_a_second_write_is_refused(self):
        harness = Harness(self)
        self.assertEqual(harness.run()[0], 0)
        record = harness.root / "record"
        self.assertEqual(stat.S_IMODE(record.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((record / "os-image-amd64.json").stat().st_mode), 0o600)
        before = (record / "os-image-amd64.json").read_text()
        link = harness.root / "link"
        os.symlink(harness.root / "elsewhere", link)
        for target in (record, link):
            with self.subTest(target=target.name), self.assertRaises(publisher.Failure) as raised:
                publisher.write_record(str(target), "os-image-amd64", {})
            self.assertEqual((raised.exception.code, raised.exception.exit_code), ("NVKR-E-IMMUTABLE-VIOLATION", 47))
            self.assertIn("refusing to replace", str(raised.exception))
        self.assertEqual((record / "os-image-amd64.json").read_text(), before)
        self.assertFalse((harness.root / "elsewhere").exists())


if __name__ == "__main__":
    unittest.main()
