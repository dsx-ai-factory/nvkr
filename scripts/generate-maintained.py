#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Regenerate/check the authored maintained bundle inventory, without discovery filters."""

import argparse
import json
from pathlib import Path
import subprocess
import sys

GROUPS = ["main", "nightly", "split", "gpu-site", "development"]


def load_inventory(root):
    inventory = json.loads((root / "inputs/policies/maintained-generation.json").read_text())
    if not isinstance(inventory, list) or any(not isinstance(entry, dict) for entry in inventory):
        raise ValueError("maintained generation inventory must be a list of selections")
    bundles = [entry["bundle"] for entry in inventory]
    if any(not isinstance(bundle, str) or not bundle.startswith("nvkr-") or
           any(character not in "abcdefghijklmnopqrstuvwxyz0123456789-" for character in bundle)
           for bundle in bundles):
        raise ValueError("invalid maintained bundle name")
    if not bundles or bundles != sorted(set(bundles)):
        raise ValueError("maintained bundle inventory must be sorted and unique")
    # Admit the entire authored input selection before invoking any generator.
    for entry in inventory:
        if set(entry) - {"bundle", "group", "definition", "environment", "qualificationPolicy", "runtime"}:
            raise ValueError("unknown maintained generation field")
        if entry["group"] not in GROUPS or type(entry.get("runtime", False)) is not bool:
            raise ValueError("invalid maintained group or runtime selection")
        for key in ("definition", "environment", "qualificationPolicy"):
            if key not in entry and key == "qualificationPolicy":
                continue
            if not isinstance(entry[key], str) or not entry[key]:
                raise ValueError(f"invalid maintained {key} path")
            value = Path(entry[key])
            if value.is_absolute() or ".." in value.parts or not (root / value).is_file():
                raise ValueError(f"invalid maintained {key} path")
    return inventory


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--group", choices=GROUPS)
    parser.add_argument("--projection-pilot", choices=["in-process", "out-of-process"])
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    inventory = load_inventory(root)
    for entry in inventory:
        if args.group and args.group != entry["group"]:
            continue
        command = [str(root / "bin/nvkr-exec"), "run", "generate", "--engine", "github",
                   "--definition", entry["definition"], "--environment", entry["environment"],
                   "--root", "."]
        if entry.get("qualificationPolicy"):
            command += ["--qualification-policy", entry["qualificationPolicy"]]
        if args.check:
            command += ["--check"]
            if entry.get("runtime"):
                command += ["--runtime"]
        if args.projection_pilot and not entry.get("runtime"):
            command += ["--projection-pilot", args.projection_pilot]
        subprocess.run(command, cwd=root, check=True, timeout=900)


if __name__ == "__main__":
    try:
        main()
    except (KeyError, ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"NVKR-E-PIPELINE-DRIFT: maintained generation failed: {error}", file=sys.stderr)
        sys.exit(1)
