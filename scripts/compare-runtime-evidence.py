#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Compare downloaded native runtime diagnostics; this grants no authority."""

import argparse
import json
from pathlib import Path


def read_record(path):
    with path.open("rb") as stream:
        raw = stream.read((65 << 20) + 1)
    if len(raw) > 65 << 20:
        raise ValueError(f"diagnostic exceeds size limit: {path}")
    return json.loads(raw)


def compare(first, second):
    productions, inventories = [], []
    for directory in (first, second):
        production = read_record(directory / "disk-result.json")["runtimeUse"]["production"]
        inventory = read_record(directory / "runtime-tar-entries.json")
        if inventory["layerDigest"] != production["layerDigest"]:
            raise ValueError("inventory differs from completed use description")
        entries = inventory["entries"]
        by_path = {entry["path"]: entry for entry in entries}
        if not entries or len(by_path) != len(entries):
            raise ValueError("empty inventory or duplicate entry paths")
        productions.append(production)
        inventories.append((entries, by_path))
    fields = ("buildDigest", "platform", "component", "digest", "layerDigest", "layoutDigest", "size")
    identities = {
        field: [productions[0][field], productions[1][field]]
        for field in fields if productions[0][field] != productions[1][field]
    }
    changes = []
    left, right = inventories[0][1], inventories[1][1]
    for path in sorted(left.keys() | right.keys()):
        a, b = left.get(path, {}), right.get(path, {})
        changed = {key: [a.get(key), b.get(key)] for key in sorted(a.keys() | b.keys())
                   if a.get(key) != b.get(key)}
        if changed:
            changes.append({"path": path, "fields": changed})
    order_equal = [e["path"] for e in inventories[0][0]] == [e["path"] for e in inventories[1][0]]
    equal = not identities and not changes and order_equal
    return {"equal": equal, "identityDifferences": identities, "entryOrderEqual": order_equal,
            "changedEntryCount": len(changes), "entries": changes[:100],
            "reportedEntryLimit": 100}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("first", type=Path)
    parser.add_argument("second", type=Path)
    args = parser.parse_args()
    try:
        result = compare(args.first, args.second)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(2, f"invalid runtime diagnostics: {error}\n")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["equal"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
