#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Run unqualified diagnostics using upstream tests and a supplied NVKR-built CLI.

Trusted operator inputs only: this script does not admit a Run or issue evidence.
Plan validation delegates to Go; production source/execution ownership is deferred.
"""

import argparse
import csv
import hashlib
import io
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

REPOSITORY = Path(__file__).resolve().parents[1]
STEP_TIMEOUT_SECONDS = 30 * 60


class Interrupted(Exception):
    """Request subprocess stop/join and container cleanup on cancellation."""

    def __init__(self, signum):
        super().__init__(f"interrupted by signal {signum}")
        self.signum = signum


def handle_signal(signum, _frame):
    raise Interrupted(signum)


def run_command(command, **kwargs):
    # subprocess.run kills and joins its child on exceptions, including signals.
    subprocess.run(command, check=True, timeout=STEP_TIMEOUT_SECONDS, **kwargs)


def expected_version(component):
    stamps = component["versionStamp"]
    values = [stamps[key] for key in ("NVKECTL_VERSION", "NKECTL_VERSION") if key in stamps]
    if not values or any(not isinstance(value, str) or not value.strip() for value in values):
        raise ValueError("versionStamp must supply a nonempty expected CLI version")
    if len(set(values)) != 1:
        raise ValueError("versionStamp contains conflicting CLI versions")
    return values[0]


def load_cli_inputs(plan_path, output=None, nvkr=None):
    """Delegate complete Plan validation and producer selection to NVKR."""
    executable = str(nvkr or REPOSITORY / "bin/nvkr")
    command = [executable, "run", "artifact-test-inputs", "--plan", str(plan_path)]
    if output is not None:
        command += ["--output", output]
    raw = subprocess.check_output(command, text=True, timeout=30)
    inputs = json.loads(raw)
    fields = ("repo", "commit", "treeHash", "toolchainImage", "expectedVersion", "platform", "output", "buildDigest")
    if not isinstance(inputs, dict) or any(not isinstance(inputs.get(key), str) or not inputs[key] for key in fields):
        raise ValueError("malformed artifact-test-inputs response")
    component = {key: inputs[key] for key in ("repo", "commit", "treeHash", "toolchainImage")}
    component["versionStamp"] = {"NVKECTL_VERSION": inputs["expectedVersion"]}
    return component, inputs["platform"], inputs["output"], inputs["buildDigest"]


def import_test_source(source_directory, component):
    """Import the exact pinned tree, independent of host checkout filters."""
    environment = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull,
                       GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_COUNT="0")
    git = ["git", "-c", "core.autocrlf=false", "-c", f"core.hooksPath={os.devnull}",
           "-c", f"core.attributesFile={os.devnull}"]
    run_command(git + ["init", "--quiet", str(source_directory)], env=environment)
    git += ["-C", str(source_directory)]
    run_command(git + ["remote", "add", "origin", component["repo"]], env=environment)
    run_command(git + ["fetch", "--depth=1", "--no-tags", "origin", component["commit"]], env=environment)
    run_command(git + ["checkout", "--detach", component["commit"]], env=environment)
    actual_tree = subprocess.check_output(git + ["rev-parse", "HEAD^{tree}"],
                                          env=environment, text=True, timeout=30).strip()
    if actual_tree != component["treeHash"]:
        raise ValueError("imported test source tree does not match the plan")
    if subprocess.check_output(git + ["status", "--porcelain"], env=environment, timeout=30).strip():
        raise ValueError("imported test worktree is modified")
    if not re.search(r"^test-artifact:", (source_directory / "cli/Makefile").read_text(), re.MULTILINE):
        raise ValueError("the pinned NVKE revision has no test-artifact target; update the CLI pin "
                         "and regenerate the matching plan first")


def bind_mount(source, destination, readonly=False):
    """Encode Docker's CSV mount grammar, including colon/comma/space paths."""
    buffer = io.StringIO()
    fields = ["type=bind", f"src={source}", f"dst={destination}"]
    if readonly:
        fields.append("readonly")
    csv.writer(buffer, lineterminator="").writerow(fields)
    return buffer.getvalue()


