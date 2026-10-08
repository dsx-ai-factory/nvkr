# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Scoped native networking for the downstream Colima/NICoSim demo.

This is a development host guard, not NKD authority or a networking protocol
implementation. iproute2, iptables and dnsmasq own their formats and behavior.
Use only in the dedicated single-user ARM64 Linux VM. Call close only after
every simulator/guest producer has joined. No existing bridge/policy is adopted.
"""

import importlib.util
import hashlib
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
import time
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
STATE = "network-state.json"
SYSCTLS = ("net.ipv4.ip_forward", "net.ipv4.fib_multipath_hash_policy")
CAPABILITIES = {"dnsmasq": "cap_net_bind_service,cap_net_admin,cap_net_raw=ep", "nico-sim": "cap_net_admin=ep"}
TOOLS = ("sudo", "timeout", "ip", "iptables", "dnsmasq", "setcap", "getcap", "virsh", "ps", "sysctl",
         "cp", "chmod", "install", "mv", "sha256sum", "unlink", "stat")


def boot_id():
    return str(uuid.UUID(Path("/proc/sys/kernel/random/boot_id").read_text().strip()))


def checked_work(path):
    path = Path(path).absolute()
    value = path.lstat()
    if (not stat.S_ISDIR(value.st_mode) or value.st_uid != os.getuid() or
            stat.S_IMODE(value.st_mode) != 0o700):
        raise VM.SmokeError("recovery requires the original private current-user-owned directory")
    return path.resolve(strict=True)


def read_state(work):
    path = work / STATE
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as held:
        value = os.fstat(held.fileno())
        if (not stat.S_ISREG(value.st_mode) or value.st_nlink != 1 or value.st_uid != os.getuid() or
                stat.S_IMODE(value.st_mode) != 0o600 or value.st_size > 65536):
            raise VM.SmokeError("recovery journal must be a bounded private single-link regular file")
        raw = held.read(65537)
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise VM.SmokeError("duplicate recovery field")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


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
        self.journal = False
        self.finished = False
        self.bgp_intent = False
        self.origin_boot = None
        self.origin_pid = os.getpid()
        self.persistence_failures = []

    def cleanup_checkpoint(self):
        # Durable grant intents already exist. A full/broken diagnostic volume
        # must not prevent withdrawing live privileges after producers join.
        try:
            self.checkpoint()
        except (OSError, VM.SmokeError) as err:
            self.persistence_failures.append(str(err))

    def checkpoint(self):
        # Local operator recovery hints, never an NKD receipt or arbitrary argv.
        # Persist intentions before privilege changes, and fsync rename + parent.
        if not self.journal:
            return
        value = {"version": 1, "work": str(self.work), "uid": os.getuid(), "boot": self.origin_boot,
            "pid": self.origin_pid, "owner": self.owner, "kindCIDR": self.kind_cidr,
            "bridgeMAC": self.bridge_mac, "bridgeID": self.bridge_id, "bridgeIntent": self.bridge_intent,
            "policyID": self.policy_id, "staging": None if self.staging is None else [str(self.staging[0]), self.staging[1]],
            "helperID": self.helper_id, "helperHash": self.helper_hash, "helperGranted": self.helper_granted,
            "sysctls": self.sysctls, "sysctlIntents": self.sysctl_intents, "rules": self.rule_intents,
            "caps": [[str(path), binding, digest, caps] for path, binding, digest, caps in self.caps],
            "finished": self.finished, "bgpIntent": self.bgp_intent}
        target = self.work / STATE
        if target.exists() or target.is_symlink():
            private_identity(target)
        temporary = self.work / (".network-state-" + uuid.uuid4().hex)
        try:
            with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as held:
                held.write(json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode())
                held.flush()
                os.fsync(held.fileno())
            os.replace(temporary, target)
            fd = os.open(self.work, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        finally:
            if temporary.exists():
                temporary.unlink()

    def acquire_lock(self):
        import fcntl  # Linux kernel lock, released even on SIGKILL/reboot.
        fd = os.open("/tmp/nkd-local-network-%d.lock" % os.getuid(), os.O_CREAT | os.O_NOFOLLOW | os.O_RDWR | os.O_CLOEXEC, 0o600)
        self.lock = os.fdopen(fd, "r+")
        value = os.fstat(fd)
        if (not stat.S_ISREG(value.st_mode) or value.st_nlink != 1 or value.st_uid != os.getuid() or
                stat.S_IMODE(value.st_mode) != 0o600):
            raise VM.SmokeError("network guard lock ownership differs")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as err:
            raise VM.SmokeError("another local network guard is active") from err

    def __enter__(self):
        try:
            self.admit()
        except BaseException:
            if self.lock is not None:
                self.lock.close()
            raise
        return self

    def __exit__(self, _kind, _value, _traceback):
        # Caller must stop/join producers first. close independently refuses
        # active/unknown simulator and libvirt resources.
        self.close()

    @classmethod
    def recover(cls, work, env):
        if platform.system() != "Linux" or platform.machine() != "aarch64" or sys.version_info < (3, 12):
            raise VM.SmokeError("recovery requires dedicated ARM64 Linux/Colima and Python 3.12+")
        for name in TOOLS:
            if shutil.which(name) is None:
                raise VM.SmokeError("missing native recovery prerequisite: " + name)
        work = checked_work(work)
        row = read_state(work)
        fields = {"version", "work", "uid", "boot", "pid", "owner", "kindCIDR", "bridgeMAC", "bridgeID", "bridgeIntent",
            "policyID", "staging", "helperID", "helperHash", "helperGranted", "sysctls", "sysctlIntents", "rules", "caps", "finished", "bgpIntent"}
        if (not isinstance(row, dict) or set(row) != fields or type(row["version"]) is not int or row["version"] != 1 or
                row["work"] != str(work) or type(row["uid"]) is not int or row["uid"] != os.getuid() or
                type(row["pid"]) is not int or row["pid"] <= 0 or
                any(type(row[name]) is not bool for name in ("bridgeIntent", "helperGranted", "finished", "bgpIntent"))):
            raise VM.SmokeError("recovery journal version, owner or fields differ")
        original_boot = str(uuid.UUID(row["boot"]))
        owner = "nkd-demo-" + uuid.UUID(hex=row["owner"].removeprefix("nkd-demo-")).hex
        if owner != row["owner"]:
            raise VM.SmokeError("invalid recovery firewall owner")
        guard = cls(work, env, row["kindCIDR"])
        kind = ipaddress.ip_network(guard.kind_cidr)
        if (kind.version != 4 or not kind.is_private or any(kind.overlaps(ipaddress.ip_network(cidr)) for cidr in (CIDR, VIP_CIDR))):
            raise VM.SmokeError("recovery Kind network differs from the supported separate private range")
        mac = bytes.fromhex(row["bridgeMAC"].replace(":", ""))
        if len(mac) != 6 or mac.hex(":") != row["bridgeMAC"] or mac[0] & 3 != 2:
            raise VM.SmokeError("invalid recovery bridge marker")
        bridge_id = row["bridgeID"]
        if bridge_id is not None and (not isinstance(bridge_id, str) or not bridge_id.isascii() or
                                     not bridge_id.isdecimal() or int(bridge_id) <= 0):
            raise VM.SmokeError("invalid recovery bridge index")
        def binding(value):
            if (not isinstance(value, list) or len(value) != 5 or any(type(part) is not int or part < 0 for part in value) or
                    value[1] <= 0 or value[2] > 0o7777):
                raise VM.SmokeError("invalid recovery file identity")
            return tuple(value)
        def digest(value):
            # VM.digest owns the exact-blob spelling written by checkpoint().
            if (not isinstance(value, str) or not value.startswith("sha256:") or len(value) != 71 or
                    any(char not in "0123456789abcdef" for char in value[7:])):
                raise VM.SmokeError("invalid recovery exact-byte digest")
            return value
        guard.owner, guard.bridge_mac, guard.bridge_id = owner, row["bridgeMAC"], bridge_id
        guard.bridge_intent, guard.helper_granted, guard.finished = row["bridgeIntent"], row["helperGranted"], row["finished"]
        guard.bgp_intent = row["bgpIntent"]
        guard.helper_id, guard.helper_hash = binding(row["helperID"]), digest(row["helperHash"])
        if guard.helper_id[2:] != (0o755, 0, 0):
            raise VM.SmokeError("unsupported original QEMU helper identity")
        if row["policyID"] is not None:
            guard.policy_id = binding(row["policyID"])
            if guard.policy_id[2:] != (0o644, 0, 0):
                raise VM.SmokeError("unsupported bridge policy identity")
        if row["staging"] is not None:
            staging = row["staging"]
            if not isinstance(staging, list) or len(staging) != 2:
                raise VM.SmokeError("invalid root staging identity")
            path, held = Path(staging[0]), None if staging[1] is None else binding(staging[1])
            if (path != POLICY.parent / (".nkd-policy-" + owner.removeprefix("nkd-demo-")) or
                    (held is not None and held[2:] != (0o600, 0, 0))):
                raise VM.SmokeError("unsupported root staging path")
            guard.staging = path, held
        if (not isinstance(row["sysctls"], dict) or set(row["sysctls"]) != set(SYSCTLS) or
                any(value not in ("0", "1") for value in row["sysctls"].values()) or
                not isinstance(row["sysctlIntents"], list) or len(set(row["sysctlIntents"])) != len(row["sysctlIntents"]) or
                any(key not in SYSCTLS or row["sysctls"][key] != "0" for key in row["sysctlIntents"])):
            raise VM.SmokeError("unsupported recovery sysctl intent")
        guard.sysctls, guard.sysctl_intents = row["sysctls"], row["sysctlIntents"]
        if not isinstance(row["rules"], list) or len(row["rules"]) > 14:
            raise VM.SmokeError("recovery firewall intent exceeds supported closure")
        allowed = rules(owner, guard.kind_cidr)
        for value in row["rules"]:
            if not isinstance(value, list) or len(value) != 3 or not isinstance(value[2], list):
                raise VM.SmokeError("invalid recovery firewall intent")
            candidate = tuple(value)
            if candidate not in allowed:
                argv = candidate[2]
                if len(argv) != 16:
                    raise VM.SmokeError("unknown recovery firewall command")
                address = ipaddress.IPv4Address(argv[9])
                expected = ["-m", "comment", "--comment", owner, "-i", BRIDGE, "-s", CIDR, "-d", str(address),
                            "-p", "tcp", "--dport", argv[13], "-j", "ACCEPT"]
                # Only the two exact source-owned SPIRE exceptions are admitted.
                if (candidate[:2] != ("raw", "PREROUTING") or argv != expected or argv[13] not in ("30440", "30443") or
                        address not in kind or address in (kind.network_address, kind.broadcast_address)):
                    raise VM.SmokeError("unknown recovery guest TCP intent")
            if candidate in guard.rule_intents:
                raise VM.SmokeError("duplicate recovery firewall intent")
            guard.rule_intents.append(candidate)
        if not isinstance(row["caps"], list) or len(row["caps"]) > 2:
            raise VM.SmokeError("recovery capability intent exceeds supported closure")
        seen = set()
        for value in row["caps"]:
            if not isinstance(value, list) or len(value) != 4:
                raise VM.SmokeError("invalid recovery capability intent")
            path, held, hashed, caps = Path(value[0]), binding(value[1]), digest(value[2]), value[3]
            if (path.parent != work or path.name not in CAPABILITIES or caps != CAPABILITIES[path.name] or
                    held[2] != 0o700 or held[3] != os.getuid() or path.name in seen):
                raise VM.SmokeError("unsupported recovery capability target")
            seen.add(path.name)
            guard.caps.append((path, held, hashed, caps))
        guard.origin_boot, guard.origin_pid = original_boot, row["pid"]
        try:
            guard.acquire_lock()
            if original_boot == boot_id():
                try:
                    os.kill(guard.origin_pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    raise VM.SmokeError("original PID is still present; refusing recovery or PID-based killing")
            if guard.finished:
                return guard
            # Recovery must not reuse the crashed owner's diagnostic filenames
            # or depend on its log volume while authenticating protected tasks.
            guard.cleaning = True
            # A SIGKILL releases our lock before an in-flight sudo timeout exits.
            # Its root-owned bound is 25 + 5 seconds; conservatively outwait it.
            if original_boot == boot_id():
                time.sleep(40)
            # Never signal serialized PIDs. The operator must stop/join orphan
            # producers; exact private executable and listener checks fail closed.
            targets = {tuple(held[:2]) for _path, held, _digest, _caps in guard.caps}
            deadline = time.monotonic() + 15
            for process in Path("/proc").iterdir():
                if time.monotonic() > deadline:
                    raise VM.SmokeError("recovery producer inventory exceeded its bound")
                if not process.name.isdecimal():
                    continue
                try:
                    value = (process / "exe").stat()
                except (FileNotFoundError, ProcessLookupError):
                    continue
                except PermissionError:
                    if process.stat().st_uid == os.getuid():
                        # Same-user SSH/PAM processes can be non-dumpable.
                        # Coreutils stat owns this read-only kernel metadata
                        # query; never trust comm/argv or kill a discovered PID.
                        metadata = guard.command(["stat", "--dereference", "--format=%d %i", "--",
                                                  str(process / "exe")], True).split()
                        if (len(metadata) != 2 or any(not value.isascii() or not value.isdecimal()
                                                      for value in metadata)):
                            raise VM.SmokeError("cannot authenticate protected recovery executable metadata")
                        if tuple(map(int, metadata)) in targets:
                            raise VM.SmokeError("private capability-bearing producer still runs; stop/join it before recovery")
                    continue
                if (value.st_dev, value.st_ino) in targets:
                    raise VM.SmokeError("private capability-bearing producer still runs; stop/join it before recovery")
            with socket.socket() as listener:
                listener.setsockopt(socket.SOL_IP, getattr(socket, "IP_FREEBIND", 15), 1)
                listener.bind((GATEWAY, 18080))
            guard.cleaning, guard.journal = True, True
            return guard
        except BaseException:
            if guard.lock is not None:
                guard.lock.close()
            raise

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
            self.checkpoint()
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
        for name in TOOLS:
            if shutil.which(name) is None:
                raise VM.SmokeError("missing native network prerequisite: " + name)
        checked_work(self.work)
        self.acquire_lock()
        if (self.work / STATE).exists() or (self.work / STATE).is_symlink():
            raise VM.SmokeError("recovery journal already exists; use restore with the original work directory")
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
        for key in SYSCTLS:
            value = self.command(["sysctl", "-n", key]).strip()
            if value not in ("0", "1"):
                raise VM.SmokeError("unsupported host forwarding/hash configuration")
            self.sysctls[key] = value
        self.origin_boot = boot_id()
        self.journal = True
        self.checkpoint()

    def start(self):
        self.bridge_intent = True
        self.checkpoint()
        self.command(["ip", "link", "add", "name", BRIDGE, "address", self.bridge_mac, "type", "bridge"], True)
        self.bridge_id = (Path("/sys/class/net") / BRIDGE / "ifindex").read_text().strip()
        self.checkpoint()
        self.bridge_binding()
        for address in (GATEWAY + "/24", "169.254.169.254/32"):
            self.command(["ip", "address", "add", address, "dev", BRIDGE], True)
        self.command(["ip", "link", "set", BRIDGE, "up"], True)
        policy = self.work / "bridge-policy"
        with os.fdopen(os.open(policy, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as held:
            held.write("allow " + BRIDGE + "\n")
        self.command(["install", "-d", "-m", "0755", "/etc/qemu"], True)
        if POLICY.exists() or POLICY.is_symlink():
            raise VM.SmokeError("bridge policy appeared during setup; refusing replacement")
        temporary = POLICY.parent / (".nkd-policy-" + self.owner.removeprefix("nkd-demo-"))
        if temporary.exists() or temporary.is_symlink():
            raise VM.SmokeError("fresh attempt-bound root staging already exists")
        self.staging = temporary, None
        self.checkpoint()
        self.command(["cp", "--no-clobber", "--no-dereference", "--preserve=mode", str(policy), str(temporary)], True)
        self.staging = temporary, self.stage_binding()
        self.checkpoint()
        self.command(["chmod", "0644", str(temporary)], True)
        self.policy_id = identity(temporary)
        self.checkpoint()
        self.command(["mv", "-T", "--no-clobber", str(temporary), str(POLICY)], True)
        if temporary.exists() or identity(POLICY) != self.policy_id:
            raise VM.SmokeError("policy appeared during no-replace setup; refusing adoption")
        if identity(HELPER) != self.helper_id or VM.digest(HELPER) != self.helper_hash:
            raise VM.SmokeError("QEMU helper changed before privilege grant")
        self.helper_granted = True
        self.checkpoint()
        self.command(["chmod", "4755", str(HELPER)], True)
        for key, original in self.sysctls.items():
            if original != "1":
                self.sysctl_intents.append(key)
                self.checkpoint()
                self.command(["sysctl", "-w", key + "=1"], True)
        for row in rules(self.owner, self.kind_cidr):
            if self.rule_exists(row):
                raise VM.SmokeError("fresh owned firewall rule unexpectedly exists")
            # Record intent before a possibly ambiguous command return; the
            # unique exact rule can be queried/removed during rollback.
            self.rule_intents.append(row)
            self.checkpoint()
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
        self.checkpoint()
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

    def stage_binding(self):
        path, original = self.staging
        current = identity(path)
        if (path.lstat().st_nlink != 1 or current[2:] not in ((0o600, 0, 0), (0o644, 0, 0)) or
                (original is not None and current[:2] != original[:2])):
            raise VM.SmokeError("root policy staging changed; refusing restoration")
        expected = hashlib.sha256(("allow " + BRIDGE + "\n").encode()).hexdigest()
        if self.command(["sha256sum", str(path)], True) != expected + "  " + str(path) + "\n":
            raise VM.SmokeError("root staging bytes differ from this attempt's policy")
        return current

    def stop_dns(self):
        # Process ownership is independent of permission to restore host state.
        if self.dns is not None:
            MGMT.stop_group(self.dns)
            self.dns = None

    def require_idle(self):
        if "nico-sim" in self.command(["ps", "-eo", "comm="]).split() or self.command(
                ["virsh", "-c", "qemu:///session", "list", "--all", "--name"]).strip():
            raise VM.SmokeError("active/unknown simulator or guest; refusing network restoration")

    def close(self):
        self.cleaning = True
        try:
            self.stop_dns()
            self.require_idle()
            bridge = self.bridge_binding() if self.bridge_intent else None
            for path, binding, digest, capabilities in reversed(list(self.caps)):
                if private_identity(path) != binding or VM.digest(path) != digest:
                    raise VM.SmokeError("capability target changed; refusing restoration")
                current = self.command(["getcap", str(path)]).strip()
                if current:
                    self.command(["setcap", "-v", capabilities, str(path)], True)
                    self.command(["setcap", "-r", str(path)], True)
                if self.command(["getcap", str(path)]).strip():
                    raise VM.SmokeError("private capability remains")
                self.cleanup_checkpoint()
            for row in reversed(list(self.rule_intents)):
                if self.rule_exists(row):
                    table, chain, arguments = row
                    self.command(["iptables", "-w", "5", "-t", table, "-D", chain, *arguments], True)
                if self.rule_exists(row):
                    raise VM.SmokeError("owned firewall rule remains")
                self.cleanup_checkpoint()
            for key in reversed(list(self.sysctl_intents)):
                original = self.sysctls[key]
                current = self.command(["sysctl", "-n", key]).strip()
                if current not in (original, "1"):
                    raise VM.SmokeError("host sysctl changed; refusing restoration")
                if current != original:
                    self.command(["sysctl", "-w", key + "=" + original], True)
                if self.command(["sysctl", "-n", key]).strip() != original:
                    raise VM.SmokeError("original sysctl value did not survive restoration")
                self.cleanup_checkpoint()
            if self.helper_granted:
                current = identity(HELPER)
                if (current[:2] != self.helper_id[:2] or current[2:] not in ((0o4755, 0, 0), (0o755, 0, 0)) or
                        VM.digest(HELPER) != self.helper_hash):
                    raise VM.SmokeError("privileged QEMU helper changed; refusing restoration")
                self.command(["chmod", "0755", str(HELPER)], True)
                if identity(HELPER) != self.helper_id or VM.digest(HELPER) != self.helper_hash:
                    raise VM.SmokeError("original QEMU helper did not survive restoration")
                self.helper_granted = False
                self.cleanup_checkpoint()
            if self.policy_id is not None:
                if POLICY.exists() or POLICY.is_symlink():
                    if identity(POLICY) != self.policy_id or POLICY.read_text() != "allow " + BRIDGE + "\n":
                        raise VM.SmokeError("bridge policy changed; refusing restoration")
                    self.command(["unlink", str(POLICY)], True)
                    if POLICY.exists() or POLICY.is_symlink():
                        raise VM.SmokeError("bridge policy remains")
                self.policy_id = None
                self.cleanup_checkpoint()
            if self.staging is not None and (self.staging[0].exists() or self.staging[0].is_symlink()):
                temporary = self.staging[0]
                self.stage_binding()
                self.command(["unlink", str(temporary)], True)
                if temporary.exists():
                    raise VM.SmokeError("root policy staging remains")
            self.staging = None
            self.cleanup_checkpoint()
            if bridge is not None:
                if self.bridge_binding() != bridge:
                    raise VM.SmokeError("bridge identity changed during restoration")
                self.command(["ip", "link", "delete", BRIDGE, "type", "bridge"], True)
                if self.bridge_binding() is not None:
                    raise VM.SmokeError("owned bridge remains")
            self.bridge_intent, self.bridge_id, self.finished = False, None, True
            self.cleanup_checkpoint()
            if self.persistence_failures:
                raise VM.SmokeError("host controls restored but recovery journal persistence failed: " +
                                    "; ".join(self.persistence_failures))
        finally:
            if self.log is not None:
                self.log.close()
            if self.lock is not None:
                self.lock.close()
