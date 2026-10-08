# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Consume NKD-built tools for a private NKE management-cluster diagnostic.

No artifact rebuild, CAPD tenant, Release authority or full-demo claim. Kind
hosts management only. NKE owns the pinned Cluster API install/check scripts.
Run inside the dedicated Linux ARM64 Colima host, not directly on macOS.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import time
import uuid


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


VM = module("nkd_vm_probe", "nke-nicosim-vm-smoke.py")
CHART = module("nkd_chart_inputs", "nke-prepare-chart-inputs.py")
BUNDLE = "installer-tools-arm64"
CONFIG_PATH = "controller/hack/cwe-capi-kind.yaml"
CONFIG_HASH = "ffef66a877102f4d6c69012b30ed3a3869bf530e654ba07144e39595979cbf3c"
NODE_IMAGE = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
NODE_ID = NODE_IMAGE.split("@", 1)[1]
SOCKET_MOUNT = "  extraMounts:\n    - hostPath: /var/run/docker.sock\n      containerPath: /var/run/docker.sock\n"
MEMBERS = ["clusterctl", "helm", "kind", "kubectl", "oras"]
VERSIONS_HASH = "0a4a74197a07a862faf2d536d8565bf8cce3cbc50b72d50bc7c0a67514af2f64"
SOURCE_FILES = ["deploy/stages/04-install-cluster-api.sh", "deploy/lib.sh", "make/tool-versions.mk"]


class UnjoinedProcess(VM.SmokeError, CHART.UnjoinedCommand):
    """Cleanup cannot race a producer whose process group may still run."""


