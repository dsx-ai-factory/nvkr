#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Publish an already checked toolchain pin diff with the repository review App.

No force pushes, default-branch writes, issue edits, merges or workflow dispatches.
The fixed per-preparation branch makes retries converge or fail visibly.
"""

import base64
import json
import os
from pathlib import Path
import re
import subprocess
import sys

REPOSITORY = "dsx-ai-factory/nvkr"


def invoke(arguments, payload=None):
    result = subprocess.run(arguments, input=payload, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=60, check=False)
    if result.returncode:
        raise RuntimeError(f"{arguments[0]} maintenance operation failed ({result.returncode})")
    if len(result.stdout) > 8 * 1024 * 1024:
        raise RuntimeError("maintenance command output exceeds limit")
    return result.stdout


def api(endpoint, payload=None):
    args = ["gh", "api", f"repos/{REPOSITORY}/{endpoint}"]
    if payload is not None:
        args += ["--method", "POST", "--input", "-"]
    return json.loads(invoke(args, None if payload is None else json.dumps(payload).encode()))


def allowed_path(path):
    parts = Path(path).parts
    if not parts or ".." in parts or Path(path).is_absolute():
        return False
    if path.startswith(("inputs/catalogs/", "inputs/environments/", "inputs/experiments/", "docs/examples/")):
        return path.endswith((".yaml", ".json")) and not path.endswith("/pins.lock.yaml")
    if len(parts) >= 3 and parts[0] == ".github" and parts[1].startswith("nvkr-"):
        return True
    return path.startswith(".github/workflows/nvkr-") or path == ".github/workflows/catalog-refresh.yaml"


def propose():
    source = os.environ["GITHUB_SHA"]
    run = os.environ["APPROVAL_RUN"]
    slug = os.environ["APP_SLUG"]
    if (os.environ.get("GITHUB_REPOSITORY") != REPOSITORY
            or os.environ.get("GITHUB_REF") != "refs/heads/main"
            or os.environ.get("GITHUB_WORKFLOW_REF") != f"{REPOSITORY}/.github/workflows/nvkr-native-toolchains.yml@refs/heads/main"
            or not re.fullmatch(r"[0-9a-f]{40}", source)
            or not re.fullmatch(r"[1-9][0-9]{0,18}", run)
            or not re.fullmatch(r"[a-z0-9][a-z0-9-]*", slug)):
        raise RuntimeError("review publication requires the admitted main workflow")
    if invoke(["git", "rev-parse", "HEAD"]).decode().strip() != source:
        raise RuntimeError("checkout differs from triggering main revision")
    if api("git/ref/heads/main")["object"]["sha"] != source:
        raise RuntimeError("main advanced; retry maintenance against current main")
    paths = invoke(["git", "diff", "--name-only", "-z", "HEAD"]).decode().strip("\0").split("\0")
    paths = sorted(path for path in paths if path)
    untracked = invoke(["git", "ls-files", "--others", "--exclude-standard", "-z"]).decode().strip("\0").split("\0")
    paths = sorted(set(paths + [path for path in untracked if path and not path.startswith(".nvkr-inputs/source/")]))
    if not paths:
        print("Selected toolchain pins and generated bundles already match.")
        return
    if any(not allowed_path(path) for path in paths):
        raise RuntimeError("candidate changes a path outside authored pins/generated bundles")
    branch = f"automation/toolchain-refresh-{run}"
    refs = api(f"git/matching-refs/heads/{branch}")
    matches = [ref for ref in refs if ref["ref"] == f"refs/heads/{branch}"]
    if len(matches) > 1 or len(refs) >= 1000:
        raise RuntimeError("ambiguous review branch")
    entries = []
    for path in paths:
        file = Path(path)
        if file.is_symlink():
            raise RuntimeError("candidate symlinks are forbidden")
        if not file.exists():
            entries.append({"path": path, "mode": "100644", "type": "blob", "sha": None})
            continue
        if not file.is_file() or file.stat().st_size > 8 * 1024 * 1024:
            raise RuntimeError("candidate file is not a bounded regular file")
        blob = api("git/blobs", {"encoding": "base64", "content": base64.b64encode(file.read_bytes()).decode()})
        entries.append({"path": path, "mode": "100755" if file.stat().st_mode & 0o111 else "100644", "type": "blob", "sha": blob["sha"]})
    base_tree = invoke(["git", "rev-parse", "HEAD^{tree}"]).decode().strip()
    tree = api("git/trees", {"base_tree": base_tree, "tree": entries})["sha"]
    if matches:
        commit = api("git/commits/" + matches[0]["object"]["sha"])
        if commit["tree"]["sha"] != tree:
            raise RuntimeError("existing review branch differs; retain it for owner review instead of force pushing")
    else:
        bot = json.loads(invoke(["gh", "api", f"users/{slug}%5Bbot%5D"]))
        if not isinstance(bot["id"], int) or bot["id"] <= 0:
            raise RuntimeError("invalid review App bot identity")
        identity = f"{slug}[bot] <{bot['id']}+{slug}[bot]@users.noreply.github.com>"
        commit = api("git/commits", {"message": f"build(toolchains): refresh native candidate images\n\nSigned-off-by: {identity}", "tree": tree, "parents": [source]})
        api("git/refs", {"ref": f"refs/heads/{branch}", "sha": commit["sha"]})
    pulls = api(f"pulls?state=all&base=main&head=dsx-ai-factory:{branch}&per_page=100")
    if len(pulls) >= 100:
        raise RuntimeError("pull request lookup may be incomplete")
    pulls = [p for p in pulls if p["head"]["repo"] and p["head"]["repo"]["full_name"] == REPOSITORY]
    if len(pulls) > 1:
        raise RuntimeError("ambiguous maintenance pull request")
    if pulls:
        print(pulls[0]["html_url"])
        return  # A closed candidate is a human decision; never reopen it.
    url = f"https://github.com/{REPOSITORY}/actions/runs/{os.environ['GITHUB_RUN_ID']}"
    record = json.loads(Path(f"inputs/policies/toolchain-maintenance/{run}.json").read_text())
    subjects = "\n".join(f"- {im['arch']}/{im['kind']}: `{im['manifestDigest']}`" for im in record["images"])
    body = (f"Refresh both native Packer and inspector pins and their dependent generated bundles.\n\n"
            f"Preparation: https://github.com/{REPOSITORY}/actions/runs/{run}\n"
            f"Publication, independent-reader verification and native pull checks: {url}\n"
            f"Source: `{record['sourceCommit']}`. Reviewed exceptions: `inputs/policies/toolchain-maintenance/{run}.json`.\n\n"
            f"{subjects}\n\nCandidate-only license/security exceptions remain scoped to these subjects. "
            "General organizational approval and qualified reproduction are not asserted. "
            "Generation and drift checks passed before this proposal. Roll back by reverting this pin change and regenerating; old registry subjects are retained.")
    pull = api("pulls", {"head": branch, "base": "main", "title": "build(toolchains): refresh native candidate images", "body": body, "draft": False})
    print(pull["html_url"])


if __name__ == "__main__":
    try:
        propose()
    except (RuntimeError, ValueError, KeyError, OSError, subprocess.TimeoutExpired) as error:
        print(f"[NVKR-E-GATE-UNMET] {error}", file=sys.stderr)
        sys.exit(1)
