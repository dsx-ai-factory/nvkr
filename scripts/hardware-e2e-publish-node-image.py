#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Copy an NVKR-built node image to the HTTPS location the hardware site's imager reads, and record what was checked.

The hardware site boots a node from a plain HTTPS URL, and the registry that holds NVKR's publication needs
a token the imager cannot present. This is the workflow's stand-in for NVKR's own boot-asset publication (ADR-20):
it pulls one disk output by exact digest, checks that it is a plain qcow2, copies it to an object store
under a key that contains its sha256, reads it back from the store with credentials and hashes what it
read. The record it writes is a description. It is unsigned, binds no projection of the publication and
carries qualificationAuthority "none".

The read-back goes to the object store, never through the HTTPS front end: that front end can sit behind
an address allowlist that CI runners are not on, so a request from here proves nothing about the
imager's path. Whether the imager can fetch the URL, trusts its certificate and boots the image is
established only by a machine booting it.

The key contains the sha256, so a second publication of the same image rewrites identical bytes and a
different image can never replace this one. An object already at the key must read back to the same
bytes, or the script stops with exit 47 and changes nothing. The destination prefix must begin with
nvkr/, so a misspelt prefix cannot land in a tree another producer owns.

Exit codes: 0 ok, 2 usage, 13 unsupported image or destination, 17 a precondition failed, 20 the
pull failed, 22 the bytes did not match, 30 the object store failed, 31 a deadline elapsed, 47 an
existing object or output differs.
"""

import argparse
import hashlib
import importlib.util
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
from pathlib import Path

HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("hardware_e2e_check_node_image", HERE / "hardware-e2e-check-node-image.py")
checker = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(checker)

SCHEMA = "nvkr.dev/hardware-e2e-node-image-publication/v1"
PULL_SCHEMA = "nvkr.dev/release-pull-result/v1"
SLOT_ARCH = {"os-image-amd64": "amd64", "os-image-arm64": "arm64"}
REFERENCE = re.compile(r"^oci://([a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64})$")
OUTPUT_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
VERSION = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$")
S3_URI = re.compile(r"^s3://([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])((?:/[A-Za-z0-9][A-Za-z0-9._-]*)*)$")
PREFIX = re.compile(r"^nvkr(/[A-Za-z0-9][A-Za-z0-9._-]*)*$")
DOMAIN = re.compile(r"^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?$")
REGION = re.compile(r"^[a-z]{2}(-[a-z]+)+-[0-9]+$")
RUN_URL = re.compile(r"^https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[1-9][0-9]{0,19}(/attempts/[1-9][0-9]{0,5})?$")
SHA256 = checker.SHA256
CHUNK_BYTES = checker.CHUNK_BYTES
MAX_BYTES = checker.DEFAULT_MAX_BYTES
PULL_SECONDS = 35 * 60
STORE_SECONDS = 60 * 60
SHORT_SECONDS = 120


class Failure(Exception):
    def __init__(self, code, exit_code, message):
        super().__init__(message)
        self.code = code
        self.exit_code = exit_code


def unsupported(message):
    return Failure("NVKR-E-UNSUPPORTED", 13, message)


def preflight(message):
    return Failure("NVKR-E-PREFLIGHT-FAILED", 17, message)


def fetch_failed(message):
    return Failure("NVKR-E-FETCH-FAILED", 20, message)


def verify_failed(message):
    return Failure("NVKR-E-VERIFY-FAILED", 22, message)


def store_failed(message):
    return Failure("NVKR-E-UNAVAILABLE", 30, message)


def timed_out(message):
    return Failure("NVKR-E-TIMEOUT", 31, message)


def immutable(message):
    return Failure("NVKR-E-IMMUTABLE-VIOLATION", 47, message)


class Parser(argparse.ArgumentParser):
    def error(self, message):
        print(f"nvkr: [NVKR-E-USAGE] {message}", file=sys.stderr)
        sys.exit(2)


class Config:
    """The validated request. Nothing is built from a value that did not match its pattern."""

    def __init__(self, args):
        self.slot = args.slot
        self.arch = SLOT_ARCH[args.slot]
        match = REFERENCE.match(args.reference)
        if not match:
            raise unsupported("the reference must be oci://REPOSITORY@sha256:DIGEST, pinned by digest and not by tag")
        self.reference = args.reference
        self.registry_reference = match.group(1)
        if not OUTPUT_NAME.match(args.output):
            raise unsupported("the output must be a plain output name")
        self.output = args.output
        if not VERSION.match(args.version):
            raise unsupported("the version must look like vX.Y.Z or vX.Y.Z-pre")
        self.version = args.version
        match = S3_URI.match(args.s3_uri)
        if not match:
            raise preflight("the object store root must be s3://BUCKET[/PATH] with no trailing slash")
        self.bucket, root = match.group(1), match.group(2).strip("/")
        if not PREFIX.match(args.prefix):
            raise unsupported("the prefix must begin with nvkr/ and hold plain path segments")
        self.prefix = args.prefix
        if not DOMAIN.match(args.cdn_domain):
            raise preflight("the HTTPS domain must be a lower-case host name")
        self.cdn_domain = args.cdn_domain
        if not REGION.match(args.region):
            raise preflight("the region must look like us-west-2")
        self.region = args.region
        if not args.dry_run:
            if not args.run_url or not RUN_URL.match(args.run_url):
                raise unsupported("a publication names the workflow run that made it: --run-url https://github.com/OWNER/REPO/actions/runs/ID")
        elif args.run_url and not RUN_URL.match(args.run_url):
            raise unsupported("--run-url must be https://github.com/OWNER/REPO/actions/runs/ID")
        self.run_url = args.run_url or None
        self.dry_run = args.dry_run
        self.work_dir = args.work_dir
        self.out = args.out
        self.nvkr = args.nvkr
        self.aws = args.aws
        parts = [root, self.prefix, self.version, self.arch, "{sha256}", f"{self.output}.qcow2"]
        self._key_template = "/".join(part for part in parts if part)

    def key(self, sha256):
        return self._key_template.replace("{sha256}", sha256)

    def url(self, sha256):
        url = f"https://{self.cdn_domain}/{self.key(sha256)}"
        if not checker.URL.match(url):
            raise unsupported("the object key does not make a plain https URL to a .qcow2")
        return url


def tool_env():
    env = dict(os.environ)
    env["AWS_PAGER"] = ""
    return env


def run_capture(argv, timeout, stdin=None):
    try:
        done = subprocess.run(argv, input=stdin, capture_output=True, timeout=timeout, env=tool_env(), check=False)
    except FileNotFoundError as error:
        raise preflight(f"{argv[0]} is not installed") from error
    except subprocess.TimeoutExpired as error:
        raise timed_out(f"{argv[0]} {argv[1] if len(argv) > 1 else ''} was still running after {timeout}s") from error
    return done.returncode, done.stdout, done.stderr.decode("utf-8", "replace").strip()


def stream_sha256(argv, seconds):
    """Hash what a command writes to stdout without keeping it. A watchdog owns the child's lifetime."""
    with tempfile.TemporaryFile() as errors:
        try:
            child = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=errors, env=tool_env())
        except FileNotFoundError as error:
            raise preflight(f"{argv[0]} is not installed") from error
        expired = threading.Event()

        def expire():
            expired.set()
            child.kill()

        watchdog = threading.Timer(seconds, expire)
        watchdog.start()
        digest, count = hashlib.sha256(), 0
        try:
            while True:
                chunk = child.stdout.read(CHUNK_BYTES)
                if not chunk:
                    break
                count += len(chunk)
                digest.update(chunk)
        except BaseException:
            child.kill()
            raise
        finally:
            watchdog.cancel()
            child.stdout.close()
            code = child.wait()
        if expired.is_set():
            raise timed_out(f"reading back from the object store was still running after {seconds}s")
        if code != 0:
            errors.seek(0)
            raise store_failed(f"{argv[0]} exited {code} after {count} bytes: {errors.read().decode('utf-8', 'replace').strip()}")
    return digest.hexdigest(), count