def native_environment():
    # Do not inherit a different Docker context, kubeconfig, Kind provider,
    # clusterctl version override, release selection or user-site credentials.
    env = {key: os.environ[key] for key in ("PATH", "USER", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY")
           if key in os.environ}
    env.update({"DOCKER_HOST": "unix:///var/run/docker.sock", "KIND_EXPERIMENTAL_PROVIDER": "docker",
                "CLUSTERCTL_DISABLE_VERSIONCHECK": "true"})
    return env


def inspect(argv, label, env):
    try:
        result = subprocess.run(argv, capture_output=True, text=True, timeout=30, check=False, env=env)
    except (OSError, subprocess.TimeoutExpired) as err:
        raise VM.SmokeError(f"{label}: unavailable or timed out") from err
    if result.returncode or len(result.stdout) > 8 * 1024 * 1024:
        raise VM.SmokeError(f"{label}: failed or exceeds diagnostic bound")
    return result.stdout


def admit(args, env):
    if platform.system() != "Linux" or platform.machine() != "aarch64" or sys.version_info < (3, 12):
        raise VM.SmokeError("require native Linux ARM64 and Python 3.12+ inside the dedicated Colima VM")
    if not shutil.which("docker") or not shutil.which("git"):
        raise VM.SmokeError("native Docker and Git are required")
    checkout = Path(args.nke_checkout).resolve(strict=True)
    tool = Path(args.nkdctl).resolve(strict=True)
    root = Path(args.tools_run).resolve(strict=True)
    destination = Path(args.out).absolute()
    if destination.exists() or destination.is_symlink():
        raise VM.SmokeError("choose a fresh private output directory")
    destination = destination.parent.resolve(strict=True) / destination.name
    if destination.is_relative_to(checkout) or destination.is_relative_to(root):
        raise VM.SmokeError("diagnostic output must be outside source and accepted Run")
    if not os.access(tool, os.X_OK):
        raise VM.SmokeError("require an already built native nkdctl")
    if inspect(["git", "-C", str(checkout), "rev-parse", "HEAD"], "installer pin", env).strip() != CHART.INSTALLER_COMMIT:
        raise VM.SmokeError("require the exact selected NKE installer commit")
    if inspect(["git", "-C", str(checkout), "status", "--porcelain", "--untracked-files=all"], "clean installer", env):
        raise VM.SmokeError("installer checkout must be clean")
    config = inspect(["git", "-C", str(checkout), "show", f"{CHART.INSTALLER_COMMIT}:{CONFIG_PATH}"],
                     "source-owned Kind configuration", env)
    if hashlib.sha256(config.encode()).hexdigest() != CONFIG_HASH or config.count(SOCKET_MOUNT) != 1:
        raise VM.SmokeError("Kind fixture differs from the inspected source interface")
    # This consumer has no CAPD tenant. Remove only the guarded Docker socket
    # mount; retain the source node pin, dual-stack and image-volume settings.
    config = config.replace(SOCKET_MOUNT, "")
    observed = VM.complete_output(root, BUNDLE, "executable")
    layout = root / "packages" / BUNDLE
    if not layout.is_dir() or any(path.is_symlink() for path in layout.rglob("*")):
        raise VM.SmokeError("require a regular producer OCI layout")
    if (layout / "index.json").stat().st_size > 8 * 1024 * 1024:
        raise VM.SmokeError("tool OCI index exceeds the diagnostic bound")
    descriptors = json.loads((layout / "index.json").read_text()).get("manifests", [])
    if layout.is_symlink() or len(descriptors) != 1 or descriptors[0].get("digest") != observed["digest"]:
        raise VM.SmokeError("tool layout is not the selected observed manifest")
    docker = json.loads(inspect(["docker", "info", "--format", "{{json .}}"], "native Docker admission", env))
    if (docker.get("OSType") != "linux" or docker.get("Architecture") not in ("aarch64", "arm64") or
            docker.get("NCPU", 0) < 8 or docker.get("MemTotal", 0) < 20 * 1024**3):
        raise VM.SmokeError("management diagnostic requires ARM64 Docker, 8 CPUs and 20 GiB RAM")
    if shutil.disk_usage(destination.parent).free < 20 * 1024**3:
        raise VM.SmokeError("management diagnostic requires 20 GiB free on the native output volume")
    image = json.loads(inspect(["docker", "image", "inspect", NODE_IMAGE], "cached pinned Kind image", env))
    if len(image) != 1 or image[0].get("Id") != NODE_ID or image[0].get("Architecture") != "arm64":
        raise VM.SmokeError("pull the exact source-pinned ARM64 Kind image before running this diagnostic")
    # Execute a private projection of tracked Git bytes, not working-tree
    # files that skip-worktree/assume-unchanged could hide from status.
    source = {path: inspect(["git", "-C", str(checkout), "show", f"{CHART.INSTALLER_COMMIT}:{path}"],
                            "pinned management-stage source", env) for path in SOURCE_FILES}
    if hashlib.sha256(source["make/tool-versions.mk"].encode()).hexdigest() != VERSIONS_HASH:
        raise VM.SmokeError("installer tool pins differ from the inspected interface")
    return checkout, tool, root, layout, destination, config, observed, source


def group_alive(group):
    try:
        os.killpg(group, 0)
        return True
    except ProcessLookupError:
        return False


def stop_group(process, grace=40, kill_grace=5):
    # A shell may have exited while one of its descendants still holds the
    # session. Signalling only a live leader would leave that child running.
    deadline = time.monotonic() + grace
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=grace)
    except subprocess.TimeoutExpired:
        pass
    while group_alive(process.pid) and time.monotonic() < deadline:
        time.sleep(.05)
    if group_alive(process.pid):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + kill_grace
        process.wait(timeout=kill_grace)
        while group_alive(process.pid) and time.monotonic() < deadline:
            time.sleep(.05)
    else:
        process.wait(timeout=kill_grace)
    if group_alive(process.pid):
        raise UnjoinedProcess("owned process group remains after stop/join")


