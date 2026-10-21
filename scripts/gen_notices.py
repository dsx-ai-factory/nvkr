#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Emit THIRD_PARTY_NOTICES.md for the distributed NVKR binaries."""
import os, re, subprocess, sys

def go(*a):
    # Fail closed: a legal-attribution artifact must never be produced from a
    # silently degraded `go list` (cold cache, offline, transient error), which
    # would emit empty versions and "no license" for real dependencies and still
    # exit 0.
    env = dict(os.environ)
    r = subprocess.run(["go", *a], capture_output=True, text=True, env=env)
    if r.returncode != 0:
        sys.exit(f"gen_notices: `go {' '.join(a)}` failed: {r.stderr.strip()}")
    return r.stdout.strip()

mods = [l.strip() for l in open(sys.argv[1]) if l.strip()]
print("# Third-Party Notices\n")
print("NVKR (`nvkr`, `nvke-demo`, and the private `nvkr-site-identity-helper`) is")
print("distributed under the Apache License 2.0. These binaries link the third-party")
print("modules listed below. Each module's license, copyright")
print("notice, and NOTICE file (where one exists) is reproduced in full.\n")
print("This file lists modules linked into those three binaries. Tools invoked as")
print("separate processes (the Go and GCC toolchains, `cosign`, `syft`, and `docker`)")
print("are not linked and are not covered here.\n")
print("The License column in the Summary is a best-effort SPDX guess from each")
print("license's opening text; the reproduced license and NOTICE text below it is the")
print("authoritative record. Only each module's top-level LICENSE/NOTICE is")
print("reproduced — a module that vendors sub-tree licenses under a different license")
print("(for example an Apache-2.0 subtree inside a BSD module) is not fully covered")
print("by that reproduction.\n")
print("Regenerate with `make notices`.\n")

summary, bodies = [], []
for m in mods:
    version, directory = go("list", "-m", "-f", "{{.Version}}", m), go("list", "-m", "-f", "{{.Dir}}", m)
    if not directory or not os.path.isdir(directory):
        sys.exit(f"gen_notices: module {m}: no local module directory "
                 f"(run `go mod download {m}` and retry)")
    license_file = notice_file = None
    for name in sorted(os.listdir(directory)):
        if license_file is None and re.match(r"^(LICENSE|LICENCE|COPYING)", name, re.I):
            license_file = name
        if notice_file is None and re.match(r"^NOTICE", name, re.I):
            notice_file = name
    if license_file is None:
        sys.exit(f"gen_notices: module {m}: no LICENSE/LICENCE/COPYING file in {directory}")
    def read(name):
        # Strict UTF-8: invalid bytes raise rather than shipping U+FFFD mojibake
        # into the legal notices unnoticed.
        with open(os.path.join(directory, name), encoding="utf-8") as fh:
            return fh.read().strip()
    text = read(license_file)
    notice = read(notice_file) if notice_file else None
    head = text[:400].lower()
    spdx = ("Apache-2.0" if "apache license" in head
            else "MIT" if "mit license" in head or "permission is hereby granted, free of charge" in head
            else "BSD-3-Clause" if "redistribution and use in source and binary forms" in head
            else "see text")
    summary.append(f"| `{m}` | {version} | {spdx} |")
    body = f"\n---\n\n## {m}\n\nVersion: {version}\n\n```\n{text}\n```\n"
    if notice:
        body += f"\nNOTICE:\n\n```\n{notice}\n```\n"
    bodies.append(body)

print("## Summary\n")
print("| Module | Version | License (heuristic) |\n|---|---|---|")
print("\n".join(summary))
print("".join(bodies))