def pull(cfg):
    parent = Path(cfg.work_dir).parent
    if os.path.lexists(cfg.work_dir) or not parent.is_dir():
        raise preflight(f"{cfg.work_dir} must not exist and its parent must")
    argv = [cfg.nvkr, "release", "pull", "--reference", cfg.registry_reference, "--output", cfg.output,
            "--out", cfg.work_dir, "--format", "json", "--timeout", f"{PULL_SECONDS // 60}m"]
    code, stdout, stderr = run_capture(argv, PULL_SECONDS + 60)
    if code != 0:
        raise fetch_failed(f"release pull exited {code}: {stderr}")
    try:
        result = json.loads(stdout)
        output = result["output"]
        files = result["files"]
    except (ValueError, KeyError, TypeError) as error:
        raise verify_failed(f"release pull printed no result this script understands: {error}") from error
    if result.get("schemaVersion") != PULL_SCHEMA:
        raise verify_failed(f"release pull result has schema {result.get('schemaVersion')!r}, not {PULL_SCHEMA}")
    if result.get("reference") != cfg.registry_reference or output.get("name") != cfg.output:
        raise verify_failed("release pull answered for a different reference or output than the one asked")
    if output.get("kind") != "disk-image":
        raise unsupported(f"output {cfg.output} is a {output.get('kind')!r}, not a disk-image")
    # A published disk image always names its architecture, taken from its verified wrapper. An output that
    # names none is not one this lane can place in a slot: a wrong-architecture image would boot nothing.
    declared = output.get("arch")
    if declared is None:
        raise verify_failed(f"output {cfg.output} declares no architecture, so it cannot be placed in the slot {cfg.slot} ({cfg.arch})")
    if declared != cfg.arch:
        raise verify_failed(f"output {cfg.output} is built for {declared}, but the slot {cfg.slot} is {cfg.arch}")
    if not isinstance(files, list) or len(files) != 1 or files[0].get("path") != f"{cfg.output}.qcow2":
        raise verify_failed(f"release pull wrote {len(files) if isinstance(files, list) else 'no'} files; expected only {cfg.output}.qcow2")
    sha256, size = files[0].get("sha256"), files[0].get("size")
    if not isinstance(sha256, str) or not SHA256.match(sha256) or not isinstance(size, int) or isinstance(size, bool) or size <= 0:
        raise verify_failed("release pull gave no usable sha256 and size for the image")
    return {"path": Path(cfg.work_dir) / files[0]["path"], "sha256": sha256, "size": size,
            "declaredArch": declared, "digest": output.get("digest"), "buildDigest": output.get("buildDigest")}