def execute(argv, phase, work, env, timeout, diagnostic_optional=False, output=None):
    """Own and join the entire subprocess group, including on interruption."""
    print("Management phase: " + phase, flush=True)
    try:
        log = (work / (phase + ".log")).open("wb")
    except OSError:
        if not diagnostic_optional:
            raise
        print("Private cleanup log unavailable; exact owned-node cleanup still proceeds", file=sys.stderr)
        log = open(os.devnull, "wb")
    with log:
        process = subprocess.Popen(argv, stdout=log if output is None else output,
                                   stderr=subprocess.STDOUT if output is None else log, env=env,
                                   cwd=work, start_new_session=True)
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired as err:
            raise VM.SmokeError(f"{phase}: timed out; private phase log retained") from err
        finally:
            try:
                stop_group(process)
            except (VM.SmokeError, OSError, subprocess.TimeoutExpired) as err:
                raise UnjoinedProcess(f"{phase}: stop/join failed; refusing concurrent cleanup") from err
    if process.returncode:
        raise VM.SmokeError(f"{phase}: failed (exit {process.returncode}); private phase log retained", process.returncode)


def capture(argv, phase, work, env, timeout=30, text=True, volatile=False):
    """Capture a bounded private result with the same stop/join ownership."""
    if volatile:
        # Native Linux cleanup must not require writable diagnostic storage.
        # memfd is a stdlib-managed anonymous RAM file, not a release receipt.
        if not hasattr(os, "memfd_create"):
            raise VM.SmokeError("volatile cleanup capture requires native Linux")
        with os.fdopen(os.memfd_create("nkd-cleanup", os.MFD_CLOEXEC), "w+b") as output:
            execute(argv, phase, work, env, timeout, diagnostic_optional=True, output=output)
            if os.fstat(output.fileno()).st_size > 8 * 1024**2:
                raise VM.SmokeError(phase + ": result exceeds diagnostic bound")
            output.seek(0)
            raw = output.read(8 * 1024**2)
        return raw.decode() if text else raw
    result = work / (phase + ".stdout")
    with result.open("xb") as output:
        execute(argv, phase, work, env, timeout, output=output)
    if result.stat().st_size > 8 * 1024**2:
        raise VM.SmokeError(phase + ": result exceeds diagnostic bound")
    raw = result.read_bytes()
    return raw.decode() if text else raw


def owned_nodes(name, env):
    ids = inspect(["docker", "ps", "-aq", "--filter", "label=io.x-k8s.kind.cluster=" + name],
                  "owned management-node inventory", env).split()
    if not ids:
        return []
    rows = json.loads(inspect(["docker", "inspect", *ids], "owned management-node descriptions", env))
    if len(rows) != len(ids):
        raise VM.SmokeError("incomplete management-node ownership description")
    for row in rows:
        labels = row.get("Config", {}).get("Labels", {})
        if (labels.get("io.x-k8s.kind.cluster") != name or labels.get("io.x-k8s.kind.role") != "control-plane" or
                row.get("Image") != NODE_ID or row.get("Name") != "/" + name + "-control-plane"):
            raise VM.SmokeError("management-node ownership changed; refusing cleanup")
    return sorted(row["Id"] for row in rows)


def volumes_for_nodes(ids, env, existing):
    rows = json.loads(inspect(["docker", "inspect", *ids], "node-volume descriptions", env))
    volumes = []
    for row in rows:
        mounts = [item for item in row.get("Mounts", []) if item.get("Type") == "volume"]
        if (row.get("Id") not in ids or len(mounts) != 1 or mounts[0].get("Destination") != "/var" or
                mounts[0].get("Driver") != "local" or mounts[0].get("RW") is not True or
                "/var" not in row.get("Config", {}).get("Volumes", {})):
            raise VM.SmokeError("unexpected management-node volume; refusing volume cleanup")
        volume = mounts[0]["Name"]
        if volume in existing or volume in volumes:
            raise VM.SmokeError("management volume predates this attempt or is shared")
        description = json.loads(inspect(["docker", "volume", "inspect", volume], "anonymous-volume ownership", env))
        if (len(description) != 1 or description[0].get("Name") != volume or description[0].get("Driver") != "local" or
                "com.docker.volume.anonymous" not in (description[0].get("Labels") or {})):
            raise VM.SmokeError("management volume is not a Docker-issued anonymous local volume")
        consumers = inspect(["docker", "ps", "-aq", "--no-trunc", "--filter", "volume=" + volume],
                            "anonymous-volume consumers", env).split()
        if consumers != [row["Id"]]:
            raise VM.SmokeError("management volume has foreign consumers")
        volumes.append(volume)
    if len(rows) != len(ids):
        raise VM.SmokeError("incomplete node-volume descriptions")
    return sorted(volumes)


