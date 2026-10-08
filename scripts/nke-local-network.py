# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Scoped native networking for the downstream Colima/NICoSim demo.

This is a development host guard, not NKD authority or a networking protocol
implementation. iproute2, iptables and dnsmasq own their formats and behavior.
Use only in the dedicated single-user ARM64 Linux VM. Call close only after
every simulator/guest producer has joined. No existing bridge/policy is adopted.
"""

import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import stat
import subprocess
import sys
import uuid


SPEC = importlib.util.spec_from_file_location("nkd_network_management", Path(__file__).with_name("nke-local-management.py"))
MGMT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MGMT)
VM = MGMT.VM
BRIDGE = "nkdvm0"
CIDR = "172.30.0.0/24"
GATEWAY = "172.30.0.1"
VIP_CIDR = "172.31.0.0/24"
HELPER = Path("/usr/lib/qemu/qemu-bridge-helper")
POLICY = Path("/etc/qemu/bridge.conf")


def identity(path):
    value = path.lstat()
    if not stat.S_ISREG(value.st_mode):
        raise VM.SmokeError("host control must be a regular file: " + str(path))
    return value.st_dev, value.st_ino, stat.S_IMODE(value.st_mode), value.st_uid, value.st_gid


def private_identity(path):
    value = path.lstat()
    if (not stat.S_ISREG(value.st_mode) or value.st_nlink != 1 or value.st_uid != os.getuid() or
            stat.S_IMODE(value.st_mode) & 0o077):
        raise VM.SmokeError("capability target must be a single-link, current-user-owned private copy")
    return identity(path)


def rules(owner, kind_cidr):
    # Exact commands, with a unique comment, not a parser for iptables output.
    rows = []
    for protocol, port in (("udp", "67"), ("udp", "53"), ("tcp", "53"),
                           ("tcp", "7777"), ("tcp", "10179"), ("tcp", "18080")):
        rows.append(("filter", "INPUT", ["-i", BRIDGE, "-p", protocol, "--dport", port, "-j", "ACCEPT"]))
    rows += [
        ("filter", "FORWARD", ["-i", BRIDGE, "-s", CIDR, "-j", "ACCEPT"]),
        ("filter", "FORWARD", ["-o", BRIDGE, "-d", CIDR, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"]),
        ("filter", "FORWARD", ["-o", BRIDGE, "-s", kind_cidr, "-d", VIP_CIDR, "-p", "tcp", "--dport", "6443", "-j", "ACCEPT"]),
        ("nat", "POSTROUTING", ["-s", CIDR, "!", "-d", CIDR, "-j", "MASQUERADE"]),
    ]
    for port, target in (("80", "7777"), ("179", "10179")):
        rows.append(("nat", "PREROUTING", ["-i", BRIDGE, "-d", GATEWAY, "-p", "tcp", "--dport", port,
                                           "-j", "REDIRECT", "--to-port", target]))
    return [(table, chain, ["-m", "comment", "--comment", owner, *arguments]) for table, chain, arguments in rows]


class OwnedNetwork:
    def __init__(self, work, env, kind_cidr):
        self.work = Path(work).resolve(strict=True)
        self.env = env
        self.owner = "nkd-demo-" + uuid.uuid4().hex
        # Creation-time explicit, locally administered unicast address. Unlike
        # alias (ignored during creation by this kernel), this binds ambiguous
        # outcomes before a successful ifindex readback. iproute2 owns encoding.
        marker = bytearray(uuid.uuid4().bytes[:6])
        marker[0] = (marker[0] | 2) & 0xfe
        self.bridge_mac = marker.hex(":")
        self.kind_cidr = str(ipaddress.ip_network(kind_cidr, strict=True))
        self.sequence = 0
        self.bridge_id = None
        self.bridge_intent = False
        self.policy_id = None
        self.staging = None
        self.helper_id = None
        self.helper_hash = None
        self.helper_granted = False
        self.sysctls = {}
        self.sysctl_intents = []
        self.rule_intents = []
        self.caps = []
        self.dns = None
        self.log = None
        self.lock = None
        self.cleaning = False

    def command(self, argv, privileged=False):
        self.sequence += 1
        if privileged:
            # The root child owns a shorter TERM/KILL deadline as well. A
            # caller cannot rely on user signals terminating a privileged child.
            argv = ["sudo", "-n", "timeout", "--signal=TERM", "--kill-after=5", "25", *argv]
        try:
            return MGMT.capture(argv, "network-%03d" % self.sequence, self.work, self.env, 40, volatile=self.cleaning)
        except PermissionError as err:
            raise MGMT.UnjoinedProcess("privileged network helper could not be joined; retaining host setup") from err

    def rule_exists(self, row):
        table, chain, arguments = row
        try:
            self.command(["iptables", "-w", "5", "-t", table, "-C", chain, *arguments], True)
            return True
        except VM.SmokeError as err:
            if isinstance(err, MGMT.UnjoinedProcess) or err.returncode != 1:
                raise
            return False

    def allow_guest_tcp(self, address, ports):
        """Explicit diagnostic exception for Docker's raw direct-routing gate.

        Bind one admitted Kind address and exact TCP ports, not the subnet or
        all container traffic. No Docker rule or daemon setting is modified.
        The existing intent ledger owns rollback even after an ambiguous insert.
        """
        target = ipaddress.IPv4Address(address)
        network = ipaddress.ip_network(self.kind_cidr)
        if (target not in network or target in (network.network_address, network.broadcast_address) or
                not isinstance(ports, tuple) or not ports or
                any(type(port) is not int or not 1 <= port <= 65535 for port in ports) or
                len(set(ports)) != len(ports)):
            raise VM.SmokeError("guest TCP exception requires one Kind host and unique exact ports")
        if self.cleaning or not self.bridge_intent or self.bridge_binding() is None:
            raise VM.SmokeError("guest TCP exception requires this active owned bridge")
        for port in sorted(ports):
            row = ("raw", "PREROUTING", ["-m", "comment", "--comment", self.owner,
                "-i", BRIDGE, "-s", CIDR, "-d", str(target), "-p", "tcp", "--dport", str(port), "-j", "ACCEPT"])
            if row in self.rule_intents or self.rule_exists(row):
                raise VM.SmokeError("fresh guest TCP exception unexpectedly exists")
            self.rule_intents.append(row)
            table, chain, arguments = row
            self.command(["iptables", "-w", "5", "-t", table, "-I", chain, *arguments], True)
            if not self.rule_exists(row):
                raise VM.SmokeError("guest TCP exception insert did not survive readback")

    def revoke_guest_tcp(self):
        # The node address must not be released while a guest exception remains.
        # Keep the ledger for outer rollback/readback, including partial inserts.
        previous, self.cleaning = self.cleaning, True
        try:
            for row in reversed(self.rule_intents):
                table, chain, arguments = row
                if (table, chain) != ("raw", "PREROUTING"):
                    continue
                if self.rule_exists(row):
                    self.command(["iptables", "-w", "5", "-t", table, "-D", chain, *arguments], True)
                if self.rule_exists(row):
                    raise VM.SmokeError("guest TCP exception remains before node release")
        finally:
            self.cleaning = previous

    def admit(self):
        if platform.system() != "Linux" or platform.machine() != "aarch64" or sys.version_info < (3, 12):
            raise VM.SmokeError("network guard requires dedicated ARM64 Linux/Colima and Python 3.12+")
        if self.work.stat().st_mode & 0o077:
            raise VM.SmokeError("network diagnostic directory must be private")
        for name in ("sudo", "timeout", "ip", "iptables", "dnsmasq", "setcap", "getcap", "virsh", "ps", "sysctl"):
            if shutil.which(name) is None:
                raise VM.SmokeError("missing native network prerequisite: " + name)
        import fcntl  # Linux-only standard-library kernel lock, no linked dependency.
        fd = os.open("/tmp/nkd-local-network-%d.lock" % os.getuid(), os.O_CREAT | os.O_NOFOLLOW | os.O_RDWR | os.O_CLOEXEC, 0o600)
        self.lock = os.fdopen(fd, "r+")
        lock_stat = os.fstat(fd)
        if not stat.S_ISREG(lock_stat.st_mode) or lock_stat.st_uid != os.getuid() or stat.S_IMODE(lock_stat.st_mode) != 0o600:
            raise VM.SmokeError("network guard lock ownership differs")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as err:
            raise VM.SmokeError("another local network guard is active") from err
        self.command(["true"], True)
        if BRIDGE in [row["ifname"] for row in json.loads(self.command(["ip", "-j", "link", "show"]))]:
            raise VM.SmokeError("refusing a pre-existing demo bridge")
        if POLICY.exists() or POLICY.is_symlink():
            raise VM.SmokeError("refusing a pre-existing QEMU bridge policy")
        self.helper_id, self.helper_hash = identity(HELPER), VM.digest(HELPER)
        if self.helper_id[2:] != (0o755, 0, 0):
            raise VM.SmokeError("QEMU helper must initially be root-owned mode 0755")
        if self.command(["virsh", "-c", "qemu:///session", "list", "--all", "--name"]).strip():
            raise VM.SmokeError("refusing a nonempty libvirt session")
        if "nico-sim" in self.command(["ps", "-eo", "comm="]).split():
            raise VM.SmokeError("refusing an already running simulator")
        for route in json.loads(self.command(["ip", "-j", "route", "show", "table", "main"])):
            if route.get("dst", "default") == "default":
                continue
            network = ipaddress.ip_network(route["dst"], strict=False)
            if any(network.overlaps(ipaddress.ip_network(cidr)) for cidr in (CIDR, VIP_CIDR)):
                raise VM.SmokeError("existing native route overlaps the demo guest/VIP ranges")
        for row in json.loads(self.command(["ip", "-j", "address", "show"])):
            if any(address.get("local") == "169.254.169.254" for address in row.get("addr_info", [])):
                raise VM.SmokeError("metadata link-local address is already assigned")
        kind = ipaddress.ip_network(self.kind_cidr)
        if kind.version != 4 or not kind.is_private or any(kind.overlaps(ipaddress.ip_network(cidr)) for cidr in (CIDR, VIP_CIDR)):
            raise VM.SmokeError("Kind network must be a separate private IPv4 range")
        for protocol, port in ((socket.SOCK_STREAM, 53), (socket.SOCK_DGRAM, 53), (socket.SOCK_DGRAM, 67),
                               (socket.SOCK_STREAM, 18080), (socket.SOCK_STREAM, 7777), (socket.SOCK_STREAM, 10179)):
            # Linux IP_FREEBIND admits this exact future bridge address without
            # creating it. The value 15 is the Linux UAPI constant (linux/in.h).
            # Other-address DNS listeners need not be adopted or stopped.
            with socket.socket(socket.AF_INET, protocol) as probe:
                probe.setsockopt(socket.SOL_IP, getattr(socket, "IP_FREEBIND", 15), 1)
                if port == 67:
                    # dnsmasq binds DHCP to its interface and shares this port
                    # with other bridges. Their listeners are not ours to stop.
                    # Actual DHCP delivery still requires a real guest test.
                    probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                probe.bind((GATEWAY, port))
        for key in ("net.ipv4.ip_forward", "net.ipv4.fib_multipath_hash_policy"):
            value = self.command(["sysctl", "-n", key]).strip()
            if value not in ("0", "1"):
                raise VM.SmokeError("unsupported host forwarding/hash configuration")
            self.sysctls[key] = value

    def start(self):
        self.bridge_intent = True
        self.command(["ip", "link", "add", "name", BRIDGE, "address", self.bridge_mac, "type", "bridge"], True)
        self.bridge_id = (Path("/sys/class/net") / BRIDGE / "ifindex").read_text().strip()
        self.bridge_binding()
        for address in (GATEWAY + "/24", "169.254.169.254/32"):
            self.command(["ip", "address", "add", address, "dev", BRIDGE], True)
        self.command(["ip", "link", "set", BRIDGE, "up"], True)
        policy = self.work / "bridge-policy"
        policy.write_text("allow " + BRIDGE + "\n")
        self.command(["install", "-d", "-m", "0755", "/etc/qemu"], True)
        if POLICY.exists() or POLICY.is_symlink():
            raise VM.SmokeError("bridge policy appeared during setup; refusing replacement")
        temporary = Path(self.command(["mktemp", "/etc/qemu/.nkd-policy-XXXXXXXX"], True).strip())
        if temporary.parent != POLICY.parent or not temporary.name.startswith(".nkd-policy-"):
            raise VM.SmokeError("root policy staging returned an unexpected path")
        self.staging = temporary, identity(temporary)
        self.command(["cp", str(policy), str(temporary)], True)
        self.command(["chmod", "0644", str(temporary)], True)
        self.policy_id = identity(temporary)
        self.command(["mv", "-T", "--no-clobber", str(temporary), str(POLICY)], True)
        if temporary.exists() or identity(POLICY) != self.policy_id:
            raise VM.SmokeError("policy appeared during no-replace setup; refusing adoption")
        if identity(HELPER) != self.helper_id or VM.digest(HELPER) != self.helper_hash:
            raise VM.SmokeError("QEMU helper changed before privilege grant")
        self.helper_granted = True
        self.command(["chmod", "4755", str(HELPER)], True)
        for key, original in self.sysctls.items():
            if original != "1":
                self.sysctl_intents.append(key)
                self.command(["sysctl", "-w", key + "=1"], True)
        for row in rules(self.owner, self.kind_cidr):
            if self.rule_exists(row):
                raise VM.SmokeError("fresh owned firewall rule unexpectedly exists")
            # Record intent before a possibly ambiguous command return; the
            # unique exact rule can be queried/removed during rollback.
            self.rule_intents.append(row)
            table, chain, arguments = row
            self.command(["iptables", "-w", "5", "-t", table, "-I", chain, *arguments], True)
        (self.work / "dhcp-hosts").mkdir(mode=0o700)
        (self.work / "leases").touch(mode=0o600)
        self.dns_binary = self.work / "dnsmasq"
        shutil.copyfile(shutil.which("dnsmasq"), self.dns_binary)
        self.dns_binary.chmod(0o700)
        self.grant(self.dns_binary, "cap_net_bind_service,cap_net_admin,cap_net_raw=ep")
        argv = [str(self.dns_binary), "--keep-in-foreground", "--conf-file=/dev/null", "--interface=" + BRIDGE,
                "--bind-interfaces", "--except-interface=lo", "--dhcp-range=172.30.0.10,172.30.0.254,255.255.255.0,30m",
                "--dhcp-hostsdir=" + str(self.work / "dhcp-hosts"), "--dhcp-leasefile=" + str(self.work / "leases"),
                "--dhcp-option=option:router," + GATEWAY, "--dhcp-option=option:dns-server," + GATEWAY,
                "--resolv-file=/etc/resolv.conf", "--no-hosts", "--address=/carbide-pxe.forge/" + GATEWAY,
                "--pid-file=", "--log-dhcp", "--log-facility=-", "--dhcp-ignore-clid"]
        self.log = (self.work / "dnsmasq.log").open("xb")
        self.dns = subprocess.Popen(argv, stdout=self.log, stderr=subprocess.STDOUT, env=self.env,
                                    cwd=self.work, start_new_session=True)

    def grant(self, path, capabilities):
        if not path.resolve(strict=True).is_relative_to(self.work) or path.is_symlink():
            raise VM.SmokeError("capability target must be this guard's private regular file")
        binding, digest = private_identity(path), VM.digest(path)
        if self.command(["getcap", str(path)]).strip():
            raise VM.SmokeError("private capability target already has privileges")
        # libcap's own verifier authenticates a joined ambiguous grant. Do not
        # reconstruct or approximately parse its capability text format.
        self.caps.append((path, binding, digest, capabilities))
        self.command(["setcap", capabilities, str(path)], True)
        self.command(["setcap", "-v", capabilities, str(path)], True)

    def bridge_binding(self):
        # Only a successfully returned complete inventory can prove absence.
        # A joined iproute2 error, including exit 1, is not an absence receipt.
        rows = json.loads(self.command(["ip", "-d", "-j", "link", "show"]))
        rows = [row for row in rows if row.get("ifname") == BRIDGE]
        if not rows:
            return None
        if (len(rows) != 1 or rows[0].get("address") != self.bridge_mac or
                rows[0].get("linkinfo", {}).get("info_kind") != "bridge" or
                (self.bridge_id is not None and str(rows[0].get("ifindex")) != self.bridge_id)):
            raise VM.SmokeError("bridge identity/owner changed; refusing restoration")
        return str(rows[0]["ifindex"])

    def close(self):
        self.cleaning = True
        try:
            if self.dns is not None:
                MGMT.stop_group(self.dns)
            if "nico-sim" in self.command(["ps", "-eo", "comm="]).split() or self.command(
                    ["virsh", "-c", "qemu:///session", "list", "--all", "--name"]).strip():
                raise VM.SmokeError("active/unknown simulator or guest; refusing network restoration")
            bridge = self.bridge_binding() if self.bridge_intent else None
            for path, binding, digest, capabilities in reversed(self.caps):
                if private_identity(path) != binding or VM.digest(path) != digest:
                    raise VM.SmokeError("capability target changed; refusing restoration")
                current = self.command(["getcap", str(path)]).strip()
                if current:
                    self.command(["setcap", "-v", capabilities, str(path)], True)
                    self.command(["setcap", "-r", str(path)], True)
                if self.command(["getcap", str(path)]).strip():
                    raise VM.SmokeError("private capability remains")
            for row in reversed(self.rule_intents):
                if self.rule_exists(row):
                    table, chain, arguments = row
                    self.command(["iptables", "-w", "5", "-t", table, "-D", chain, *arguments], True)
                if self.rule_exists(row):
                    raise VM.SmokeError("owned firewall rule remains")
            for key in reversed(self.sysctl_intents):
                original = self.sysctls[key]
                current = self.command(["sysctl", "-n", key]).strip()
                if current not in (original, "1"):
                    raise VM.SmokeError("host sysctl changed; refusing restoration")
                if current != original:
                    self.command(["sysctl", "-w", key + "=" + original], True)
            if self.helper_granted:
                current = identity(HELPER)
                if (current[:2] != self.helper_id[:2] or current[2:] not in ((0o4755, 0, 0), (0o755, 0, 0)) or
                        VM.digest(HELPER) != self.helper_hash):
                    raise VM.SmokeError("privileged QEMU helper changed; refusing restoration")
                self.command(["chmod", "0755", str(HELPER)], True)
            if self.policy_id is not None:
                if identity(POLICY) != self.policy_id or POLICY.read_text() != "allow " + BRIDGE + "\n":
                    raise VM.SmokeError("bridge policy changed; refusing restoration")
                archived = self.work / "bridge-policy.disabled"
                if archived.exists() or archived.is_symlink():
                    raise VM.SmokeError("policy archive exists; refusing replacement")
                self.command(["mv", "-T", "--no-clobber", str(POLICY), str(archived)], True)
                if POLICY.exists() or POLICY.is_symlink():
                    raise VM.SmokeError("bridge policy remains")
            if self.staging is not None and self.staging[0].exists():
                temporary, original = self.staging
                current = identity(temporary)
                if current[:2] != original[:2] or current[3:] != (0, 0):
                    raise VM.SmokeError("root policy staging changed; refusing restoration")
                archived = self.work / "bridge-policy.staging"
                if archived.exists() or archived.is_symlink():
                    raise VM.SmokeError("root policy staging archive already exists")
                self.command(["mv", "-T", "--no-clobber", str(temporary), str(archived)], True)
                if temporary.exists():
                    raise VM.SmokeError("root policy staging remains")
            if bridge is not None:
                if self.bridge_binding() != bridge:
                    raise VM.SmokeError("bridge identity changed during restoration")
                self.command(["ip", "link", "delete", BRIDGE, "type", "bridge"], True)
                if self.bridge_binding() is not None:
                    raise VM.SmokeError("owned bridge remains")
        finally:
            if self.log is not None:
                self.log.close()
            if self.lock is not None:
                self.lock.close()
