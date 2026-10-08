# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Deploy completed ARM64 NKD Runs into an owned real QCOW2 NKE tenant.

Native Linux/Colima, trusted single-user diagnostic only. No artifact builds,
CAPD tenants, mock backend, Release authority or qualified execution. Existing
NKD modules verify/extract materials; NKE owns installation and tenant requests.
"""

import argparse
from contextlib import contextmanager
import hashlib
import ipaddress
import json
import os
import pwd
from pathlib import Path
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import time
import urllib.request
import uuid
import xml.etree.ElementTree as ET
import importlib.util


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


SITE = module("nkd_full_site", "nke-local-site.py")
MGMT, VM = SITE.MGMT, SITE.VM
NET = module("nkd_full_network", "nke-local-network.py")
# Share the process/error owner, rather than importing a second class identity.
NET.MGMT, NET.VM = MGMT, VM
VIP = "172.31.0.10"
PROFILE = "nke-local-26-04-arm64-plain"
TYPE = "a1b2c3d4-0005-4000-8000-000000000003"
PREFIX = "a1b2c3d4-0004-4000-8000-000000000001"
SEED_SHA256 = "e713b6e74e884bfba5a942b10eee142171565dcec2f24c6217cd9baecea03cac"
LAPTOP_INSTANCE = ('  - id: "' + TYPE + '"\n'
    '    name: "cpu4-mem6gb-disk60gb"\n'
    '    site_id: "' + VM.SITE + '"\n'
    '    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n').encode()
WORKLOAD_INDEX = "sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"
# Exact linux/arm64/v8 manifest read from that OCI index using Docker's
# descriptor interface; never compare a runtime image ID with its parent index.
WORKLOAD_IMAGE = "docker.io/library/alpine@sha256:260479a1cfaf304c4c20da7f8405d3ce313513dcd534bb743257bdd2fe0f3e2d"


@contextmanager
def cleanup_signals():
    previous = {number: signal.signal(number, signal.SIG_IGN) for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


def tenant_request(name):
    # JSON is valid YAML. The exact built NKE CLI owns decoding/validation of
    # CreateClusterRequest; this is deployment intent, not NKD lifecycle input.
    return {"name": name, "controlPlaneKubernetesVersion": "1.34.2", "cniPlugin": "ovn-kubernetes",
        "cniVersion": "1.4.0", "etcdEncrypted": False, "controlPlaneVIP": VIP, "teleportEnabled": False,
        "nico": {"orgID": "novxwn552ssc", "siteID": VM.SITE, "vpcID": VM.VPC},
        "controlPlaneConfig": {"desiredCount": 1, "systemReservedMemory": "1Gi", "teleportEnabled": False,
            "provisioningMonitorDisabled": True,
            "nico": {"configProfile": "nico-vms-cp", "osProfile": PROFILE,
                     "instanceTypePreference": [TYPE], "vpcPrefixIDs": [PREFIX]}},
        "nodePools": [{"name": "worker", "nodeNamePrefix": "w", "desiredNodeCount": 1, "controlPlane": False,
            "systemReservedMemory": "1Gi", "kubeReservedMemory": "1Gi", "teleportEnabled": False,
            "autoProvisionDisabled": False, "autoModeDisabled": True, "provisioningMonitorDisabled": True,
            "recycleBudget": 0, "nico": {"configProfile": "nico-vms-worker", "osProfile": PROFILE,
                                       "instanceTypeID": TYPE, "vpcPrefixIDs": [PREFIX]}}]}


def write_nico_header(work, token):
    # curl's established @file interface keeps the per-attempt credential out
    # of process arguments and phase logs. The caller owns this private workdir.
    path = work / "nico-api-header"
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as held:
        held.write("Authorization: Bearer " + token + "\n")


def project_laptop_seed(simulator):
    # Not a YAML parser: admit the entire exact source fixture, then project one
    # known byte block into a separate runtime configuration. NICoSim's pinned
    # YAML decoder/seed owner still validates it. Accepted assets stay unchanged.
    source = simulator / "internal/seed/default-seed.yaml"
    with os.fdopen(os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as held:
        value = os.fstat(held.fileno())
        if (not stat.S_ISREG(value.st_mode) or value.st_uid != os.getuid() or value.st_size > 65536):
            raise VM.SmokeError("source seed is not a bounded regular owned file")
        raw = held.read(65537)
    if hashlib.sha256(raw).hexdigest() != SEED_SHA256 or raw.count(LAPTOP_INSTANCE) != 1:
        raise VM.SmokeError("source simulator seed differs from the inspected laptop interface")
    projected = raw.replace(LAPTOP_INSTANCE, LAPTOP_INSTANCE.replace(b"mem6gb", b"mem8gb").replace(
        b"memory_gib: 6", b"memory_gib: 8"))
    path = simulator / "laptop-seed.yaml"
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as held:
        held.write(projected)
    return {"sourceDigest": "sha256:" + SEED_SHA256, "runtimeDigest": "sha256:" + hashlib.sha256(projected).hexdigest(),
            "instanceType": TYPE, "vcpus": 4, "memoryGiB": 8, "diskGiB": 60}


def selected_tenant(rows, name, expected_id=None):
    if rows is None:
        rows = []  # NKE's empty collection may serialize as null.
    if not isinstance(rows, list):
        raise VM.SmokeError("NKE tenant inventory interface differs")
    matches = [row for row in rows if row.get("name") == name]
    if not matches:
        return None
    if len(matches) != 1:
        raise VM.SmokeError("ambiguous tenant name; refusing ownership")
    value = matches[0]
    identifier = str(uuid.UUID(value["id"]))
    if (value.get("nico") != tenant_request(name)["nico"] or
            (expected_id is not None and identifier != expected_id)):
        raise VM.SmokeError("tenant identity/provider changed; refusing adoption or removal")
    return value


def admit_guest_management(guard, name, env):
    # Reuse the management owner's exact image/cluster/role binding. These
    # NodePorts belong to the pinned NKE deploy/nico SPIRE manifests, not a
    # caller-provided allowlist or a global Docker direct-routing exception.
    nodes = MGMT.owned_nodes(name, env)
    if len(nodes) != 1:
        raise VM.SmokeError("guest SPIRE route requires one owned management node")
    rows = json.loads(MGMT.inspect(["docker", "inspect", nodes[0]], "guest SPIRE endpoint", env))
    if len(rows) != 1 or rows[0].get("Id") != nodes[0]:
        raise VM.SmokeError("management node changed before guest route admission")
    guard.allow_guest_tcp(rows[0]["NetworkSettings"]["Networks"]["kind"]["IPAddress"], (30440, 30443))


def consume_tenant(guard, name, tools, work, env, timeout, server, image, domain_runner, diagnostic_url=None):
    tenant = Tenant("nkd-tenant-" + uuid.uuid4().hex[:12], tools, work, env, timeout, server, image, domain_runner,
                    diagnostic_url)
    joined = True
    try:
        admit_guest_management(guard, name, env)
        tenant.run()
    except MGMT.UnjoinedProcess:
        joined = False
        raise
    finally:
        if joined:
            with cleanup_signals(), MGMT.cleanup_diagnostics():
                try:
                    tenant.close()
                except MGMT.UnjoinedProcess:
                    joined = False
                    raise
                finally:
                    if joined:
                        try:
                            guard.revoke_guest_tcp()
                        except (VM.SmokeError, OSError) as err:
                            # A guest producer may still run after failed deletion.
                            # Preserve the node/IP and outer controls until the
                            # exact exception can be withdrawn, never recycle it.
                            raise MGMT.RetainedControls("guest route could not be withdrawn; retaining node and host controls") from err


class Tenant:
    def __init__(self, name, tools, work, env, timeout, simulator, image, domain_runner=None, diagnostic_url=None):
        self.name, self.tools, self.work, self.env = name, tools, work, env
        self.timeout, self.simulator, self.image = timeout, simulator, image
        self.cli = work / "cli/nkectl"
        self.id = None
        self.intent = False
        self.sequence = 0
        self.cleaning = False
        self.domain_runner = domain_runner
        self.diagnostic_url = diagnostic_url

    def capture(self, argv, phase, timeout=30):
        self.sequence += 1
        return MGMT.capture(argv, "tenant-%03d-%s" % (self.sequence, phase), self.work, self.env, timeout,
                            volatile=self.cleaning)

    def inventory(self):
        return json.loads(self.capture([str(self.cli), "get", "clusters", "-o", "json"], "inventory"))

    def row(self):
        return selected_tenant(self.inventory(), self.name, self.id)

    def run(self):
        if self.inventory() not in ([], None):
            raise VM.SmokeError("require an empty freshly installed site before tenant creation")
        request = self.work / "tenant-request.json"
        request.write_text(json.dumps(tenant_request(self.name), sort_keys=True, indent=2))
        self.intent = True  # Lookup after a joined ambiguous create, not CLI prose.
        self.capture([str(self.cli), "create", "cluster", "-f", str(request)], "create", 120)
        row = self.row()
        if row is None:
            raise VM.SmokeError("NKE did not expose the created exact tenant")
        self.id = str(uuid.UUID(row["id"]))
        print("Tenant created: " + self.name + "; waiting for real QCOW2 control plane and worker", flush=True)
        deadline = time.monotonic() + self.timeout
        kubeconfig = self.work / "tenant-kubeconfig"
        while time.monotonic() < deadline:
            if self.simulator.poll() is not None:
                raise VM.SmokeError("NICoSim exited during tenant provisioning")
            if self.row() is None:
                raise VM.SmokeError("created tenant disappeared")
            result = json.loads(self.capture([str(self.tools / "kubectl"), "--kubeconfig", self.env["SITE_KUBECONFIG"],
                "-n", "nke-system", "get", "secret", self.name + "-admin-kubeconfig", "--ignore-not-found", "-o", "json"],
                "kubeconfig-secret") or "null")
            if result:
                self.capture([str(self.cli), "get", "kubeconfig", self.name, "--output-file", str(kubeconfig)], "kubeconfig")
                break
            time.sleep(10)
        else:
            raise VM.SmokeError("tenant provisioning budget expired; private phase, simulator and VM console logs retained")
        kubectl = [str(self.tools / "kubectl"), "--kubeconfig", str(kubeconfig)]
        config = json.loads(self.capture([*kubectl, "config", "view", "-o", "json"], "tenant-config"))
        if [row["cluster"]["server"] for row in config.get("clusters", [])] != ["https://" + VIP + ":6443"]:
            raise VM.SmokeError("tenant kubeconfig does not select the admitted VM VIP")
        while time.monotonic() < deadline:
            if self.simulator.poll() is not None or self.row() is None:
                raise VM.SmokeError("simulator/tenant disappeared while waiting for both nodes")
            try:
                nodes = json.loads(self.capture([*kubectl, "--request-timeout=10s", "get", "nodes", "-o", "json"], "nodes"))["items"]
                if len(nodes) > 2:
                    raise VM.SmokeError("unexpected extra tenant node")
                if len(nodes) == 2 and all(any(c.get("type") == "Ready" and c.get("status") == "True"
                                             for c in row["status"].get("conditions", [])) for row in nodes):
                    break
            except MGMT.UnjoinedProcess:
                raise
            except VM.SmokeError as err:
                # The admin Secret can precede API reachability. Retain each
                # joined kubectl failure; a timeout never becomes acceptance.
                if err.returncode != 1:
                    raise
            time.sleep(10)
        else:
            raise VM.SmokeError("both tenant nodes did not become ready within the provisioning budget")
        if (len(nodes) != 2 or sum("node-role.kubernetes.io/control-plane" in row["metadata"].get("labels", {}) for row in nodes) != 1 or
                any(row["status"]["nodeInfo"]["architecture"] != "arm64" or
                    row["status"]["nodeInfo"]["kubeletVersion"] != "v1.34.2" or
                    not any(c.get("type") == "Ready" and c.get("status") == "True" for c in row["status"].get("conditions", []))
                    for row in nodes)):
            raise VM.SmokeError("tenant requires exactly two ready ARM64 1.34.2 nodes, one control plane")
        domains = vm_evidence(self.work.parent / "simulator", self.image, self.domain_runner)
        job = self.work / "workload.json"
        job.write_text(json.dumps({"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "nkd-smoke"},
            "spec": {"backoffLimit": 0, "activeDeadlineSeconds": 180, "template": {"spec": {"restartPolicy": "Never",
                "containers": [{"name": "smoke", "image": WORKLOAD_IMAGE,
                    "command": ["sh", "-ec", "test $(uname -m) = aarch64; nslookup kubernetes.default.svc.cluster.local; echo NKE_LOCAL_WORKLOAD_PASS"]}]}}}}))
        self.capture([*kubectl, "apply", "-f", str(job)], "workload-apply")
        self.capture([*kubectl, "wait", "job/nkd-smoke", "--for=condition=Complete", "--timeout=210s"], "workload-complete", 240)
        logs = self.capture([*kubectl, "logs", "job/nkd-smoke"], "workload-log")
        if "NKE_LOCAL_WORKLOAD_PASS" not in logs.splitlines():
            raise VM.SmokeError("workload did not report ARM64 execution and cluster DNS success")
        pods = json.loads(self.capture([*kubectl, "get", "pods", "-l", "job-name=nkd-smoke", "-o", "json"], "workload-pod"))
        states = [s for p in pods["items"] for s in p.get("status", {}).get("containerStatuses", [])]
        if len(states) != 1 or states[0].get("imageID", "").split("@")[-1] != WORKLOAD_IMAGE.split("@")[-1]:
            raise VM.SmokeError("workload image ID differs from the pinned diagnostic acquisition")
        (self.work / "tenant-result.json").write_text(json.dumps({"scope": "unqualified-local-demo", "tenantID": self.id,
            "name": self.name, "nodes": nodes, "domains": domains, "workload": pods}, indent=2, sort_keys=True))

    def close(self):
        with cleanup_signals():
            self.preserve_consoles()
            self.preserve_guest_diagnostics()
            self.restore()

    def preserve_guest_diagnostics(self):
        # The pinned NICoSim owns these diagnostic commands and SSH transport.
        # Never collect credentials/config files or stream/follow indefinitely.
        # Private log storage and a failed diagnostic must not prevent cleanup.
        if not self.intent or self.diagnostic_url is None:
            return
        simulator = self.work.parent / "simulator"
        try:
            header = self.work.parent / "nico-api-header"
            with os.fdopen(os.open(header, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as held:
                value = os.fstat(held.fileno())
                if (not stat.S_ISREG(value.st_mode) or value.st_uid != os.getuid() or value.st_nlink != 1 or
                        stat.S_IMODE(value.st_mode) != 0o600 or not 0 < value.st_size <= 256):
                    raise VM.SmokeError("diagnostic header is not a bounded private owned file")
            names = VM.domain_inventory(self.domain_runner)
            if len(names) > 2:
                raise VM.SmokeError("diagnostic guest inventory exceeds the two-VM contract")
            for name in names:
                if VM.owned_domain(name, simulator, self.domain_runner) is None:
                    continue
                instance = str(uuid.UUID(name.removeprefix("nico-vm-")))
                for item in ("agent-logs", "kubelet-logs", "containerd-logs"):
                    try:
                        url = self.diagnostic_url + "/v1/orgs/novxwn552ssc/instances/" + instance + "/diagnostics/" + item
                        # -q must be first: a user's curlrc can add URLs or
                        # enable traces that would leak the private header.
                        data = self.capture(["curl", "-q", "--noproxy", "*", "--max-time", "30", "--max-filesize", "1048576",
                            "--fail", "--silent", "--show-error", "--header", "@" + str(header), url],
                            "diagnostic-" + instance + "-" + item, 32)
                        if len(data.encode()) > 1024**2:
                            raise VM.SmokeError("guest diagnostic exceeds the private retention bound")
                        path = self.work / (instance + "." + item + ".log")
                        with path.open("x") as saved:
                            saved.write(data)
                        path.chmod(0o600)
                    except MGMT.UnjoinedProcess:
                        raise
                    except (OSError, VM.SmokeError, UnicodeError):
                        print("A private guest diagnostic could not be retained; owned cleanup still proceeds", file=sys.stderr)
        except MGMT.UnjoinedProcess:
            raise
        except (OSError, VM.SmokeError, ValueError, ET.ParseError):
            print("Private guest diagnostics unavailable; owned cleanup still proceeds", file=sys.stderr)

    def preserve_consoles(self):
        # Source-owned deletion removes instance directories. Retain bounded
        # private console tails first; diagnostic storage must not block cleanup.
        if not self.intent:
            return
        simulator = self.work.parent / "simulator"
        try:
            for name in VM.domain_inventory(self.domain_runner):
                if VM.owned_domain(name, simulator, self.domain_runner) is None:
                    continue
                instance = name.removeprefix("nico-vm-")
                for channel in ("ttyS0", "ttyS1"):
                    path = simulator / "var/instances" / instance / ("disk.qcow2." + channel + ".log")
                    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as held:
                        value = os.fstat(held.fileno())
                        if not stat.S_ISREG(value.st_mode) or value.st_uid != os.getuid():
                            raise VM.SmokeError("console file ownership differs")
                        held.seek(max(0, value.st_size - 4 * 1024**2))
                        tail = held.read(4 * 1024**2)
                    with (self.work / (instance + "." + channel + ".log")).open("xb") as saved:
                        saved.write(tail)
        except MGMT.UnjoinedProcess:
            raise
        except (VM.SmokeError, OSError, ValueError, ET.ParseError):
            print("Some private VM console tails could not be retained; owned cleanup still proceeds", file=sys.stderr)

    def restore(self):
        self.cleaning = True
        if not self.intent:
            return
        row = self.row()
        if row is not None:
            self.id = str(uuid.UUID(row["id"]))
            self.capture([str(self.cli), "delete", "cluster", self.name], "delete", 120)
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline:
                if self.row() is None:
                    return
                time.sleep(5)
            raise VM.SmokeError("NKE tenant deletion did not finish; exact simulator VM sweep still required")


def vm_evidence(work, image, runner=None):
    runner = runner or VM.run
    names = VM.domain_inventory(runner)
    if len(names) != 2:
        raise VM.SmokeError("require exactly two real simulator-owned libvirt domains")
    evidence = []
    for name in names:
        binding = VM.owned_domain(name, work, runner)
        if binding is None:
            raise VM.SmokeError("tenant domain is not owned by this simulator directory")
        tree = ET.fromstring(binding[1])
        if tree.find("./devices/tpm") is None or tree.findtext("./os/type") is None:
            raise VM.SmokeError("tenant domain lacks real guest/TPM configuration")
        disk = work / "var/instances" / name.removeprefix("nico-vm-") / "disk.qcow2"
        info = json.loads(runner(["qemu-img", "info", "--force-share", "--output=json", str(disk)], "tenant disk inspection"))
        original = Path(info["full-backing-filename"])
        base = original.resolve(strict=True)
        if (not base.is_relative_to(work / "cache") or original.is_symlink() or base.stat().st_size != image["disk"]["size"] or
                VM.digest(base) != image["digest"]):
            raise VM.SmokeError("tenant overlay does not use the exact downloaded NKD QCOW2")
        evidence.append({"name": name, "uuid": binding[0], "backingDigest": VM.digest(base), "disk": info})
    return evidence


def restore_bgp(guard):
    # Only after NICoSim and all owned domains have joined/disappeared. The
    # pinned upstream leaves its covering blackhole on shutdown. Admission
    # proved these ranges absent; never flush a range or delete unknown routes.
    guard.cleaning = True  # Restoration must not depend on writable logs.
    rows = json.loads(guard.command(["ip", "-j", "route", "show", "table", "main"]))
    selected = [row for row in rows if row.get("dst", "default") != "default" and
                ipaddress.ip_network(row["dst"], strict=False).overlaps(ipaddress.ip_network(NET.VIP_CIDR))]
    for row in selected:
        if row.get("dst") == NET.VIP_CIDR and row.get("type") == "blackhole":
            if set(row) - {"dst", "type", "flags"} or row.get("flags", []):
                raise VM.SmokeError("covering BGP route changed; refusing removal")
        elif ipaddress.ip_network(row["dst"], strict=False) == ipaddress.ip_network(VIP + "/32"):
            hops = row.get("nexthops") or [row]
            if (set(row) - {"dst", "gateway", "dev", "flags", "nexthops"} or row.get("flags", []) or
                    any(set(hop) - {"dst", "gateway", "dev", "flags", "weight"} or hop.get("flags", []) or
                        hop.get("weight", 1) != 1 or hop.get("dev") != NET.BRIDGE or not hop.get("gateway") or
                   ipaddress.ip_address(hop["gateway"]) not in ipaddress.ip_network(NET.CIDR) for hop in hops)):
                raise VM.SmokeError("VIP next-hop route changed; refusing removal")
            if guard.bridge_binding() is None:
                raise VM.SmokeError("VIP route bridge disappeared; refusing removal")
        else:
            raise VM.SmokeError("unknown overlapping BGP route; refusing removal")
    for row in selected:
        current = json.loads(guard.command(["ip", "-j", "route", "show", "table", "main"]))
        if row not in current:
            raise VM.SmokeError("BGP route changed during restoration")
        argv = ["ip", "route", "delete"] + (["blackhole"] if row.get("type") == "blackhole" else [])
        guard.command([*argv, row["dst"], "table", "main"], True)
    final = json.loads(guard.command(["ip", "-j", "route", "show", "table", "main"]))
    if any(row.get("dst", "default") != "default" and ipaddress.ip_network(row["dst"], strict=False).overlaps(
            ipaddress.ip_network(NET.VIP_CIDR)) for row in final):
        raise VM.SmokeError("BGP routes remain")


def admit_libvirt_environment(env):
    # Session TPM placement belongs to the daemon, not a later client's private
    # HOME. Bind the native account's standard paths, never inherited settings.
    home = Path(pwd.getpwuid(os.getuid()).pw_dir)
    account = home.lstat()
    if not home.is_absolute() or not stat.S_ISDIR(account.st_mode) or account.st_uid != os.getuid():
        raise VM.SmokeError("require the native account's regular owned home")
    runtime = Path("/run/user") / str(os.getuid())
    value = runtime.lstat()
    if not stat.S_ISDIR(value.st_mode) or value.st_uid != os.getuid() or stat.S_IMODE(value.st_mode) != 0o700:
        raise VM.SmokeError("require the current user's private native libvirt runtime directory")
    bound = {"HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
             "XDG_CACHE_HOME": str(home / ".cache"), "XDG_RUNTIME_DIR": str(runtime)}
    inventory = MGMT.inspect(["ps", "-u", str(os.getuid()), "-o", "pid=,comm="], "session daemon inventory", env)
    for line in inventory.splitlines():
        fields = line.split()
        if len(fields) != 2 or fields[1] not in ("libvirtd", "virtqemud"):
            continue
        pid = int(fields[0])
        path = Path("/proc") / str(pid) / "environ"
        # Kernel procfs environment ABI: bounded NUL-separated key=value bytes.
        # Keep all unrelated values out of logs; only compare admitted paths.
        with path.open("rb") as held:
            raw = held.read(1024**2 + 1)
        if len(raw) > 1024**2 or path.stat().st_uid != os.getuid():
            raise VM.SmokeError("session daemon environment exceeds bound or changed owner")
        observed = {}
        for entry in raw.split(b"\0"):
            key, separator, value = entry.partition(b"=")
            if separator and key.decode(errors="replace") in bound:
                name = key.decode()
                if name in observed:
                    raise VM.SmokeError("duplicate session daemon environment binding")
                observed[name] = value.decode()
        if any(value != bound[key] for key, value in observed.items()):
            raise VM.SmokeError("session daemon uses different account/runtime paths; refusing TPM namespace mismatch")
    return bound


def simulator_environment(work, disk, guard_work, gateway, binding):
    env = MGMT.native_environment()
    controls = VM.environment(work, disk, NET.BRIDGE, ipaddress.ip_network(NET.CIDR))
    env.update({key: value for key, value in controls.items() if key.startswith("NICO_")})
    env.update(binding)
    temporary = work / "tmp"
    temporary.mkdir(mode=0o700)
    env.update({"TMPDIR": str(temporary), "NICO_SIM_LISTEN_ADDR": gateway + ":18089",
        "NICO_SIM_SEED_PATH": str(work / "laptop-seed.yaml"),
        "NICO_VMS_BGP_ENABLED": "true", "NICO_VMS_BGP_VIP_CIDRS": NET.VIP_CIDR,
        "NICO_VMS_BGP_LISTEN_PORT": "10179", "NICO_VMS_READINESS_TIMEOUT": "30m",
        "NICO_VMS_DIAG_SSH_ENABLED": "true", "NICO_VMS_DIAG_SSH_USER": "nkd-demo",
        "NICO_VMS_DIAG_SSH_PASSWORD": secrets.token_hex(24),
        "NICO_VMS_DHCP_HOSTS_DIR": str(guard_work / "dhcp-hosts"), "NICO_VMS_DHCP_LEASE_FILE": str(guard_work / "leases")})
    return env


def cleanup_runtime(server, http, guard, work, domain_runner, bgp_intent, restore_controls):
    failures = []
    # Join each independent daemon even when another join or host restoration
    # fails. Never remove shared controls underneath an unjoined producer.
    for process in (server, http):
        if process is not None:
            try:
                MGMT.stop_group(process)
            except (VM.SmokeError, OSError, subprocess.TimeoutExpired) as err:
                failures.append(err)
    try:
        guard.stop_dns()
    except (VM.SmokeError, OSError, subprocess.TimeoutExpired) as err:
        failures.append(err)
    if failures:
        error = MGMT.UnjoinedProcess if any(isinstance(err, (MGMT.UnjoinedProcess, subprocess.TimeoutExpired))
                                          for err in failures) else VM.SmokeError
        raise error("daemon cleanup failed; host controls retained: " + "; ".join(str(err) for err in failures))
    if not restore_controls:
        return
    if server is not None:
        VM.sweep_owned_domains(work / "simulator", domain_runner)
    if VM.domain_inventory(domain_runner):
        raise VM.SmokeError("unknown/remaining domain; network retained")
    if bgp_intent:
        restore_bgp(guard)
        guard.bgp_intent = False
        guard.cleanup_checkpoint()
    guard.close()


def probe(args):
    outputs, gateway, registry = SITE.admit_materials(args)
    layout, disk, work, sim, image = VM.inputs(args.simulator_run, args.os_run, args.out)
    if not 300 <= args.timeout <= 3600:
        raise VM.SmokeError("tenant budget must be 300..3600 seconds")
    for name in ("qemu-img", "qemu-io", "qemu-system-aarch64", "cloud-localds", "swtpm_setup", "certtool", "debugfs"):
        if shutil.which(name) is None:
            raise VM.SmokeError("missing native VM prerequisite: " + name)
    if shutil.disk_usage(work.parent).free < 30 * 1024**3 + disk.stat().st_size:
        raise VM.SmokeError("need disk-copy bytes plus 30 GiB free for the two-VM diagnostic")
    env = MGMT.native_environment()
    libvirt_binding = admit_libvirt_environment(env)  # Before any host mutation.
    network = json.loads(MGMT.inspect(["docker", "network", "inspect", "kind"], "Kind IPv4 subnet", env))
    cidrs = [row["Subnet"] for row in network[0]["IPAM"]["Config"] if ipaddress.ip_network(row["Subnet"]).version == 4]
    if len(cidrs) != 1:
        raise VM.SmokeError("require one Kind IPv4 subnet")
    with socket.socket() as listener:
        listener.bind((gateway, 18089))
    os.umask(0o077)
    work.mkdir(mode=0o700)
    guard = NET.OwnedNetwork(work, env, cidrs[0])
    server = http = None
    logs = []
    server_env = env
    domain_sequence = 0
    def domain_runner(argv, _label, timeout=30):
        nonlocal domain_sequence
        domain_sequence += 1
        return MGMT.capture(argv, "domain-%03d" % domain_sequence, work, server_env, timeout, volatile=True)
    bgp_intent = False
    cleanup_allowed = True
    try:
        guard.admit()
        simulator = work / "simulator"
        simulator.mkdir(mode=0o700)
        MGMT.execute([args.nkdctl, "publish", "root", "--bundle", VM.BUNDLE + "=" + str(layout),
            "--out", str(work / "sim-publication")], "verify-simulator", work, env, 120)
        bundle = simulator / "bundle"
        MGMT.execute([args.nkdctl, "publish", "extract", "--layout", str(work / "sim-publication"),
            "--bundle", VM.BUNDLE, "--out", str(bundle)], "extract-simulator", work, env, 120)
        if sorted(path.name for path in bundle.iterdir()) != ["nico-sim", "nico-sim-runtime-assets", "nico-simctl"]:
            raise VM.SmokeError("require exactly the three accepted simulator members")
        VM.extract_assets(bundle / "nico-sim-runtime-assets", simulator)
        seed_projection = project_laptop_seed(simulator)
        # Grant only a private single-link copy, never mutate accepted inputs.
        binary = work / "nico-sim"
        shutil.copyfile(bundle / "nico-sim", binary)
        binary.chmod(0o700)
        serve = work / "disk-http"
        serve.mkdir(mode=0o700)
        private_disk = serve / (VM.DISK + ".qcow2")
        shutil.copyfile(disk, private_disk)
        private_disk.chmod(0o444)
        if VM.digest(private_disk) != image["digest"] or private_disk.stat().st_size != image["disk"]["size"]:
            raise VM.SmokeError("private served disk differs from the completed Run")
        info = json.loads(MGMT.capture(["qemu-img", "info", "--output=json", str(private_disk)], "inspect-base-disk", work, env))
        if info.get("format") != "qcow2" or info.get("backing-filename") or not 0 < info.get("virtual-size", 0) <= 60 * 1024**3:
            raise VM.SmokeError("require a standalone QCOW2 fitting the selected tenant instance")
        guard.start()
        guard.grant(binary, "cap_net_admin=ep")
        logs.append((work / "disk-http.log").open("xb"))
        http = subprocess.Popen([sys.executable, "-B", "-m", "http.server", "18080", "--bind", NET.GATEWAY,
            "--directory", str(serve)], cwd=work, env=env, stdout=logs[-1], stderr=subprocess.STDOUT, start_new_session=True)
        url = "http://" + NET.GATEWAY + ":18080/" + private_disk.name
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        deadline = time.monotonic() + 30
        while True:
            if http.poll() is not None or time.monotonic() > deadline:
                raise VM.SmokeError("private disk HTTP service startup failed")
            try:
                with opener.open(url, timeout=3) as response:
                    length, hashed, budget = 0, hashlib.sha256(), time.monotonic() + 180
                    while chunk := response.read(1024**2):
                        length += len(chunk)
                        if length > image["disk"]["size"] or time.monotonic() > budget:
                            raise VM.SmokeError("served disk readback exceeds exact subject or budget")
                        hashed.update(chunk)
                    if length != image["disk"]["size"] or "sha256:" + hashed.hexdigest() != image["digest"]:
                        raise VM.SmokeError("HTTP disk readback differs from the completed Run")
                break
            except ConnectionError:
                time.sleep(.2)
            except urllib.error.URLError as err:
                if not isinstance(err.reason, ConnectionRefusedError):
                    raise
                time.sleep(.2)
        token = secrets.token_hex(24)
        write_nico_header(work, token)
        # Guard's session admission may have activated the daemon. Re-admit its
        # effective paths before starting the capability-bearing simulator.
        if admit_libvirt_environment(env) != libvirt_binding:
            raise VM.SmokeError("libvirt path binding changed during setup")
        server_env = simulator_environment(simulator, private_disk, work, gateway, libvirt_binding)
        logs.append((work / "simulator.log").open("xb"))
        bgp_intent = True
        guard.bgp_intent = True
        guard.checkpoint()
        server = subprocess.Popen([str(binary)], cwd=simulator, env=server_env, stdout=logs[-1],
                                  stderr=subprocess.STDOUT, start_new_session=True)
        nico = "http://" + gateway + ":18089"
        deadline = time.monotonic() + 30
        while True:
            if server.poll() is not None or time.monotonic() > deadline:
                raise VM.SmokeError("real VM-backed NICoSim startup failed; private log retained")
            try:
                with opener.open(nico + "/healthz", timeout=2) as response:
                    if json.loads(response.read(4097)) == {"status": "ok"}:
                        break
            except OSError:
                time.sleep(.2)
        (work / "demo-inputs.json").write_text(json.dumps({"scope": "unqualified-local-demo", "simulator": sim,
            "disk": image, "httpReadback": image["digest"], "workloadAcquisition": WORKLOAD_IMAGE,
            "workloadSourceIndex": WORKLOAD_INDEX,
            "seedProjection": seed_projection,
            "outputs": {key: row[1] for key, row in outputs.items()}}, indent=2, sort_keys=True))
        args.out = str(work / "site")
        args.nico_org = "novxwn552ssc"
        def continuation(name, tools, site_work, site_env):
            consume_tenant(guard, name, tools, site_work, site_env, args.timeout, server, image, domain_runner, nico)
        MGMT.probe(args, lambda name, tools, site_work, site_env: SITE.consume(args, outputs, token, nico, gateway,
            registry, name, tools, site_work, site_env, continuation))
    except (MGMT.UnjoinedProcess, MGMT.RetainedControls):
        cleanup_allowed = False
        raise
    finally:
        previous = {number: signal.signal(number, signal.SIG_IGN) for number in (signal.SIGINT, signal.SIGTERM)}
        try:
            with MGMT.cleanup_diagnostics():
                cleanup_runtime(server, http, guard, work, domain_runner, bgp_intent, cleanup_allowed)
        finally:
            for log in logs:
                log.close()
            for number, handler in previous.items():
                signal.signal(number, handler)
    if VM.digest(disk) != image["digest"] or VM.digest(private_disk) != image["digest"]:
        raise VM.SmokeError("accepted/served disk changed during demo")
    print("PASS: real ARM64 QCOW2 tenant, two ready nodes, workload/DNS, exact artifacts and owned cleanup.")
    print("Unqualified local diagnostic, not a signed Release or complete source-built distribution.")


def main():
    if sys.argv[1:2] == ["restore"]:
        parser = argparse.ArgumentParser(description="Restore recorded local network controls after producers have stopped.")
        parser.add_argument("--work", required=True)
        args = parser.parse_args(sys.argv[2:])
        guard = None
        try:
            env = MGMT.native_environment()
            env.update(admit_libvirt_environment(env))
            guard = NET.OwnedNetwork.recover(args.work, env)
            if not guard.finished:
                guard.require_idle()
                if guard.bgp_intent:
                    restore_bgp(guard)
                    guard.bgp_intent = False
                    guard.cleanup_checkpoint()
                guard.close()
            print("PASS: recorded local network controls restored; no tenant, VM or management deletion was attempted.")
            return 0
        except (VM.SmokeError, OSError, ValueError, KeyError, TypeError) as err:
            print("NKD-E-PREFLIGHT-FAILED: local network recovery: " + str(err), file=sys.stderr)
            return 17
        finally:
            if guard is not None and guard.lock is not None:
                guard.lock.close()
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("nke-checkout", "nkdctl", "tools-run", "site-run", "components-run", "profiles-run", "provider-run",
                 "simulator-run", "os-run", "out"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--timeout", type=int, default=3600)
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise VM.SmokeError("demo interrupted; cleaning only joined owned resources")
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        probe(args)
    except (VM.SmokeError, SITE.CHART.HandoffError, OSError, ValueError, KeyError, TypeError, ET.ParseError) as err:
        print("NKD-E-PREFLIGHT-FAILED: local demo: " + str(err), file=sys.stderr)
        return 17
    return 0


if __name__ == "__main__":
    sys.exit(main())
