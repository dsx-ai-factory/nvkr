# Build and run NKE on Ubuntu ARM64

NKD builds the artifacts. A separate command deploys those exact outputs, creates
a real ARM64 control-plane and worker from the QCOW2 through CAPNICo/NICoSim,
tests a workload and Kubernetes DNS, then removes its owned resources.

All build and deployment commands run on a **Linux ARM64 execution host**.
Use a dedicated Ubuntu ARM64 machine, or follow the additional macOS/Colima
setup below to provide that host. This is not a native macOS build: the outputs
are Linux ARM64 binaries, images and charts, plus an ARM64 QCOW2. Docker Desktop
alone cannot run this demo's libvirt tenant VMs. Other architectures are not
selected automatically by this example.

A real Colima run passed on October 8, 2026, using retained completed artifacts,
including workload/DNS testing and final cleanup. A fresh clean-machine build
of all lanes is not yet verified. This is a trusted-user local demo, not a
signed Release or complete source-built distribution. Acquired images/charts,
the missing DSX input and PR #113 integration remain explicit
[full-source gaps](full-nke-source-build.md).
The expanded handoff also passed an integrated tenant/workload/cleanup replay
with five freshly built non-OS lanes and a retained QCOW2. All fifteen selected
site image identities were checked. Those runs preceded the Go consumer port;
they do not prove its fresh runtime acceptance or a fresh OS rebuild. See
[PR #118's validation status](https://github.com/dsx-ai-factory/nke-recipes/pull/118).

## 1. Choose your Linux execution host

### Ubuntu ARM64

Use a dedicated host with Docker Engine and Buildx, eight CPUs, 24 GiB RAM,
Python 3.12+ and about 90 GiB free on the build filesystem. Install Docker using
the [official Ubuntu instructions](https://docs.docker.com/engine/install/ubuntu/).
No host Go, Packer or Helm is required. The recorded E2E validation below used
Colima; it is not evidence of a separate bare-metal Ubuntu acceptance run.

Clone into native ext4 storage, not a network or macOS shared filesystem. Keep
this checkout private because its `.nkd` directory will hold build and demo data:

```sh
set -euo pipefail
sudo apt-get update
sudo apt-get install -y git
umask 077
git clone https://github.com/dsx-ai-factory/nke-recipes.git
cd nke-recipes
test "$(uname -m)" = aarch64
test "$(findmnt -n -o FSTYPE -T .)" = ext4
df -h .
```

Continue with step 2 in this same Linux terminal; skip the macOS section. The
clone commands target `main` after merge; pre-merge instructions are in PR #118.

### macOS: provide Ubuntu ARM64 through Colima

You need Git, Homebrew, access to the pinned NKE sources, and about 90 GiB free
on the Mac and build filesystem for a fresh OS build. Docker Desktop alone does
not supply this demo's native VM runtime. No host Go, Packer or Helm is required.

From your NKE Recipes checkout, create the dedicated profile **only if it does
not already exist**. Do not reset another profile or change your Docker context.

```sh
brew install colima
colima list
colima start --profile nkd-demo --arch aarch64 --runtime docker --vm-type vz \
  --cpus 8 --memory 24 --disk 150 --activate=false --ssh-config=false \
  --mount "$(pwd -P):w"
df -h .
```

For an existing profile, inspect its architecture, resources and mounts first.
See the [Colima reference](local-development.md#apple-silicon-mac-with-colima)
for reuse and storage troubleshooting.

Still on the Mac, copy committed NKD source into native VM storage. Run this
block from the checkout in Bash. The destination must be new; uncommitted files
are not included.

```sh
set -euo pipefail
NKD_SOURCE=$(git rev-parse HEAD)
export NKD_NATIVE_REPO=/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-$NKD_SOURCE
colima ssh --profile nkd-demo -- bash -c \
  'test "$(findmnt -n -o FSTYPE --mountpoint /mnt/lima-colima-nkd-demo)" = ext4 && sudo install -d -o "$(id -u)" -g "$(id -g)" -m 700 /mnt/lima-colima-nkd-demo/nkd-local'
colima ssh --profile nkd-demo -- mkdir -m 700 "$NKD_NATIVE_REPO"
git archive "$NKD_SOURCE" | colima ssh --profile nkd-demo -- tar -xf - -C "$NKD_NATIVE_REPO"
printf 'Inside Colima, cd to: %s\n' "$NKD_NATIVE_REPO"
colima ssh --profile nkd-demo -- bash -c 'cd "$1" && exec bash' bash "$NKD_NATIVE_REPO"
```

You are now inside the Ubuntu ARM64 VM, in the directory just printed. Native
ext4 avoids the shared-mount file-handle problem encountered during source export.
**Run every remaining command in this Linux terminal, not on the Mac.**

## 2. Install prerequisites on the Ubuntu execution host

On either your native Ubuntu ARM64 host or the Colima Ubuntu ARM64 VM:

```sh
set -euo pipefail
sudo apt-get update
sudo apt-get install -y --no-install-recommends \
  git jq coreutils python3 openssh-client bash curl openssl \
  libvirt-daemon-system libvirt-clients qemu-system-arm qemu-utils \
  qemu-efi-aarch64 cloud-image-utils swtpm swtpm-tools gnutls-bin ipxe-qemu \
  dnsmasq-base iptables iproute2 libcap2-bin e2fsprogs
sudo -n true
docker info >/dev/null
docker buildx version
df -h .
ip route
virsh -c qemu:///session list --all
```

Python 3.12+ is required by the pinned NKE installer's existing readers and
stages, not by the Go demo command. The libvirt session must be empty. The demo
refuses overlapping
networks and existing owned resources rather than deleting them. Host package
installation is explicit setup, not a hermetic NKD toolchain.

Git authentication must work **on the Linux execution host**; a Mac's `gh` login
is not proof of access inside Colima. Alternatively, use an exact local NKE Git
repository accessible from Linux and set `NKE_LOCAL_CLONE` to its absolute Linux
path. Only committed, pinned Git objects are used.

## 3. Build the Linux ARM64 artifacts

From the Linux NKD checkout or committed export, run sequentially. Use new attempt
names for a rerun.

```sh
set -euo pipefail
export CONTAINER_RUNTIME=docker
umask 022
# Optional: export NKE_LOCAL_CLONE=/absolute/path/to/mounted/nke
for lane in os components site tools profiles simulator; do
  bash scripts/nke-full-arm64-local.sh --check "$lane"
  bash scripts/nke-full-arm64-local.sh "demo-$lane-01" 1 "$lane"
done
```

A failed lane stops the sequence. `--check` is preflight, not a build.
OS execution uses QEMU software acceleration; its duration is not guaranteed.
Build the OS first to preserve its disk-space headroom. The components Run
already contains both providers; a separate `providers` build is unnecessary
for this demo. The historical `all` selection does not contain this whole set.
The build umask allows the rootless builder to read source files; the private
checkout or export parent still prevents access by other host users. The `tools`
lane builds the demo's Linux Helm, kubectl, clusterctl, ORAS and Kind bundle;
the consumer extracts it privately rather than requiring those tools on the Mac
or preinstalled on Ubuntu.
The launcher also builds native `nkdctl` and `nke-demo` beside each other under
`.nkd/laptop-bootstrap/<attempt>/`; no host Go installation is needed.
To reuse completed Runs instead, skip this step and supply their absolute paths
below. Never overwrite or rebuild accepted artifacts just to relocate them.

## 4. Prepare the matching installer

The installer must match the site/tools/profiles source pin. This creates a new
clean clone; use an exact local Git repository instead of the URL if appropriate.

```sh
set -euo pipefail
export NKE_INSTALLER_CLONE="$(pwd)/.nkd/installers/nke-8fb2d533"
mkdir -p "$(pwd)/.nkd/installers"
git clone --no-checkout https://github.com/dsx-ai-factory/nke.git "$NKE_INSTALLER_CLONE"
git -C "$NKE_INSTALLER_CLONE" fetch origin 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
git -C "$NKE_INSTALLER_CLONE" checkout --detach 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
test "$(git -C "$NKE_INSTALLER_CLONE" rev-parse HEAD)" = 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
```

## 5. Run the demo without rebuilding

Inspect `docker network ls` and `ip route`. If `kind` does not exist and
`172.18.0.0/16` is unused, create it once:

```sh
docker network create --driver bridge --subnet 172.18.0.0/16 --gateway 172.18.0.1 kind
```

Do not recreate an existing network. Cache the pinned management-node and
registry images (runtime acquisitions, not NKD source-built outputs):

```sh
docker pull kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
```

Set the paths below to your completed Runs. The output directory must be new.
Deployment requires the disk-copy bytes plus 30 GiB free on the Linux execution
filesystem. If using Colima, check physical Mac capacity separately too.

```sh
set -euo pipefail
export NKD_NATIVE_BOOTSTRAP="$(pwd)/.nkd/laptop-bootstrap/demo-tools-01/nkdctl"
export NKD_TOOLS_RUN="$(pwd)/.nkd/runs/demo-tools-01"
export NKD_SITE_RUN="$(pwd)/.nkd/runs/demo-site-01"
export NKD_COMPONENTS_RUN="$(pwd)/.nkd/runs/demo-components-01"
export NKD_PROVIDER_RUN="$NKD_COMPONENTS_RUN"
export NKD_PROFILES_RUN="$(pwd)/.nkd/runs/demo-profiles-01"
export NKD_SIMULATOR_RUN="$(pwd)/.nkd/runs/demo-simulator-01"
export NKD_OS_RUN="$(pwd)/.nkd/runs/demo-os-01"
export NKD_DEMO_OUT="$(pwd)/.nkd/deploy-demo-01"

"$(pwd)/.nkd/laptop-bootstrap/demo-tools-01/nke-demo" run \
  --nke-checkout "$NKE_INSTALLER_CLONE" --nkdctl "$NKD_NATIVE_BOOTSTRAP" \
  --tools-run "$NKD_TOOLS_RUN" --site-run "$NKD_SITE_RUN" \
  --components-run "$NKD_COMPONENTS_RUN" --provider-run "$NKD_PROVIDER_RUN" \
  --profiles-run "$NKD_PROFILES_RUN" --simulator-run "$NKD_SIMULATOR_RUN" \
  --os-run "$NKD_OS_RUN" --out "$NKD_DEMO_OUT" --timeout 3600
```

The 60-minute tenant budget is a bound, not a completion estimate. Kind hosts
management services only. Tenant nodes are real libvirt guests using Ubuntu
26.04, Kubernetes 1.34.2 and OVN-Kubernetes 1.4.0. Each has four CPUs, eight GiB
RAM and a 60-GiB overlay. Packer is not called during deployment.
The thin Go command delegates to the typed local-demo library. Its NKD operations
are `nkdctl` subprocesses; the consumer does not execute builds through an SDK.

## 6. Recognize success

The command must exit zero and print:

```text
PASS: real ARM64 QCOW2 tenant, two ready nodes, workload/DNS, exact artifacts and owned cleanup.
```

This means both nodes were Ready, the pinned ARM64 workload and DNS test passed,
backing-disk and deployed image identities matched, and owned cleanup finished.
No PASS is printed for an installation-only result or cleanup failure.

Private `demo-inputs.json` records consumed artifacts; `site/site-inputs.json`
records deployed image/chart references; `site/tenant-result.json` records node,
VM/backing-disk and workload observations. These are diagnostics, not signed
receipts. Keep the entire output directory out of Git and shared logs.

The command removes its tenant, VMs, management node/volume, registry, bridge,
firewall rules and temporary grants. Accepted Runs, private logs, shared image
caches and the native TPM CA remain. It does not prune Docker or reset Colima.

## If something fails

Read the last printed phase and private logs in the output directory. Do not
delete unknown VMs, change NKE or prune the runtime to force a rerun.

- Inspect containers from the Mac: `colima ssh --profile nkd-demo -- docker ps`.
  Tenant VMs are not Docker containers.
- A new source change needs a new committed source export and fresh Run names.
  See [local rebuilds](local-development.md#rebuild-a-committed-local-source-change).
- For cleanup refusal or interruption, use the
  [network recovery procedure](nkd-to-nke-demo.md#recover-network-controls-after-a-crash).
- See the [installer and safety reference](nkd-to-nke-demo.md) for transport,
  privilege, offline-package and diagnostic security boundaries.

## Repeat the real build and demo as a Go test

For developers of the Go adapter, `TestEndToEnd` builds all six lanes and deploys
their newly completed outputs. It is opt-in because it performs real builds,
temporary host grants and tenant creation. Use the same dedicated Ubuntu ARM64
host and prerequisites above, a fresh attempt prefix and a new output directory.
This developer test also requires the repository's pinned Go toolchain.

```sh
export NKD_NKE_E2E=1
export NKD_NKE_E2E_REPO="$(pwd)"
export NKD_NKE_E2E_INSTALLER="$NKE_INSTALLER_CLONE"
export NKD_NKE_E2E_OUT="$(pwd)/.nkd/e2e-demo-02"
export NKD_NKE_E2E_ATTEMPT=demo-e2e-02
# Optional: export NKE_LOCAL_CLONE=/absolute/path/to/nke
go test ./internal/nkedemo -run '^TestEndToEnd$' -count=1 -timeout 9h
```

Ordinary `go test` skips this test. The test calls the same build launcher and
typed deployment library; it does not select an SDK execution path or replace
the real tenant/workload and cleanup gates with mocks.
The outer test deadline exceeds the eight-hour work context so bounded cleanup
can run after its cancellation; neither duration is a completion estimate.
