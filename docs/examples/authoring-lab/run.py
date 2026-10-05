#!/usr/bin/env python3
"""Exercise public authoring commands against an isolated, deterministic Git fixture."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


HERE = Path(__file__).resolve().parent
UPSTREAM = "https://authoring.example.invalid/platform"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run_lab(binary, work):
    env = os.environ.copy()
    env.update({
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_COUNT": "1",
        "GIT_CONFIG_KEY_0": "url." + str(work / "upstream") + ".insteadOf",
        "GIT_CONFIG_VALUE_0": UPSTREAM,
        "GIT_AUTHOR_NAME": "NKD Authoring Fixture",
        "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
        "GIT_COMMITTER_NAME": "NKD Authoring Fixture",
        "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
        "GIT_AUTHOR_DATE": "2025-01-01T00:00:00Z",
        "GIT_COMMITTER_DATE": "2025-01-01T00:00:00Z",
    })
    shutil.copytree(HERE / "catalog", work / "catalog")
    shutil.copytree(HERE / "upstream", work / "upstream")
    for name in ("definition.yaml", "environment.yaml", "supplier-doctor.sh"):
        shutil.copyfile(HERE / name, work / name)
    (work / "plans").mkdir()

    def command(argv, expected=0, code=None):
        result = subprocess.run(argv, cwd=work, env=env, capture_output=True,
                                text=True, timeout=60, check=False)
        require(result.returncode == expected,
                f"{argv}: exit {result.returncode}, expected {expected}\n"
                f"{result.stdout}{result.stderr}")
        if code:
            require(code in result.stderr, f"missing {code}: {result.stderr}")
        return result.stdout

    def git(*args):
        return command(["git", "-C", str(work / "upstream"),
                        "-c", "commit.gpgsign=false", "-c", "tag.gpgSign=false",
                        "-c", "core.autocrlf=false", *args])

    def cli(*args, expected=0, code=None):
        return command([binary, *args], expected, code)

    def write_json(name, document):
        # These are authored inputs or explanatory reports, never canonical identities.
        (work / name).write_text(json.dumps(document, indent=2, sort_keys=True) + "\n")

    def compile_plan(name, definition="definition.yaml"):
        raw = cli("run", "plan", "--definition", definition,
                  "--environment", "environment.yaml", "--json")
        (work / "plans" / (name + ".json")).write_text(raw)
        return json.loads(raw)

    git("init", "-q", "--template=", "--object-format=sha1", "--initial-branch=main")
    git("add", ".")
    git("commit", "-q", "-m", "fixture 1.0.0")
    for tag in ("app/v1.0.0", "tools/v1.0.0", "platform/v1.0.0"):
        git("tag", tag)
    cli("catalog", "pin", "--dir", "catalog", "--tag", "1.0.0", "platform")
    original_lock = (work / "catalog/pins.lock.yaml").read_bytes()
    cli("catalog", "pin", "--dir", "catalog", "--tag", "1.0.0", "platform")
    require(original_lock == (work / "catalog/pins.lock.yaml").read_bytes(),
            "no-op pin rewrote the lock")
    cli("catalog", "pin", "--dir", "catalog", "--tag", "1.0.0", "app",
        expected=13, code="NKD-E-UNSUPPORTED")
    require(original_lock == (work / "catalog/pins.lock.yaml").read_bytes(),
            "refused derived-member pin changed the lock")

    definition = json.loads(cli("definition", "inspect", "--definition", "definition.yaml"))
    baseline = compile_plan("baseline")
    compile_plan("baseline-repeat")
    require((work / "plans/baseline.json").read_bytes() ==
            (work / "plans/baseline-repeat.json").read_bytes(), "plan regeneration drifted")
    builds = {build["name"]: build for build in baseline["builds"]}
    require(len(builds) == 7, "baseline must retain all seven outputs")
    require(builds["cli-amd64"]["buildDigest"] != builds["cli-arm64"]["buildDigest"],
            "target architecture did not change Build identity")
    require(sum(node["kind"] == "fetch" for node in
                builds["tools"]["executionGraph"]["nodes"]) == 1,
            "the grouped tools output must share one source fetch")
    package_chart = next(node for node in baseline["graph"]["nodes"]
                         if node["id"] == "package/controller-chart")
    require("package/controller" in package_chart["inputs"],
            "chart package lost its exact image package dependency")

    supplier_digest = "sha256:" + hashlib.sha256((work / "supplier-doctor.sh").read_bytes()).hexdigest()
    overlay = {
        "apiVersion": "nkd.dev/catalog-overlay/v3", "kind": "CatalogOverlay",
        "name": "supplier-doctor",
        "requiresCatalogDigest": cli("catalog", "digest", "--data", "catalog").strip(),
        "components": {"doctor": {
            "version": "1.0.0", "sourceDateEpoch": 1735689600,
            "prebuilt": {
                "url": "https://authoring.example.invalid/supplier/doctor.sh",
                "digests": {"amd64": supplier_digest, "arm64": supplier_digest},
                "evidence": {"none": True},
            },
        }},
    }
    write_json("supplier.json", overlay)
    variants = {}
    global_replacement = copy.deepcopy(definition)
    global_replacement["catalog"]["overlays"] = ["supplier.json"]
    variants["whole-producer"] = global_replacement
    scoped_replacement = copy.deepcopy(definition)
    next(output for output in scoped_replacement["outputs"] if output["name"] == "tools")["catalog"] = {
        "base": "./catalog", "overlays": ["supplier.json"],
    }
    variants["output-catalog"] = scoped_replacement
    output_selection = copy.deepcopy(definition)
    next(output for output in output_selection["outputs"] if output["name"] == "diagnostics")["components"] = ["doctor-alt"]
    variants["output-selection"] = output_selection
    plans = {"baseline": baseline}
    for name, variant in variants.items():
        write_json(name + ".json", variant)
        plans[name] = compile_plan(name, name + ".json")
    replaced = next(build for build in plans["whole-producer"]["builds"] if build["name"] == "tools")
    doctor = next(node for node in replaced["executionGraph"]["nodes"] if node.get("component") == "doctor")
    require(doctor["kind"] == "pull", "whole replacement did not switch from build to acquisition")

    (work / "upstream/cli.sh").write_text("#!/bin/sh\nprintf 'authoring lab CLI 1.0.1\\n'\n")
    git("add", "cli.sh")
    git("commit", "-q", "-m", "fixture app 1.0.1")
    git("tag", "app/v1.0.1")
    (work / "upstream/versions.yaml").write_text(
        "dependencies:\n- {name: app, version: 1.0.1}\n- {name: tools, version: 1.0.0}\n")
    git("add", "versions.yaml")
    git("commit", "-q", "-m", "fixture platform 1.0.1")
    git("tag", "platform/v1.0.1")
    preview = cli("catalog", "bump", "--dir", "catalog", "--check", "platform",
                  expected=3, code="NKD-E-DIFF")
    (work / "pin-preview.txt").write_text(preview)
    require(original_lock == (work / "catalog/pins.lock.yaml").read_bytes(),
            "pin preview modified the lock")
    cli("catalog", "bump", "--dir", "catalog", "platform")
    plans["source-pin"] = compile_plan("source-pin")
    cli("run", "plan", "--definition", "whole-producer.json",
        "--environment", "environment.yaml", "--json",
        expected=43, code="NKD-E-UNVERIFIED-REF")

    expected_changes = {
        "source-pin": {"cli-amd64", "cli-arm64", "controller", "controller-chart"},
        "whole-producer": {"tools", "diagnostics"},
        "output-catalog": {"tools"},
        "output-selection": {"diagnostics"},
    }
    report = {"scope": "authoring/compilation fixture; no artifact execution or release acceptance",
              "scenarios": {}}
    for name, expected in expected_changes.items():
        digests = {build["name"]: build["buildDigest"] for build in plans[name]["builds"]}
        require(digests.keys() == builds.keys(), f"{name} changed required output membership")
        changed = {output for output in digests if digests[output] != builds[output]["buildDigest"]}
        require(changed == expected, f"{name}: changed {sorted(changed)}, expected {sorted(expected)}")
        report["scenarios"][name] = {
            "changedOutputs": sorted(changed),
            "outputs": {output: {"before": builds[output]["buildDigest"], "after": digests[output]}
                        for output in sorted(digests)},
        }
        print(f"{name}: changed Build Digests: {', '.join(sorted(changed))}")
    write_json("identity-report.json", report)
    print(f"Authoring checks passed. Plans and identity report: {work}")
    print("UNQUALIFIED fixture: no container build, download, publication, or installation executed.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nkdctl", required=True, help="path to an existing nkdctl binary")
    parser.add_argument("--work", type=Path, help="new directory for retained fixture inputs and reports")
    args = parser.parse_args()
    binary = str(Path(args.nkdctl).resolve())
    require(Path(binary).is_file() and os.access(binary, os.X_OK), "--nkdctl must be executable")
    require(shutil.which("git") is not None, "git is required")
    if args.work is None:
        work = Path(tempfile.mkdtemp(prefix="nkd-authoring-"))
    else:
        work = args.work.absolute()
        # Never overwrite a caller's existing directory, including a previous lab run.
        work.mkdir(parents=True, exist_ok=False)
    run_lab(binary, work)


if __name__ == "__main__":
    main()
