# Build and run NKE on Ubuntu ARM64

This guide builds the selected artifacts with NKD, deploys their exact outputs
using NKE's installer, and provisions a real QCOW2 control-plane and worker
through CAPNICo/NICoSim. Kind hosts management services, not tenant nodes.

**Current E2E blocker:** the selected NVIDIA OVN image lacks `firewall-cmd`,
which its startup code requires even with firewalld disabled. Artifact builds
can succeed while OVN startup fails. The demo selects NVIDIA OVN for both the
Kind site cluster and the QCOW2 tenant. See [OVN issue #1](https://github.com/dsx-ai-factory/ovn-kubernetes/issues/1).
Wait for the source-owner fix and a reviewed pin update before expecting a
successful complete E2E with this selection. No local catalog workaround is
applied. The build-only development loops remain separate from tenant acceptance.

Run everything on a dedicated Ubuntu ARM64 host with Docker. On an Apple Silicon
Mac, first create that Linux host with Colima below. This is **not a native macOS
build**. Docker Desktop alone does not provide the libvirt tenant runtime.

This is a trusted-user, unqualified diagnostic, not a signed Release, complete
source-built distribution or qualified OS. See the
[full-source gaps](full-nke-source-build.md) and
[current validation status](adr/reconciliation.md). A successful separate
deployment replay does not establish a clean one-shot E2E build.

## 1. Choose your Linux execution host

Use eight CPUs, 24 GiB RAM and about 90 GiB free on native ext4 storage before
building. This is the recommended **complete-demo** setup, not the OS-only
minimum. Capacity is not reserved and execution rechecks it at each stage:

| Stage | Checked capacity |
| --- | --- |
| Artifact launcher / OS execution | Eight CPUs, 10 GiB available RAM and 64 GiB free workspace; the launcher recommends 90 GiB before materials consume space |
| Management deployment | Docker reports at least eight CPUs and 20 GiB total RAM; at least 20 GiB native disk headroom |
| Two-VM deployment | Space for a served QCOW2 copy **plus 30 GiB free**, checked after artifact building |

The [launcher](../scripts/nke-full-arm64-local.sh),
[Docker Environment](../definitions/nke-local-dev-arm64/environment-local-docker.json)
and [deployment checks](../internal/nkedemo/run.go) own these gates. Passing the
10-GiB/64-GiB artifact check alone does **not** prove the full demo fits. Keep
24 GiB assigned to the dedicated host and check `df -h .` before deployment;
parallel builds and unrelated workloads can consume the setup allowance.
Install Docker Engine and Buildx using the
[Ubuntu instructions](https://docs.docker.com/engine/install/ubuntu/).

If this is a fresh Ubuntu installation without Git, install it before cloning:

```sh
sudo apt-get update
sudo apt-get install -y git ca-certificates
```

On native Ubuntu ARM64, including an ARM64 GCP VM, clone privately and continue
with step 2. No Colima is needed. The demo uses QEMU software acceleration,
not a requirement for nested KVM:

```sh
# Keep newly created checkout files private to this user; not a Git requirement.
umask 077
git clone --branch main https://github.com/dsx-ai-factory/nke-recipes.git
cd nke-recipes
test "$(uname -m)" = aarch64
test "$(findmnt -n -o FSTYPE -T .)" = ext4
```

### Additional macOS setup

From your NKE Recipes checkout, install Colima if needed. Create the dedicated
profile only if it does not exist; inspect an existing profile rather than
resetting it. Keep enough physical Mac space as well as guest space.

```sh
brew install colima
colima list
colima start --profile nkd-demo --arch aarch64 --runtime docker --vm-type vz \
  -c 8 --memory 24 --disk 150 --activate=false --ssh-config=false \
  --mount "$(pwd -P):w"
df -h .
```

Copy committed source into native VM storage. Run this block from the Mac
checkout in Bash. `-c` works with both Colima 0.7.5's `--cpu` and newer
versions' `--cpus`. The block selects Colima's separate data volume if mounted;
otherwise it uses the **Linux** user's home directory, not the Mac home directory.
It checks ext4 on that existing storage before creating the copy. The destination
must be new; dirty files are not exported.

```sh
set -euo pipefail
NKD_SOURCE=$(git rev-parse HEAD)
NKD_NATIVE_BASE=$(colima ssh --profile nkd-demo -- bash -c '
  set -eu
  if mountpoint -q /mnt/lima-colima-nkd-demo; then
    base=/mnt/lima-colima-nkd-demo
  else
    base=$HOME
  fi
  test "$(findmnt -n -o FSTYPE -T "$base")" = ext4
  if test "$base" = /mnt/lima-colima-nkd-demo; then
    sudo install -d -o "$(id -u)" -g "$(id -g)" -m 700 "$base/nkd-local"
  else
    install -d -m 700 "$base/nkd-local"
  fi
  printf "%s\n" "$base/nkd-local"
')
export NKD_NATIVE_REPO=$NKD_NATIVE_BASE/nke-recipes-$NKD_SOURCE
colima ssh --profile nkd-demo -- mkdir -m 700 "$NKD_NATIVE_REPO"
git archive "$NKD_SOURCE" | colima ssh --profile nkd-demo -- tar -xf - -C "$NKD_NATIVE_REPO"
colima ssh --profile nkd-demo -- bash -c 'cd "$1" && exec bash' bash "$NKD_NATIVE_REPO"
```

**Run the remaining commands inside Linux**, with Linux paths. Inspect this
profile from the Mac using `colima ssh --profile nkd-demo -- docker ps`;
tenant VMs are not Docker containers.

## 2. Install prerequisites on the Ubuntu execution host

First check Docker and its Buildx plugin:

```sh
docker version
docker buildx version
```

If Docker works but `docker buildx` is an unknown command, install the plugin
from the same package source as Docker. For Ubuntu's `docker.io` package, use
Ubuntu's [docker-buildx package](https://packages.ubuntu.com/docker-buildx):

```sh
sudo apt-get update
sudo apt-get install -y docker-buildx
docker buildx version
```

For Docker's official `docker-ce` packages, use `docker-buildx-plugin` from
Docker's configured APT repository instead. Do not mix the two package sources.
If `docker info` reports permission denied, follow Docker's
[Linux post-installation instructions](https://docs.docker.com/engine/install/linux-postinstall/).
Docker access grants root-level privileges; use the dedicated trusted-user host.

Install the remaining tools. This includes `virsh` and the QEMU/libvirt tenant
runtime; Docker alone is not enough:

```sh
set -euo pipefail
sudo apt-get update
sudo apt-get install -y --no-install-recommends \
  git jq coreutils python3 openssh-client bash curl openssl \
  libvirt-daemon-system libvirt-clients qemu-system-arm qemu-utils \
  qemu-efi-aarch64 cloud-image-utils swtpm swtpm-tools gnutls-bin ipxe-qemu \
  dnsmasq-base iptables iproute2 libcap2-bin e2fsprogs kmod
docker info >/dev/null
docker buildx version
df -h .
virsh -c qemu:///session list --all
```

The libvirt session must be empty. The demo refuses occupied resources and
overlapping networks. Python 3.12+ is an upstream NKE installer prerequisite;
the demo itself is Go. No host Go, Packer or Helm is required.

Check unattended administrator access before starting the hours-long build:

```sh
sudo -n true
```

E2E also checks this before fetching its installer or starting artifact builds.

The site uses Open vSwitch's kernel datapath inside the owned Kind node. On this
dedicated Linux execution host, have the administrator load its kernel modules
before E2E; these are host prerequisites, not artifacts NKD builds:

```sh
sudo modprobe openvswitch
sudo modprobe geneve
test -d /sys/module/openvswitch
test -d /sys/module/geneve
test -c /dev/net/tun
```

Run these inside Colima on a Mac, not in macOS. If a module is missing for the
running kernel, stop and arrange compatible kernel modules with the host
administrator. The adapter refuses missing prerequisites before installer
acquisition or artifact building; it does not install or load modules for you.
The privileged CNI containers share that host kernel. This is trusted local
execution, not independent isolation. Kernel modules remain loaded after demo
cleanup; the demo does not unload shared host modules.

Network admission uses a fixed Go helper under bounded `sudo -n` to bind-check
DNS/DHCP ports before recording host-control intent. The main demo stays
unprivileged: keep the host's default privileged-port threshold and do not grant
file capabilities to the demo executable. DNS/DHCP capabilities are granted only
to the private dnsmasq copy and journaled for restoration.

If it says `interactive authentication is required`, stop and arrange approved
non-interactive sudo access with the host administrator. Deployment and cleanup
use `sudo -n` for host networking and temporary grants. A one-time `sudo -v`
only caches authentication temporarily and may expire before deployment; it is
not a reliable setup for the complete E2E. Do not run the whole demo as root or
automatically change sudoers to bypass this gate. See Ubuntu's
[sudo timestamp documentation](https://manpages.ubuntu.com/manpages/resolute/man5/sudoers_timestamp.5.html).

Git authorization must work **inside Linux**. The example fetches pinned remote
sources. Configure Git's credential helper there. Source fetch projects that
credential configuration only; checkout and status ignore global hooks, filters
and line-ending settings. Deployment does not inherit source credentials. For a
local NKE Git clone, use its absolute Linux path in
`NKE_LOCAL_CLONE` and `build.nkeClone`; only committed pinned objects are used.

Other repositories can use explicit local Git fetch redirects too. For example,
with a local NVIDIA OVN checkout containing the catalog's pinned commit:

```sh
OVN_LOCAL_CLONE=/absolute/Linux/path/to/ovn-kubernetes
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$OVN_LOCAL_CLONE.insteadOf"
export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes
```

Set these in the shell that runs `nke-demo e2e`. Its NKE clone option preserves
this redirect and adds its own two entries. At most 16 entries are admitted;
only absolute local-repository redirects are accepted. Commit, tree and source
closure verification still apply, and deployment inherits none of these settings.
This supplies Git objects locally, not a fully offline build: other acquisitions
still need their configured repositories and registries.

## 3. Bootstrap the demo command

This prepares the command you will use in step 4; it does **not** build NKE or
boot a tenant yet. No host Go installation is needed: the launcher compiles
`nkdctl` and `nke-demo` in its pinned Go container, then builds the five-tool
installer bundle (Kind, kubectl, Helm, clusterctl and ORAS).

```sh
# Use normal permissions for new build files; demo secrets use private files.
umask 022
# Read-only prerequisites check: no downloads, builds or attempt directories.
bash scripts/nke-full-arm64-local.sh --check tools
# Prepare the CLI and tools; "1" limits concurrent operations, not CPU cores.
bash scripts/nke-full-arm64-local.sh demo-bootstrap-tools-01 1 tools
# Point at the newly compiled CLI. This variable does not launch anything.
export NKE_DEMO="$(pwd)/.nkd/laptop-bootstrap/demo-bootstrap-tools-01/nke-demo"
# Show available options only. The actual build/deployment starts in step 4.
"$NKE_DEMO" e2e --help
```

Each attempt name must be new. This preliminary tools Run is separate from the
complete artifact Run: E2E subsequently builds the selected tools again as part
of its complete fresh membership, rather than silently reusing bootstrap outputs.

## 4. Build and run the demo

Inspect existing Docker subnets and host routes first:

```sh
docker network ls -q | xargs -r docker network inspect \
  --format '{{.Name}} {{json .IPAM.Config}}'
ip -4 route
```

If `kind` does not exist, let Docker select a subnet that does not overlap its
existing networks, instead of assuming `172.18.0.0/16` is free:

```sh
docker network create --driver bridge kind
docker network inspect kind --format '{{json .IPAM.Config}}'
```

The demo discovers that network's actual IPv4 subnet and gateway; no YAML change
is needed. Check that the chosen private subnet also avoids your host/VPN routes
and the demo's reserved tenant ranges `172.30.0.0/24` and `172.31.0.0/24`,
site pod range `10.244.0.0/16`, and site service range `10.96.0.0/16`.
If it conflicts, choose an unused private subnet with Docker's `--subnet` and
`--gateway` options before starting the demo. An existing `kind` network is
inspected, not recreated. A `Pool overlaps` error means the requested subnet is
already allocated: inspect the networks above and do not delete an unrelated
network such as `fleet` to make room.

The adapter checks the pinned Kind configuration and projects IPv4 networking
without a Docker-socket mount into a private copy; it does not edit NKE. It
disables Kind's default CNI and kube-proxy, then installs the built NVIDIA OVN
chart and image. Open vSwitch starts in an owned Kind DaemonSet using the same
selected image's existing entrypoint. Management-node readiness and Cluster API
installation follow site-network readiness; the tenant remains a separate
QCOW2-backed cluster.

Cache the explicit runtime acquisitions:

```sh
docker pull kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
```

E2E checks both cached images on this same Linux Docker daemon before fetching
the installer or building artifacts. Missing images fail early with the exact
pull command. Artifact-only builds do not require these deployment images.
It also checks the discovered Kind subnet for overlap and probes its gateway's
NICoSim API port `18089`. An occupied endpoint fails before the build and names
the inspection command. No listener is stopped automatically; stop an earlier
demo through its original process, or inspect its exact resources before using
[network recovery](nkd-to-nke-demo.md#recover-network-controls-after-a-crash).

During deployment, the demo starts an owned registry directly on the admitted
Kind network, discovers its IPv4 address and checks `/v2/` from the actual
management node before uploading anything. It does not use a host-published
registry port. Local transfers bypass proxies only for this registry address;
external source and registry traffic retains your proxy settings.

Run the checked-in example directly from the repository root. The flags below
replace its placeholder repository and output paths; no YAML copy or editing
is required. For each rerun, change both `--attempt` and `--out` to unused names.
An attempt reserves `.nkd/installers/<attempt>`,
`.nkd/laptop-bootstrap/<attempt>` and `.nkd/runs/<attempt>`. Deleting only
the Run or demo output does not make that attempt fresh. Prefer a new name;
keep the bootstrap directory containing your `$NKE_DEMO` executable.
Do not add retained `run.runs` or `run.nkdctl` references to E2E configuration.

```sh
"$NKE_DEMO" e2e \
  --config demos/local-nke.yaml \
  --repo "$PWD" \
  --attempt demo-e2e-01 \
  --out "$PWD/.nkd/demo-e2e-01-demo" \
  --jobs 2
```

For a **temporary diagnostic** while the NVIDIA OVN runtime issue is unresolved,
use this alternative command. `--network kind-calico` keeps Kind's default site
networking and selects Calico `3.30.1` for the QCOW2 tenant; the default remains
OVN. It does not rewrite catalogs or remove OVN build outputs. A passing run in
this mode does **not** validate NVIDIA OVN or establish M2 acceptance. The selected
mode is retained in `demo-inputs.json`; `nke-demo run` accepts the same flag.

```sh
"$NKE_DEMO" e2e \
  --config demos/local-nke.yaml \
  --repo "$PWD" \
  --attempt demo-kind-calico-01 \
  --out "$PWD/.nkd/demo-kind-calico-01-demo" \
  --jobs 2 \
  --network kind-calico
```

E2E preflights the combined selection, then builds OS, components, site, tools,
profiles and simulator in one immutable Run. Deployment consumes selected outputs
from that completed Run; a failed build stops deployment.

Before the artifact build, E2E prepares the pinned NKE installer automatically
under `.nkd/installers/<build.attempt>/source`. This supplies NKE's deployment
scripts, not prebuilt artifacts. No separate manual clone or folder naming is
needed. With `build.nkeClone` configured, installer acquisition uses that local
Git repository; otherwise it fetches the pinned commit from GitHub. NKD's build
source acquisition remains separate and verifies the catalog's own pins.
An optional `run.nkeCheckout` supplies an existing clean checkout at the exact
installer pin instead; the demo never updates that checkout in place.

`build.jobs` sets the maximum number of concurrent ready build/package operations.
The example uses two. For a larger execution host, change `--jobs 2` to
`--jobs 4` in the command above.

Dependencies still gate execution: package work waits for its producer, and
Packer waits for its materials. This is an operation limit, not a CPU count or
resource reservation; choose it within your host's available memory and disk.

The local OS selection builds OVN-Kubernetes from the NKE/NVIDIA
[`dsx-ai-factory/ovn-kubernetes`](https://github.com/dsx-ai-factory/ovn-kubernetes)
fork at `26cc199de8e814a7fe6d899a770fc04df0bbc2f9`, using the producer catalog
introduced in PR #113. The image carries its chart/image packages for stream
`1.5.18`, and the tenant requests that stream. This uses the fork's standard
Ubuntu image producer, not its private FIPS/STIG flavor. Access to the fork must
work from the Linux execution host.

Before toolchain preparation, the launcher prints the selected output names and
saves the complete linked graph to
`.nkd/laptop-bootstrap/<build.attempt>/build-graph.md`. Open it in a
Mermaid-capable Markdown viewer to see producers, dependencies, artifacts and
required outputs. It comes from `nkdctl run graph` over the compiled baseline
Plan, not a hand-maintained diagram; the later attempt-local toolchain substitution
does not make this display execution evidence.

In an interactive terminal, the demo automatically opens a dashboard with a
phase map, observed state and bounded public activity. The phases are Prepare,
Artifacts, Site, Tenant, Workload/DNS and Cleanup; narrow terminals stack the panes.
The state pane explains the current wait and shows the observed tenant node counts
and remaining readiness budget. It does not infer success from log text.
The phase map keeps the Site and Tenant checklists visible together when the
terminal has room; compact terminals prioritize the live wait and timeout.
The Site checklist retains individual Cluster API and NKE installation stages;
an NKE stage is checked off only after both its run and verification succeed.
The Tenant checklist shows kubeconfig, API, two-node readiness and workload/DNS
observations. Active steps show their own elapsed time. Pending steps have not
been observed; the display does not estimate an overall completion percentage.
Routine command START/DONE traces and repeated wait messages stay out of the
dashboard activity pane; failures remain visible. Use `--plain` for the full
command-boundary trace. ETA remains unknown: a readiness budget is a timeout,
not an estimated finish. The state pane distinguishes time since a stage, node
count or reported NKE condition changed from the age of the latest observation.
Repeated polling is not proof that provisioning is advancing. Only the pinned
API's `True`, `False` and `Unknown` provisioning condition statuses are shown;
neither `Provisioning=True` nor `Provisioned=True` means tenant nodes are Ready.

| Key | Action |
| --- | --- |
| Tab | Change the focused pane |
| PgUp / PgDn | Scroll retained activity |
| End | Return to live activity |
| q | Ask for confirmation; only y/Y requests cancellation and owned cleanup |
| n / Escape at confirmation | Dismiss the prompt and keep working |
| Ctrl+C | Request cancellation immediately, then wait for owned cleanup |
| f while canceling | Ask to force exit; only y/Y exits immediately, leaving cleanup unverified |

Add `--plain` to the command above for scrolling elapsed START/DONE/FAIL output.
Piped or redirected output also stays plain. `build` and `e2e` retain up to 8 MiB
of raw public build output in the private log whose path they display; use
`tail -f` on that path, or add `--verbose` for plain streaming output instead of
the dashboard. At the log limit, one notice is printed and work continues.
Private installer output stays in its private logs. If terminal initialization
fails before work, retry with `--plain`.

Network progress groups private commands into preflight, setup and cleanup;
expected absence checks do not print failures. Numbered command logs remain
available for diagnosis. Tenant polling likewise keeps individual inventory,
kubeconfig and node commands in private logs. The console shows the current
wait, elapsed/remaining readiness budget, and observed registered/Ready counts.
Changes appear immediately; in plain output an unchanged wait repeats at most
every 30 seconds. The dashboard updates the wait in place instead of adding
duplicate activity lines.
Before the tenant API is available, guest boot and node readiness are explicitly
unobserved: a successful inventory or secret query does not mean provisioning
succeeded. Two Ready nodes still require workload/DNS checks and owned cleanup.

An observed substep is not completion of its entire phase. The dashboard and
graph are diagnostics, not artifact verification receipts or a signed Release;
the final command result and owned checks still determine success. After deployment
fails, unfinished active panes show `stopped`; explicit failures remain `failed`.
This does not claim that every unfinished check failed. Cancellation
waits for bounded subprocess joining and owned cleanup before restoring the
terminal. The state pane names diagnostics collection, tenant deletion and the
deletion wait as their owners report them; it does not keep counting the canceled
tenant readiness budget. Tenant cleanup has its own six-minute budget; the delete
command allows up to two minutes and its subsequent deletion wait up to three.
These are bounds, not completion estimates or one global cleanup deadline.
If you confirm force exit with `f` then `y`, the terminal is restored and the
process exits with status 130 **without waiting for cleanup**. Processes,
containers, VMs and network controls may remain. Inspect and recover the exact
attempt before restarting; force exit never reports success or completed cleanup.
If cleanup refuses, inspect retained resources and recovery instructions
before rerunning. [Dashboard tests](../cmd/nke-demo/dashboard_test.go) and
[simulation-screen tests](../cmd/nke-demo/dashboard_view_test.go) cover presentation,
cancellation and output failures with controlled workers. They do not prove
interruption at every live NKE stage or a successful fresh E2E with these changes.

The default OS profile explicitly forbids KVM and uses QEMU's TCG software
emulation, even on a KVM-capable bare-metal host. `Waiting for SSH to become
available` includes Ubuntu autoinstall and its reboot; a quiet Packer console
alone does not show a stall. The limits apply to different scopes:

| Limit | Scope and owner |
| --- | --- |
| Eight hours | Entire `e2e` work context, including installer preparation, artifact building and deployment; [demo command](../cmd/nke-demo/main.go) |
| Six hours | **Entire artifact-build invocation**, including toolchain preparation, materials, Packer and packaging; [build adapter](../internal/nkedemo/build.go) |
| Six hours | OS VM execution request, admitted by the Environment's `maxTimeoutSeconds: 21600`; [OS overlay](../definitions/nke-local-dev-arm64/os-tcg.json) and [Docker Environment](../definitions/nke-local-dev-arm64/environment-local-docker.json) |
| 180 minutes | Packer's wait for SSH, not the whole OS build; `ssh_timeout` in the OS overlay ([Packer SSH documentation](https://developer.hashicorp.com/packer/docs/communicators/ssh)) |
| 60 minutes | Tenant kubeconfig / two-node readiness wait after tenant creation; `run.timeoutSeconds: 3600` or `--timeout`, owned by [tenant deployment](../internal/nkedemo/tenant.go) |

**The earliest active deadline wins.** For example, if artifact preparation has
already used four hours, the six-hour build deadline leaves only two hours for
Packer and packaging, even though SSH permits three hours. Eight hours does not
extend that build deadline; changing `--timeout` changes only tenant readiness.
Individual commands also have shorter bounds, and cleanup has separate bounded
budgets. These are ceilings, not completion estimates. Increasing `--jobs` does
not accelerate one guest. Tenant guests use the selected plain ARM64 disk, four
CPUs, eight GiB RAM and a 60-GiB overlay each.

### Optional KVM for OS assembly on native ARM64

For the Docker route, check device permissions and an actual read/write open in
the same Linux terminal that will launch the demo:

```sh
test -r /dev/kvm && test -w /dev/kvm && echo 'KVM accessible'
bash -c 'exec 3<>/dev/kvm && echo "KVM read/write open OK"'
```

The first command checks permissions; the second opens and closes the device
without starting a VM. Both must succeed. NKD still validates the device and
KVM API. If either fails, inspect `ls -l /dev/kvm` and `id -nG` first.
For example, with a `root:kvm` device and a user outside `kvm`, the administrator
can add that user with `sudo usermod -aG kvm "$(id -un)"`. Open a new login
session and repeat the check; changing account groups does not update an
existing session. Do not run the whole demo as root.

If a TCG run is already active, press Ctrl+C and wait for its command and cleanup
to finish before changing the inputs. Before a new build, run this from the
NKE Recipes repository root on Linux. It updates [the OS overlay](../definitions/nke-local-dev-arm64/os-tcg.json)
and [the Docker Environment](../definitions/nke-local-dev-arm64/environment-local-docker.json):

```sh
(
  set -eu
  kvm_os=definitions/nke-local-dev-arm64/os-tcg.json
  kvm_env=definitions/nke-local-dev-arm64/environment-local-docker.json
  kvm_tmp=$(mktemp)
  jq '
    (.components["nke-node-image"].component.buildRequirements.capabilities[]
      | select(.name == "kvm") | .policy) = "required"
    | .components["nke-node-image"].component.packer.variables.cpu_model = "host"
  ' "$kvm_os" > "$kvm_tmp"
  cp "$kvm_tmp" "$kvm_os"
  jq '.executionClasses[0].capabilities = ["kvm"]' "$kvm_env" > "$kvm_tmp"
  cp "$kvm_tmp" "$kvm_env"
  rm "$kvm_tmp"
)
```

This requires KVM for OS assembly, selects QEMU's `host` CPU model and declares
the Docker execution class's KVM capability. It does not change source pins.

The compiled Plan then requires KVM; runtime admission checks the actual device
before VM execution. Keep the source/material pins unchanged.
Use a fresh attempt and output path; an already-running TCG Plan cannot switch
accelerators. These settings have been checked by compiling the complete
29-output Plan. No real KVM build was performed for this example. They are not
suitable for a Colima or cloud VM that does not expose usable nested KVM.

Then launch a fresh attempt:

```sh
"$NKE_DEMO" e2e \
  --config demos/local-nke.yaml \
  --repo "$PWD" \
  --attempt demo-kvm-01 \
  --out "$PWD/.nkd/demo-kvm-01-demo" \
  --jobs 2
```

The attempt name does not select acceleration; the JSON input changes do.

## 5. Recognize success

Require exit zero and this exact final message:

```text
PASS: real ARM64 QCOW2 tenant, two ready nodes, workload/DNS, exact artifacts and owned cleanup.
```

The adapter requires both nodes Ready, workload/DNS success, artifact/disk
correspondence and owned cleanup. The private output directory retains
`demo-inputs.json`, `site/site-inputs.json`, `site/tenant-result.json` and
logs. They are diagnostics, not signed receipts.

The command removes owned tenant/VMs, management node/volume, registry, bridge,
temporary firewall rules and grants. It retains accepted Runs, logs, shared
image caches and the native TPM CA. It does not prune Docker or reset Colima.

For failures, inspect the last phase and private logs. Do not delete unknown
resources to force a rerun. See [network recovery](nkd-to-nke-demo.md#recover-network-controls-after-a-crash).
For focused rebuilds see [local development](local-development.md).

## Repeat the real build and demo as a Go test

This opt-in test performs real builds and host mutations on the same dedicated
Ubuntu ARM64 host. Ordinary tests skip it.

```sh
export NKD_NKE_E2E=1
export NKD_NKE_E2E_REPO="$(pwd)"
# Optional: export NKD_NKE_E2E_INSTALLER=/absolute/path/to/a/matching/clean/checkout
export NKD_NKE_E2E_OUT="$(pwd)/.nkd/e2e-demo-02"
export NKD_NKE_E2E_ATTEMPT=demo-e2e-02
go test ./internal/nkedemo -run '^TestEndToEnd$' -count=1 -timeout 9h
```

The test requires the pinned Go toolchain. Its deadline allows bounded cleanup
after the eight-hour work context expires.

## Where the NKE versions come from

These are exact source selections recorded by this example, not a command to
build whichever NKE release is newest. A Git SHA identifies a commit; NKD also
checks its pinned tree. The guide does not establish that these candidates are
one tagged NKE release.

| Part | Selected NKE commit | Owning input |
| --- | --- | --- |
| Installer, site CLI/controller/charts and diagnostic profiles | [`8fb2d533`](https://github.com/dsx-ai-factory/nke/commit/8fb2d53368823bd386b4fbd55b4ab6d3c0931345) | [Site lock](../definitions/nke-local-dev-arm64/site-catalog/pins.lock.yaml), [profiles lock](../definitions/nke-local-dev-arm64/profiles-catalog/pins.lock.yaml) and the demo's [installer pin](../internal/nkedemo/model.go) |
| OS assembly, agent and most NKE-owned OS material producers | [`ff21ba8c`](https://github.com/dsx-ai-factory/nke/commit/ff21ba8c07cafff74b68e238a8876af11e5beded) | [OS lock](../definitions/nke-m2/os-catalog/pins.lock.yaml) and [local OS overlay](../definitions/nke-local-dev-arm64/os-tcg.json) |
| SPIRE package exception | [`726f7694`](https://github.com/dsx-ai-factory/nke/commit/726f76944404e1106626b8333d3576bdf67b575b) | Same OS lock; this older plugin-free package is an explicit [source gap](full-nke-source-build.md), not the intended complete NKE baseline |
| NICoSim | [`6ffc2e5c`](https://github.com/dsx-ai-factory/nke/commit/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4) | [Simulator lock](../definitions/nke-local-dev-arm64/simulator-catalog/pins.lock.yaml) |
| Provisioned provider (built, not used by this NICoSim tenant) | [`33fb71b2`](https://github.com/dsx-ai-factory/nke/commit/33fb71b2f4224b470ebc628a9165fc4b92870406) | [Provider lock](../definitions/nke-controller/catalog/pins.lock.yaml), selecting `v0.21.0` |

The OS commit added APT archive capture; the installer commit includes the staged
deployment interface this adapter checks. The NVIDIA OVN fork, CAPNICo, NICoSim
and upstream tools have their own source pins in the selected catalogs. A new
NKE release does **not** automatically move any of these selections.

For developers following the guide, use an NKE Recipes revision containing the
desired maintained selection; do not replace a SHA with `main` or `latest`.
For maintainers updating it, resolve the intended release/commit through
`nkdctl catalog pin` ([catalog maintenance](catalog-maintenance.md)), update the
matching installer/interface checks, profile patch and OS overlay bindings, then
review the resulting Plan. The OS overlay contains its own resolved source pins
and predecessor catalog digest: updating the base lock alone does not update it.
Run `make check` and the complete fresh E2E before
publishing the updated guide. Updating only the installer pin does not update
the artifact graph or prove compatibility with a new release.
