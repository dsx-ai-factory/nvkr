# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Boot an existing local ARM64 QCOW2 through the source-built NICoSim.

Diagnostic only: no rebuild, Release verification, full image contract, NKE
installation or tenant/workload acceptance. Host networking is admitted, never
installed or permission-modified by this command. Run inside Linux/Colima.
"""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import time
import urllib.request
import uuid
import xml.etree.ElementTree as ET


SITE = "a1b2c3d4-0001-4000-8000-000000000001"
TENANT = "d282929f-49d5-4dec-8d27-21ceb30ade19"
TYPE = "a1b2c3d4-0005-4000-8000-000000000002"
VPC = "a1b2c3d4-0002-4000-8000-000000000001"
SUBNET = "a1b2c3d4-0003-4000-8000-000000000001"
BUNDLE = "nicosim-tools-arm64"
DISK = "nke-node-image-arm64"


class SmokeError(Exception):
    def __init__(self, message, returncode=None):
        super().__init__(message)
        self.returncode = returncode


def run(argv, label, timeout=30, diagnostic=None):
    try:
        result = subprocess.run(argv, capture_output=True, text=True,
                                timeout=timeout, check=False)
    except subprocess.TimeoutExpired as err:
        if diagnostic is not None:
            # TimeoutExpired can retain bytes even for text=True. Keep the
            # last phase privately, without printing potentially sensitive data.
            def text(value):
                return value.decode("utf-8", errors="replace") if isinstance(value, bytes) else value or ""
            try:
                diagnostic.write_text(text(err.stdout) + text(err.stderr))
            except OSError:
                print("Some private timeout diagnostics could not be retained", file=sys.stderr)
        raise SmokeError(f"{label}: timed out") from err
    except OSError as err:
        raise SmokeError(f"{label}: unavailable") from err
    if diagnostic is not None:
        diagnostic.write_text(result.stdout + result.stderr)
    if result.returncode:
        raise SmokeError(f"{label}: failed (exit {result.returncode})", result.returncode)
    return result.stdout


def digest(path):
    with path.open("rb") as held:
        return "sha256:" + hashlib.file_digest(held, "sha256").hexdigest()


def complete_output(root, name, kind):
    # These are local observations, not authenticated lifecycle/Release records.
    path = root / "lifecycle.json"
    if path.stat().st_size > 8 * 1024 * 1024:
        raise SmokeError("local lifecycle description exceeds 8 MiB")
    record = json.loads(path.read_text())
    expected, observed = record.get("expected", []), record.get("observed", [])
    if (record.get("status") != "complete" or record.get("failure") is not None or
            not expected or len(expected) != len(observed)):
        raise SmokeError("both local Runs must be complete with all expected observations")
    names = [item["name"] for item in expected]
    actual = [item["name"] for item in observed]
    if len(set(names)) != len(names) or len(set(actual)) != len(actual) or set(names) != set(actual):
        raise SmokeError("local Run output membership is missing, extra or duplicated")
    for item in expected:
        match = next(row for row in observed if row["name"] == item["name"])
        if any(match.get(key) != item.get(key) for key in ("kind", "buildDigest", "components")):
            raise SmokeError("local Run expected/observed producer binding differs")
    selected = [row for row in observed if row["name"] == name and row["kind"] == kind]
    if len(selected) != 1:
        raise SmokeError("local Run does not contain the exact required output")
    return selected[0]


def inputs(simulator_run, os_run, out):
    simulator = Path(simulator_run).resolve(strict=True)
    image = Path(os_run).resolve(strict=True)
    destination = Path(out).absolute()
    if destination.exists() or destination.is_symlink():
        raise SmokeError("output exists; choose a fresh private directory")
    parent = destination.parent.resolve(strict=True)
    if any(parent.is_relative_to(root) for root in (simulator, image)):
        raise SmokeError("probe output must be outside both input Runs")
    sim = complete_output(simulator, BUNDLE, "executable")
    os_image = complete_output(image, DISK, "disk-image")
    disk_record = os_image.get("disk", {})
    if (disk_record.get("format") != "qcow2" or disk_record.get("encryption") != "none" or
            disk_record.get("outcome") != "succeeded" or
            disk_record.get("payloadDigest") != os_image.get("digest") or
            type(disk_record.get("size")) is not int or disk_record["size"] <= 0):
        raise SmokeError("require a successful observed plain QCOW2")
    disk = image / "outputs" / (DISK + ".qcow2")
    layout = simulator / "packages" / BUNDLE
    if disk.is_symlink() or not disk.is_file() or layout.is_symlink():
        raise SmokeError("input disk/layout must not be symlink substitutions")
    index = json.loads((layout / "index.json").read_text())
    if [item.get("digest") for item in index.get("manifests", [])] != [sim.get("digest")]:
        raise SmokeError("simulator layout differs from the observed manifest")
    if disk.stat().st_size != disk_record["size"] or digest(disk) != os_image["digest"]:
        raise SmokeError("QCOW2 bytes differ from the local observation")
    return layout, disk, parent / destination.name, sim, os_image


def host(bridge, cidr):
    if platform.system() != "Linux" or platform.machine() not in ("aarch64", "arm64"):
        raise SmokeError("run inside native Linux ARM64, not directly on macOS")
    if sys.version_info < (3, 12):
        raise SmokeError("Python 3.12 or newer is required for safe archive extraction")
    for name in ("ip", "virsh", "qemu-img", "qemu-system-aarch64", "cloud-localds", "swtpm_setup", "certtool", "ssh", "ssh-keygen"):
        if shutil.which(name) is None:
            raise SmokeError(f"missing host prerequisite: {name}")
    if not bridge or len(bridge) > 15 or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789-" for c in bridge):
        raise SmokeError("bridge must be a simple Linux interface name")
    network = ipaddress.ip_network(cidr, strict=True)
    if network.version != 4 or network.prefixlen != 24 or not network.is_private:
        raise SmokeError("probe requires a private IPv4 /24 bridge")
    for reserved in ("192.168.0.0/16", "10.96.0.0/12"):
        if network.overlaps(ipaddress.ip_network(reserved)):
            raise SmokeError("bridge overlaps the NKE pod/service ranges")
    gateway = str(network.network_address + 1)
    interfaces = json.loads(run(["ip", "-j", "address", "show", "dev", bridge], "bridge inspection"))
    if len(interfaces) != 1 or "UP" not in interfaces[0]["flags"] or not any(
            row.get("local") == gateway and row.get("prefixlen") == 24
            for row in interfaces[0].get("addr_info", [])):
        raise SmokeError("create the intended /24 bridge/gateway before the probe")
    if run(["virsh", "-c", "qemu:///session", "list", "--all", "--name"], "session admission").split():
        raise SmokeError("probe requires an empty libvirt session; it never adopts existing VMs")
    for address, port in (("127.0.0.1", 18089), (gateway, 7777)):
        with socket.socket() as listener:
            try:
                listener.bind((address, port))
            except OSError as err:
                raise SmokeError("probe API/metadata listener is already in use") from err
    return network, gateway


def extract_assets(archive, out):
    # Standard-library tar interpretation/filter, not a home-grown archive codec.
    with tarfile.open(archive, "r:gz") as source:
        total, count = 0, 0
        for member in source:
            total += member.size
            count += 1
            if not (member.isfile() or member.isdir()) or total > 16 * 1024 * 1024 or count > 256:
                raise SmokeError("runtime assets exceed bounds or contain non-regular entries")
        source.extractall(out, filter="data")
    if not (out / "internal/seed/default-seed.yaml").is_file() or not (out / "ui/index.html").is_file():
        raise SmokeError("bundle is missing the source-owned seed/UI interface")


def stop(process):
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=40)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired as err:
            raise SmokeError("server did not join after SIGKILL; cleanup remains incomplete") from err


def domain_inventory(runner=None):
    run = runner or globals()["run"]
    return run(["virsh", "-c", "qemu:///session", "list", "--all", "--name"], "cleanup inventory").split()


def owned_domain(name, work, runner=None):
    run = runner or globals()["run"]
    instance_id = name.removeprefix("nico-vm-")
    try:
        if name != "nico-vm-" + str(uuid.UUID(instance_id)):
            return None
    except ValueError:
        return None
    xml = run(["virsh", "-c", "qemu:///session", "dumpxml", name], "owned domain inspection")
    tree = ET.fromstring(xml)
    domain_uuid = str(uuid.UUID(tree.findtext("uuid")))
    expected = str(work / "var/instances" / instance_id / "disk.qcow2")
    paths = [element.get("file") for element in tree.findall("./devices/disk/source")]
    if tree.findtext("name") != name or expected not in paths:
        return None
    return domain_uuid, xml


def remove_owned_domain(name, work, runner=None):
    run = runner or globals()["run"]
    binding = owned_domain(name, work, runner)
    if binding is None:
        raise SmokeError("refusing to delete a domain not bound to this private probe directory")
    domain_uuid, _ = binding
    state = run(["virsh", "-c", "qemu:///session", "domstate", domain_uuid], "owned domain state").strip()
    if state != "shut off":
        run(["virsh", "-c", "qemu:///session", "destroy", domain_uuid], "owned domain stop")
    run(["virsh", "-c", "qemu:///session", "undefine", domain_uuid, "--nvram", "--tpm"], "owned domain removal")


def sweep_owned_domains(work, runner=None):
    # Run only AFTER the producer has stopped/joined. This covers an accepted
    # Create whose CLI response timed out/interrupted before an ID was returned.
    # No private-DB parser: libvirt owns XML, interpreted with the stdlib.
    for name in domain_inventory(runner):
        if name.startswith("nico-vm-") and owned_domain(name, work, runner) is not None:
            remove_owned_domain(name, work, runner)


def cleanup_vm(cli, instance_id, work):
    domain = "nico-vm-" + instance_id
    try:
        # Diagnostic failures must never skip deletion/stop/join.
        try:
            if domain in domain_inventory():
                binding = owned_domain(domain, work)
                if binding is not None:
                    (work / "domain-before-cleanup.xml").write_text(binding[1])
            for name in ("disk.qcow2.ttyS0.log", "disk.qcow2.ttyS1.log"):
                console = work / "var/instances" / instance_id / name
                if console.is_file() and not console.is_symlink():
                    with console.open("rb") as held:
                        held.seek(max(0, console.stat().st_size - 4 * 1024 * 1024))
                        (work / name).write_bytes(held.read(4 * 1024 * 1024))
        except (OSError, SmokeError, ValueError, ET.ParseError):
            print("Some private cleanup diagnostics could not be retained", file=sys.stderr)
        cli("instance", "delete", "--instance-id", instance_id)
        deadline = time.monotonic() + 30
        while domain in domain_inventory() and time.monotonic() < deadline:
            time.sleep(.5)
    finally:
        # A failed CreateAndStart can leave a defined domain before NICoSim
        # records machineId. Delete then misses it. Only this API-issued ID in
        # the freshly created private database may be removed by the fallback.
        if domain in domain_inventory():
            print("Cleaning the exact probe-owned domain left by NICoSim", flush=True)
            remove_owned_domain(domain, work)
        if domain in domain_inventory():
            raise SmokeError("owned VM remains after cleanup")


def environment(work, disk, bridge, network):
    env = {key: value for key, value in os.environ.items() if not key.startswith("NICO_")}
    env.update({"NICO_SIM_LISTEN_ADDR": "127.0.0.1:18089", "NICO_SIM_DB_PATH": str(work / "var/nico-sim.db"),
        "NICO_SIM_SEED_PATH": str(work / "internal/seed/default-seed.yaml"), "NICO_SIM_FILES_DIR": str(work / "files"),
        "NICO_VMS_VM_BACKEND": "libvirt", "NICO_VMS_LIBVIRT_URI": "qemu:///session", "NICO_VMS_LIBVIRT_NETWORK": "",
        "NICO_VMS_BRIDGE_NAME": bridge, "NICO_VMS_BRIDGE_CIDR": str(network), "NICO_VMS_BASE_IMAGE": str(disk),
        "NICO_VMS_BGP_ENABLED": "false", "NICO_VMS_CONSOLE_SSH_PORT": "-1", "NICO_VMS_CLEANUP_ORPHANED_VMS": "false",
        "NICO_VMS_DIAG_SSH_ENABLED": "false", "NICO_CONTAINERS_ENABLED": "false", "NICO_VMS_IMAGE_CACHE_DIR": str(work / "cache"),
        "NICO_VMS_DHCP_HOSTS_DIR": str(work / "dhcp-hosts"), "NICO_VMS_DHCP_LEASE_FILE": str(work / "leases"),
        "NICO_VMS_PROMTAIL_TARGETS_FILE": str(work / "promtail.json"), "NICO_SIM_LOG_FORMAT": "json",
        "NICO_VMS_QEMU_IMG_PATH": shutil.which("qemu-img"), "NICO_VMS_CLOUD_LOCALDS_PATH": shutil.which("cloud-localds"),
        "NICO_VMS_VM_VCPUS": "2", "NICO_VMS_VM_MEMORY_MIB": "4096", "NICO_VMS_VM_DISK_GIB": "30"})
    return env


def finish(process, cli, instance_id, work):
    # Repeated interrupts must not abort cleanup halfway through.
    previous = {number: signal.signal(number, signal.SIG_IGN) for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        try:
            if instance_id is not None:
                cleanup_vm(cli, instance_id, work)
        finally:
            stop(process)
            sweep_owned_domains(work)
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


def probe(args):
    if not 60 <= args.timeout <= 1800:
        raise SmokeError("VM timeout must be between 60 and 1800 seconds")
    network, gateway = host(args.bridge, args.cidr)
    tool = Path(args.nkdctl).resolve(strict=True)
    if not os.access(tool, os.X_OK):
        raise SmokeError("nkdctl must already be built and executable")
    layout, disk, work, sim, image = inputs(args.simulator_run, args.os_run, args.out)
    if shutil.disk_usage(work.parent).free < disk.stat().st_size + 8 * 1024**3:
        raise SmokeError("need disk-copy bytes plus 8 GiB free for the bounded VM probe")
    # No VM/process/output work has occurred before full input/host admission.
    work.mkdir(mode=0o700)
    run([str(tool), "publish", "root", "--bundle", BUNDLE + "=" + str(layout),
         "--out", str(work / "publication")], "NKD diagnostic root verification", timeout=120)
    bundle = work / "bundle"
    run([str(tool), "publish", "extract", "--layout", str(work / "publication"),
         "--bundle", BUNDLE, "--out", str(bundle)], "NKD bundle extraction", timeout=120)
    if sorted(item.name for item in bundle.iterdir()) != ["nico-sim", "nico-sim-runtime-assets", "nico-simctl"]:
        raise SmokeError("require exactly the three simulator bundle members")
    extract_assets(bundle / "nico-sim-runtime-assets", work)
    private_disk = work / "base.qcow2"
    shutil.copyfile(disk, private_disk)
    private_disk.chmod(0o444)
    if digest(private_disk) != image["digest"]:
        raise SmokeError("private disk copy differs from observed QCOW2")
    inspected = json.loads(run(["qemu-img", "info", "--output=json", str(private_disk)], "QCOW2 decoder inspection"))
    if (inspected.get("format") != "qcow2" or inspected.get("backing-filename") or
            not 0 < inspected.get("virtual-size", 0) <= 30 * 1024**3):
        raise SmokeError("decoder requires a standalone QCOW2 no larger than the 30 GiB instance disk")
    (work / "inputs.json").write_text(json.dumps({"scope": "unqualified-local-diagnostic", "simulator": sim,
        "image": image, "members": {item.name: digest(item) for item in bundle.iterdir()}}, indent=2, sort_keys=True))
    run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(work / "ssh-key")], "private probe key")
    pubkey = (work / "ssh-key.pub").read_text().strip()
    userdata = ("#cloud-config\nusers:\n  - name: nkd-demo\n    shell: /bin/bash\n    lock_passwd: true\n"
                "    sudo: ALL=(ALL) NOPASSWD:ALL\n    ssh_authorized_keys:\n      - " + pubkey + "\n")
    env = environment(work, private_disk, args.bridge, network)
    def cli(*arguments):
        return json.loads(run([str(bundle / "nico-simctl"), *arguments, "--base-url", "http://127.0.0.1:18089",
                               "--org", "novxwn552ssc"], "NICoSim request", timeout=40))
    instance_id = None
    with (work / "startup.log").open("wb") as log:
        process = subprocess.Popen([str(bundle / "nico-sim")], cwd=work, env=env,
                                   stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            start = time.monotonic()
            while True:
                if process.poll() is not None or time.monotonic() - start >= 30:
                    raise SmokeError("NICoSim failed its 30-second startup budget; see private startup.log")
                try:
                    with urllib.request.urlopen("http://127.0.0.1:18089/healthz", timeout=2) as response:
                        if json.load(response) != {"status": "ok"}:
                            raise SmokeError("unexpected health interface")
                    started = [json.loads(line) for line in (work / "startup.log").read_text().splitlines()
                               if line.startswith("{")]
                    if any(row.get("msg") == "starting nico-sim server" and
                           row.get("dbPath") == env["NICO_SIM_DB_PATH"] for row in started):
                        break
                except OSError:
                    time.sleep(.2)
            created = cli("instance", "create", "--name", "nkd-qcow-probe", "--tenant-id", TENANT,
                          "--instance-type-id", TYPE, "--vpc-id", VPC, "--subnet-id", SUBNET, "--user-data", userdata)
            # UUID handling uses the standard library, not name-pattern guessing.
            instance_id = str(uuid.UUID(created["id"]))
            print("Created probe-owned QCOW2 instance " + instance_id, flush=True)
            deadline = time.monotonic() + args.timeout
            while time.monotonic() < deadline:
                owned = [row for row in cli("instance", "list", "--site-id", SITE) if row["id"] == instance_id]
                if len(owned) != 1:
                    raise SmokeError("private simulator lost the exact probe instance")
                current = owned[0]
                (work / "instance.json").write_text(json.dumps(current, indent=2))
                status = current.get("status")
                if status == "Error":
                    raise SmokeError("NICoSim provisioning failed; see private startup.log")
                if status == "Ready":
                    break
                time.sleep(1)
            else:
                raise SmokeError("VM readiness budget expired; retained phase/console diagnostics")
            address = ipaddress.ip_address(current["interfaces"][0]["ipAddresses"][0])
            if address not in network or str(address) == gateway:
                raise SmokeError("VM address is outside the admitted probe bridge")
            command = ("set -eu; printf 'architecture\\n'; test $(uname -m) = aarch64; "
                       "printf 'cloud-init\\n'; cloud-init status --wait; printf 'TPM\\n'; sudo test -e /dev/tpm0; "
                       "printf 'agent\\n'; /usr/local/bin/nke-agent -version; "
                       "printf 'staged kubeadm\\n'; test -n \"$(apt-cache search --names-only '^nke-kubeadm-distro-[0-9]')\"; uname -r")
            for attempt in range(3):
                try:
                    run(["ssh", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=accept-new",
                        "-o", "UserKnownHostsFile=" + str(work / "known_hosts"), "-o", "ConnectTimeout=30",
                        "-i", str(work / "ssh-key"), "nkd-demo@" + str(address), command], "guest SSH/cloud-init/TPM probe", timeout=180,
                        diagnostic=work / "guest-check.log")
                    break
                except SmokeError as err:
                    if err.returncode != 255 or attempt == 2:
                        raise
                    time.sleep(3)
        finally:
            finish(process, cli, instance_id, work)
    if process.returncode != 0 or digest(disk) != image["digest"] or digest(private_disk) != image["digest"]:
        raise SmokeError("server shutdown or post-probe disk identity check failed")
    print("PASS: QCOW2 guest, key-only SSH, cloud-init, TPM, agent, staged Kubernetes package and owned cleanup.")
    print("Diagnostic only: full image smoke, site/tenant/workload acceptance are not established.")
    print("Private evidence: " + str(work))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("nkdctl", "simulator-run", "os-run", "out"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--bridge", default="nkdvm0")
    parser.add_argument("--cidr", default="172.30.0.0/24")
    parser.add_argument("--timeout", type=int, default=1200)
    args = parser.parse_args()
    def interrupted(_number, _frame):
        raise SmokeError("probe interrupted; cleaning owned resources")
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        probe(args)
    except (SmokeError, OSError, ValueError, KeyError, TypeError, tarfile.TarError, ET.ParseError) as err:
        print(f"NKD-E-PREFLIGHT-FAILED: NICoSim VM diagnostic: {err}", file=sys.stderr)
        return 17
    return 0


if __name__ == "__main__":
    sys.exit(main())