def inspect_image(path, want_sha256, want_size):
    """Hash the file as it is on disk and require it to be the bytes the pull reported, as a plain qcow2."""
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    except OSError as error:
        raise verify_failed(f"the pulled image could not be opened: {error}") from error
    with os.fdopen(descriptor, "rb") as handle:
        if not stat.S_ISREG(os.fstat(handle.fileno()).st_mode):
            raise verify_failed("the pulled image is not a regular file")
        digest, count, head, qcow2 = hashlib.sha256(), 0, b"", None
        while True:
            chunk = handle.read(CHUNK_BYTES)
            if not chunk:
                break
            count += len(chunk)
            if count > MAX_BYTES:
                raise unsupported(f"the image is over the {MAX_BYTES} byte limit")
            digest.update(chunk)
            if qcow2 is None:
                head += chunk
                if len(head) >= checker.qcow2_header_bytes(head):
                    qcow2 = parse_header(head)
    if qcow2 is None:
        qcow2 = parse_header(head)
    if count != want_size:
        raise verify_failed(f"the pulled image is {count} bytes; release pull reported {want_size}")
    if digest.hexdigest() != want_sha256:
        raise verify_failed(f"the pulled image hashes to {digest.hexdigest()}; release pull reported {want_sha256}")
    return qcow2


