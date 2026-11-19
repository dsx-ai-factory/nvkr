#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Check that a node image URL serves the bytes the Forge hardware lane names, as Forge will fetch them.

The lane hands Forge's imager a plain HTTPS URL and a digest recorded elsewhere. This fetches that
exact URL the way the imager will: a GET with no credentials, no redirects followed and no proxy,
streaming the whole body. It requires the digest and the declared length to match and the first bytes
to be a plain qcow2. It runs before any machine is taken, so a URL that answers HEAD but refuses GET
(GitLab artifact downloads do exactly that), a moved object or an expired artifact fails here instead
of forty minutes into provisioning.

What this establishes is narrow. It proves the URL served those bytes to the runner that asked, at that
moment. It does not prove Forge's network can reach the host, that the imager trusts its certificate,
that the image boots, or that the object will still be there when the run starts. Whether the server
honours Range is recorded, not required: it is a fact about the server, and what the imager needs is
a question for the imager.

With --substitutions the URL, the digest and the method come from the substituted plan's record, where
the outputs manifest named how each image was checked. http-get is the fetch above. s3-readback
fetches nothing: the publication workflow already read the object back from the object store with
credentials, because the HTTPS front end can sit behind an address allowlist that a hosted runner is
not on. The record then says what that run reported, which run it was and that this run did not
observe it. Nothing here authenticates the cited run; it is a description until a machine boots the image.

