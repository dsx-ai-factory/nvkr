# Boot the existing NKD QCOW2 through NICoSim

This is the artifact-only guest diagnostic for the requested laptop demo, not
the complete demo. It uses existing completed ARM64 simulator and OS Runs, with
no Make, Go, Packer or application rebuild. Run inside a dedicated Linux ARM64
Colima VM. Docker alone does not provide the native libvirt/TPM prerequisites.

The command checks the local completed-Run descriptions and disk hash/size,
uses NKD's delivery owner to verify/extract the simulator bundle, then boots a
private byte-identical disk copy through the actual source-built NICoSim API.
It checks key-only SSH, cloud-init, software TPM, the installed NKE agent and
the staged Kubernetes package. It deletes only its owned VM and stops/joins
the server. Private logs, the disk copy and ephemeral SSH key remain in the
fresh output directory. Keep that directory out of Git and shared logs.

These observations are unqualified local diagnostics. They do not authenticate
a Release, pass NKE's complete OS smoke, attest a tenant, install a management
plane or prove a Kubernetes workload. The missing DSX input still blocks the
complete image contract. See the [build guide](local-development.md) and
[downstream handoff](nkd-to-nke-demo.md). No private guest acceptance record is
shipped here; run the command's assertions against your own completed Runs.

## Prepare the dedicated Linux VM once

On macOS, enter the explicitly selected profile:

```sh
colima ssh --profile nkd-demo
```

Inside that VM, inspect current networks and the session before changing them:

```sh
ip -br address
ip route
virsh -c qemu:///session list --all
```

If libvirt is not installed yet, the last command fails; install the prerequisites
below, then repeat it. The probe requires an empty session and never adopts
existing VMs. Confirm `172.30.0.0/24` is unused; it must not overlap the pod or
service ranges. The earlier startup-only `192.168.122.0/24` network is not
appropriate for this tenant path because it overlaps the default pod range.

For the tested Ubuntu 24.04 ARM64 host, install its native packages:

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends \
  libvirt-daemon-system libvirt-clients qemu-system-arm qemu-utils \
  qemu-efi-aarch64 cloud-image-utils swtpm swtpm-tools gnutls-bin ipxe-qemu