def parse_header(data):
    try:
        return checker.parse_qcow2_header(data)
    except checker.Failure as failure:
        raise unsupported(str(failure)) from failure


class Store:
    """The few object store calls this script makes, each bounded and each coded on failure."""

    def __init__(self, cfg):
        self.cfg = cfg
        self.base = [cfg.aws]
        self.region = ["--region", cfg.region]

    def head(self, key):
        argv = self.base + ["s3api", "head-object", "--bucket", self.cfg.bucket, "--key", key, "--output", "json"] + self.region
        code, stdout, stderr = run_capture(argv, SHORT_SECONDS)
        if code == 0:
            try:
                length = int(json.loads(stdout)["ContentLength"])
            except (ValueError, KeyError, TypeError) as error:
                raise store_failed(f"head-object for {key} answered something this script does not understand: {error}") from error
            return length
        if "(404)" in stderr or "Not Found" in stderr:
            return None
        hint = ""
        if "(403)" in stderr or "Forbidden" in stderr:
            hint = " (S3 also answers 403 for a missing key when the role cannot list the prefix, so the role needs s3:ListBucket on it)"
        raise store_failed(f"head-object for {key} failed: {stderr}{hint}")

    def put_file(self, path, key):
        argv = self.base + ["s3", "cp", str(path), f"s3://{self.cfg.bucket}/{key}", "--only-show-errors"] + self.region
        code, _, stderr = run_capture(argv, STORE_SECONDS)
        if code != 0:
            raise store_failed(f"uploading {key} failed: {stderr}")

    def put_text(self, text, key):
        argv = self.base + ["s3", "cp", "-", f"s3://{self.cfg.bucket}/{key}", "--only-show-errors"] + self.region
        code, _, stderr = run_capture(argv, SHORT_SECONDS, stdin=text.encode("ascii"))
        if code != 0:
            raise store_failed(f"uploading {key} failed: {stderr}")

    def read_text(self, key):
        argv = self.base + ["s3", "cp", f"s3://{self.cfg.bucket}/{key}", "-", "--only-show-errors"] + self.region
        code, stdout, stderr = run_capture(argv, SHORT_SECONDS)
        if code != 0:
            raise store_failed(f"reading {key} failed: {stderr}")
        return stdout.decode("ascii", "replace")

    def read_back(self, key):
        argv = self.base + ["s3", "cp", f"s3://{self.cfg.bucket}/{key}", "-", "--only-show-errors"] + self.region
        return stream_sha256(argv, STORE_SECONDS)


def publish(cfg, image, store):
    """Put the image at its key, then require the store to hold exactly these bytes. Returns what was done."""
    key, sidecar = cfg.key(image["sha256"]), cfg.key(image["sha256"]) + ".sha256"
    existed = store.head(key) is not None
    if not existed:
        store.put_file(image["path"], key)
    mismatch = immutable if existed else verify_failed
    length = store.head(key)
    if length != image["size"]:
        raise mismatch(f"{key} is {length} bytes in the object store; the image is {image['size']}")
    have, count = store.read_back(key)
    if have != image["sha256"] or count != image["size"]:
        raise mismatch(f"{key} reads back as sha256 {have} ({count} bytes), not {image['sha256']} ({image['size']} bytes)")
    sidecar_existed = store.head(sidecar) is not None
    if not sidecar_existed:
        store.put_text(image["sha256"], sidecar)
    text = store.read_text(sidecar)
    if text != image["sha256"]:
        raise immutable(f"{sidecar} holds {text!r}, not {image['sha256']}")
    return {"reused": existed, "sidecarReused": sidecar_existed, "contentLength": length}


