#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Bounded, disposable native CLI candidate verification; never updates maintained pins."""

import argparse
import hashlib
import importlib.util
import json
import os
import platform
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
IMAGE = "golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c"
NATIVE_IMAGES = {
    "amd64": "golang@sha256:2d658507a011f1eadc0114d7a421b312b2e77d9042a24036a26e267cc9231677",
    "arm64": "golang@sha256:a83e961ad968e2e7abc2a59554e356f00c2a270a15fbb39f2c84a4caf13be9c3",
}
SOURCE = "3da7d64940ffe94722a5a913ab5dc26b28430d72"
TREE = "86d6b4cc3a9a021cc6d38aa445ed90f04da2b28b"
OLD_SOURCE = "ed9fb09b04fd40bf331d25df2222206ffaca593e"


def run(command, log=None, timeout=1800):
    if log:
        with log.open("w") as output:
            try:
                subprocess.run(command, check=True, timeout=timeout, stdout=output, stderr=subprocess.STDOUT)
            except subprocess.SubprocessError:
                output.flush()
                print(log.read_text()[-12000:], file=sys.stderr, flush=True)
                raise
    else:
        subprocess.run(command, check=True, timeout=timeout)


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def adapter():
    spec = importlib.util.spec_from_file_location("artifact_adapter", REPO / "scripts/test-artifacts.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def cancellation(args):
    a = adapter()
    a.REPOSITORY = args.root / ("cancel-" + args.cancel)
    a.REPOSITORY.mkdir()
    original = a.run_container

    def hold(command, name, **kwargs):
        # Retain the real acquisition isolation argv; hold its process for cancellation.
        command = command[:-3] + ["sleep", "60"]
        if args.cancel == "timeout":
            a.STEP_TIMEOUT_SECONDS = 3

        def observe():
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                probe = subprocess.run(["docker", "inspect", "--format", "{{.State.Running}}", name],
                                       capture_output=True, text=True, timeout=5)
                if probe.returncode == 0 and probe.stdout.strip() == "true":
                    write(args.root / (args.cancel + "-ready.json"), {"container": name})
                    return
                time.sleep(0.1)

        worker = threading.Thread(target=observe, daemon=True)
        worker.start()
        try:
            return original(command, name, **kwargs)
        finally:
            worker.join(timeout=3)

    a.run_container = hold
    sys.argv = ["adapter", "--artifact", str(args.artifact), "--plan", str(args.plan),
                "--nvkr", str(REPO / "bin/nvkr")]
    return a.cli()


def suite_worker(args):
    a = adapter()
    a.REPOSITORY = args.root / 'repo space:comma,quote"'
    original = a.run_container
    records = []

    def observe_container(command, name, **kwargs):
        record = {"command": command}
        # Discover the actual cache independently of argv's CSV encoding.
        workspaces = list((a.REPOSITORY / ".nvkr/artifact-tests").glob("cli-*"))
        workspace = next(path for path in workspaces if path.name in name)
        record["cacheInitiallyEmpty"] = not any((workspace / "cache").iterdir())

        def observe():
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                probe = subprocess.run(["docker", "inspect", name], capture_output=True, text=True, timeout=5)
                if probe.returncode == 0:
                    record["inspection"] = json.loads(probe.stdout)[0]
                    return
                time.sleep(0.1)

        worker = threading.Thread(target=observe, daemon=True)
        worker.start()
        try:
            return original(command, name, **kwargs)
        finally:
            worker.join(timeout=3)
            records.append(record)
            write(args.root / (args.suite_worker + "-commands.json"), records)

    a.run_container = observe_container
    sys.argv = ["adapter", "--artifact", str(args.artifact), "--plan", str(args.plan),
                "--nvkr", str(REPO / "bin/nvkr"), "--suite", args.suite_worker, "--keep"]
    return a.cli()


def inputs(root, arch, mode):
    catalog = root / "catalog"
    catalog.mkdir(parents=True)
    source = {
        "repo": "https://github.com/dsx-ai-factory/nvke",
        "toolchainImage": IMAGE,
        "vendorCommand": ["bash", "-euc", "export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=; cd cli; timeout --kill-after=10s 540s go mod download; GOPROXY=off timeout --kill-after=10s 60s go mod verify; GOPROXY=off timeout --kill-after=10s 180s go mod vendor"],
    }
    selected = IMAGE
    if mode != "scalar":
        source["toolchainImage"] = "nvkr-toolchain-unused@" + IMAGE.split("@", 1)[1]
        source["toolchainImagesByArch"] = {
            key: value if mode == "map" else "nvkr-toolchain-cli@" + value.split("@", 1)[1]
            for key, value in NATIVE_IMAGES.items()
        }
        selected = source["toolchainImagesByArch"][arch]
    component = {
        "fromSource": True, "source": "nvke-cli", "cgo": False,
        "platforms": ["linux/" + arch], "outputType": "host-package",
        "artifactPath": "cli/bin/nvkectl", "writeDir": "cli/bin",
        "versionStamp": {"NKECTL_VERSION": "{{.Commit}}", "NKECTL_COMMIT": "{{.Commit}}", "NKECTL_DATE": "{{.Date}}"},
        "buildCommand": ["bash", "-euc", "export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=-mod=vendor; export NVKECTL_VERSION=$NKECTL_VERSION NVKECTL_COMMIT=$NKECTL_COMMIT NVKECTL_DATE=$NKECTL_DATE; make -C cli build NVKECTL_TREE=clean"],
    }
    write(catalog / "registry.yaml", {"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
          "metadata": {"name": "cli-candidate-diagnostic"}, "sources": {"nvke-cli": source},
          "components": {"nvkectl": component}})
    write(root / "definition.json", {"apiVersion": "nvkr.dev/definition/v5", "kind": "Definition",
          "metadata": {"name": "cli-candidate-diagnostic"}, "catalog": {"base": "./catalog"},
          "outputs": [{"name": "nvkectl", "components": ["nvkectl"], "format": "executable", "platform": "linux/" + arch}]})
    environment = {"apiVersion": "nvkr.dev/environment/v1", "kind": "Environment",
                   "metadata": {"name": "cli-candidate-diagnostic"}, "utilityContainer": IMAGE,
                   "utilityPlatform": "linux/" + arch, "runtime": {"runtime": "docker"}}
    if mode == "relocated":
        environment["runtime"]["toolchainRegistry"] = "localhost:5000"
        selected = "localhost:5000/" + selected
    write(root / "environment.json", environment)
    return selected


def invoke(root, artifact, plan, suite, label, keep=True, expected=0):
    command = [sys.executable, str(REPO / "scripts/test-artifacts.py"), "--artifact", str(artifact),
               "--plan", str(plan), "--nvkr", str(REPO / "bin/nvkr"), "--suite", suite]
    if keep:
        command.append("--keep")
    log = root / (label + ".log")
    with log.open("w") as output:
        process = subprocess.run(command, timeout=1800, stdout=output, stderr=subprocess.STDOUT)
    match = re.search(r"^Workspace: (.+) \(retained=", log.read_text(), re.MULTILINE)
    workspace = Path(match.group(1)) if match else None
    result = workspace / "result.json" if workspace else None
    if expected == 0:
        assert process.returncode == 0 and result and result.exists(), (label, process.returncode)
    else:
        assert process.returncode != 0 and (not result or not result.exists()), (label, process.returncode)
    if workspace and (workspace / "cache").exists():
        shutil.rmtree(workspace / "cache")
    return workspace, process.returncode


def main(args):
    os.chdir(REPO)
    root = args.root.resolve()
    root.mkdir(parents=True)
    assert sys.platform == "linux" and platform.machine() in ({"amd64": {"x86_64"}, "arm64": {"aarch64"}}[args.arch])
    actual = subprocess.check_output(["docker", "info", "--format", "{{.OSType}}/{{.Architecture}}"], text=True).strip()
    assert actual in {"linux/" + args.arch, "linux/" + platform.machine()}, actual
    selected = inputs(root / "inputs", args.arch, args.mode)
    registry = args.mode == "relocated"
    try:
        if registry:
            run(["docker", "run", "-d", "--name", "nvkr-cli-diagnostic-registry", "-p", "127.0.0.1:5000:5000", "registry:3"])
            run([str(REPO / "bin/crane"), "copy", NATIVE_IMAGES[args.arch], "localhost:5000/nvkr-toolchain-cli:diagnostic"])
            copied = subprocess.check_output([str(REPO / "bin/crane"), "digest", "localhost:5000/nvkr-toolchain-cli:diagnostic"], text=True).strip()
            assert copied == NATIVE_IMAGES[args.arch].split("@", 1)[1], copied
        nvkr = str(REPO / "bin/nvkr")
        authored = ["--definition", str((root / "inputs/definition.json").relative_to(REPO)),
                    "--environment", str((root / "inputs/environment.json").relative_to(REPO))]
        run([nvkr, "catalog", "pin", "--dir", str(root / "inputs/catalog"), "--commit", SOURCE, "--timeout", "5m", "nvke-cli"], root / "pin.log")
        plan = root / "plan.json"
        run([nvkr, "run", "plan", *authored, "--json"], plan)
        chosen = json.loads(subprocess.check_output([nvkr, "run", "artifact-test-inputs", "--plan", str(plan)], text=True))
        assert chosen["commit"] == SOURCE and chosen["treeHash"] == TREE
        assert chosen["toolchainImage"] == selected and chosen["platform"] == "linux/" + args.arch, chosen
        run(["tar", "-czf", str(root / "candidate-inputs.tgz"), "-C", str(root), "inputs"])
        attempts_root = str((root / "runs").relative_to(REPO))
        run([nvkr, "run", "local", *authored, "--attempt", "fresh", "--attempts-root", attempts_root, "--ui", "plain"], root / "build.log", 2400)
        artifact = root / "runs/fresh/nvkectl/out/nvkectl/nvkectl"
        before = sha(artifact)
        a = adapter()
        # Import paths and staged mounts all contain Docker CSV metacharacters.
        special = root / 'repo space:comma,quote"'
        (special / "scripts").mkdir(parents=True)
        (special / "scripts/test-artifacts.py").write_bytes((REPO / "scripts/test-artifacts.py").read_bytes())
        suites = {}
        for suite in ("contract", "cli"):
            command = [sys.executable, str(Path(__file__).resolve()), "--root", str(root), "--arch", args.arch,
                       "--artifact", str(artifact), "--plan", str(plan), "--suite-worker", suite]
            run(command, root / (suite + ".log"))
            commands = json.loads((root / (suite + "-commands.json")).read_text())
            assert len(commands) == 2 and commands[0]["cacheInitiallyEmpty"]
            acquisition, execution = (record["inspection"] for record in commands)
            assert not any(mount["Destination"] == "/artifact/nvkectl" for mount in acquisition["Mounts"])
            assert execution["HostConfig"]["NetworkMode"] == "none" and execution["HostConfig"]["ReadonlyRootfs"]
            assert "ALL" in execution["HostConfig"]["CapDrop"]
            assert any(value.split("=", 1)[0] == "no-new-privileges" for value in execution["HostConfig"]["SecurityOpt"])
            assert all(not mount["RW"] for mount in execution["Mounts"] if mount["Destination"] in ("/src", "/artifact/nvkectl"))
            assert not any("TOKEN=" in value for value in execution["Config"]["Env"])
            workspace = next(path for path in (special / ".nvkr/artifact-tests").glob("cli-*")
                             if (path / "result.json").exists() and json.loads((path / "result.json").read_text())["suite"] == suite)
            result = json.loads((workspace / "result.json").read_text())
            events = [json.loads(line) for line in (workspace / "tests.jsonl").read_text().splitlines() if line.startswith("{")]
            inventory = sorted({(event["Package"], event["Test"]) for event in events if event["Action"] == "pass" and event.get("Test") and "/" not in event["Test"]})
            declared = re.findall(r"^func (Test\w+)\(t \*testing.T\)", (workspace / "nvke/cli/cmd/nvkectl/contract_test.go").read_text(), re.MULTILINE)
            assert declared, "no declared process contracts in this exact source"
            assert all(any(test == name for _, test in inventory) for name in declared), (suite, declared, inventory)
            write(root / (suite + "-inventory.json"), {"result": result, "inventory": inventory, "declaredBinaryContracts": declared})
            suites[suite] = result
            # Keep raw source and result records, omit module/build caches from upload.
            run(["tar", "-czf", str(root / (suite + "-import.tgz")), "-C", str(workspace), "nvke", "tests.jsonl", "result.json"])
            if suite == "contract":
                probe = ["docker", "run", "--rm", "--read-only", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                         "--mount", a.bind_mount(workspace / "nvke", "/src", True), "--mount", a.bind_mount(workspace / "nvkectl", "/artifact/nvkectl", True), selected,
                         "sh", "-ec", "if touch /src/probe; then exit 90; fi; if printf x >>/artifact/nvkectl; then exit 91; fi; if touch /probe; then exit 92; fi; if timeout 3 bash -c 'echo x >/dev/tcp/1.1.1.1/443'; then exit 93; fi; uname -m"]
                run(probe, root / "isolation.log", 30)
            if suite == "cli" and args.mode == "scalar":
                broken = root / "broken"
                broken.write_text("#!/bin/sh\nexit 7\n")
                broken.chmod(0o555)
                execution_argv = commands[1]["command"]
                prefix = execution_argv[:execution_argv.index(selected)]
                old_mount = a.bind_mount(workspace / "nvkectl", "/artifact/nvkectl", True)
                prefix[prefix.index(old_mount)] = a.bind_mount(broken, "/artifact/nvkectl", True)
                name = prefix[prefix.index("--name") + 1]
                filtered = prefix + ["--env", "NVKECTL=/artifact/nvkectl", "--env", "NVKECTL_EXPECTED_VERSION=" + SOURCE,
                                     selected, "go", "test", "-count=1", "-timeout=1m", "-json", "-run", "^TestReportFailure$", "./cmd/nvkectl"]
                ordinary_log = root / "ordinary-only.jsonl"
                with ordinary_log.open("w") as output:
                    a.run_container(filtered, name, stdout=output)
                accepted = a.summarize_tests(ordinary_log, "cli")
                assert accepted["topLevelTests"] == 1, accepted
                write(root / "completeness-limitation.json", {"brokenBinarySha256": sha(broken),
                      "checkerAccepted": accepted, "requiredBinaryContractsRan": False, "reusedVerifiedTestCaches": True})
            shutil.rmtree(workspace / "cache")
        assert sha(artifact) == before
        write(root / "proof.json", {"nvkrCommit": subprocess.check_output(["git", "-c", "safe.directory=" + str(REPO), "-C", str(REPO), "rev-parse", "HEAD"], text=True).strip(),
              "sourceCommit": SOURCE, "sourceTree": TREE, "selected": chosen, "planSha256": sha(plan), "artifactSha256Before": before,
              "artifactSha256After": sha(artifact), "nativeHost": platform.machine(), "dockerPlatform": actual,
              "mode": args.mode, "freshRecipeBuild": True, "freshImportAndEmptyCachesPerSuite": True, "suites": suites})
        if args.mode == "scalar":
            negatives = {}
            for case, code, sig in (("timeout", 1, None), ("sigterm", 143, signal.SIGTERM), ("sigint", 130, signal.SIGINT)):
                command = [sys.executable, str(Path(__file__).resolve()), "--root", str(root), "--arch", args.arch,
                           "--cancel", case, "--artifact", str(artifact), "--plan", str(plan)]
                with (root / (case + ".log")).open("w") as log:
                    process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
                    ready = root / (case + "-ready.json")
                    deadline = time.monotonic() + 90
                    try:
                        while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
                            time.sleep(0.1)
                        assert ready.exists(), case
                        name = json.loads(ready.read_text())["container"]
                        if sig:
                            process.send_signal(sig)
                        assert process.wait(timeout=20) == code
                        assert subprocess.run(["docker", "inspect", name], capture_output=True).returncode != 0
                        assert not list((root / ("cancel-" + case)).glob(".nvkr/artifact-tests/cli-*"))
                        write(root / (case + "-result.json"), {"exit": code, "containerRemoved": True, "workspaceRemoved": True})
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait(timeout=10)
            registry_path = root / "inputs/catalog/registry.yaml"
            registry_input = json.loads(registry_path.read_text())
            registry_input["components"]["nvkectl"]["versionStamp"]["NKECTL_VERSION"] = "v0.0.0-negative"
            write(registry_path, registry_input)
            wrong_plan = root / "wrong-version-plan.json"
            run([nvkr, "run", "plan", *authored, "--json"], wrong_plan)
            run(["tar", "-czf", str(root / "wrong-version-inputs.tgz"), "-C", str(root), "inputs"])
            run([nvkr, "run", "local", *authored, "--attempt", "wrong-version", "--attempts-root", attempts_root, "--ui", "plain"], root / "wrong-version-build.log", 2400)
            wrong_artifact = root / "runs/wrong-version/nvkectl/out/nvkectl/nvkectl"
            # Real recipe-built executable, deliberately tested against the original version binding.
            _, code = invoke(root, wrong_artifact, plan, "contract", "wrong-version", expected=1)
            assert code == 2, code
            negatives["wrongVersion"] = {"exit": code, "recipeArtifactSha256": sha(wrong_artifact), "successResult": False}
            broken = root / "broken"
            broken.write_text("#!/bin/sh\nexit 7\n")
            broken.chmod(0o555)
            _, code = invoke(root, broken, plan, "contract", "broken", expected=1)
            assert code == 2, code
            negatives["broken"] = {"exit": code, "successResult": False}
            # The old maintained revision must fail at import, before acquisition/execution.
            run([nvkr, "catalog", "pin", "--dir", str(root / "inputs/catalog"), "--commit", OLD_SOURCE, "nvke-cli"], root / "old-pin.log")
            old_plan = root / "old-plan.json"
            run([nvkr, "run", "plan", *authored, "--json"], old_plan)
            run(["tar", "-czf", str(root / "old-inputs.tgz"), "-C", str(root), "inputs"])
            _, code = invoke(root, artifact, old_plan, "contract", "missing-interface", expected=1)
            assert code == 1
            negatives["missingInterface"] = {"exit": code, "commit": OLD_SOURCE, "successResult": False}
            assert sha(artifact) == before
            write(root / "negative-results.json", negatives)
    finally:
        if registry:
            subprocess.run(["docker", "rm", "-f", "nvkr-cli-diagnostic-registry"], check=False, timeout=30)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--mode", choices=("scalar", "map", "relocated"), default="scalar")
    parser.add_argument("--cancel", choices=("timeout", "sigterm", "sigint"))
    parser.add_argument("--suite-worker", choices=("contract", "cli"))
    parser.add_argument("--artifact", type=Path)
    parser.add_argument("--plan", type=Path)
    options = parser.parse_args()
    if options.cancel:
        sys.exit(cancellation(options))
    if options.suite_worker:
        sys.exit(suite_worker(options))
    main(options)
