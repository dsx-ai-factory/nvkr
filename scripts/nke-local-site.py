# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Install a private NKE site from completed local NKD outputs, without rebuilding.

Integration development step, not the complete tenant/workload demo. Run in
native ARM64 Linux/Colima. Supply an already running private NICoSim endpoint.
The diagnostic site uses a fresh local CA and ephemeral credentials; these are
not production trust or a signed Release. NKE owns format/render/install checks.
"""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import stat
import sys
import time
import urllib.parse
import urllib.request

import importlib.util


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


MGMT = module("nkd_management", "nke-local-management.py")
CHART = MGMT.CHART
VM = MGMT.VM
REGISTRY_IMAGE = "docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
REGISTRY_PORT = 15501
CHART_OUTPUTS = {"controller": "controller-chart", "nktp": "nktp",
                 "postgresql": "postgresql", "spire": "spire", "spire-crds": "spire-crds", "temporal": "temporal"}
RUNTIME_IMAGES = ("csi-node-driver-registrar", "postgresql", "spiffe-csi", "spire-agent", "spire-bash",
                  "spire-controller-manager", "spire-oidc", "spire-server", "spire-tpm-server",
                  "temporal-admin-tools", "temporal-server", "temporal-ui")
IMAGE_KEYS = ("cwe", "profiles", "provider", *("runtime-" + name for name in RUNTIME_IMAGES))
TEMPORAL_TAGS = {"temporal-server": "1.30.3", "temporal-admin-tools": "1.30.3", "temporal-ui": "2.48.1"}
# Exact enabled roles from the pinned charts rendered with NKE's values. Bind
# roles as well as references, so one correct replica cannot mask a substitute.
# Each tuple is namespace, release instance, application, required pod roles;
# a role is container kind, container name, optional application component.
RUNTIME_ROLES = {
    "csi-node-driver-registrar": ("spire-system", "spire", "spiffe-csi-driver", (("containers", "node-driver-registrar", None),)),
    "postgresql": ("postgres", "postgres", "postgresql", (("containers", "postgresql", "primary"),)),
    "spiffe-csi": ("spire-system", "spire", "spiffe-csi-driver", (("containers", "spiffe-csi-driver", None),)),
    "spire-agent": ("spire-system", "spire", "agent", (("containers", "spire-agent", None),)),
    "spire-bash": ("spire-system", "spire", "agent", (("initContainers", "ensure-alternate-names", None), ("initContainers", "fsgroupfix", None))),
    "spire-controller-manager": ("spire-server", "spire", "server", (("containers", "spire-controller-manager", None),)),
    "spire-oidc": ("spire-server", "spire", "spiffe-oidc-discovery-provider", (("containers", "spiffe-oidc-discovery-provider", None),)),
    "spire-server": ("spire-server", "spire", "server", (("containers", "spire-server", None),)),
    "spire-tpm-server": ("spire-server", "spire", "server", (("initContainers", "init-tpm-direct", None),)),
    "temporal-admin-tools": ("temporal", "temporal", "temporal", (("containers", "admin-tools", "admintools"),)),
    "temporal-server": ("temporal", "temporal", "temporal", tuple(("containers", "temporal-" + name, name)
        for name in ("frontend", "history", "matching", "worker"))),
    "temporal-ui": ("temporal", "temporal", "temporal", (("containers", "temporal-web", "web"),))}
# Schema Job pods may have expired by site registration. If still present, every
# schema container must consume the accepted admin image and finish successfully.
SCHEMA_ROLES = (("containers", "done", "database"), *( ("initContainers", operation + "-" + store + "-store", "database")
    for operation in ("create", "setup", "update") for store in ("default", "visibility")))
REGISTRY_BYTES = 1024**3


def selection(root, name, kind):
    root = Path(root).resolve(strict=True)
    observed = VM.complete_output(root, name, kind)
    layout = root / "packages" / name
    if (layout.parent.is_symlink() or layout.is_symlink() or not layout.is_dir() or
            not layout.resolve(strict=True).is_relative_to(root) or
            any(path.is_symlink() for path in layout.rglob("*"))):
        raise VM.SmokeError("require a regular completed producer layout: " + name)
    index = layout / "index.json"
    if index.stat().st_size > 8 * 1024**2:
        raise VM.SmokeError("producer index exceeds diagnostic bound")
    if [row.get("digest") for row in json.loads(index.read_text()).get("manifests", [])] != [observed["digest"]]:
        raise VM.SmokeError("selected layout does not match its completed Run: " + name)
    return layout, observed


def endpoint(value):
    parsed = urllib.parse.urlsplit(value)
    try:
        address = ipaddress.ip_address(parsed.hostname)
    except ValueError as err:
        raise VM.SmokeError("NICoSim endpoint must use an explicit private IPv4 address") from err
    if (parsed.scheme != "http" or parsed.username or parsed.password or parsed.path not in ("", "/") or
            parsed.query or parsed.fragment or address.version != 4 or not address.is_private or
            address.is_loopback or address.is_unspecified or parsed.port != 18089):
        raise VM.SmokeError("require private non-loopback NICoSim HTTP endpoint on port 18089")
    return value.rstrip("/")


def admit_materials(args):
    env = MGMT.native_environment()
    MGMT.admit(args, env)  # Complete host/tools/source admission before mutation.
    if shutil.which("openssl") is None:
        raise VM.SmokeError("native OpenSSL is required before creating a diagnostic site")
    for name in ("bash", "curl"):
        if shutil.which(name) is None:
            raise VM.SmokeError("missing native site prerequisite: " + name)
    outputs = {}
    for item, name in CHART_OUTPUTS.items():
        outputs[item] = selection(args.site_run if item in ("controller", "nktp") else args.components_run,
                                  name, "helm-oci")
    outputs["cwe"] = selection(args.site_run, "cwe", "oci")
    outputs["profiles"] = selection(args.profiles_run, "controller-profiles", "oci")
    outputs["provider"] = selection(args.provider_run, "capnico-controller-arm64", "oci")
    outputs["nkectl"] = selection(args.site_run, "nkectl", "executable")
    for key in RUNTIME_IMAGES:
        # PostgreSQL's chart and image are independent named outputs.
        outputs["runtime-" + key] = selection(args.components_run, key + "-image-arm64", "oci")
    registry_capacity(outputs)
    parent = Path(args.out).absolute().parent.resolve(strict=True)
    for layout, _ in outputs.values():
        if parent.is_relative_to(layout.parent.parent):
            raise VM.SmokeError("site diagnostic must be outside accepted Runs")
    image = json.loads(MGMT.inspect(["docker", "image", "inspect", REGISTRY_IMAGE], "cached registry service", env))
    if len(image) != 1 or image[0].get("Architecture") != "arm64":
        raise VM.SmokeError("pull the exact ARM64 registry service before site execution")
    network = json.loads(MGMT.inspect(["docker", "network", "inspect", "kind"], "existing management network", env))
    if len(network) != 1 or network[0].get("Driver") != "bridge":
        raise VM.SmokeError("require the existing Kind bridge network")
    gateways = [row["Gateway"] for row in network[0]["IPAM"]["Config"]
                if ipaddress.ip_address(row["Gateway"]).version == 4]
    if len(gateways) != 1 or not ipaddress.ip_address(gateways[0]).is_private:
        raise VM.SmokeError("ambiguous private Kind IPv4 gateway")
    with socket.socket() as bound:
        bound.bind((gateways[0], REGISTRY_PORT))
    return outputs, gateways[0], image[0]["Id"]


def registry_capacity(outputs):
    # Sizing only, not OCI verification. ORAS and the registry own blob/digest
    # validation. Allow one in-flight duplicate blob plus bounded metadata.
    blobs = {}
    for key in IMAGE_KEYS:
        for path in (outputs[key][0] / "blobs" / "sha256").iterdir():
            value = path.lstat()
            if not stat.S_ISREG(value.st_mode) or value.st_size > REGISTRY_BYTES:
                raise VM.SmokeError("require bounded regular registry blobs: " + key)
            if path.name in blobs and blobs[path.name] != value.st_size:
                raise VM.SmokeError("conflicting registry blob sizes")
            blobs[path.name] = value.st_size
            if len(blobs) > 4096:
                raise VM.SmokeError("registry blob count exceeds diagnostic bound")
    required = sum(blobs.values()) + max(blobs.values(), default=0) + 64 * 1024**2
    if required > REGISTRY_BYTES:
        raise VM.SmokeError("local registry capacity exceeded before creation: %d/%d bytes" % (required, REGISTRY_BYTES))
    return required


def runtime_values(host, outputs):
    # These keys/syntax belong to the exact prepared SPIRE 0.30.1, PostgreSQL
    # 18.5.15 and Temporal 0.74.0 charts, exercised with their actual Helm loader.
    # Never substitute an upstream parent index for an observed native manifest.
    def spiffe(key):
        return {"registry": host, "repository": "nkd-local/" + key, "tag": outputs["runtime-" + key][1]["digest"]}
    def temporal(key):
        return {"image": {"repository": host + "/nkd-local/" + key,
                          "tag": TEMPORAL_TAGS[key] + "@" + outputs["runtime-" + key][1]["digest"]}}
    return {
        "spire": {
            "spire-server": {"image": spiffe("spire-server"),
                "controllerManager": {"image": spiffe("spire-controller-manager")},
                "nodeAttestor": {"tpmDirect": {"image": spiffe("spire-tpm-server")}},
                "tests": {"bash": {"image": spiffe("spire-bash")}}},
            "spire-agent": {"image": spiffe("spire-agent"),
                            "socketAlternate": {"image": spiffe("spire-bash")},
                            "fsGroupFix": {"image": spiffe("spire-bash")}},
            "spiffe-csi-driver": {"image": spiffe("spiffe-csi"),
                                  "nodeDriverRegistrar": {"image": spiffe("csi-node-driver-registrar")}},
            "spiffe-oidc-discovery-provider": {"image": spiffe("spire-oidc"),
                                               "tests": {"bash": {"image": spiffe("spire-bash")}}}},
        "postgresql": {"global": {"security": {"allowInsecureImages": True}},
                       "image": {"registry": host, "repository": "nkd-local/postgresql",
                                 "digest": outputs["runtime-postgresql"][1]["digest"]}},
        "temporal": {"server": temporal("temporal-server"),
                     "admintools": temporal("temporal-admin-tools"),
                     "web": temporal("temporal-ui")}}


def prepare_runtime_values(checkout, deploy, host, outputs, runner):
    # A narrow projection of an already validated source-owned format, not a
    # YAML parser. Revalidate every field through NKE's existing strict reader.
    reader = checkout / "deploy/lib/nke-deploy-items.py"
    for item, values in sorted(runtime_values(host, outputs).items()):
        release = deploy / item / "release.yaml"
        before = CHART.release_fields(reader, release, runner)
        lines = release.read_text().splitlines()
        starts = [index for index, line in enumerate(lines) if line == "values:"]
        if len(starts) != 1 or not before[7]:
            raise VM.SmokeError("runtime overrides require the inspected item values form: " + item)
        end = next((index for index in range(starts[0] + 1, len(lines))
                    if lines[index] and not lines[index].startswith((" ", "#"))), len(lines))
        filename = "nkd-image-inputs.json"
        with (release.parent / filename).open("x") as held:
            json.dump(values, held, sort_keys=True, indent=2)
        lines.insert(end, "  - " + filename)
        release.write_text("\n".join(lines) + "\n")
        expected = before[:]
        expected[7] += "," + filename
        if CHART.release_fields(reader, release, runner) != expected:
            raise VM.SmokeError("runtime override changed other NKE deploy semantics: " + item)


def admit(args):
    outputs, gateway, registry = admit_materials(args)
    if not Path(args.nico_token_file).is_file() or Path(args.nico_token_file).is_symlink():
        raise VM.SmokeError("NICo token must be a private regular file")
    token_path = Path(args.nico_token_file)
    if token_path.stat().st_mode & 0o077 or token_path.stat().st_size > 4096:
        raise VM.SmokeError("NICo token file must be private and no larger than 4 KiB")
    token = token_path.read_text().strip()
    if not token or "\n" in token:
        raise VM.SmokeError("NICo token must be one nonempty scalar")
    nico = endpoint(args.nico_endpoint)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(nico + "/healthz", timeout=3) as response:
        if json.loads(response.read(4097)) != {"status": "ok"}:
            raise VM.SmokeError("NICoSim health interface differs")
    return outputs, token, nico, gateway, registry


def registry_cleanup(name, owner, image_id, work, env):
    ids = MGMT.inspect(["docker", "ps", "-aq", "--no-trunc", "--filter", "name=^/" + name + "$"],
                       "private registry inventory", env).split()
    if not ids:
        return
    rows = json.loads(MGMT.inspect(["docker", "inspect", *ids], "private registry ownership", env))
    if (len(rows) != 1 or rows[0].get("Id") != ids[0] or rows[0].get("Name") != "/" + name or
            rows[0].get("Image") != image_id or
            rows[0].get("Config", {}).get("Labels", {}).get("dev.nkd.demo.owner") != owner or
            any(row.get("Type") in ("volume", "bind") for row in rows[0].get("Mounts", []))):
        raise VM.SmokeError("private registry ownership changed; refusing removal")
    MGMT.execute(["docker", "rm", "--force", ids[0]], "registry-cleanup", work, env, 60, diagnostic_optional=True)
    if MGMT.inspect(["docker", "ps", "-aq", "--filter", "name=^/" + name + "$"], "registry disappearance", env).split():
        raise VM.SmokeError("private registry remains")


def wait_registry(name, host, work, env):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        rows = json.loads(MGMT.inspect(["docker", "inspect", name], "registry startup state", env))
        if len(rows) != 1 or rows[0].get("State", {}).get("Running") is not True:
            break
        try:
            with opener.open("http://" + host + "/v2/", timeout=2) as response:
                if response.status == 200 and json.loads(response.read(4097)) == {}:
                    return
        except OSError:
            time.sleep(.2)
    try:
        MGMT.execute(["docker", "logs", "--tail", "100", name], "registry-startup-diagnostics", work, env, 30)
    except MGMT.UnjoinedProcess:
        raise
    except (VM.SmokeError, OSError):
        pass
    raise VM.SmokeError("registry failed its startup budget; private diagnostics retained")


def kind_nktp_values(checkout, work, env):
    # The NKE-owned Kind fixture supplies the chart's actual values interface.
    # Do not alter the produced chart or relax its Helm readiness gate.
    values = MGMT.capture(["git", "-C", str(checkout), "show",
                           CHART.INSTALLER_COMMIT + ":nktp/hack/e2e-values.yaml"],
                          "kind-nktp-values", work, env)
    if '  args: ["--kubelet-insecure-tls"]' not in values:
        raise VM.SmokeError("selected NKE Kind telemetry fixture differs")
    return values


def bootstrap_ca(work, env):
    MGMT.execute(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=nkd-local-demo",
                  "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign",
                  "-keyout", str(work / "ca.key"), "-out", str(work / "ca.crt")], "local-bootstrap-ca", work, env, 30)
    MGMT.execute(["openssl", "verify", "-x509_strict", "-CAfile", str(work / "ca.crt"), str(work / "ca.crt")],
                 "check-local-bootstrap-ca", work, env, 30)


def deployed_images(tools, work, env, refs):
    observed = {}
    for key, namespace, selector, field, container in (
            ("cwe", "nke-controller", "app.kubernetes.io/name=nke-controller", "containerStatuses", "nke-controller"),
            ("profiles", "nke-controller", "app.kubernetes.io/name=nke-controller", "initContainerStatuses", "materialize-profiles"),
            ("provider", "capnico-system", "control-plane=controller-manager", "containerStatuses", "manager")):
        rows = json.loads(MGMT.capture([str(tools / "kubectl"), "--kubeconfig", env["SITE_KUBECONFIG"],
                                       "-n", namespace, "get", "pods", "-l", selector, "-o", "json"],
                                      "deployed-" + key, work, env))
        pods = rows.get("items", [])
        states = [state for pod in pods for state in pod.get("status", {}).get(field, []) if state.get("name") == container]
        if (len(pods) != 1 or len(states) != 1 or states[0].get("imageID") != refs[key] or
                (field == "containerStatuses" and states[0].get("ready") is not True) or
                (field == "initContainerStatuses" and states[0].get("state", {}).get("terminated", {}).get("exitCode") != 0)):
            raise VM.SmokeError("deployed image does not match accepted manifest/readiness: " + key)
        observed[key] = {"podUID": pods[0]["metadata"]["uid"], "imageID": states[0]["imageID"]}
    observed.update(deployed_runtime_images(tools, work, env, refs))
    return observed


def deployed_runtime_images(tools, work, env, refs):
    # Inspect Kubernetes' structured result, not a parsed OCI reference. The
    # exact accepted spellings below are the ones our pinned charts render.
    rows = json.loads(MGMT.capture([str(tools / "kubectl"), "--kubeconfig", env["SITE_KUBECONFIG"],
                                   "get", "pods", "-A", "-o", "json"], "deployed-runtime-images", work, env))
    observed = {}
    for name in RUNTIME_IMAGES:
        key = "runtime-" + name
        repository, digest = refs[key].split("@", 1)
        spellings = {refs[key]}
        if name in TEMPORAL_TAGS:
            spellings.add(repository + ":" + TEMPORAL_TAGS[name] + "@" + digest)
        namespace, instance, application, required_roles = RUNTIME_ROLES[name]
        roles = (*required_roles, *SCHEMA_ROLES) if name == "temporal-admin-tools" else required_roles
        seen = set()
        members = []
        for pod in rows.get("items", []):
            metadata = pod.get("metadata", {})
            labels = metadata.get("labels", {})
            selected = (metadata.get("namespace") == namespace and labels.get("app.kubernetes.io/instance") == instance
                        and labels.get("app.kubernetes.io/name") == application)
            for field, status_field in (("containers", "containerStatuses"), ("initContainers", "initContainerStatuses")):
                for container in pod.get("spec", {}).get(field, []):
                    image = container.get("image", "")
                    matched = [role for role in roles if selected and role[0] == field and role[1] == container.get("name")
                               and (role[2] is None or role[2] == labels.get("app.kubernetes.io/component"))]
                    if not matched and not image.startswith((repository + "@", repository + ":")):
                        continue
                    states = [state for state in pod.get("status", {}).get(status_field, [])
                              if state.get("name") == container.get("name")]
                    if image not in spellings or len(states) != 1 or states[0].get("imageID") != refs[key]:
                        raise VM.SmokeError("deployed dependency does not match accepted native manifest: " + key)
                    state = states[0]
                    completed = state.get("state", {}).get("terminated", {}).get("exitCode") == 0
                    if status_field == "initContainerStatuses":
                        ready = completed
                    else:
                        ready = (state.get("ready") is True and "running" in state.get("state", {})) or (
                            completed and pod.get("status", {}).get("phase") == "Succeeded")
                    if not ready:
                        raise VM.SmokeError("deployed dependency is not ready or successfully completed: " + key)
                    seen.update(matched)
                    members.append({"podUID": pod["metadata"]["uid"], "namespace": pod["metadata"]["namespace"],
                                    "container": container["name"], "kind": field, "imageID": state["imageID"]})
        if not set(required_roles).issubset(seen):
            raise VM.SmokeError("missing enabled pod role for accepted dependency: " + key)
        observed[key] = {"status": "observed", "containers": sorted(members, key=lambda row: (
            row["namespace"], row["podUID"], row["kind"], row["container"]))}
    return observed


def copy_images(tools, work, env, host, name, outputs):
    refs = {}
    for key in IMAGE_KEYS:
        layout, observed = outputs[key]
        target = host + "/nkd-local/" + key.removeprefix("runtime-") + ":" + name
        MGMT.execute([str(tools / "oras"), "cp", "--concurrency", "1", "--from-oci-layout", str(layout) + "@" + observed["digest"],
                      target, "--to-plain-http"], "copy-" + key, work, env, 180)
        descriptor = json.loads(MGMT.inspect([str(tools / "oras"), "manifest", "fetch", "--plain-http", "--descriptor", target],
                                             "registry manifest readback", env))
        if descriptor.get("digest") != observed["digest"]:
            raise VM.SmokeError("registry changed observed manifest: " + key)
        refs[key] = target.rsplit(":", 1)[0] + "@" + observed["digest"]
    return refs


def consume(args, outputs, token, nico, gateway, registry_id, name, tools, work, env, continuation=None):
    node_ids = MGMT.owned_nodes(name, env)
    if len(node_ids) != 1:
        raise VM.SmokeError("site requires exactly the owned management node")
    node = node_ids[0]
    rows = json.loads(MGMT.inspect(["docker", "inspect", node], "site node address", env))
    vip = rows[0]["NetworkSettings"]["Networks"]["kind"]["IPAddress"]
    ipaddress.IPv4Address(vip)
    env = dict(env, STAGE_NEEDS_RELEASE="1", NODE_EXEC="docker exec {node}", PREPULL_EXTERNAL="0")
    (work / "site-selected-outputs.json").write_text(json.dumps(
        {"scope": "unqualified-local-site-diagnostic", "outputs": {key: row[1] for key, row in outputs.items()}},
        sort_keys=True, indent=2))
    print("Diagnostic exceptions: external charts/runtime images are acquisitions, not additional source builds or qualification.", flush=True)
    cli_root = work / "cli-publication"
    MGMT.execute([args.nkdctl, "publish", "root", "--bundle", "nkectl=" + str(outputs["nkectl"][0]),
                  "--out", str(cli_root)], "verify-site-cli", work, env, 120)
    cli_tools = work / "cli"
    MGMT.execute([args.nkdctl, "publish", "extract", "--layout", str(cli_root), "--bundle", "nkectl", "--out", str(cli_tools)],
                 "extract-site-cli", work, env, 120)
    if sorted(path.name for path in cli_tools.iterdir()) != ["nkectl"] or not os.access(cli_tools / "nkectl", os.X_OK):
        raise VM.SmokeError("require the exact executable site CLI member")
    env["PATH"] = str(cli_tools) + os.pathsep + env["PATH"]
    checkout = work / "installer"
    MGMT.execute(["git", "clone", "--no-hardlinks", "--no-checkout", args.nke_checkout, str(checkout)],
                 "clone-installer", work, env, 120)
    MGMT.execute(["git", "-C", str(checkout), "sparse-checkout", "set", "deploy", "make", "controller/deploy/charts",
                  "controller/deploy/k8s/capi-spiffe-exec-auth", "controller/hack/ci"],
                 "installer-membership", work, env, 60)
    MGMT.execute(["git", "-C", str(checkout), "checkout", "--detach", CHART.INSTALLER_COMMIT],
                 "checkout-installer", work, env, 60)
    chart_sequence = 0
    def chart_runner(argv, _label, timeout=30, text=True):
        nonlocal chart_sequence
        chart_sequence += 1
        return MGMT.capture(argv, "chart-input-%03d" % chart_sequence, work, env, timeout, text)
    deploy = CHART.prepare(checkout, args.nkdctl, {key: str(outputs[key][0]) for key in CHART_OUTPUTS},
                           {key: outputs[key][1]["digest"] for key in CHART_OUTPUTS}, work / "chart-inputs", chart_runner)
    env["DEPLOY_PATH"] = str(deploy)
    host = gateway + ":" + str(REGISTRY_PORT)
    prepare_runtime_values(checkout, deploy, host, outputs, chart_runner)
    registry_name = name + "-registry"
    if MGMT.inspect(["docker", "ps", "-aq", "--filter", "name=^/" + registry_name + "$"],
                    "fresh private registry identity", env).split():
        raise VM.SmokeError("fresh private registry identity already exists")
    cleanup_allowed = True
    try:
        MGMT.execute(["docker", "run", "-d", "--name", registry_name, "--label", "dev.nkd.demo.owner=" + name,
                      "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network", "kind",
                      "--tmpfs", "/var/lib/registry:rw,size=" + str(REGISTRY_BYTES), "--publish", host + ":5000", REGISTRY_IMAGE],
                     "start-registry", work, env, 60)
        wait_registry(registry_name, host, work, env)
        refs = copy_images(tools, work, env, host, name, outputs)
        host_config = work / "registry-hosts.toml"
        host_config.write_text('server = "http://' + host + '"\n[host."http://' + host + '"]\n  capabilities = ["pull", "resolve"]\n')
        certs = "/etc/containerd/certs.d/" + host
        MGMT.execute(["docker", "exec", node, "mkdir", "-p", certs], "node-registry-dir", work, env, 30)
        MGMT.execute(["docker", "cp", str(host_config), node + ":" + certs + "/hosts.toml"], "node-registry-config", work, env, 30)
        bootstrap_ca(work, env)
        config = {"site": {"name": name, "trustDomain": "nkd.local", "vip": vip, "arch": "arm64"},
                  "spire": {"caCertFile": str(work / "ca.crt"), "caKeyFile": str(work / "ca.key")},
                  "images": {"controller": refs["cwe"], "profiles": refs["profiles"]},
                  "nico": {"endpoint": nico, "orgId": args.nico_org, "token": token, "apiName": "forge"},
                  "registry": {"server": host, "username": "local-diagnostic", "token": secrets.token_hex(24)},
                  "tenant": {"user": "nkd-demo", "password": secrets.token_hex(24)}}
        site_config = work / "site-config.yaml"
        telemetry = kind_nktp_values(checkout, work, env)
        site_config.write_text("".join(section + ":\n" + "".join("  " + key + ": " + json.dumps(value) + "\n"
                                                                   for key, value in values.items()) for section, values in config.items()) +
                               "nktp:\n" + "".join("  " + line + "\n" for line in telemetry.splitlines()))
        env["SITE_CONFIG"] = str(site_config)
        manifest = work / "local-assembly.yaml"
        manifest.write_text("apiVersion: nke.nvidia.com/release/v1\nrelease: local-assembly\nlane: local-assembly\nscope: none\n"
                            "source:\n  repo: https://github.com/dsx-ai-factory/nke\n  tag: local-assembly\n  commit: " +
                            CHART.INSTALLER_COMMIT + "\nitems:\n  - {name: controller, type: image, ref: " + refs["cwe"] + "}\n")
        env["RELEASE_FILE"] = str(manifest)
        env["CAPNICO_IMAGE"] = refs["provider"].split("@", 1)[0] + ":" + name
        stages = checkout / "deploy/stages"
        env["API_CLIENT_CIDRS"] = gateway + "/32"
        for phase in ("00-check-readiness", "01-prepare-cluster", "02-stage-images", "03-describe-site",
                      "05-install-site-controller", "06-secure-and-register"):
            stage = stages / (phase + ".sh")
            MGMT.execute(["bash", str(stage), "run"], "site-" + phase, work, env, 1800)
            MGMT.execute(["bash", str(stage), "check"], "check-" + phase, work, env, 180)
        images = deployed_images(tools, work, env, refs)
        (work / "site-inputs.json").write_text(json.dumps({"scope": "unqualified-local-site-diagnostic", "refs": refs,
            "deployed": images,
            "outputs": {key: row[1] for key, row in outputs.items()}, "charts": {key: VM.digest(deploy / "charts" / (key + ".tgz"))
                                                                                  for key in CHART_OUTPUTS}}, sort_keys=True, indent=2))
        if continuation is not None:
            # Trusted deploy-only continuation, not a caller-selected command.
            # Keep the registry and management node alive through tenant work.
            continuation(name, tools, work, env)
    except (MGMT.UnjoinedProcess, MGMT.RetainedControls):
        cleanup_allowed = False
        raise
    finally:
        with VM.cleanup_signals():
            if cleanup_allowed:
                with MGMT.cleanup_diagnostics():
                    registry_cleanup(registry_name, name, registry_id, work, env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("nke-checkout", "nkdctl", "tools-run", "site-run", "components-run", "profiles-run", "provider-run",
                 "nico-endpoint", "nico-token-file", "out"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--nico-org", default="novxwn552ssc")
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise VM.SmokeError("site diagnostic interrupted")
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        outputs, token, nico, gateway, registry_id = admit(args)
        MGMT.probe(args, lambda name, tools, work, env: consume(args, outputs, token, nico, gateway, registry_id, name, tools, work, env))
        print("PASS: local NKE site install/check, registration and owned cleanup; tenant/workload not exercised.")
    except (VM.SmokeError, CHART.HandoffError, OSError, ValueError, KeyError, TypeError) as err:
        print("NKD-E-PREFLIGHT-FAILED: site integration: " + str(err), file=sys.stderr)
        return 17
    return 0


if __name__ == "__main__":
    sys.exit(main())