def record_for(cfg, image, qcow2, done):
    key = cfg.key(image["sha256"])
    url = cfg.url(image["sha256"])
    verification = {"method": "s3-readback", "run": cfg.run_url, "bytes": image["size"],
                    "qcow2Version": qcow2["version"], "virtualSizeBytes": qcow2["virtualSizeBytes"]}
    return {
        "schema": SCHEMA,
        "slot": cfg.slot,
        "qualificationAuthority": "none",
        "signedProjection": False,
        "source": {"kind": "nvkr-release", "reference": cfg.reference, "version": cfg.version},
        "pulled": {"output": cfg.output, "declaredArch": image["declaredArch"], "digest": image["digest"], "buildDigest": image["buildDigest"]},
        "store": {"bucket": cfg.bucket, "key": key, "sidecarKey": key + ".sha256", "region": cfg.region,
                  "contentLength": done["contentLength"], "reusedExistingObject": done["reused"],
                  "reusedExistingSidecar": done["sidecarReused"]},
        "qcow2": qcow2,
        "manifestEntry": {"url": url, "sha256": image["sha256"], "source": {"kind": "nvkr-release", "reference": cfg.reference, "version": cfg.version},
                          "verification": verification},
    }


def write_record(out, slot, record):
    if os.path.lexists(out):
        raise immutable(f"refusing to replace {out}")
    try:
        os.mkdir(out, 0o700)
        descriptor = os.open(Path(out) / f"{slot}.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    except OSError as error:
        raise immutable(f"could not create the record under {out}: {error}") from error
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        handle.write(json.dumps(record, indent=2, sort_keys=True) + "\n")


def build_parser():
    parser = Parser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--slot", required=True, choices=sorted(SLOT_ARCH))
    parser.add_argument("--reference", required=True, help="oci://REPOSITORY@sha256:DIGEST of the publication root")
    parser.add_argument("--output", required=True, help="the exact disk output name")
    parser.add_argument("--version", required=True, help="the NVKE version the image was built at")
    parser.add_argument("--s3-uri", required=True, help="s3://BUCKET[/PATH], the object store root")
    parser.add_argument("--prefix", required=True, help="a path under the root; begins with nvkr/")
    parser.add_argument("--cdn-domain", required=True, help="the host that serves the root over HTTPS")
    parser.add_argument("--region", required=True)
    parser.add_argument("--work-dir", required=True, help="a new directory that receives the pulled image")
    parser.add_argument("--out", required=True, help="a new directory that receives the publication record")
    parser.add_argument("--run-url", default="", help="the workflow run that publishes; required unless --dry-run")
    parser.add_argument("--nvkr", default="nvkr")
    parser.add_argument("--aws", default="aws")
    parser.add_argument("--dry-run", action="store_true", help="pull and check the image and name its location; write nothing")
    return parser


def main(argv):
    args = build_parser().parse_args(argv)
    try:
        cfg = Config(args)
        for tool in (cfg.nvkr,) if cfg.dry_run else (cfg.nvkr, cfg.aws):
            if shutil.which(tool) is None:
                raise preflight(f"{tool} is not installed")
        if not cfg.dry_run and os.path.lexists(cfg.out):
            raise immutable(f"refusing to replace {cfg.out}")
        image = pull(cfg)
        qcow2 = inspect_image(image["path"], image["sha256"], image["size"])
        # The location is named, and shown to be a plain https URL to a .qcow2, before anything is written.
        location = {"key": cfg.key(image["sha256"]), "url": cfg.url(image["sha256"])}
        if cfg.dry_run:
            print(json.dumps({"dryRun": True, "slot": cfg.slot, "bytes": image["size"], "sha256": image["sha256"],
                              **location, "qcow2": qcow2}, indent=2, sort_keys=True))
            return 0
        done = publish(cfg, image, Store(cfg))
        write_record(cfg.out, cfg.slot, record_for(cfg, image, qcow2, done))
    except Failure as failure:
        print(f"nvkr: [{failure.code}] {failure}", file=sys.stderr)
        return failure.exit_code
    print(f"nvkr: node image published: {cfg.slot}, {image['size']} bytes, sha256 {image['sha256']}, "
          f"{'reused the existing object' if done['reused'] else 'uploaded'}; read back from s3://{cfg.bucket}/{cfg.key(image['sha256'])}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