def cleanup(name, env, work, existing_volumes):
    # Only after Kind's process group is stopped/joined. A fresh random name
    # was proven absent before create. Discover partial-create nodes as well.
    ids = owned_nodes(name, env)
    if ids:
        volumes = volumes_for_nodes(ids, env, existing_volumes)
        try:
            (work / "node-ownership.json").write_text(json.dumps({"cluster": name, "nodeIDs": ids, "volumes": volumes},
                                                               indent=2, sort_keys=True))
        except OSError:
            print("Private ownership log unavailable; exact owned-node cleanup still proceeds", file=sys.stderr)
        execute(["docker", "rm", "--force", "--volumes", *ids], "owned-node-cleanup", work, env, 60,
                diagnostic_optional=True)
        remaining = set(inspect(["docker", "volume", "ls", "-q"], "post-cleanup volumes", env).split())
        if remaining.intersection(volumes):
            raise VM.SmokeError("owned anonymous volumes remain after cleanup")
    if owned_nodes(name, env):
        raise VM.SmokeError("owned management nodes remain after cleanup")


def verify_versions(tools, work, env):
    # Values bind to the exact guarded NKE tool-versions.mk above. This is an
    # observed executable interface, not an authenticated Release receipt.
    for name, arguments, expected in (("kind", ["version", "-q"], "0.32.0"),
                                      ("clusterctl", ["version", "-o", "short"], "v1.12.3"),
                                      ("helm", ["version", "--template", "{{.Version}}"], "v4.2.4")):
        execute([str(tools / name), *arguments], "version-" + name, work, env, 30)
        if (work / ("version-" + name + ".log")).read_text().strip() != expected:
            raise VM.SmokeError("built " + name + " does not match the pinned installer version")
    execute([str(tools / "kubectl"), "version", "--client", "-o", "json"], "version-kubectl", work, env, 30)
    kubectl = json.loads((work / "version-kubectl.log").read_text())["clientVersion"]
    if kubectl.get("gitVersion") != "v1.36.0" or kubectl.get("platform") != "linux/arm64":
        raise VM.SmokeError("built kubectl version/platform does not match the installer")
    execute([str(tools / "oras"), "version"], "version-oras", work, env, 30)
    lines = (work / "version-oras.log").read_text().splitlines()
    # ORAS 1.3.3 exposes only its source-owned text version interface.
    if (sum(line.split() == ["Version:", "1.3.3"] for line in lines) != 1 or
            sum(line.split() == ["OS/Arch:", "linux/arm64"] for line in lines) != 1):
        raise VM.SmokeError("built ORAS version/platform does not match the installer")


