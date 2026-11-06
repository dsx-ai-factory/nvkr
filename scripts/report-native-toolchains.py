# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Diagnostic refresh notifications; never publication or pin-update authority."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys


REPOSITORY = "dsx-ai-factory/nvkr"
REGISTRY = f"ghcr.io/{REPOSITORY}/m2-candidate/toolchains"
LABEL = "native-toolchain-refresh"
TITLE = "Native toolchain refresh"
ARCHES = ("amd64", "arm64")
JOBS = ("measure", "admit", "publish", "verify-candidate", "propose")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")


def compare(plan, proposal, arch):
    """Compare the two described manifest subjects with the selected Plan pins."""
    if arch not in ARCHES:
        raise ValueError("unsupported native architecture")
    platform = f"linux/{arch}"
    packers = {
        node["toolchain"]
        for build in plan["builds"] if build["platform"] == platform
        for node in build["executionGraph"]["nodes"]
        if node.get("toolchain", "").startswith(f"{REGISTRY}/packer-native@")
    }
    if len(packers) != 1:
        raise ValueError("expected one selected native Packer pin")
    selected = {
        "packer": packers.pop(),
        "inspector": plan["environment"]["diskInspectionProfiles"][platform]["image"],
    }
    for kind, ref in selected.items():
        prefix = f"{REGISTRY}/{kind}-native@"
        if not ref.startswith(prefix) or not DIGEST.fullmatch(ref[len(prefix):]):
            raise ValueError(f"invalid selected {kind} manifest pin")
    measured = {}
    for image in proposal:
        kind = image["kind"]
        if image["arch"] != arch or kind not in selected or kind in measured:
            raise ValueError("expected one measured subject per native image")
        digest = image["manifestDigest"]
        if not DIGEST.fullmatch(digest):
            raise ValueError("invalid measured manifest digest")
        measured[kind] = f"{REGISTRY}/{kind}-native@{digest}"
    if measured.keys() != selected.keys():
        raise ValueError("incomplete native measurements")
    return measured != selected


def reason(results, approval_run):
    for job in JOBS:
        if results[job]["result"] not in ("success", "failure", "cancelled", "skipped"):
            raise ValueError(f"unknown job result: {job}")
    failed = [job for job in JOBS if results[job]["result"] in ("failure", "cancelled")]
    if failed:
        return "Refresh failed or was cancelled: " + ", ".join(failed)
    if not approval_run and results["measure"]["result"] == "success":
        outputs = results["measure"].get("outputs", {})
        changed = []
        for arch in ARCHES:
            value = outputs.get(f"changed_{arch}")
            if value not in ("true", "false"):
                raise ValueError(f"missing or invalid {arch} manifest comparison")
            if value == "true":
                changed.append(arch)
        if changed:
            return "Measured manifests differ from committed pins; review required: " + ", ".join(changed)
    return None


def gh(*args, payload=None):
    command = ["gh", *args]
    if payload is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=None if payload is None else json.dumps(payload),
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            check=True, timeout=60)
    return json.loads(result.stdout)


def pages(endpoint):
    return [item for page in gh("api", "--paginate", "--slurp", endpoint) for item in page]


def post(body):
    # The workflow's non-cancelling main-branch concurrency group serializes
    # lookup/create. Read every page; search-index lag must not create duplicates.
    endpoint = f"repos/{REPOSITORY}"
    labeled = pages(f"{endpoint}/issues?state=open&labels={LABEL}&per_page=100")
    candidates = [issue for issue in labeled if "pull_request" not in issue]
    if not candidates:
        candidates = [
            issue for issue in pages(f"{endpoint}/issues?state=open&per_page=100")
            if "pull_request" not in issue
            and re.fullmatch(r"Native toolchain refresh(?: [0-9]+)?", issue["title"])
        ]
    # Adopt the oldest legacy report when duplicates already exist. Closing those
    # duplicates is an owner decision; this workflow never opens another one.
    issue = min(candidates, key=lambda item: item["number"]) if candidates else None
    labels = pages(f"{endpoint}/labels?per_page=100")
    if not any(label["name"] == LABEL for label in labels):
        gh("api", "--method", "POST", f"{endpoint}/labels", payload={
            "name": LABEL, "color": "0e8a16", "description": "Native toolchain refresh tracking",
        })
    if issue is not None:
        if not any(label["name"] == LABEL for label in issue["labels"]):
            gh("api", "--method", "POST", f"{endpoint}/issues/{issue['number']}/labels",
               payload={"labels": [LABEL]})
        gh("api", "--method", "POST", f"{endpoint}/issues/{issue['number']}/comments",
           payload={"body": body})
    else:
        gh("api", "--method", "POST", f"{endpoint}/issues", payload={
            "title": TITLE, "labels": [LABEL], "assignees": ["njhensley"], "body": body,
        })


def report():
    if (os.environ.get("GITHUB_REPOSITORY") != REPOSITORY
            or os.environ.get("GITHUB_REF") != "refs/heads/main"):
        raise ValueError("refresh reporting requires the upstream main branch")
    results = json.loads(os.environ["RESULTS"])
    approval = os.environ.get("APPROVAL_RUN", "")
    actionable = reason(results, approval)
    if actionable is None:
        print("No actionable native toolchain refresh; no issue update.")
        return
    body = (
        f"@njhensley: {actionable}\n\nRun: {os.environ['RUN_URL']}\n\n"
        f"Preparation approval run: {approval or 'not yet approved'}\n\nJob results:\n"
        + "\n".join(f"- {job}: {results[job]['result']}" for job in JOBS)
        + "\n\nOld pins remain selected until the complete pin change is reviewed and merged.\n"
        "Review retained proposals, license/security findings and source-lock freshness. "
        "Changed measurements require reviewed per-image exceptions before publication; "
        "a refresh report is not approval. Follow docs/toolchain-maintenance.md.\n"
    )
    with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
        summary.write(body)
    post(body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    measurement = commands.add_parser("compare")
    measurement.add_argument("--arch", choices=ARCHES, required=True)
    measurement.add_argument("--proposal", type=Path, required=True)
    measurement.add_argument("--plan", type=Path, default=Path(".github/nvkr-run/plan.json"))
    commands.add_parser("report")
    args = parser.parse_args()
    if args.command == "compare":
        changed = compare(json.loads(args.plan.read_text()), json.loads(args.proposal.read_text()), args.arch)
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
            output.write(f"changed_{args.arch}={str(changed).lower()}\n")
    else:
        report()


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as exc:
        print(f"[NVKR-E-GATE-UNMET] native refresh notification: {exc}", file=sys.stderr)
        sys.exit(1)