Exit codes: 0 ok, 2 usage, 13 unsupported URL, image, record or verification, 22 the URL did not serve
the named bytes, 47 the output already exists.
"""

import argparse
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import struct
import sys
import time
import urllib.error
import urllib.request

SCHEMA = "nvkr.dev/forge-e2e-node-image-check/v1"
SLOTS = ("os-image-amd64", "os-image-arm64")
# Plain HTTPS to a .qcow2: no credentials, query or fragment. The substitution script admits the same shape.
URL = re.compile(r"^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?/[A-Za-z0-9._~%+=/-]+\.qcow2$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
# The publication workflow's run, with or without its attempt.
RUN_URL = re.compile(r"^https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[1-9][0-9]{0,19}(/attempts/[1-9][0-9]{0,5})?$")
METHOD_GET = "http-get"
METHOD_READBACK = "s3-readback"
READBACK_KEYS = ["bytes", "method", "qcow2Version", "run", "virtualSizeBytes"]
SUBSTITUTIONS_SCHEMA = "nvkr.dev/forge-e2e-substitutions/v1"
QCOW2_MAGIC = b"QFI\xfb"
QCOW2_HEADER_BYTES = 72
CHUNK_BYTES = 1 << 20
DEFAULT_MAX_BYTES = 64 << 30
DEFAULT_TIMEOUT_SECONDS = 60
DEFAULT_MAX_SECONDS = 1800
BASE_HEADERS = {"User-Agent": "nvkr-forge-e2e-check-node-image", "Accept-Encoding": "identity"}


class Failure(Exception):
    def __init__(self, code, exit_code, message):
        super().__init__(message)
        self.code = code
        self.exit_code = exit_code


def unsupported(message):
    return Failure("NVKR-E-UNSUPPORTED", 13, message)


def verify_failed(message):
    return Failure("NVKR-E-VERIFY-FAILED", 22, message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """The imager is given one URL, so a redirect is a different URL than the one being checked."""

    def redirect_request(self, *args, **kwargs):
        return None


def open_url(url, headers, timeout):
    # An empty ProxyHandler: an ambient proxy would change what is being checked.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect)
    return opener.open(urllib.request.Request(url, headers=headers, method="GET"), timeout=timeout)


def status_of(response):
    return getattr(response, "status", None) or response.getcode()


def get(opener, url, headers, timeout):
    """Return an open response for a 2xx answer; turn everything else into a coded failure."""
    try:
        return opener(url, headers, timeout)
    except urllib.error.HTTPError as error:
        what = "a redirect, which is not followed" if 300 <= error.code < 400 else "an error"
        raise verify_failed(f"{url} answered HTTP {error.code} {error.reason}, {what}") from error
    except (urllib.error.URLError, OSError, http.client.HTTPException) as error:
        raise verify_failed(f"{url} could not be fetched: {error}") from error


def parse_qcow2_header(data):
    """Return the facts about a plain qcow2 image, or refuse one the lane does not support."""
    if len(data) < QCOW2_HEADER_BYTES:
        raise unsupported(f"the image is {len(data)} bytes, too small to hold a qcow2 header")
    magic, version, backing_offset, _, cluster_bits, size, crypt_method = struct.unpack(">4sIQIIQI", data[:36])
    if magic != QCOW2_MAGIC:
        raise unsupported("the image does not start with the qcow2 magic")
    if version not in (2, 3):
        raise unsupported(f"qcow2 version {version} is not supported")
    if backing_offset != 0:
        raise unsupported("the image has a backing file, which the imager cannot resolve")
    if crypt_method != 0:
        raise unsupported(f"the image is encrypted at the qcow2 layer (method {crypt_method})")
    if not 9 <= cluster_bits <= 21:
        raise unsupported(f"qcow2 cluster size 2^{cluster_bits} is out of range")
    return {"version": version, "virtualSizeBytes": size, "clusterSizeBytes": 1 << cluster_bits}


def probe_range(opener, url, timeout):
    """Ask for one byte. Recorded, never required: a 200 means the server ignored the Range header."""
    response = get(opener, url, {**BASE_HEADERS, "Range": "bytes=0-0"}, timeout)
    try:
        status = status_of(response)
        accept = response.headers.get("Accept-Ranges", "")
        if status not in (200, 206):
            raise verify_failed(f"{url} answered the range request with HTTP {status}")
        return status == 206, accept
    finally:
        response.close()


def stream(opener, url, expected_sha256, timeout, max_bytes, max_seconds, clock):
    started = clock()
    response = get(opener, url, BASE_HEADERS, timeout)
    try:
        if status_of(response) != 200:
            raise verify_failed(f"{url} answered HTTP {status_of(response)} to a plain GET, not 200")
        encoding = response.headers.get("Content-Encoding", "")
        if encoding.lower() not in ("", "identity"):
            raise verify_failed(f"{url} is served with Content-Encoding {encoding}, so the bytes on the wire are not the image")
        declared = response.headers.get("Content-Length", "")
        if not declared.isdigit() or int(declared) == 0:
            raise verify_failed(f"{url} declares no usable Content-Length ({declared!r})")
        declared = int(declared)
        if declared > max_bytes:
            raise verify_failed(f"{url} declares {declared} bytes, over the {max_bytes} byte limit")

        digest, count, head, qcow2 = hashlib.sha256(), 0, b"", None
        while True:
            if clock() - started > max_seconds:
                raise verify_failed(f"{url} was still streaming after {max_seconds}s, at {count} of {declared} bytes")
            try:
                chunk = response.read(CHUNK_BYTES)
            except (OSError, http.client.HTTPException) as error:
                raise verify_failed(f"{url} failed after {count} of {declared} bytes: {error}") from error
            if not chunk:
                break
            count += len(chunk)
            if count > declared:
                raise verify_failed(f"{url} sent more than its declared {declared} bytes")
            digest.update(chunk)
            if qcow2 is None:
                head += chunk
                if len(head) >= QCOW2_HEADER_BYTES:
                    qcow2 = parse_qcow2_header(head)
        if count != declared:
            raise verify_failed(f"{url} sent {count} of its declared {declared} bytes")
        if qcow2 is None:
            qcow2 = parse_qcow2_header(head)
        have = digest.hexdigest()
        if have != expected_sha256:
            raise verify_failed(f"{url} served sha256 {have}, not the {expected_sha256} the lane names")
        return count, qcow2
    finally:
        response.close()


def check(slot, url, expected_sha256, opener=open_url, timeout=DEFAULT_TIMEOUT_SECONDS,
          max_bytes=DEFAULT_MAX_BYTES, max_seconds=DEFAULT_MAX_SECONDS, clock=time.monotonic):
    if not URL.match(url):
        raise unsupported("the node image must be a plain https URL to a .qcow2, with no credentials, query or fragment")
    range_supported, accept_ranges = probe_range(opener, url, timeout)
    size, qcow2 = stream(opener, url, expected_sha256, timeout, max_bytes, max_seconds, clock)
    return {
        "schema": SCHEMA,
        "slot": slot,
        "method": METHOD_GET,
        "observedByThisRun": True,
        "url": url,
        "sha256": expected_sha256,
        "bytes": size,
        "rangeSupported": range_supported,
        "acceptRanges": accept_ranges,
        "qcow2": qcow2,
    }


def whole(value):
    """A positive integer. bool and float are not: JSON true and 2.0 must not pass for 1 and 2."""
    return type(value) is int and value >= 1


def parse_verification(value):
    """Return a node image's verification from the outputs manifest, or refuse one the lane does not know."""
    if not isinstance(value, dict):
        raise unsupported("a node image's verification must be an object")
    method = value.get("method")
    if method == METHOD_GET:
        if sorted(value) != ["method"]:
            raise unsupported(f"verification {METHOD_GET} takes no other field, got {sorted(value)}")
        return value
    if method == METHOD_READBACK:
        if sorted(value) != READBACK_KEYS:
            raise unsupported(f"verification {METHOD_READBACK} needs exactly {READBACK_KEYS}, got {sorted(value)}")
        if not isinstance(value["run"], str) or not RUN_URL.match(value["run"]):
            raise unsupported("verification run must be the https URL of a GitHub Actions run")
        if not whole(value["bytes"]) or not whole(value["virtualSizeBytes"]):
            raise unsupported("verification bytes and virtualSizeBytes must be positive integers")
        if type(value["qcow2Version"]) is not int or value["qcow2Version"] not in (2, 3):
            raise unsupported("verification qcow2Version must be 2 or 3")
        return value
    raise unsupported(f"verification method must be {METHOD_GET} or {METHOD_READBACK}, got {method!r}")