def probe(args, consumer=None):
    env = native_environment()
    checkout, tool, root, layout, work, config, observed, source = admit(args, env)
    name = "nker-demo-" + uuid.uuid4().hex[:16]
    if owned_nodes(name, env):
        raise VM.SmokeError("fresh management identity unexpectedly exists")
    work.mkdir(mode=0o700)
    (work / "home").mkdir(mode=0o700)
    env["HOME"] = str(work / "home")
    os.umask(0o077)
    execute([str(tool), "publish", "root", "--bundle", BUNDLE + "=" + str(layout),
             "--out", str(work / "publication")], "verify-tools", work, env, 120)
    tools = work / "tools"
    execute([str(tool), "publish", "extract", "--layout", str(work / "publication"),
             "--bundle", BUNDLE, "--out", str(tools)], "extract-tools", work, env, 120)
    if sorted(path.name for path in tools.iterdir()) != MEMBERS or any(
            path.is_symlink() or not path.is_file() or not os.access(path, os.X_OK) for path in tools.iterdir()):
        raise VM.SmokeError("require exactly the five executable installer-tool members")
    env["PATH"] = str(tools) + os.pathsep + env.get("PATH", os.defpath)
    verify_versions(tools, work, env)
    env.update({"SITE_KUBECONFIG": str(work / "kubeconfig"), "OUT_DIR": str(work / "site-install"),
                "STAGE_NEEDS_RELEASE": "0", "PHASE_TIMEOUT": "10m", "REQUIRE_TIMEOUT": "600s"})
    (work / "kind.yaml").write_text(config)
    existing_volumes = set(inspect(["docker", "volume", "ls", "-q"], "initial volume inventory", env).split())
    (work / "inputs.json").write_text(json.dumps({"scope": "unqualified-local-management-diagnostic",
        "cluster": name, "installerCommit": CHART.INSTALLER_COMMIT, "tools": observed,
        "toolMembers": {path.name: VM.digest(path) for path in sorted(tools.iterdir())},
        "nodeImage": NODE_IMAGE, "configSHA256": VM.digest(work / "kind.yaml"), "initialVolumes": sorted(existing_volumes),
        "sourceSHA256": {path: "sha256:" + hashlib.sha256(content.encode()).hexdigest()
                         for path, content in source.items()}}, indent=2, sort_keys=True))
    for relative, content in source.items():
        target = work / "source" / relative
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        target.write_text(content)
        target.chmod(0o400)
    stage = work / "source/deploy/stages/04-install-cluster-api.sh"
    cleanup_allowed = True
    try:
        execute([str(tools / "kind"), "create", "cluster", "--name", name, "--config", str(work / "kind.yaml"),
                 "--kubeconfig", env["SITE_KUBECONFIG"], "--wait", "5m"], "create-management", work, env, 600)
        if len(owned_nodes(name, env)) != 1:
            raise VM.SmokeError("expected exactly one owned management node")
        execute([str(tools / "kubectl"), "--kubeconfig", env["SITE_KUBECONFIG"], "wait", "nodes", "--all",
                 "--for=condition=Ready", "--timeout=180s"], "management-ready", work, env, 210)
        execute(["bash", str(stage), "run"], "install-capi", work, env, 1200)
        execute(["bash", str(stage), "check"], "check-capi", work, env, 180)
        execute([str(tools / "kubectl"), "--kubeconfig", env["SITE_KUBECONFIG"], "get", "pods", "-A", "-o", "json"],
                "management-pods", work, env, 30)
        if consumer is not None:
            # Trusted downstream adapter, not a caller-selected shell command.
            # Keep node ownership/process failure handling around the consumer.
            consumer(name, tools, work, env)
    except UnjoinedProcess:
        cleanup_allowed = False
        raise
    finally:
        previous = {number: signal.signal(number, signal.SIG_IGN) for number in (signal.SIGINT, signal.SIGTERM)}
        try:
            if cleanup_allowed:
                cleanup(name, env, work, existing_volumes)
        finally:
            for number, handler in previous.items():
                signal.signal(number, handler)
    if consumer is None:
        print("PASS: NKD-built tools, real management Kind/CAPI and owned-node cleanup.")
        print("NOT full demo: NKE site, CAPNICo/QCOW2 tenant and workload remain separate acceptance.")
    else:
        print("PASS: downstream consumer returned and exact management cleanup completed; no full-demo claim.")
    print("Private diagnostic directory: " + str(work))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("nke-checkout", "nkdctl", "tools-run", "out"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise VM.SmokeError("management diagnostic interrupted; cleaning owned nodes")
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        probe(args)
    except (VM.SmokeError, OSError, ValueError, KeyError, TypeError) as err:
        print(f"NKD-E-PREFLIGHT-FAILED: management diagnostic: {err}", file=sys.stderr)
        return 17
    return 0


if __name__ == "__main__":
    sys.exit(main())