def run_container(command, name, **kwargs):
    try:
        run_command(command, **kwargs)
    finally:
        try:
            subprocess.run(["docker", "rm", "--force", name], check=False, timeout=30,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except (OSError, subprocess.SubprocessError, Interrupted, KeyboardInterrupt) as error:
            print(f"test-artifacts: container cleanup failed: {error}", file=sys.stderr)


def summarize_tests(log_path, suite):
    """Reject empty, skipped, failed, or incomplete required Go test runs."""
    with log_path.open() as log:
        return summarize_events(log, suite)


def summarize_events(lines, suite):
    started, passed, packages = set(), set(), set()
    for line in lines:
        line = line.strip()
        if not line.startswith(("{", "[")):
            continue  # Make command echo and prerequisite output.
        event = json.loads(line)
        if not isinstance(event, dict) or not isinstance(event.get("Action"), str) or not isinstance(event.get("Package"), str) or ("Test" in event and not isinstance(event["Test"], str)):
            raise ValueError("malformed Go test event")
        action, package, test = event.get("Action"), event.get("Package"), event.get("Test")
        if action in ("fail", "skip"):
            raise ValueError(f"required suite {action}: {package} {test or ''}")
        if test and "/" not in test:
            key = (package, test)
            if action == "run":
                started.add(key)
            elif action == "pass":
                passed.add(key)
        elif not test and action == "pass":
            packages.add(package)
    contract_package = "command-line-arguments" if suite == "contract" else "github.com/dsx-ai-factory/nvke/cli/cmd/nvkectl"
    if not started or started != passed or not any(p == contract_package for p, _ in passed):
        raise ValueError("required suite has zero or incomplete tests, or no CLI contract package")
    if not {package for package, _ in passed}.issubset(packages):
        raise ValueError("required suite has incomplete package results")
    return {"topLevelTests": len(passed), "packages": len(packages)}


def test_binary(workspace, artifact_path, component, platform, suite):
    """Download runner dependencies, then run upstream tests with no network."""
    cache = workspace / "cache"
    cache.mkdir()
    name = f"nvke-artifact-{workspace.name}"
    command = ["docker", "run", "--name", name, "--rm", "--init", "--platform", platform,
               "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
               "--user", f"{os.getuid()}:{os.getgid()}", "--workdir", "/src/cli",
               "--tmpfs", "/tmp:rw,exec,mode=1777",
               "--mount", bind_mount(workspace / "nvke", "/src", True),
               "--mount", bind_mount(cache, "/cache")]
    environment = {"GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=readonly -modcacherw",
                   "CGO_ENABLED": "0", "GOCACHE": "/cache/go-build", "GOPATH": "/cache/go", "HOME": "/tmp"}
    for key, value in environment.items():
        command += ["--env", f"{key}={value}"]
    image = component["toolchainImage"]
    # The artifact is absent from acquisition; only the offline phase mounts it.
    run_container(command + [image, "go", "mod", "download"], name)
    log_path = workspace / "tests.jsonl"
    offline = command + ["--network", "none", "--env", "GOPROXY=off",
                         "--mount", bind_mount(artifact_path, "/artifact/nvkectl", True), image,
                         "make", "--no-print-directory", "test" if suite == "cli" else "test-artifact",
                         "GO=go", "GO_TEST_FLAGS=-count=1 -timeout=15m -json", "COVERAGE_DIR=/cache/coverage",
                         "NVKECTL=/artifact/nvkectl", f"NVKECTL_EXPECTED_VERSION={expected_version(component)}"]
    try:
        with log_path.open("w") as log:
            run_container(offline, name, stdout=log)
    finally:
        if log_path.exists():
            try:
                with log_path.open() as log:
                    for line in log:
                        print(line, end="", flush=True)
            except OSError as error:
                print(f"test-artifacts: log replay failed: {error}", file=sys.stderr)
    return summarize_tests(log_path, suite)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--plan", type=Path, required=True, help="generated plan matching the artifact's build")
    parser.add_argument("--nvkr", type=Path, default=REPOSITORY / "bin/nvkr", help="NVKR executable built from this checkout")
    parser.add_argument("--output", help="authored CLI output name (required for multi-CLI plans)")
    parser.add_argument("--suite", choices=("contract", "cli"), default="contract")
    parser.add_argument("--keep", action="store_true", help="retain private source, cache, and result logs")
    args = parser.parse_args()
    component, platform, output, build_digest = load_cli_inputs(args.plan.resolve(strict=True), args.output, args.nvkr)
    artifact = args.artifact.resolve(strict=True)
    if not artifact.is_file() or not os.access(artifact, os.X_OK):
        raise ValueError("--artifact must name an executable regular file")
    root = REPOSITORY / ".nvkr/artifact-tests"
    root.mkdir(parents=True, exist_ok=True)
    workspace = Path(tempfile.mkdtemp(prefix="cli-", dir=root))
    print(f"Workspace: {workspace} (retained={args.keep})", flush=True)
    try:
        # Freeze the tested bytes; a later caller-path swap cannot change the mount.
        staged = workspace / "nvkectl"
        shutil.copyfile(artifact, staged)
        staged.chmod(0o555)
        checksum = hashlib.sha256(staged.read_bytes()).hexdigest()
        metadata = {"output": output, "buildDigest": build_digest, "testRevision": component["commit"],
                    "treeHash": component["treeHash"], "artifactSha256": checksum,
                    "toolchainImage": component["toolchainImage"],
                    "platform": platform, "suite": args.suite, "authority": "unqualified-diagnostic"}
        print(json.dumps(metadata, sort_keys=True), flush=True)
        import_test_source(workspace / "nvke", component)
        counts = test_binary(workspace, staged, component, platform, args.suite)
        if hashlib.sha256(staged.read_bytes()).hexdigest() != checksum:
            raise ValueError("staged artifact bytes changed during testing")
        metadata.update(counts, result="pass")
        (workspace / "result.json").write_text(json.dumps(metadata, sort_keys=True) + "\n")
        print(f"PASS: upstream {args.suite} suite; staged artifact checksum unchanged", flush=True)
    finally:
        if not args.keep:
            shutil.rmtree(workspace)


def cli():
    previous = {signum: signal.signal(signum, handle_signal) for signum in (signal.SIGINT, signal.SIGTERM)}
    try:
        main()
        return 0
    except Interrupted as error:
        print(f"test-artifacts: {error}", file=sys.stderr)
        return 128 + error.signum
    except KeyboardInterrupt:
        print("test-artifacts: interrupted", file=sys.stderr)
        return 130
    except subprocess.CalledProcessError as error:
        print(f"test-artifacts: command failed (exit {error.returncode})", file=sys.stderr)
        return error.returncode if error.returncode > 0 else 128 - error.returncode
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"test-artifacts: {error}", file=sys.stderr)
        return 1
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


if __name__ == "__main__":
    sys.exit(cli())
