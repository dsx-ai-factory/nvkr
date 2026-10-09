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

On native Ubuntu ARM64, clone privately and continue with step 2:

```sh
umask 077
git clone https://github.com/dsx-ai-factory/nke-recipes.git
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
virsh -c qemu:///session list --all
```

The libvirt session must be empty. The demo refuses occupied resources and
overlapping networks. Python 3.12+ is an upstream NKE installer prerequisite;
the demo itself is Go. No host Go, Packer or Helm is required.

Git authorization must work **inside Linux**. The example fetches pinned remote
sources. For a local NKE Git clone, use its absolute Linux path in
`NKE_LOCAL_CLONE` and `build.nkeClone`; only committed pinned objects are used.

## 3. Bootstrap the demo command

```sh
export CONTAINER_RUNTIME=docker
umask 022
# Optional: export NKE_LOCAL_CLONE=/absolute/Linux/path/to/nke
bash scripts/nke-full-arm64-local.sh --check tools
bash scripts/nke-full-arm64-local.sh demo-bootstrap-tools-01 1 tools
export NKE_DEMO="$(pwd)/.nkd/laptop-bootstrap/demo-bootstrap-tools-01/nke-demo"
"$NKE_DEMO" e2e --help
```

Preflight does not build. Each attempt name must be new. This bootstrap supplies
the CLI; E2E subsequently builds its own fresh artifact Runs, including tools.
Developers with the pinned Go toolchain can instead use `make demo`.

## 4. Prepare the matching installer

```sh
export NKE_INSTALLER_CLONE="$(pwd)/.nkd/installers/nke-8fb2d533"
mkdir -p "$(pwd)/.nkd/installers"
git clone --no-checkout https://github.com/dsx-ai-factory/nke.git "$NKE_INSTALLER_CLONE"
git -C "$NKE_INSTALLER_CLONE" fetch origin 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
git -C "$NKE_INSTALLER_CLONE" checkout --detach 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
test "$(git -C "$NKE_INSTALLER_CLONE" rev-parse HEAD)" = 8fb2d53368823bd386b4fbd55b4ab6d3c0931345
```

Use a new clean clone. A different installer revision is not automatically
compatible with these selected artifacts.

## 5. Build and run the demo

Inspect `docker network ls` and `ip route`. Only if `kind` does not exist
and its subnet is unused:

```sh
docker network create --driver bridge --subnet 172.18.0.0/16 --gateway 172.18.0.1 kind
```

The adapter checks the pinned Kind configuration and projects IPv4 networking
without a Docker-socket mount into a private copy; it does not edit NKE.

Cache the explicit runtime acquisitions, then copy the example config:

```sh
docker pull kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
docker pull docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
test ! -e .nkd/demo.local.yaml
cp demos/local-nke.yaml .nkd/demo.local.yaml
chmod 600 .nkd/demo.local.yaml
printf 'build.repo: %s\nrun.nkeCheckout: %s\nrun.out: %s\n' \
  "$(pwd)" "$NKE_INSTALLER_CLONE" "$(pwd)/.nkd/e2e-demo-01"
```

Edit those three YAML paths using the printed values. They are literal Linux
paths: `~` and shell variables are not expanded. For each rerun, use a fresh
`build.attempt` **and** `run.out`. Do not add retained `run.runs` or
`run.nkdctl` references to E2E configuration.

```sh
"$NKE_DEMO" e2e --config .nkd/demo.local.yaml
```

E2E preflights all lanes, then builds OS, components, site, tools, profiles and
simulator. The components Run supplies both providers. The first lane's bootstrap
tools are reused by sibling lanes; artifact Runs and their observations remain
separate. A failed lane stops deployment.

Progress streams to the terminal with observed START/DONE/FAIL boundaries;
private installer output stays in private logs. Ctrl+C cancels work and waits
for owned cleanup. There is no terminal dashboard or presentation-mode flag.

The OS uses QEMU software acceleration. The eight-hour E2E budget and 60-minute
tenant budget are ceilings, not completion estimates. Deployment requires the
disk-copy bytes plus 30 GiB free. Tenant guests use the selected plain ARM64 disk,
four CPUs, eight GiB RAM and a 60-GiB overlay each.

## 6. Recognize success

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
export NKD_NKE_E2E_INSTALLER="$NKE_INSTALLER_CLONE"
export NKD_NKE_E2E_OUT="$(pwd)/.nkd/e2e-demo-02"
export NKD_NKE_E2E_ATTEMPT=demo-e2e-02
go test ./internal/nkedemo -run '^TestEndToEnd$' -count=1 -timeout 9h
```

The test requires the pinned Go toolchain. Its deadline allows bounded cleanup
after the eight-hour work context expires.
