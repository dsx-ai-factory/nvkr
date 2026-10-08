# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Prepare diagnostic NKE installer inputs without rebuilding charts.

NKE's own strict items/release reader owns its format. NKD's delivery module
owns OCI/archive byte verification. This adapter only copies the pinned deploy
files and changes their chart-source lines, revalidating through NKE afterward.
It does not create a Release, prove provenance or install a site.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


# This is the inspected installer interface, not a floating latest source.
INSTALLER_COMMIT = "8fb2d53368823bd386b4fbd55b4ab6d3c0931345"


class HandoffError(Exception):
    pass


def run(argv, label, timeout=30, text=True):
    try:
        result = subprocess.run(argv, capture_output=True, text=text,
                                timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as err:
        raise HandoffError(f"{label}: unavailable or timed out") from err
    if result.returncode:
        # Neither private caller inputs nor full subprocess diagnostics belong
        # in a potentially shared terminal. The owning tool can be run directly.
        raise HandoffError(f"{label}: failed (exit {result.returncode})")
    return result.stdout


def assignments(values, label):
    result = {}
    for value in values:
        key, sep, item = value.partition("=")
        if not sep or not key or not item or key in result:
            raise HandoffError(f"{label}: require unique ITEM=VALUE assignments")
        result[key] = item
    return result


def release_fields(reader, path):
    fields = run([sys.executable, str(reader), "release", str(path)],
                 "NKE deploy-item validation").rstrip("\n").split("\x1f")
    if len(fields) != 9 or fields[0] != "single":
        raise HandoffError("NKE deploy-item reader returned an unsupported interface")
    return fields


def prepare(nke_checkout, nkdctl, charts, manifests, out):
    checkout = Path(nke_checkout).resolve(strict=True)
    tool = Path(nkdctl).resolve(strict=True)
    destination = Path(out).absolute()
    if destination.exists() or destination.is_symlink():
        raise HandoffError("destination already exists; choose a fresh private directory")
    parent = destination.parent.resolve(strict=True)
    destination = parent / destination.name
    if parent.is_relative_to(checkout):
        raise HandoffError("prepared inputs must be outside the NKE checkout")
    head = run(["git", "-C", str(checkout), "rev-parse", "HEAD"], "NKE source pin").strip()
    if head != INSTALLER_COMMIT:
        raise HandoffError("NKE checkout does not match the selected installer commit")
    dirty = run(["git", "-C", str(checkout), "status", "--porcelain", "--untracked-files=all"],
                "NKE clean-checkout check")
    if dirty:
        raise HandoffError("NKE installer checkout must be clean; use a separate pinned clone")
    reader = checkout / "deploy/lib/nke-deploy-items.py"
    source_path = checkout / "deploy/nico"
    listed = run([sys.executable, str(reader), "items", str(source_path / "nke-deploy.yaml")],
                 "NKE deploy-item listing")
    items = []
    for line in listed.splitlines():
        fields = line.split("\t")
        if len(fields) != 2:
            raise HandoffError("NKE deploy-item reader returned an unsupported interface")
        item, kind = fields
        if not item or Path(item).name != item or item in (".", "..") or item in [i[0] for i in items]:
            raise HandoffError("NKE deploy items must be unique simple names")
        items.append((item, kind))
    helm_items = {item for item, kind in items if kind == "helm"}
    if set(charts) != helm_items or set(manifests) != helm_items:
        raise HandoffError("supply exactly one chart layout and manifest pin for every NKE Helm item")
    # No output work begins until the complete source and requested set pass.
    originals = {item: release_fields(reader, source_path / item / "release.yaml")
                 for item in sorted(helm_items)}
    layouts = {item: str(Path(charts[item]).resolve(strict=True)) for item in helm_items}
    if any(parent.is_relative_to(Path(layout)) for layout in layouts.values()):
        raise HandoffError("prepared inputs must be outside every producer OCI layout")
    tracked = run(["git", "-C", str(checkout), "ls-tree", "-rz", head, "--", "deploy/nico"],
                  "NKE deploy-file membership").split("\0")
    with tempfile.TemporaryDirectory(prefix=".nkd-chart-inputs-", dir=parent) as temporary:
        stage = Path(temporary) / "deploy"
        stage.mkdir(mode=0o700)
        for entry in filter(None, tracked):
            info, path = entry.split("\t", 1)
            mode, kind, _ = info.split(" ")
            if kind != "blob" or mode not in ("100644", "100755"):
                raise HandoffError("NKE deploy copy requires tracked regular files only")
            relative = Path(path).relative_to("deploy/nico")
            if relative.is_absolute() or ".." in relative.parts:
                raise HandoffError("NKE deploy file escapes its source directory")
            raw = run(["git", "-C", str(checkout), "show", f"{head}:{path}"],
                      "NKE pinned deploy-file read", text=False)
            if len(raw) > 8 * 1024 * 1024:
                raise HandoffError("NKE deploy file exceeds the adapter's 8 MiB bound")
            target = stage / relative
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            target.write_bytes(raw)
            target.chmod(0o600)
        (stage / "charts").mkdir(mode=0o700)
        for item in sorted(helm_items):
            fields = originals[item]
            archive_path = f"charts/{item}.tgz"
            # Byte verification and no-replace extraction stay NKD-owned. Helm
            # remains the authoritative loader during inventory/installation.
            observation = run([str(tool), "publish", "chart", "--layout", layouts[item],
                               "--manifest", manifests[item], "--name", fields[1],
                               "--version", fields[2], "--out", str(stage / archive_path)],
                              "NKD chart extraction", timeout=120)
            json.loads(observation)  # Command output is an observation, not authority.
            release = stage / item / "release.yaml"
            lines = release.read_text().splitlines()
            sources = [i for i, line in enumerate(lines)
                       if line.startswith("  path: ") or line.startswith("  repository: ")]
            if len(sources) != 1:
                raise HandoffError("NKE chart source is not the inspected single-source form")
            lines[sources[0]] = f"  path: {archive_path}"
            release.write_text("\n".join(lines) + "\n")
            expected = fields[:]
            expected[3], expected[4] = "", archive_path
            if release_fields(reader, release) != expected:
                raise HandoffError("prepared chart source changed other NKE deploy semantics")
        # Reserve, do not replace, the caller's destination. The only retained
        # child is complete input data; there is no success marker on a partial.
        destination.mkdir(mode=0o700)
        try:
            os.rename(stage, destination / "deploy")
        except OSError:
            destination.rmdir()  # Only the just-created empty directory.
            raise
    return destination / "deploy"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nke-checkout", required=True)
    parser.add_argument("--nkdctl", required=True)
    parser.add_argument("--chart", action="append", default=[], required=True, metavar="ITEM=LAYOUT")
    parser.add_argument("--manifest", action="append", default=[], required=True, metavar="ITEM=SHA256")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    try:
        deploy = prepare(args.nke_checkout, args.nkdctl, assignments(args.chart, "--chart"),
                         assignments(args.manifest, "--manifest"), args.out)
    except (HandoffError, OSError, ValueError) as err:
        print(f"NKD-E-PREFLIGHT-FAILED: chart input preparation: {err}", file=sys.stderr)
        return 17
    print("Prepared diagnostic chart inputs; no rebuild, Release verification or deployment.")
    print(f"DEPLOY_PATH={deploy}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
