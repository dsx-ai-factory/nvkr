#!/usr/bin/env python3
"""Emit THIRD_PARTY_NOTICES.md for the modules linked into nkdctl."""
import os, re, subprocess, sys

def go(*a):
    env = dict(os.environ)
    return subprocess.run(["go", *a], capture_output=True, text=True, env=env).stdout.strip()

mods = [l.strip() for l in open(sys.argv[1]) if l.strip()]
print("# Third-Party Notices\n")
print("NKE Recipes (`nkdctl`) is distributed under the Apache License 2.0. The binary")
print("links the third-party modules listed below. Each module's license, copyright")
print("notice, and NOTICE file (where one exists) is reproduced in full.\n")
print("This file lists modules linked into the `nkdctl` binary. Tools invoked as")
print("separate processes (the Go and GCC toolchains, `cosign`, `syft`, `docker`, and")
print("the `aicr` CLI) are not linked and are not covered here.\n")
print("Regenerate with `make notices`.\n")

summary, bodies = [], []
for m in mods:
    version, directory = go("list", "-m", "-f", "{{.Version}}", m), go("list", "-m", "-f", "{{.Dir}}", m)
    license_file = notice_file = None
    entries = sorted(os.listdir(directory)) if directory and os.path.isdir(directory) else []
    for name in entries:
        if license_file is None and re.match(r"^(LICENSE|LICENCE|COPYING)", name, re.I):
            license_file = name
        if notice_file is None and re.match(r"^NOTICE", name, re.I):
            notice_file = name
    def read(name):
        return open(os.path.join(directory, name), encoding="utf-8", errors="replace").read().strip()
    text = read(license_file) if license_file else "(no license file found in module)"
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
print("| Module | Version | License |\n|---|---|---|")
print("\n".join(summary))
print("".join(bodies))
