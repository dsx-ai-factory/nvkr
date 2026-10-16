# Build and run NVKE on Ubuntu ARM64

This guide builds the selected artifacts with NVKR, deploys their exact outputs
using NVKE's installer, and provisions a real QCOW2 control-plane and worker
through CAPNICo/NICoSim. Kind hosts management services, not tenant nodes.

**Known NVIDIA OVN startup blocker:** the selected NVIDIA OVN image lacks `firewall-cmd`,
which its startup code requires even with firewalld disabled. Artifact builds
can succeed while OVN startup fails. The demo selects NVIDIA OVN for both the
Kind site cluster and the QCOW2 tenant. See [OVN issue #1](https://github.com/dsx-ai-factory/ovn-kubernetes/issues/1).
Wait for the source-owner fix and a reviewed pin update before expecting a
successful complete E2E with this selection. No local catalog workaround is
applied. The build-only development loops remain separate from tenant acceptance.
An explicit `--network upstream-ovn` alternative selects upstream OVN for both
site and tenant; it does not fix or validate the NVIDIA fork. A fresh E2E at
`71b356f5899efb759571f0ea51781ead3f7c466a` passed on 2026-10-11; see the
[exact-revision runtime evidence](evidence/local-upstream-ovn-2026-10-11.md).
This does not establish live acceptance of later changes. Neither this source
fix nor a different CNI alone guarantees tenant provisioning, workload/DNS or cleanup. The temporary
Calico diagnostic has separate readiness and datapath checks, not an OVN pass.
See the selections below.

Run everything on a dedicated Ubuntu ARM64 host with Docker. On an Apple Silicon
Mac, first create that Linux host with Colima below. The Go CLI itself can be
built on macOS with the pinned Go toolchain using `make demo`, which produces
`bin/nvke-demo`; help, tests and diagnostics are separate from deployment.
Native macOS cannot run this Linux ARM64 deployment or its retained service
owner. Docker Desktop alone does not provide the libvirt tenant runtime.

This is a trusted-user, unqualified diagnostic, not a signed Release, complete
source-built distribution or qualified OS. See the
[full-source gaps](full-nvke-source-build.md) and
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

The [launcher](../scripts/nvke-full-arm64-local.sh),
[Docker Environment](../inputs/environments/local-arm64-tcg.json)
and [deployment checks](../internal/nvkedemo/run.go) own these gates. Passing the
10-GiB/64-GiB artifact check alone does **not** prove the full demo fits. Keep
24 GiB assigned to the dedicated host and check `df -h .` before deployment;
parallel builds and unrelated workloads can consume the setup allowance.
Install Docker Engine and Buildx using the
[Ubuntu instructions](https://docs.docker.com/engine/install/ubuntu/).

Local source admission requires Git 2.38 or later (`git --version`). Its bounded
Git-owned index-mode query fails closed when the required format is unsupported.
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
git clone --branch main https://github.com/dsx-ai-factory/nvkr.git
cd nvkr
test "$(uname -m)" = aarch64
test "$(findmnt -n -o FSTYPE -T .)" = ext4
```

### Additional macOS setup

From your NVKR checkout, install Colima if needed. Create the dedicated
profile only if it does not exist; inspect an existing profile rather than
resetting it. Keep enough physical Mac space as well as guest space.

```sh
brew install colima
colima list
colima start --profile nvkr-demo --arch aarch64 --runtime docker --vm-type vz \
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
NVKR_SOURCE=$(git rev-parse HEAD)
NVKR_NATIVE_BASE=$(colima ssh --profile nvkr-demo -- bash -c '
  set -eu
  if mountpoint -q /mnt/lima-colima-nvkr-demo; then
    base=/mnt/lima-colima-nvkr-demo
  else
    base=$HOME
  fi
  test "$(findmnt -n -o FSTYPE -T "$base")" = ext4
  if test "$base" = /mnt/lima-colima-nvkr-demo; then
    sudo install -d -o "$(id -u)" -g "$(id -g)" -m 700 "$base/nvkr-local"
  else
    install -d -m 700 "$base/nvkr-local"
  fi
  printf "%s\n" "$base/nvkr-local"
')
export NVKR_NATIVE_REPO=$NVKR_NATIVE_BASE/nvkr-$NVKR_SOURCE
colima ssh --profile nvkr-demo -- mkdir -m 700 "$NVKR_NATIVE_REPO"
git archive "$NVKR_SOURCE" | colima ssh --profile nvkr-demo -- tar -xf - -C "$NVKR_NATIVE_REPO"
colima ssh --profile nvkr-demo -- bash -c 'cd "$1" && exec bash' bash "$NVKR_NATIVE_REPO"
```

**Run the remaining commands inside Linux**, with Linux paths. Inspect this
profile from the Mac using `colima ssh --profile nvkr-demo -- docker ps`;
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
overlapping networks. Python 3.12+ is an upstream NVKE installer prerequisite;
the demo itself is Go. No host Go, Packer or Helm is required.

Run the demo from an interactive Linux terminal. It first checks the exact
unattended networking wrapper. If that is approved without cached credentials,
no password authentication or session refresh is needed. Otherwise it authenticates
before opening the dashboard or starting the build, then refreshes that sudo session
non-interactively every 30 seconds while the foreground command owns its work. Deployment and cleanup
do not open password prompts. The main demo remains unprivileged; passwords go
directly to the initial sudo prompt, not through the demo's logs.
Do not add a separate `sudo -v` before the demo: it manages authentication itself.

Interactive refresh is not a command-scoped credential. With terminal-scoped
sudo timestamps, other processes in that terminal session may reuse authentication
for commands already allowed by your full sudoers policy. Timestamp scope depends
on administrator policy; the demo does not change it. Use a trusted dedicated
execution host. See the upstream
[sudoers timestamp contract](https://github.com/sudo-project/sudo/blob/main/docs/sudoers.man.in).

For a redirected or unattended run, check non-interactive administrator access:

```sh
sudo -n -k timeout --signal=TERM --kill-after=5 25 true
```

In that unattended mode, E2E checks this exact bounded networking command before
fetching its installer or starting artifact builds. `-n` forbids password prompts; `-k` with a command
ignores cached credentials for that invocation without clearing your timestamp.
Unlike `sudo -n true`, this tests the command wrapper used for privileged network
setup and cleanup, without accepting a temporary `sudo -v` cache. Success admits
this probe only: command-specific policy can still deny later operations, and
administrator policy changes cannot be predicted by preflight.

In either OVN mode, the site uses Open vSwitch's kernel datapath inside
the owned Kind node. On this dedicated Linux execution host, have the
administrator load its kernel modules before E2E; these are host prerequisites,
not artifacts NVKR builds:

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
cleanup; the demo does not unload shared host modules. The temporary
`--network kind-calico` diagnostic skips this OVN module admission, but still
requires the common Docker, libvirt and host-network prerequisites above.

Network admission uses a fixed Go helper under bounded `sudo -n` to bind-check
DNS/DHCP ports before recording host-control intent. The main demo stays
unprivileged: keep the host's default privileged-port threshold and do not grant
file capabilities to the demo executable. DNS/DHCP capabilities are granted only
to the private dnsmasq copy and journaled for restoration.

Hosts with an INPUT-drop firewall also need the management node to reach the
host simulator. The demo journals a temporary rule allowing only the admitted
Kind subnet to its exact gateway on TCP port 18089. Cleanup removes that exact
owned rule; it does not disable UFW or change the host's default policy. An
iptables accept does not override a separate nftables/firewalld input-hook drop
([nftables verdict semantics](https://netfilter.org/projects/nftables/manpage.html)).
If that policy rejects the admitted path, arrange its equally scoped allowance
with the administrator; the demo's real node probe still decides admission. Before
installing the site, the demo probes simulator health from the owned Kind node
with that node's real proxy environment. Host-side health alone is insufficient.
A failed node probe stops deployment before tenant provisioning and retains
`site/node-simulator-connectivity.log` for diagnosis. A passing probe establishes
that node path only, not controller health or tenant readiness.

Initial interactive authentication has a two-minute deadline; type your password
at sudo's prompt (typing is hidden) and press Enter. Ctrl+C cancels it. The session
refresher is stopped and joined when the command exits. It does not change sudoers
or extend authentication after the workflow ends. If policy disallows cached
authentication, revokes access or rejects a protected command, the run fails
explicitly without opening a mid-run prompt or retrying that operation. Preserve
completed artifacts and any recovery journal; cleanup may need a separate
`restore` invocation. Do not pipe or redirect the interactive demo's standard
streams: those runs cannot prompt and require approved unattended access.
The protected operations still use bounded `sudo -n`. Authentication does not
override command-specific sudo policy. Do not run the whole demo as root or change
sudoers to bypass a failure. See the upstream
[sudo manual](https://raw.githubusercontent.com/sudo-project/sudo/main/docs/sudo.man.in)
for `-v` session refresh and `-n` non-interactive behavior.

Git authorization must work **inside Linux**. The example fetches pinned remote
sources. Within Git children, ordinary source fetch projects global `credential.helper`,
`credential.username` and `credential.useHttpPath`, plus fetch-only HOME/XDG,
SSH-agent, proxy and TLS-trust paths. It does not inherit system helpers,
GH/GCM/keyring environment, arbitrary SSH commands or other global URL rewrites.
Use a compatible configured helper or an explicit local repository containing
the pinned objects. Checkout and status ignore global hooks, filters and
line-ending settings; deployment does not inherit source credentials. The native
build launcher itself still receives developer HOME/XDG, SSH-agent and transport
settings; fetch-only projection does not isolate every launcher subprocess.
For a local NVKE Git clone, use its absolute Linux path in
`NVKE_LOCAL_CLONE` and `build.nvkeClone`; only committed pinned objects are used.

Other repositories can use explicit local Git fetch redirects too. For example,
with a local NVIDIA OVN checkout containing the catalog's pinned commit:

```sh
OVN_LOCAL_CLONE=/absolute/Linux/path/to/ovn-kubernetes
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$OVN_LOCAL_CLONE.insteadOf"
export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes
```

Set these in the shell that runs `nvke-demo e2e`. Its NVKE clone option preserves
this redirect and adds its own two entries. At most 16 entries are admitted;
only absolute local-repository redirects are accepted. Commit, tree and source
closure verification still apply, and deployment inherits none of these settings.
This supplies Git objects locally, not a fully offline build: other acquisitions
still need their configured repositories and registries.

## 3. Bootstrap the demo command

This prepares the command you will use in step 4; it does **not** build NVKE or
boot a tenant yet. No host Go installation is needed: the launcher compiles
`nvkr` and `nvke-demo` in its pinned Go container, then builds the five-tool
installer bundle (Kind, kubectl, Helm, clusterctl and ORAS).

```sh
# Use normal permissions for new build files; demo secrets use private files.
umask 022
# Read-only prerequisites check: no downloads, builds or attempt directories.
bash scripts/nvke-full-arm64-local.sh --check tools
# Prepare the CLI and tools; "1" limits concurrent operations, not CPU cores.
bash scripts/nvke-full-arm64-local.sh demo-bootstrap-tools-01 1 tools
# Point at the newly compiled CLI. This variable does not launch anything.
export NVKE_DEMO="$(pwd)/.nvkr/laptop-bootstrap/demo-bootstrap-tools-01/nvke-demo"
# Show available options only. The actual build/deployment starts in step 4.
"$NVKE_DEMO" e2e --help
```

Each attempt name must be new. This preliminary tools Run is separate from the
complete artifact Run: E2E subsequently builds the selected tools again as part
of its complete fresh membership, rather than silently reusing bootstrap outputs.

## 4. Build and run the demo

By default, `run` and `e2e` return to the shell after the checked deployment
reaches Ready and leave the tenant, management cluster and supporting services
running under their exact local service owner. They do not delete them merely
because the dashboard closes. A deployment stopped before Ready retains its
partial environment for inspection too; it returns failure, not a Ready claim.
Installer or artifact-build failures before deployment have no such usable
cluster promise. Neither stale output nor process existence proves readiness.

For a one-shot acceptance run or continuous recording that must include natural
NVKE tenant deletion and joined owned cleanup, add `--cleanup` to the same command.
The YAML equivalent is the strictly typed `run.cleanup: true`; the default is
false, and an explicit `--cleanup=false` overrides it. Cleanup is never a
silent fallback from retention to a different network or input selection.

For a recording, add `--walkthrough` to that same `e2e --cleanup`
invocation. TUI remains the default; `--plain` is optional scrolling output. The `e2e`
introduction explains the selected authored inputs and links their source documentation before
the dashboard; it is read-only presentation, not runtime admission. After the existing workload
and DNS checks, the adapter runs bounded
read-only kubectl observations against both explicit owned kubeconfigs before
natural tenant deletion. The public view shows Ready node roles, all-namespace
pod status and restarts, actual Deployment/DaemonSet/StatefulSet inventory,
selected component rollouts, and the existing `nvkr-smoke` Job completion,
zero-retry policy, container exit/restarts and bounded DNS success marker. It
does not rerun the Job. Kubeconfig contents, node addresses, pod identities, raw
DNS output and private diagnostics remain private. A failed walkthrough fails
the recording and still runs the existing mandatory owned cleanup. The YAML
equivalent is `run.walkthrough: true`; default workflows are unchanged.

The new retained-owner CLI and live access presentation have focused contract
tests and native process/control fixtures. Those fixtures do not prove a fresh
NVKE retained deployment, SSH-logout persistence or acceptance of this revision.
Session detachment does not escape host cgroup/login policy; do not assume
services survive logout or reboot. No linger or administrator policy is changed.

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
without a Docker-socket mount into a private copy; it does not edit NVKE. It
disables Kind's default CNI and kube-proxy, then installs the built NVIDIA OVN
chart and image. Open vSwitch starts in an owned Kind DaemonSet using the same
selected image's existing entrypoint. Management-node readiness and Cluster API
installation follow site-network readiness; the tenant remains a separate
QCOW2-backed cluster.
The explicit upstream mode uses the same CNI-disabled Kind topology and kernel
prerequisites, with its separately selected upstream image and chart.

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
[network recovery](nvkr-to-nvke-demo.md#recover-network-controls-after-a-crash).

During deployment, the demo starts an owned registry directly on the admitted
Kind network, discovers its IPv4 address and checks `/v2/` from the actual
management node before uploading anything. It does not use a host-published
registry port. Local transfers bypass proxies only for this registry address;
external source and registry traffic retains your proxy settings.
The registry uses temporary storage capped at 2 GiB. Admission deduplicates the
selected image blobs and requires room for their total size, the largest
in-flight upload, and 64 MiB of metadata headroom before creating resources.
Tmpfs allocates memory as content is written; this ceiling is neither reserved
RAM nor proof of available memory. The deployment host must still meet the
complete-demo capacity checks above.

Run the checked-in example directly from the repository root. The flags below
replace its placeholder repository and output paths; no YAML copy or editing
is required. For each rerun, change both `--attempt` and `--out` to unused names.
An attempt reserves `.nvkr/installers/<attempt>`,
`.nvkr/laptop-bootstrap/<attempt>` and `.nvkr/runs/<attempt>`. Deleting only
the Run or demo output does not make that attempt fresh. Prefer a new name;
keep the bootstrap directory containing your `$NVKE_DEMO` executable.
Do not add retained `run.runs` or `run.nvkr` references to E2E configuration.

```sh
"$NVKE_DEMO" e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --attempt demo-e2e-01 \
  --out "$PWD/.nvkr/demo-e2e-01-demo" \
  --jobs 2
```

### Explicit upstream OVN alternative

`--network upstream-ovn` selects upstream OVN-Kubernetes `v1.4.0` at
[`d6a3812d`](https://github.com/ovn-kubernetes/ovn-kubernetes/commit/d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d)
for both the site and QCOW2 tenant. NVIDIA OVN remains the default; neither OVN
mode falls back to Calico. This is a separate unqualified diagnostic selection,
not NVIDIA-fork validation or proof of a supported NVKE release combination.

```sh
"$NVKE_DEMO" e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --attempt demo-upstream-ovn-01 \
  --out "$PWD/.nvkr/demo-upstream-ovn-01-demo" \
  --jobs 2 \
  --network upstream-ovn
```

This chooses the authored [upstream Definition](../inputs/definitions/demo/arm64-upstream-tcg.json),
not a replacement of the NVIDIA catalog's pins. Image, chart, disk and profiles
have distinct upstream output names. The complete selection still contains 29
outputs in one fresh Run. The disk embeds the matching upstream image/chart
packages; its profiles image installs stream `1.4.0`, and the tenant requests
that same stream. Deployment replay with `nvke-demo run --network upstream-ovn`
requires those distinct outputs and rejects opposite-mode inputs.

For an artifact-only complete build, use
`bash scripts/nvke-full-arm64-local.sh --network upstream-ovn ATTEMPT JOBS all`.
The launcher rejects upstream subset selections so the required inputs cannot
be mixed by that route. Bootstrap the CLI with the ordinary tools command first.
Source acquisition and Docker pulls still require their configured endpoints;
this option is not an offline mode. A local upstream Git redirect must name
`https://github.com/ovn-kubernetes/ovn-kubernetes`, not the NVIDIA fork.

The explicit source and package bindings are contract checks. A separate fresh
real E2E at `71b356f5899efb759571f0ea51781ead3f7c466a` passed matched upstream
site networking, two Ready ARM64 QCOW2 nodes, the unchanged zero-retry
workload/DNS check and natural owned cleanup. The
[dated evidence](evidence/local-upstream-ovn-2026-10-11.md) records its exact
scope and retained observations. It is an unqualified local diagnostic, not a
supported NVKE combination or live acceptance of subsequent changes.

### Temporary Calico diagnostic

For a **temporary diagnostic** while the NVIDIA OVN runtime issue is unresolved,
use this alternative command. `--network kind-calico` keeps Kind's default site
networking and selects Calico `3.30.1` for the QCOW2 tenant; the default remains
OVN. It does not rewrite catalogs or remove OVN build outputs. A passing run in
this mode does **not** validate NVIDIA OVN or establish M2 acceptance. The selected
mode is retained in `demo-inputs.json`; `nvke-demo run` accepts the same flag.

In this diagnostic mode, both tenant nodes must be Ready and the tenant's
`calico-system/daemonset/calico-node` rollout must pass before the DNS rollouts
and the unchanged zero-retry workload test. Node Ready and ready DNS pods alone
do not establish a working CNI datapath. The Calico check has a four-minute
ceiling within the remaining tenant deadline; a failed rollout stops deployment.
This additional admission is contract-tested, not proof that a fresh complete
E2E or functional DNS test succeeds.

```sh
"$NVKE_DEMO" e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --attempt demo-kind-calico-01 \
  --out "$PWD/.nvkr/demo-kind-calico-01-demo" \
  --jobs 2 \
  --network kind-calico
```

E2E preflights the combined selection, then builds OS, components, site, tools,
profiles and simulator in one immutable Run. Deployment consumes selected outputs
from that completed Run; a failed build stops deployment.

Before the artifact build, E2E prepares the pinned NVKE installer automatically
under `.nvkr/installers/<build.attempt>/source`. This supplies NVKE's deployment
scripts, not prebuilt artifacts. No separate manual clone or folder naming is
needed. With `build.nvkeClone` configured, installer acquisition uses that local
Git repository; otherwise it fetches the pinned commit from GitHub. NVKR's build
source acquisition remains separate and verifies the catalog's own pins.
An optional `run.nvkeCheckout` supplies an existing clean checkout at the exact
installer pin instead; the demo never updates that checkout in place.

`build.jobs` sets the maximum number of concurrent ready build/package operations.
The example uses two. For a larger execution host, change `--jobs 2` to
`--jobs 4` in the command above.

Dependencies still gate execution: package work waits for its producer, and
Packer waits for its materials. This is an operation limit, not a CPU count or
resource reservation; choose it within your host's available memory and disk.

The local OS selection builds OVN-Kubernetes from the NVKE/NVIDIA
[`dsx-ai-factory/ovn-kubernetes`](https://github.com/dsx-ai-factory/ovn-kubernetes)
fork at tag `v2.4.2`, commit `8af51273d52dfa07454435d18d37230e582d797a`, using the
producer catalog introduced in PR #113. The image carries its chart/image packages
for stream `2.4.2`, and the tenant requests that stream. This uses the fork's
`Dockerfile.ubuntu.nv` producer with its base image overridden to a pinned public
`ubuntu:24.04` digest, not its private FIPS/STIG base. Access to the fork must
work from the Linux execution host.

Before toolchain preparation, the launcher prints the selected output names and
saves the complete linked graph to
`.nvkr/laptop-bootstrap/<build.attempt>/build-graph.md`. Open it in a
Mermaid-capable Markdown viewer to see producers, dependencies, artifacts and
required outputs. It comes from `nvkr run graph` over the compiled baseline
Plan, not a hand-maintained diagram; the later attempt-local toolchain substitution
does not make this display execution evidence.
After that substitution, the launcher also saves the selected Plan and its
complete linked display to `selected-plan.json` and `graph.json` in the same
directory. The dashboard's dependency links come from that selected graph, not
from a guessed sequence of fetch/build/package stages.

In an interactive terminal, the demo automatically opens a dashboard. During
artifact builds, the workspace focuses on the build pipeline: a progress and
health ribbon, a selectable operation board, and details for the selected step.
It lists required outputs separately from reported done/cached, running and
queued operations. Rows show the producer/step, stage and running duration.
The progress bar counts only dry-run-listed steps; it is not verified artifact
completion, overall E2E progress or an ETA. Missing or truncated listings show
an unknown total. Only the owning artifact operation completes the phase.

The ribbon separates the age of raw build output from the age of the last step
change, even when concise mode hides compiler output. It also shows the last
recognized Packer report and its age: boot-console preparation, SSH connection,
file transfer, shell provisioning, shutdown or final QCOW2 post-processing.
Concise output uses fixed summaries; private script paths and console addresses
are not copied into those summaries. These messages are not live Ubuntu
installer telemetry or a guest-health probe. During an opted-in OS build, a
read-only observer of that admitted attempt's work volume samples the existing
build-log directory. The dashboard shows available log-file count, aggregate
size, sample age and age of observed growth separately from Packer stdout.
A first sample is a baseline, not growth; replacement, truncation, disappearance
or unsafe entries reset the observation. Unavailable metadata is shown explicitly.
Names, console text and private paths are never sent to the dashboard.
Observer stop/join is attempted before terminal log collection and resource
cleanup. If its stop or attachment cannot be joined within the bounded shutdown
window, the build fails and retains dependent resources, including the exact
runtime executable capability; cleanup is not reported
as complete. Bounded private log collection and independent resource cleanup are
still attempted after Packer exits. Metadata-only
activity does not prove guest health, installation progress or an ETA.
Launcher start/exit observations do not establish
the health of every Docker, Packer or QEMU descendant; silence is not classified
as a stall.

Up/Down selects an operation and opens its plan-linked inputs, outputs and retained
public marker events. Enter toggles details; Escape closes them without canceling
work. Outside details or a confirmation prompt, Escape has no effect. Raw parallel
output is shared, so the selected view does not mislabel it as that step's log.
Press `a` to return to shared activity and automatic running-step selection.

During deployment, the phases are Prepare, Artifacts, Site, Tenant, Workload/DNS
and Cleanup. The state pane puts the current blocker first, followed by the four
tenant checkpoints and separate control-plane/worker observations. Each guest
row distinguishes an owned VM's state, infrastructure readiness and console-file
activity from Kubernetes readiness. Narrow terminals stack the panes.
The phase map keeps the Site and Tenant checklists visible together when the
terminal has room; compact terminals prioritize the live wait and timeout.
The Site checklist retains individual Cluster API and NVKE installation stages;
an NVKE stage is checked off only after both its run and verification succeed.
The Tenant checklist shows kubeconfig, API, two-node readiness and workload/DNS
observations. Active steps show their own elapsed time. Pending steps have not
been observed; the display does not estimate an overall completion percentage.
Routine command START/DONE traces stay out of the dashboard activity pane;
failures remain visible. Tenant stage, node-count or reported condition changes
add an activity entry immediately. Unchanged tenant reports add a short wait
heartbeat at most once per 30 seconds. A heartbeat does not prove a successful
API query, guest activity or forward provisioning progress. Use `--plain` for
the full command-boundary trace. ETA remains unknown: a readiness budget is a timeout,
not an estimated finish. The state pane distinguishes time since a stage, node
count or reported NVKE condition changed from the age of the latest observation.
Repeated polling is not proof that provisioning is advancing. Only the pinned
API's `True`, `False` and `Unknown` provisioning condition statuses are shown;
neither `Provisioning=True` nor `Provisioned=True` means tenant nodes are Ready.

Guest observations use the selected tenant's exact Machine/NicoMachine binding,
then the owned libvirt domain and its private console files. A first file-size
sample is a baseline, not activity. Only positive growth of the same console
file identity counts as observed activity; replacement or truncation resets that baseline.
The dashboard shows the size and age of observed growth at the guest snapshot,
and separately shows how old that snapshot is. It does not extrapolate the
growth clock when polling stops. It never displays console text,
instance identifiers or provider error messages. Missing or rejected bindings
remain unavailable. A role-bound `InstanceCreateFailed` condition is shown as
an instance lookup/create failure even when no VM exists yet; this provider
report does not diagnose the underlying network or API error.

Guest snapshots run at most once per 30 seconds, with a separate bounded query
window. Their age is independent of the latest wait report and the last tenant
state change. A running VM or growing console file does not prove successful
boot, guest health or Ready nodes. The four-checkpoint bar counts observed
checks, not elapsed-time progress or an ETA. During tenant provisioning at
80×24, the phase strip moves into the header so the blocker, guest rows and
activity remain visible.

| Key | Action |
| --- | --- |
| Tab | Change the focused pane |
| Up / Down during builds | Select an operation in the build pipeline |
| Enter during builds | Toggle details |
| Escape | Close details or dismiss a confirmation; never cancel work |
| a during builds | Return to shared activity and automatic selection |
| PgUp / PgDn | Scroll retained activity |
| End | Return to live activity |
| q / Ctrl+C | Ask for confirmation; only y/Y stops current work; default deployment retains its environment, while `--cleanup` requests owned teardown |
| n / Escape at confirmation | Dismiss the prompt and keep working |
| f while canceling | Ask to force exit; only y/Y exits immediately, leaving cleanup unverified |

Confirmation applies while the dashboard owns terminal input. During the initial
sudo prompt, or in plain or verbose mode, interrupt and termination signals still
cancel work without this dialog. Default deployment retention and explicit
`--cleanup` remain distinct; cancellation alone does not request retained cleanup.
Timeouts and terminal failures also cancel without requiring a usable prompt.

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
Changes appear immediately; an unchanged wait adds a short heartbeat at most
every 30 seconds. Plain guest observations add fixed, public activity summaries on the
first snapshot, a semantic change or observed positive console growth. Plain
output does not repeat unchanged role rows merely because their ages advance;
fresh snapshots alone do not count as state changes. Before a successful tenant
node query, registered/Ready counts remain unknown. Available VM/console metadata
can explain activity before the tenant API exists, but does not establish boot
completion. Two Ready nodes still require workload/DNS checks and owned cleanup.

An observed substep is not completion of its entire phase. The dashboard and
graph are diagnostics, not artifact verification receipts or a signed Release;
the final command result and owned checks still determine success. After deployment
fails, unfinished active panes show `stopped`; explicit failures remain `failed`.
This does not claim that every unfinished check failed. Normal confirmed cancellation
waits for its work owner to return before restoring the terminal. A retained
deployment then prints its exact inspection and explicit-cleanup commands.
In `--cleanup` mode, the state pane names diagnostics collection, tenant deletion and the
deletion wait as their owners report them; it does not keep counting the canceled
tenant readiness budget. Tenant cleanup has its own one-hour ceiling, independent
of canceled work: up to six minutes for diagnostics, two minutes for the delete
request, 45 minutes for the owned tenant to disappear, and two minutes for guest
route withdrawal, with additional joining/headroom inside that hour. The following
host-runtime cleanup has its own eight-minute bound. These are separate ceilings,
not completion estimates. The [selected NVKE teardown workflow](https://github.com/dsx-ai-factory/nvke/blob/8fb2d53368823bd386b4fbd55b4ab6d3c0931345/controller/internal/temporal/workflows/teardown_cluster.go)
tears down worker pools before control-plane nodes and permits 20-minute waits
for each; retries and other steps
can extend teardown. The 45-minute wait is a finite local policy, not an upstream
maximum or a guarantee that deletion succeeds. A timeout still fails E2E and
retains private cleanup observations; removing owned local VMs cannot substitute
for successful NVKE deletion.
During cancellation, the state pane displays **[ f ] FORCE EXIT** with a warning
that resources may remain; this is a keyboard action, not a mouse control.
If you confirm force exit with `f` then `y`, the terminal is restored and the
process exits with status 130 **without waiting for cleanup**. Processes,
containers, VMs and network controls may remain. Inspect and recover the exact
attempt before restarting; force exit never reports success or completed cleanup.
The exit warning prints the configured recovery workspace and, for printable
paths, a quoted command using the actual executable. This locator does not prove
that admission completed or a journal exists. Use it only if the original
`network-state.json` exists, after inspecting and stopping surviving owned
producers. `restore` repairs recorded network controls, not tenants, VMs or
containers. Renderer panics, terminal output/input failures and a closed event
stream restore the terminal before waiting for the workflow's cleanup to join;
they still return failure and preserve the owner's error rather than reporting
successful cleanup. Default retained deployment does not turn a view failure
into permission to delete its environment. Confirmed cancellation keeps its
waiting screen until the work owner returns, unless force exit is confirmed.
Recovered foreground workflow or renderer panics report `NVKR-E-INTERNAL`, not a host
preflight error. A value description and stack are retained in a separate, bounded
mode-0600 `nvke-demo-panic-*.log` file; only its escaped locator is printed.
Primitive payloads are bounded before formatting. Composite or custom payloads
retain a type label and explicit omission, not a traversal or user-defined
formatter that could block shutdown. Their original stack is still recorded.
Plain and dashboard workflow presentation use the same recovery boundary.
The detached retained owner instead records a bounded private value description and stack in
`owner-panic.log` before a nested cleanup hold blocks unwinding, and carries only
the stable `NVKR-E-INTERNAL` classification to its foreground. It uses the same
primitive bounds and composite omission policy, never exposing the panic payload
through that channel. Its private `owner-error.log` retains the
operation failure; `owner-return.log` records a returned failed teardown.
Keep these diagnostics private: panic values and stacks can contain sensitive
configuration or implementation details. If storage fails, the internal error
states that diagnostic retention failed rather than claiming a complete log.
If an OS-build log observer cannot join, the build fails and retains that
observer's container, work volume and private identity workspace rather than
deleting inputs that it may still use. Those Packer resources are separate from
the deployment network journal: `restore` does not reap them. Do not prune them
while their owning build or observer remains live. Inspect the exact stopped
attempt and its owned resources before arranging recovery. If persistence
succeeds, the attempt's private `diagnostics/packer/run-*/observer-retained.json`
contains the stop owner's exact `observerContainer` and `workVolume` locators.
The timeout names this fixed record, not resource identities or provider text;
a record-write failure is separately reported. Keep the record and its material
manifest private. The record is inspection data, not deletion authorization.

Copy that exact private record path into `RECOVERY_RECORD`; do not pick a
wildcard match or the newest timestamp. For that selected route, read the two
fields and inspect the objects without changing them. Keep inspect output private:

```sh
OBSERVER=$(jq -er '.observerContainer' "$RECOVERY_RECORD")
WORK_VOLUME=$(jq -er '.workVolume' "$RECOVERY_RECORD")
docker container inspect "$OBSERVER"
docker volume inspect "$WORK_VOLUME"
```

Compare the observer's `nvkr.dev/role=log-activity`, exact `nvkr.dev/attempt` label,
selected toolchain image and read-only work-volume mount with the original
attempt's material manifest and retained local command logs. Confirm the
original build/observer owners and their runtime clients have stopped and
joined. A missing client, timer, stale JSON name or one inspect response does
not establish that join or resource ownership. An administrator may then remove
only the independently verified observer and volume:

```sh
docker rm -fv "$OBSERVER"
docker volume rm "$WORK_VOLUME"
```

If identity, labels, mounts or owner/client termination cannot be established,
stop and retain the evidence. These are manual operator steps, not a new
restore/adoption API. `restore` still covers only authenticated deployment
network controls. Automated cross-process Packer orphan recovery is not implemented.
If cleanup refuses, inspect retained resources and recovery instructions
before rerunning. [Dashboard tests](../cmd/nvke-demo/dashboard_test.go) and
[simulation-screen tests](../cmd/nvke-demo/dashboard_view_test.go) cover presentation,
cancellation and output failures with controlled workers. They do not prove
interruption at every live NVKE stage or a successful fresh E2E with these changes.

The default OS profile explicitly forbids KVM and uses QEMU's TCG software
emulation, even on a KVM-capable bare-metal host. `Waiting for SSH to become
available` includes Ubuntu autoinstall and its reboot; a quiet Packer console
alone does not show a stall. The limits apply to different scopes:

| Limit | Scope and owner |
| --- | --- |
| Eight hours | Entire `e2e` work context, including installer preparation, artifact building and deployment; [demo command](../cmd/nvke-demo/main.go) |
| Six hours | **Entire artifact-build invocation**, including toolchain preparation, materials, Packer and packaging; [build adapter](../internal/nvkedemo/build.go) |
| Six hours | OS VM execution request, admitted by the Environment's `maxTimeoutSeconds: 21600`; [OS overlay](../inputs/catalogs/node-images/variants/nvidia-tcg.json) and [Docker Environment](../inputs/environments/local-arm64-tcg.json) |
| 180 minutes | Packer's wait for SSH, not the whole OS build; `ssh_timeout` in the OS overlay ([Packer SSH documentation](https://developer.hashicorp.com/packer/docs/communicators/ssh)) |
| 60 minutes by default; configurable from 5 to 120 minutes | Tenant kubeconfig / two-node readiness wait after tenant creation; `run.timeoutSeconds: 3600` or `--timeout`, owned by [tenant deployment](../internal/nvkedemo/tenant.go) |
| 45 minutes inside a separate one-hour tenant-cleanup ceiling | Wait for NVKE to delete the exact owned tenant after the two-minute delete request; this is independent of tenant readiness and is not an upstream teardown maximum |

**The earliest active deadline wins.** For example, if artifact preparation has
already used four hours, the six-hour build deadline leaves only two hours for
Packer and packaging, even though SSH permits three hours. Eight hours does not
extend that build deadline; changing `--timeout` changes only tenant readiness.
Individual commands also have shorter bounds, and cleanup has separate bounded
budgets. These are ceilings, not completion estimates. Increasing `--jobs` does
not accelerate one guest. Tenant guests use the selected plain ARM64 disk, four
CPUs, eight GiB RAM and a 60-GiB overlay each.

On a slow TCG host, explicitly add `--timeout 7200` to the E2E command to allow
up to two hours for tenant readiness, or set `run.timeoutSeconds: 7200` in your
demo YAML. The same option works with `nvke-demo run`. Values outside 300..7200
seconds are rejected before preparation or deployment. The default stays one
hour; this does not extend the eight-hour E2E context or any shorter command
deadline. These overlapping ceilings are not additive. More time does not fix
network, configuration or image-startup failures, and does not relax the two
Ready nodes, workload/DNS or owned-cleanup checks.

### Optional KVM for OS assembly on native ARM64

For the Docker route, check device permissions and an actual read/write open in
the same Linux terminal that will launch the demo:

```sh
test -r /dev/kvm && test -w /dev/kvm && echo 'KVM accessible'
bash -c 'exec 3<>/dev/kvm && echo "KVM read/write open OK"'
```

The first command checks permissions; the second opens and closes the device
without starting a VM. Both must succeed. NVKR still validates the device and
KVM API. If either fails, inspect `ls -l /dev/kvm` and `id -nG` first.
For example, with a `root:kvm` device and a user outside `kvm`, the administrator
can add that user with `sudo usermod -aG kvm "$(id -un)"`. Open a new login
session and repeat the check; changing account groups does not update an
existing session. Do not run the whole demo as root.

If a TCG run is already active, press Ctrl+C, confirm with `y` in the dashboard,
and wait for its command to return. If deployment was retained, use its printed
explicit cleanup command and wait for cleanup before changing inputs. Before a new build, run this from the
NVKR repository root on Linux. It updates [the OS overlay](../inputs/catalogs/node-images/variants/nvidia-tcg.json)
and [the Docker Environment](../inputs/environments/local-arm64-tcg.json):

```sh
(
  set -eu
  kvm_os=inputs/catalogs/node-images/variants/nvidia-tcg.json
  kvm_env=inputs/environments/local-arm64-tcg.json
  kvm_tmp=$(mktemp)
  jq '
    (.components["nvke-node-image"].component.buildRequirements.capabilities[]
      | select(.name == "kvm") | .policy) = "required"
    | .components["nvke-node-image"].component.packer.variables.cpu_model = "host"
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
"$NVKE_DEMO" e2e \
  --config demos/local-nvke.yaml \
  --repo "$PWD" \
  --attempt demo-kvm-01 \
  --out "$PWD/.nvkr/demo-kvm-01-demo" \
  --jobs 2
```

The attempt name does not select acceleration; the JSON input changes do.

## 5. Recognize success

### Retained deployment and actual cluster access

Default success returns zero with `retained-ready`, the time of the completed
Ready check, and full access instructions after the dashboard restores the
terminal. It is not a completed cleanup or full recorded E2E claim. The CLI
also executes the bundled `kubectl` against both selected APIs and prints their
`/readyz` results and a node table containing roles, Ready conditions,
architecture and Kubernetes version. The Site table is the Kind management
cluster; the Tenant table is the separate QCOW2 cluster. These are live bounded
observations, not inferred status from an elapsed timer or old dashboard events.
Names, addresses, private responses and kubeconfigs are not copied into that table.
The retained service owner relays the same redacted table as a typed public result,
not generic subprocess log text. It must fit the existing 32 KiB encoded progress
envelope; an oversized result fails presentation instead of being silently split
or truncated. The final shell handoff still performs separate fresh live queries.
Explicit `--cleanup` runs also show these live checks before tenant deletion;
plain output prints the table immediately. The dashboard keeps the table outside
its activity ring and replays the same timestamped snapshot after restoring the
terminal, including when later cleanup fails. That replay is explicitly historical:
it does not say that deleted clusters are still running. The display does not
replace workload/DNS, disk-binding or joined-cleanup acceptance.

Copy the exact `DEMO`, `SESSION`, `KUBECTL`, `SITE_CONFIG` and `TENANT_CONFIG`
assignments printed by your invocation. These are private local locators, not
portable credentials or public evidence. The printed commands include:

```sh
"$KUBECTL" --kubeconfig "$TENANT_CONFIG" --request-timeout=10s get nodes
"$KUBECTL" --kubeconfig "$TENANT_CONFIG" --request-timeout=10s get pods -A
"$KUBECTL" --kubeconfig "$SITE_CONFIG" --request-timeout=10s get nodes
"$KUBECTL" --kubeconfig "$SITE_CONFIG" --request-timeout=10s get pods -A
"$DEMO" status --session "$SESSION"
```

`status` authenticates the original live owner and runs fresh read-only cluster
queries when Ready access was issued. Owner connection and historical Ready
timestamps alone do not claim Kubernetes health. Failed health checks return
failure without deleting the environment; full commands remain available for
inspection. Private per-invocation command logs are retained separately.

The selected demo includes Temporal UI. Browser guidance requires an actual
matching Service/port and bounded HTTP response through the Kubernetes Service
proxy; the CLI does not invent a separate NVKE UI or claim functional UI health.
It prints the discovered loopback-only port-forward command, but does not start
that forward for you. Stop that forward with Ctrl+C without deleting the demo.
For a remote execution host, use your existing SSH tunnel to its loopback port;
the instructions never assume or publish a server address.

Delete the retained environment only when you choose to:

```sh
"$DEMO" cleanup --session "$SESSION"
```

This separate foreground command authenticates administrator access before
opening its dashboard. The live owner performs its natural tenant deletion and
joined service teardown; only after that owner exits does the foreground
restore the original recorded host-network controls. A refused, failed or
unjoined cleanup does not imply those controls were restored. Preserve the
original session directory and journal. The printed `restore --work` fallback
recovers only authenticated original network controls after producers are idle;
it does not delete tenants, VMs or containers. No global Docker prune occurs.

Cleanup has an 82-minute foreground wait budget: the owner's one-hour tenant
ceiling, two-minute registry cleanup, three-minute management cleanup,
eight-minute producer teardown, eight-minute network restoration, and
one minute of control-acknowledgment headroom. The dashboard relays the owner's bounded public
activity during this wait. If the foreground deadline expires, cleanup continues
in the owner; rerun the same `cleanup --session` command. Do not run `restore`
while cleanup or its producers are still active.

A returned failed teardown is reported immediately as a non-retryable
`NVKR-E-PREFLIGHT-FAILED`, naming private `owner-return.log` and `owner-error.log`.
It never triggers network restoration or claims joined cleanup. If the returned
stack has no unjoined or unknown-ownership obligation, the authenticated explicit
cleanup request permits that owner to exit, while failed resources and host
controls remain for recovery. Unjoined or unknown ownership keeps the owner
resident; a returned stack alone is not evidence that its producers joined.
Keep the original session, workspace and logs. Independently inspect the exact
owned producer identities using those records, establish their termination and
idle state, and only then use `restore --work "$WORK"` for the original journal.
Missing processes, stale records or an elapsed timer are not proof of cleanup;
resource deletion or an unknown ownership case requires administrator recovery.

### Full one-shot acceptance with explicit cleanup

Add `--cleanup` to `e2e` (or the exact-input `run` diagnostic) and require exit
zero and the final message for the selected network. For NVIDIA OVN:

```text
PASS (NVIDIA OVN): real ARM64 QCOW2 tenant, two ready nodes, workload/DNS, exact artifacts and owned cleanup.
```

The explicit upstream mode instead begins
`PASS (upstream OVN diagnostic; NVIDIA OVN not validated):`; the temporary
Calico mode begins `PASS (Kind/Calico diagnostic; NVIDIA OVN not deployed):`.
Neither alternative is NVIDIA-fork acceptance. All modes remain unqualified
local diagnostics, not signed Releases.

The adapter requires both nodes Ready, workload/DNS success, artifact/disk
correspondence and owned cleanup. The private output directory retains
`demo-inputs.json`, `site/site-inputs.json`, `site/tenant-result.json` and
logs. The successful `tenant-result.json` also names the normalized `Network`
selection. They are diagnostics, not signed receipts.

Tenant deletion also retains `site/tenant-cleanup-observations.json` when private
storage is available. It contains bounded initial/delete/latest command output
and the final command status, not a deletion receipt. Its output fields are
base64-encoded private bytes; a canceled empty poll does not erase the last useful
inventory. Failure to retain this diagnostic never prevents owned deletion or
changes its result.

The explicit `--cleanup` command removes owned tenant/VMs, management node/volume, registry, bridge,
temporary firewall rules and grants. It retains accepted Runs, logs, shared
image caches and the native TPM CA. It does not prune Docker or reset Colima.

For failures, inspect the last phase and private logs. Do not delete unknown
resources to force a rerun. See [network recovery](nvkr-to-nvke-demo.md#recover-network-controls-after-a-crash).
For focused rebuilds see [local development](local-development.md).

A failed post-deployment runtime readiness check names the container and its
observed exit code, when available. Before management cleanup, the adapter
rechecks the failed pod's UID and attempts to retain its current and previous
container logs in `site/runtime-failure-current.log` and
`site/runtime-failure-previous.log`, plus the private identity snapshot
`site/runtime-failure-identity.log`. Collection has a separate 20-second budget;
each log request has a five-second deadline and asks for at most 100 lines and
256 KiB. Raw logs stay private and never enter the dashboard. Collection failures
do not erase the original readiness failure or prevent joined cleanup; an
unjoined diagnostic child still blocks resource removal. These observations are
diagnostics, not acceptance, and collecting them never retries or bypasses the
readiness check. The focused tests exercise controlled subprocesses, not an
Ubuntu Temporal deployment or the cause of a real container crash.

## Repeat the real build and demo as a Go test

This opt-in test performs real builds and host mutations on the same dedicated
Ubuntu ARM64 host. Ordinary tests skip it.

```sh
export NVKR_NVKE_E2E=1
export NVKR_NVKE_E2E_REPO="$(pwd)"
# Optional: export NVKR_NVKE_E2E_INSTALLER=/absolute/path/to/a/matching/clean/checkout
export NVKR_NVKE_E2E_OUT="$(pwd)/.nvkr/e2e-demo-02"
export NVKR_NVKE_E2E_ATTEMPT=demo-e2e-02
go test ./internal/nvkedemo -run '^TestEndToEnd$' -count=1 -timeout 9h
```

The test requires the pinned Go toolchain. Its deadline allows bounded cleanup
after the eight-hour work context expires.

## Where the NVKE versions come from

These are exact source selections recorded by this example, not a command to
build whichever NVKE release is newest. A Git SHA identifies a commit; NVKR also
checks its pinned tree. The guide does not establish that these candidates are
one tagged NVKE release.

| Part | Selected NVKE commit | Owning input |
| --- | --- | --- |
| Installer, site CLI/controller/charts and diagnostic profiles | [`8fb2d533`](https://github.com/dsx-ai-factory/nvke/commit/8fb2d53368823bd386b4fbd55b4ab6d3c0931345) | [Site lock](../inputs/catalogs/nvke/local-site-arm64/pins.lock.yaml), [profiles lock](../inputs/catalogs/controller-profiles/nvidia-arm64/pins.lock.yaml) and the demo's [installer pin](../internal/nvkedemo/model.go) |
| OS assembly, agent and most NVKE-owned OS material producers | [`ff21ba8c`](https://github.com/dsx-ai-factory/nvke/commit/ff21ba8c07cafff74b68e238a8876af11e5beded) | [OS lock](../inputs/catalogs/node-images/release/pins.lock.yaml) and [local OS overlay](../inputs/catalogs/node-images/variants/nvidia-tcg.json) |
| Temporary SPIRE package selection | [`726f7694`](https://github.com/dsx-ai-factory/nvke/commit/726f76944404e1106626b8333d3576bdf67b575b) | Same OS lock; the prior plugin-free producer is retained temporarily. DSX remains a required missing product input, tracked in [NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641), not full source/content parity. |
| NICoSim | [`6ffc2e5c`](https://github.com/dsx-ai-factory/nvke/commit/6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4) | [Simulator lock](../inputs/catalogs/nicosim/pins.lock.yaml) |
| Provisioned provider (built, not used by this NICoSim tenant) | [`33fb71b2`](https://github.com/dsx-ai-factory/nvke/commit/33fb71b2f4224b470ebc628a9165fc4b92870406) | [Provider lock](../inputs/catalogs/nvke/release/pins.lock.yaml), selecting `v0.21.0` |

The OS commit added APT archive capture; the installer commit includes the staged
deployment interface this adapter checks. The NVIDIA OVN fork, CAPNICo, NICoSim
and upstream tools have their own source pins in the selected catalogs. A new
NVKE release does **not** automatically move any of these selections.

For developers following the guide, use an NVKR revision containing the
desired maintained selection; do not replace a SHA with `main` or `latest`.
For maintainers updating it, resolve the intended release/commit through
`nvkr catalog pin` ([catalog maintenance](catalog-maintenance.md)), update the
matching installer/interface checks, profile patch and OS overlay bindings, then
review the resulting Plan. The OS overlay contains its own resolved source pins
and predecessor catalog digest: updating the base lock alone does not update it.
Run `make check` and the complete fresh E2E before
publishing the updated guide. Updating only the installer pin does not update
the artifact graph or prove compatibility with a new release.