def recorded(slot, url, expected_sha256, verification):
    """Describe what the publication run reported, without fetching anything."""
    if not URL.match(url):
        raise unsupported("the node image must be a plain https URL to a .qcow2, with no credentials, query or fragment")
    return {
        "schema": SCHEMA,
        "slot": slot,
        "method": METHOD_READBACK,
        "observedByThisRun": False,
        "run": verification["run"],
        "url": url,
        "sha256": expected_sha256,
        "bytes": verification["bytes"],
        "qcow2": {"version": verification["qcow2Version"], "virtualSizeBytes": verification["virtualSizeBytes"]},
    }


def named_in(path, slot):
    """Return the URL, digest and verification the substituted plan's record names for one node image."""
    try:
        with open(path, "r", encoding="utf-8") as handle:
            record = json.load(handle)
    except (OSError, ValueError) as error:
        raise unsupported(f"{path} is not readable JSON: {error}") from error
    if not isinstance(record, dict) or record.get("schema") != SUBSTITUTIONS_SCHEMA:
        raise unsupported(f"{path} is not a {SUBSTITUTIONS_SCHEMA} record")
    if record.get("artifactSource") != "nvkr-outputs":
        raise unsupported("only a run of NVKR outputs names node images to check")
    replaced = record.get("replaced")
    if isinstance(replaced, list) and not all(isinstance(entry, dict) for entry in replaced):
        raise unsupported(f"{path} lists a replacement that is not an object")
    found = [entry for entry in replaced if entry.get("slot") == slot] if isinstance(replaced, list) else []
    if len(found) != 1:
        raise unsupported(f"{path} names {len(found)} replacements for {slot}, want exactly one")
    url, sha256 = found[0].get("to"), found[0].get("sha256")
    if not isinstance(url, str) or not isinstance(sha256, str) or not SHA256.match(sha256):
        raise unsupported(f"the replacement for {slot} names no URL and lower-case sha256")
    return url, sha256, parse_verification(found[0].get("verification"))


def write_new(path, record):
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    except FileExistsError as error:
        raise Failure("NVKR-E-IMMUTABLE-VIOLATION", 47, f"refusing to replace {path}") from error
    except OSError as error:
        raise Failure("NVKR-E-IMMUTABLE-VIOLATION", 47, f"could not create {path}: {error}") from error
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        handle.write(json.dumps(record, indent=2, sort_keys=True) + "\n")


class Parser(argparse.ArgumentParser):
    def error(self, message):
        print(f"nvkr: [NVKR-E-USAGE] {message}", file=sys.stderr)
        sys.exit(2)


def positive(text):
    value = int(text)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return value


def sha256_hex(text):
    if not SHA256.match(text):
        raise argparse.ArgumentTypeError("must be 64 lower-case hex digits")
    return text


def main(argv, opener=open_url, clock=time.monotonic):
    parser = Parser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--slot", required=True, choices=SLOTS)
    parser.add_argument("--url", help="fetch this URL; with --sha256, instead of --substitutions")
    parser.add_argument("--sha256", type=sha256_hex)
    parser.add_argument("--substitutions", type=Path, help="take the URL, digest and method from the substitution record")
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--timeout", type=positive, default=DEFAULT_TIMEOUT_SECONDS, help="seconds per read")
    parser.add_argument("--max-seconds", type=positive, default=DEFAULT_MAX_SECONDS, help="seconds for the whole body")
    parser.add_argument("--max-bytes", type=positive, default=DEFAULT_MAX_BYTES)
    args = parser.parse_args(argv)
    if args.substitutions is not None:
        if args.url is not None or args.sha256 is not None:
            parser.error("--substitutions names the URL and digest; do not also pass --url or --sha256")
    elif args.url is None or args.sha256 is None:
        parser.error("give --substitutions, or both --url and --sha256")
    try:
        if os.path.lexists(args.out):
            raise Failure("NVKR-E-IMMUTABLE-VIOLATION", 47, f"refusing to replace {args.out}")
        url, sha256, verification = args.url, args.sha256, {"method": METHOD_GET}
        if args.substitutions is not None:
            url, sha256, verification = named_in(args.substitutions, args.slot)
        if verification["method"] == METHOD_GET:
            record = check(args.slot, url, sha256, opener, args.timeout, args.max_bytes, args.max_seconds, clock)
        else:
            record = recorded(args.slot, url, sha256, verification)
        write_new(args.out, record)
    except Failure as failure:
        print(f"nvkr: [{failure.code}] {failure}", file=sys.stderr)
        return failure.exit_code
    if record["method"] == METHOD_GET:
        print(f"nvkr: node image ok: {args.slot}, {record['bytes']} bytes, sha256 {record['sha256']}, "
              f"range {'honoured' if record['rangeSupported'] else 'not honoured'}")
    else:
        print(f"nvkr: node image recorded, not fetched: {args.slot}, {record['bytes']} bytes, sha256 {record['sha256']}, "
              f"read back by {record['run']}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
