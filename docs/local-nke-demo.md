# Build and run NKE on Ubuntu ARM64

This guide builds the selected artifacts with NKD, deploys their exact outputs
using NKE's installer, and provisions a real QCOW2 control-plane and worker
through CAPNICo/NICoSim. Kind hosts management services, not tenant nodes.

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
building. These are setup allowances; execution rechecks authored requirements.
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

Until [PR #118](https://github.com/dsx-ai-factory/nke-recipes/pull/118) merges,
switch to its branch before continuing:

```sh
git switch codex/full-nke-arm64-local
```

After merge, stay on `main`; the temporary branch step is not needed.

### Additional macOS setup

From your NKE Recipes checkout, install Colima if needed. Create the dedicated
profile only if it does not exist; inspect an existing profile rather than
resetting it. Keep enough physical Mac space as well as guest space.

```sh
brew install colima
colima list
colima start --profile nkd-demo --arch aarch64 --runtime docker --vm-type vz \
  --cpus 8 --memory 24 --disk 150 --activate=false --ssh-config=false \
  --mount "$(pwd -P):w"
df -h .
```

Copy committed source into native VM storage. Run this block from the Mac
checkout in Bash. The destination must be new; dirty files are not exported.

```sh
set -euo pipefail
NKD_SOURCE=$(git rev-parse HEAD)
export NKD_NATIVE_REPO=/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-$NKD_SOURCE
colima ssh --profile nkd-demo -- bash -c \
  'test "$(findmnt -n -o FSTYPE --mountpoint /mnt/lima-colima-nkd-demo)" = ext4 && sudo install -d -o "$(id -u)" -g "$(id -g)" -m 700 /mnt/lima-colima-nkd-demo/nkd-local'
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
  dnsmasq-base iptables iproute2 libcap2-bin e2fsprogs
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

If it says `interactive authentication is required`, stop and arrange approved
non-interactive sudo access with the host administrator. Deployment and cleanup
use `sudo -n` for host networking and temporary grants. A one-time `sudo -v`
only caches authentication temporarily and may expire before deployment; it is
not a reliable setup for the complete E2E. Do not run the whole demo as root or
automatically change sudoers to bypass this gate. See Ubuntu's
[sudo timestamp documentation](https://manpages.ubuntu.com/manpages/resolute/man5/sudoers_timestamp.5.html).

Git authorization must work **inside Linux**. The example fetches pinned remote
sources. Configure Git's credential helper there; only build commands inherit
the developer's `HOME` for Git configuration, not deployment commands. For a
local NKE Git clone, use its absolute Linux path in
`NKE_LOCAL_CLONE` and `build.nkeClone`; only committed pinned objects are used.

## 3. Bootstrap the demo command

This prepares the command you will use in step 4; it does **not** build NKE or
boot a tenant yet. No host Go installation is needed: the launcher compiles
`nkdctl` and `nke-demo` in its pinned Go container, then builds the five-tool
installer bundle (Kind, kubectl, Helm, clusterctl and ORAS).

```sh
# Use the Docker daemon on this Linux host.
export CONTAINER_RUNTIME=docker
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
and the demo's reserved tenant ranges `172.30.0.0/24` and `172.31.0.0/24`.
If it conflicts, choose an unused private subnet with Docker's `--subnet` and
`--gateway` options before starting the demo. An existing `kind` network is
inspected, not recreated. A `Pool overlaps` error means the requested subnet is
already allocated: inspect the networks above and do not delete an unrelated
network such as `fleet` to make room.

The adapter checks the pinned Kind configuration and projects IPv4 networking
without a Docker-socket mount into a private copy; it does not edit NKE.

Cache the explicit runtime acquisitions, then copy the example config:

```sh
docker pull kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
test ! -e .nkd/demo.local.yaml
cp demos/local-nke.yaml .nkd/demo.local.yaml
chmod 600 .nkd/demo.local.yaml
printf 'build.repo: %s\nrun.out: %s\n' \
  "$(pwd)" "$(pwd)/.nkd/e2e-demo-01"
```

Edit those two YAML paths using the printed values. They are literal Linux
paths: `~` and shell variables are not expanded. For each rerun, use a fresh
`build.attempt` **and** `run.out`. Do not add retained `run.runs` or
`run.nkdctl` references to E2E configuration.

```sh
"$NKE_DEMO" e2e --config .nkd/demo.local.yaml
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
The example uses two. For a larger execution host, set `jobs: 4` or override it:

```sh
"$NKE_DEMO" e2e --config .nkd/demo.local.yaml --jobs 4
```

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

Progress streams to the terminal with observed START/DONE/FAIL boundaries;
private installer output stays in private logs. Ctrl+C requests cancellation;
the adapter attempts bounded process joins and owned cleanup. If cleanup refuses,
inspect the retained resources and recovery instructions before rerunning.
Ordinary tests do not prove interruption at every live NKE stage.

The OS uses QEMU software acceleration. The eight-hour E2E budget and 60-minute
tenant budget are ceilings, not completion estimates. Deployment requires the
disk-copy bytes plus 30 GiB free. Tenant guests use the selected plain ARM64 disk,
four CPUs, eight GiB RAM and a 60-GiB overlay each.

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
