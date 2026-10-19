#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Partition the ordinary Go suite without a maintained test allowlist."""

import argparse
import codecs
import hashlib
import json
import os
import re
import selectors
import signal
import subprocess
import sys
import time


# Heavy packages split by top-level test; all other packages run whole.
GROUPS = {"factory": ("pkg/nvkr/factory", 4), "lifecycle": ("pkg/nvkr/lifecycle", 2),
          "packages": (None, 5)}
TEST_NAME = re.compile(r"(?:Test|Example|Fuzz)\w*\Z")
# Retain at most 32 MiB of stdout per child, including discovery commands.
# Overflow fails the shard after emitting the diagnostics already read.
MAX_OUTPUT_BYTES = 32 * 1024 * 1024


def partition(names, index, count):
    """Stable assignment: adding a test does not move any existing test."""
    if count < 1 or not 0 <= index < count:
        raise ValueError("shard index must be in [0, count)")
    if len(names) != len(set(names)):
        raise ValueError("duplicate selection names")
    return sorted(name for name in names
                  if int.from_bytes(hashlib.sha256(name.encode()).digest()[:8], "big") % count == index)


def packages_for_group(packages, module, group, index):
    """Every discovered package belongs to exactly one group."""
    if group not in GROUPS or len(packages) != len(set(packages)):
        raise ValueError("unknown group or duplicate package")
    heavy = {name: module + "/" + path for name, (path, _) in GROUPS.items() if path}
    if not set(heavy.values()).issubset(packages):
        raise ValueError("a required heavy package is missing from go list")
    path, count = GROUPS[group]
    if not 0 <= index < count:
        raise ValueError("shard index is outside the group")
    if path:
        return [heavy[group]]
    return partition([package for package in packages if package not in heavy.values()], index, count)


def listed_tests(log, package):
    """Read Go's compiled, platform-specific test/example/fuzz inventory."""
    names, terminal = [], []
    for line in log.splitlines():
        event = json.loads(line)
        if event.get("Package") != package:
            raise ValueError("test discovery returned an unexpected package")
        if event.get("Action") == "output":
            name = event.get("Output", "").strip()
            if TEST_NAME.fullmatch(name):
                names.append(name)
        if event.get("Action") in {"pass", "fail", "skip"} and "Test" not in event:
            terminal.append(event["Action"])
    if terminal != ["pass"] or not names or len(names) != len(set(names)):
        raise ValueError("test discovery did not complete with a unique, nonempty inventory")
    return names


def validate_results(log, packages, selected=None):
    """Reject missing, repeated or extra package/test results, even after exit 0."""
    completed, tests = {}, set()
    for line in log.splitlines():
        event = json.loads(line)
        package = event.get("Package")
        if package not in packages:
            raise ValueError("test run returned an unexpected package")
        if event.get("Action") not in {"pass", "fail", "skip"}:
            continue
        name = event.get("Test")
        if name is None:
            if package in completed or event["Action"] == "fail":
                raise ValueError("duplicate or failed package result")
            completed[package] = event["Action"]
        elif "/" not in name:
            if (package, name) in tests or event["Action"] == "fail":
                raise ValueError("duplicate or failed top-level test result")
            tests.add((package, name))
    if set(completed) != set(packages):
        raise ValueError("test run did not complete every selected package")
    if selected is not None and tests != {(packages[0], name) for name in selected}:
        raise ValueError("test results do not match the discovered shard selection")


def captured(argv, timeout=300, emit=False, *, max_output_bytes=MAX_OUTPUT_BYTES):
    """Stream diagnostics and stop/join the entire owned process group on exit."""
    if os.name != "posix":
        raise ValueError("CI sharding requires a POSIX process-group owner")
    if max_output_bytes < 1:
        raise ValueError("stdout capture limit must be positive")
    print("+ " + " ".join(argv), file=sys.stderr, flush=True)
    output, deadline = bytearray(), time.monotonic() + timeout
    decoder = codecs.getincrementaldecoder("utf-8")()
    with subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          start_new_session=True) as process:
        try:
            with selectors.DefaultSelector() as reader:
                reader.register(process.stdout, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise subprocess.TimeoutExpired(argv, timeout)
                    if not reader.select(min(remaining, 1)):
                        continue
                    data = os.read(process.stdout.fileno(), 65536)
                    if not data:
                        break
                    if emit:
                        sys.stdout.write(decoder.decode(data))
                        sys.stdout.flush()
                    if len(output) + len(data) > max_output_bytes:
                        if not emit:
                            # Discovery is normally quiet, but retain all
                            # diagnostics read before its output limit failed.
                            sys.stdout.write(decoder.decode(output))
                            sys.stdout.write(decoder.decode(data))
                            sys.stdout.flush()
                        raise ValueError(f"stdout exceeded {max_output_bytes}-byte capture limit")
                    output.extend(data)
            process.wait(timeout=max(0, deadline - time.monotonic()))
            log = output.decode("utf-8")
            if emit:
                sys.stdout.write(decoder.decode(b"", final=True))
            elif process.returncode:
                sys.stdout.write(log)
            if process.returncode:
                raise subprocess.CalledProcessError(process.returncode, argv)
            return log
        finally:
            # A child can retain the pipe after Go exits. The new session keeps
            # cleanup isolated from the calling shell and other test shards.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", action="store_true", help="emit the complete GitHub job matrix")
    parser.add_argument("--group", choices=GROUPS)
    parser.add_argument("--index", type=int)
    parser.add_argument("--timeout", default="20m", help="Go's per-package test timeout")
    args = parser.parse_args(argv)
    if args.matrix:
        print(json.dumps({"include": [{"group": group, "index": index}
                                     for group, (_, count) in GROUPS.items() for index in range(count)]}))
        return
    if args.group is None or args.index is None:
        parser.error("--group and --index are required unless --matrix is selected")
    _, count = GROUPS[args.group]
    if not 0 <= args.index < count:
        parser.error("--index is outside the selected group")
    module = captured(["go", "list", "-m"]).strip()
    packages = packages_for_group(captured(["go", "list", "./..."]).splitlines(),
                                  module, args.group, args.index)
    if not packages:
        raise ValueError("empty package shard")
    command = ["go", "test", "-json", "-count=1", "-timeout", args.timeout]
    selected = None
    if args.group != "packages":
        names = listed_tests(captured(["go", "test", "-json", "-list", ".", *packages]), packages[0])
        selected = partition(names, args.index, count)
        if not selected:
            raise ValueError("empty test shard")
        # Listed Go identifiers contain no regexp syntax. Anchoring prevents a
        # selected TestFoo from also selecting TestFooBar; subtests stay intact.
        command += ["-run", "^(" + "|".join(selected) + ")$"]
        print(f"Selected {len(selected)} of {len(names)} top-level tests/examples/fuzz targets",
              file=sys.stderr, flush=True)
    command += packages
    # The workflow owns a shorter total job budget. This outer bound still
    # terminates a stuck compiler when reproducing a shard outside Actions.
    log = captured(command, timeout=1800, emit=True)
    validate_results(log, packages, selected)


def interrupted(signum, _frame):
    """Unwind through the process-group owner when Actions cancels this step."""
    raise InterruptedError(f"received signal {signum}")


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as failure:
        print(f"CI test shard failed: {failure}", file=sys.stderr)
        sys.exit(1)