```

This is operator-managed host setup from Ubuntu repositories, not a pinned NKD
toolchain or hermetic artifact build. Python 3.12+, OpenSSH, `ip`, and a previously
built Linux ARM64 `nkdctl` must also be available. `gnutls-bin` supplies TPM
certificate tooling; `ipxe-qemu` supplies the virtio EFI ROM required by the
tested libvirt-generated domain. Package scripts can enable libvirtd and its
default network. Inspect those host effects separately from guest acceptance.

The session backend needs a pre-created bridge and QEMU's package-owned bridge
helper. The guarded workflow below makes an explicit temporary privilege grant
inside the dedicated single-user VM, refuses existing policy/network resources,
and restores its identified changes after success or failure. Do not use it on
a shared host, change Docker socket permissions, or replace networking policy.
The Python command itself does not install packages, create bridges, add capabilities,
or change system permissions. BGP, DHCP, NAT, link-local metadata and tenant
networking are deliberately outside this narrow static-IP guest probe.

## Run without rebuilding

From the native Linux NKD source export, select completed attempts and a new
output directory outside both input Runs:

```sh
(
set -euo pipefail
NKD_SIM_ATTEMPT=laptop-colima-nicosim-assets-arm64-20261008-01
NKD_OS_ATTEMPT=laptop-colima-os-arm64-20261008-04
NKD_VM_PROBE="$(pwd)/.nkd/vm-probe-01"

# Admit before granting privileges. Retained policy archive is private.
test ! -e "$NKD_VM_PROBE"
test ! -L "$NKD_VM_PROBE"
test ! -e /etc/qemu/bridge.conf
test ! -L /etc/qemu/bridge.conf
if ip link show nkdvm0 >/dev/null 2>&1; then
  echo 'Refusing an existing bridge' >&2; exit 17
fi
test "$(stat -c '%a %U %G' /usr/lib/qemu/qemu-bridge-helper)" = '755 root root'
NKD_INITIAL_DOMAINS=$(virsh -c qemu:///session list --all --name)
test -z "$NKD_INITIAL_DOMAINS"
NKD_POLICY_ARCHIVE=$(mktemp -d /tmp/nkd-vm-policy.XXXXXXXX)
NKD_POLICY_ID=''
NKD_BRIDGE_ID=''
NKD_HELPER_GRANTED=false
NKD_HELPER_HASH=$(sha256sum /usr/lib/qemu/qemu-bridge-helper | cut -d' ' -f1)

restore_probe_host() {
  # Preserve the producer failure; failed/refused restoration also fails.
  nkd_status=$?
  trap - EXIT INT TERM
  set +e
  if ! nkd_domains=$(virsh -c qemu:///session list --all --name) ||
     ! test -z "$nkd_domains" ||
     pgrep -x nico-sim >/dev/null; then
    echo 'RESTORATION INCOMPLETE: active/unknown VM or simulator; retained setup' >&2
    exit 17
  fi
  if "$NKD_HELPER_GRANTED"; then
    if ! test "$(sha256sum /usr/lib/qemu/qemu-bridge-helper | cut -d' ' -f1)" = "$NKD_HELPER_HASH" ||
       { test "$(stat -c '%a %U %G' /usr/lib/qemu/qemu-bridge-helper)" != '4755 root root' &&
         test "$(stat -c '%a %U %G' /usr/lib/qemu/qemu-bridge-helper)" != '755 root root'; } ||
       ! sudo chmod 0755 /usr/lib/qemu/qemu-bridge-helper; then
      echo 'RESTORATION INCOMPLETE: helper changed or chmod failed' >&2; exit 17
    fi
  fi
  if test -n "$NKD_BRIDGE_ID"; then
    if ! test "$(cat /sys/class/net/nkdvm0/ifindex)" = "$NKD_BRIDGE_ID" ||
       ! sudo ip link delete nkdvm0 type bridge; then
      echo 'RESTORATION INCOMPLETE: bridge changed or removal failed' >&2; exit 17
    fi
  elif ip link show nkdvm0 >/dev/null 2>&1; then
    echo 'RESTORATION INCOMPLETE: bridge ownership not established' >&2; exit 17
  fi
  if test -n "$NKD_POLICY_ID"; then
    if ! test "$(sudo stat -c '%d:%i:%a:%U:%G' /etc/qemu/bridge.conf)" = "$NKD_POLICY_ID" ||
       ! test "$(sudo cat /etc/qemu/bridge.conf)" = 'allow nkdvm0' ||
       ! sudo mv /etc/qemu/bridge.conf "$NKD_POLICY_ARCHIVE/bridge.conf.disabled"; then
      echo 'RESTORATION INCOMPLETE: policy changed or archival failed' >&2; exit 17
    fi
  elif test -e /etc/qemu/bridge.conf || test -L /etc/qemu/bridge.conf; then
    echo 'RESTORATION INCOMPLETE: policy ownership not established' >&2; exit 17
  fi
  printf 'Retained temporary policy: %s\n' "$NKD_POLICY_ARCHIVE"
  exit "$nkd_status"
}
trap restore_probe_host EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if ! test -d /etc/qemu; then sudo install -d -m 0755 /etc/qemu; fi
printf 'allow nkdvm0\n' | sudo install -m 0644 -o root -g root /dev/stdin /etc/qemu/bridge.conf
NKD_POLICY_ID=$(sudo stat -c '%d:%i:%a:%U:%G' /etc/qemu/bridge.conf)
sudo ip link add nkdvm0 type bridge
NKD_BRIDGE_ID=$(cat /sys/class/net/nkdvm0/ifindex)
sudo ip address add 172.30.0.1/24 dev nkdvm0
sudo ip link set nkdvm0 up
NKD_HELPER_GRANTED=true
sudo chmod 4755 /usr/lib/qemu/qemu-bridge-helper

python3 scripts/nke-nicosim-vm-smoke.py \
  --nkdctl "$(pwd)/.nkd/laptop-bootstrap/$NKD_SIM_ATTEMPT/nkdctl" \
  --simulator-run "$(pwd)/.nkd/runs/$NKD_SIM_ATTEMPT" \
  --os-run "$(pwd)/.nkd/runs/$NKD_OS_ATTEMPT" \
  --out "$NKD_VM_PROBE"
)
```

These are example attempt names; substitute your own completed attempts.
If they are in different source exports, supply their absolute paths instead.
Choose a new output directory for every trial. The script does not rebuild a
missing prerequisite or retry a producer automatically. It requires space for
one exact QCOW2 copy plus 8 GiB guest-work allowance. This is not fresh-bake
capacity admission, and guest free space is not physical Mac free space.

The selected source seed owns one 2-CPU/4-GiB/30-GiB instance type; a larger base
disk is rejected rather than truncated. Readiness defaults to 1,200 seconds;
`--timeout` accepts 60–1,800 seconds. SSH has at most three bounded attempts to
accommodate TCG guest startup after NICoSim's TCP-port readiness. A failed guest
assertion is not retried or treated as success. Logs identify the last guest
check. Cleanup diagnostics are best-effort but deletion/stop/join are mandatory.
An ambiguous Create response is handled by a domain sweep only after confirmed
server join, and only where libvirt's disk path binds the exact private workspace.
An incomplete join fails without claiming cleanup.

Kubeadm is **staged, not preinstalled** in the reference image. The probe checks
`apt-cache search --names-only '^nke-kubeadm-distro-[0-9]'`, matching the
[source-owned image smoke](https://github.com/dsx-ai-factory/nke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/scripts/boot-smoke.sh).
The agent selects/installs it during cluster creation. This focused check does
not replace that script's other assertions, including DSX/SPIRE and conntrack.

## Restoration failures

Run the entire guarded block in Bash, not the setup and probe as separate steps.
Its EXIT trap restores identified changes after a nonzero probe exit as well as
success, retaining the policy privately and preserving the original failure.
SIGINT/SIGTERM also enter restoration. It refuses active or unknown VM/server
state and changed/unidentified resources. Such refusal exits 17 and explicitly
reports incomplete restoration; inspect the dedicated VM rather than deleting
foreign resources or rerunning setup. SIGKILL/host crashes cannot run shell traps.
The temporary privilege grant must be checked manually after those interruptions.
Installed packages, an empty `/etc/qemu` directory and libvirt's default network remain;
no accepted Run output is removed. The probe retains its private disk copy and
logs for inspection, not as a new accepted build output.
