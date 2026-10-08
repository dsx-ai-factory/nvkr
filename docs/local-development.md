# Build and develop NKE locally with NKD

Inspect the ARM64 NKE artifact set, build it locally, and rebuild a committed source
change. No previous NKD experience is required. Run commands from the `nke-recipes`
repository root and keep the same terminal open.

**This is not yet a full source-built NKE distribution.** The example selects
23 outputs, including the two source-built infrastructure-provider images, but
still acquires twelve dependency images, four charts and the operator tools.
The OS is not yet wired to the direct-source catalog from PR #113. See the
[full-source build gap map](full-nke-source-build.md) before using this example
as acceptance of a full build. The script's historical `full` name is not a claim
that all NKE components or their transitive dependencies are compiled.

NKD is the project; `nkdctl` is its command-line tool. You provide three inputs:

| Input | What you edit |
| --- | --- |
| Definition | Which named artifacts to produce and their platform |
| Catalog and machine-managed pins | How to build/acquire components and their exact sources/tools |
| Environment | Where to execute, with which runtime and resources |

`run plan` describes the work. `run local` executes it as a new immutable attempt.
Local builds are unqualified diagnostics, not signed releases or cluster deployment.
For the next step, see [NKD artifacts for Lia's site demo](nkd-to-nke-demo.md).
That guide distinguishes the tested artifact build from the still-unvalidated
installation and tenant-provisioning handoff.

## Start from a clean machine

The launcher needs no host Go, Make, installed `nkdctl`, Packer, QEMU, Helm or
Kubernetes. It compiles NKD from this checkout in the pinned utility container.
This launcher admits one fixed bootstrap image and requires the maintained
ARM64 utility platform/runtime before downloads or attempt creation. It is not
a generic Environment validator; complete document validation remains NKD-owned.
Component tooling runs in catalog-pinned containers; Packer creates the node OS
inside QEMU. Git, Bash, a working native container runtime, `jq` and GNU `timeout`
are still required on the Linux execution host.

| Starting point | Route |
| --- | --- |
| Native Linux ARM64, Docker already installed | `CONTAINER_RUNTIME=docker`; no Podman or host Go |
| Native Linux ARM64, rootless Podman | Default Podman route |
| Apple Silicon Mac, no runtime | Install Podman and create/start its Linux VM |
| Apple Silicon Mac, Colima Docker VM | `CONTAINER_RUNTIME=docker COLIMA_PROFILE=NAME`; launcher executes inside that explicit ARM64 profile |
| Apple Silicon Mac, only Docker Desktop | Not supported by this launcher yet; use Podman or a native Linux ARM64 Docker host |
| AMD64 machine | This ARM64 launcher rejects emulation; use a native ARM64 execution host |

### Native Linux with Docker

Install Docker using its [official Linux instructions](https://docs.docker.com/engine/install/).
The OS bootstrap also needs the Docker Buildx plugin. On Ubuntu:

```sh
sudo apt-get update
sudo apt-get install git jq coreutils
docker version
docker buildx version
git clone https://github.com/dsx-ai-factory/nke-recipes.git
cd nke-recipes
# Before PR #118 merges, check out its implementation branch:
git switch --track origin/codex/full-nke-arm64-local
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh --check providers
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh first-providers 1 providers
```

Run as a user authorized for the local Docker daemon. Docker daemon access is a
trusted host capability, not independently isolated provider execution. Remote
Docker endpoints are rejected: nested bind paths must belong to the local daemon
host. You can SSH to a native ARM64 host and run there; pointing a laptop's
`DOCKER_HOST` at it is not this route.

### Apple Silicon Mac with Podman

Install Git if absent and Podman using its
[official macOS installer](https://podman.io/docs/installation). Only if
`podman-machine-default` does not exist, initialize it:

```sh
podman machine init --cpus 8 --memory 16384 --disk-size 150 podman-machine-default
podman machine start podman-machine-default
podman machine ssh --username core podman-machine-default \
  'for tool in git jq timeout; do command -v "$tool" || exit 1; done'
git clone https://github.com/dsx-ai-factory/nke-recipes.git
cd nke-recipes
# Before PR #118 merges, check out its implementation branch:
git switch --track origin/codex/full-nke-arm64-local
bash scripts/nke-full-arm64-local.sh --check providers
bash scripts/nke-full-arm64-local.sh first-providers 1 providers
```

The default Fedora CoreOS VM commonly includes these small tools. If a check
reports one missing, use that VM distribution's installation mechanism;
Fedora CoreOS uses `rpm-ostree install` and a subsequent reboot, not an
unconditional `dnf install`. See the
[CoreOS package-layering guide](https://docs.fedoraproject.org/en-US/fedora-coreos/os-extensions/).
Do not
reinitialize/reset an existing VM; start it if stopped. Allocate fewer resources
for component-only development if needed; the OS still requires its authored
capacity. VM disk size is **not** free host workspace space: on macOS this
checkout uses shared `/Users` storage. Allow about 90 GiB free there before a
fresh OS attempt, in addition to adequate VM storage.

The launcher never installs host packages, creates/starts/resizes VMs, deletes
caches or prunes attempts automatically. Execution downloads/builds begin only
after you request a build. Private-source authentication must work on the Linux
execution host; cloning this repository does not grant access to other sources.

### Apple Silicon Mac with Colima

An explicitly selected Colima **Linux ARM64 Docker VM** is another execution
host for the existing OS producer. It is not a tenant node: the tenant must boot
the QCOW2 built by NKD. The launcher enters the profile over SSH and uses Docker
inside Linux, rather than running authority-bearing paths on macOS or forwarding
a Mac Docker socket. It does not switch your default Docker context.

Install Colima if absent, then explicitly create a dedicated profile. The following
uses the checkout paths on your machine; run it from the NKE Recipes root:

```sh
# Fail as one sequence: a failed mount check, mkdir, export or preflight must
# not overwrite an existing export or launch a build anyway.
(
set -euo pipefail
brew install colima
colima list
# Only if this dedicated profile does not already exist:
colima start --profile nkd-demo --arch aarch64 --runtime docker --vm-type vz \
  --cpus 8 --memory 16 --disk 150 --activate=false --ssh-config=false \
  --mount "$(pwd -P):w"
colima ssh --profile nkd-demo -- bash -c \
  'sudo apt-get update && sudo apt-get install -y git jq coreutils'
# Use a new committed-source export on native ext4, not the Mac shared mount.
# Inspect/commit intended changes first: git archive does not include dirty files.
NKD_COLIMA_SOURCE=$(git rev-parse HEAD)
NKD_COLIMA_REPO=/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-$NKD_COLIMA_SOURCE
colima ssh --profile nkd-demo -- bash -c \
  'test "$(findmnt -n -o FSTYPE --mountpoint /mnt/lima-colima-nkd-demo)" = ext4 && sudo install -d -o "$(id -u)" -g "$(id -g)" -m 700 /mnt/lima-colima-nkd-demo/nkd-local'
# mkdir fails if this source directory already exists; do not overwrite it.
colima ssh --profile nkd-demo -- mkdir -m 700 "$NKD_COLIMA_REPO"
git archive "$NKD_COLIMA_SOURCE" | \
  colima ssh --profile nkd-demo -- tar -xf - -C "$NKD_COLIMA_REPO"
df -h .  # physical Mac capacity still matters
printf -v NKD_COLIMA_COMMAND \
  'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh --check os' \
  "$NKD_COLIMA_REPO" "${NKE_LOCAL_CLONE:-}"
colima ssh --profile nkd-demo -- bash -c "$NKD_COLIMA_COMMAND"
printf -v NKD_COLIMA_COMMAND \
  'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh first-colima-os 1 os' \
  "$NKD_COLIMA_REPO" "${NKE_LOCAL_CLONE:-}"
colima ssh --profile nkd-demo -- bash -c "$NKD_COLIMA_COMMAND"
)
```

These are explicit setup mutations, not actions performed by `--check`. If reusing
a profile, inspect its architecture/runtime/resources and mounts first; do not
reset it. Mount an optional `NKE_LOCAL_CLONE` at the same absolute path too before
using the local Git override. Otherwise canonical source acquisition requires Git
authentication inside Colima. `DOCKER_HOST` and `DOCKER_CONTEXT` from the Mac are
not propagated into the VM. The Linux launcher checks native architecture, daemon
reachability, tools, Buildx and OS capacity before bootstrap downloads.

This exports the committed NKD source, without ignored attempts, caches, Git
metadata or uncommitted changes. `NKE_LOCAL_CLONE` remains optional pinned-object
transport, not a build of its dirty working tree. The Linux launcher uses its
normal repository-relative `.nkd/runs` and `.nkd/laptop-bootstrap` inside this
native source directory. No catalog or NKD containment rule is changed. An external
absolute `--attempts-root` is rejected by the factory runner; do not symlink
`.nkd` to work around containment.
The data-mount path above was checked with Colima 0.10.3 and profile `nkd-demo`;
inspect `colima ssh --profile NAME -- df -h` before using a different profile.
No directory is created by `--check`. Reuse an existing export for fresh attempts
only when its intended inputs are unchanged, or create a new export for a new
source commit. A canonical authenticated Git clone into native storage is also
an option; the archive approach does not require GitHub credentials in Colima
for the NKD checkout itself.

The October 8 shared-mount trial failed during the SPIRE source snapshot with
`too many open files in system`, before Packer. An explicit clean-cache diagnostic
reduced the Colima host VM process from 48,737 open file handles to 33; the next
trial moves Run storage to native ext4 rather than adding automatic cache flushes
or raising Mac limits. See the [Docker execution evidence](evidence/2026-10-08-colima-local-os-build.md).
That investigation does not prove the retry or the QCOW2 succeeds.

Colima's configured 150 GiB disk does not provide 150 GiB of physical host free
space. Allow about 90 GiB free on both the host and the selected Run filesystem
before a fresh OS attempt, with at least 64 GiB on the Run filesystem remaining
at Packer admission. The launcher measures guest capacity; it does not reserve
physical space for a sparse disk. A native guest filesystem addresses the shared
filesystem boundary, not insufficient physical storage. A successful preflight
alone does not prove an OS bake or tenant lifecycle.

### Check before the expensive build

```sh
bash scripts/nke-full-arm64-local.sh --check components
bash scripts/nke-full-arm64-local.sh --check os
# Native Linux Docker alternative:
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh --check os
```

`--check` performs no image pulls, producer work or attempt creation. It diagnoses
tools, VM state, runtime reachability, architecture and preliminary OS resources.
It does not prove Git authorization, future image availability, cgroup-capped
capacity or Packer success. Complete-Plan and held-runtime admission remain
execution gates and recheck capacity immediately before Packer.

## Optional host CLI for inspection

Use your existing checkout, or clone it:

```sh
git clone https://github.com/dsx-ai-factory/nke-recipes.git
cd nke-recipes
cat .go-version
GOTOOLCHAIN=local make build
bin/nkdctl --version
bin/nkdctl --help
bin/nkdctl run --help
bash scripts/nke-full-arm64-local.sh --help
```

These optional host CLI commands need Git, Make and the exact Go version printed by `cat .go-version`.
Building may download Go modules. See [Development](../DEVELOPMENT.md) for setup
and repository checks. Git access is required while repositories are private;
a public guide does not grant access to private sources.

## Inspect before building

These commands do not start containers or build an OS image:

```sh
bin/nkdctl definition validate \
  --definition definitions/nke-laptop-arm64/definition.json

NKD_LOCAL_PREVIEW=$(mktemp -d)
bin/nkdctl run plan \
  --definition definitions/nke-laptop-arm64/definition.json \
  --environment definitions/nke-laptop-arm64/environment-local.json \
  --json > "$NKD_LOCAL_PREVIEW/plan.json"
bin/nkdctl run plan --plan "$NKD_LOCAL_PREVIEW/plan.json"

bin/nkdctl run local \
  --definition definitions/nke-laptop-arm64/definition.json \
  --environment definitions/nke-laptop-arm64/environment-local.json \
  --attempt first-preview --dry-run
```

Validation prints the Definition name, digest and `outputs: 23`. Plan inspection
prints expected outputs, producer instructions, operations and publication routes.
This Environment has no publication route. The preview ends with `pending`,
`static-unqualified`, and `observed outputs: 0`: those are expected preview results,
not a successful build. Keep canonical `--json` bytes unchanged;
`--pretty` is a display-only view. Plan loading may access pinned Git objects
needed by selected pin sets even though it does not build artifacts.

## Understand the selected artifact set

The [example inputs](../definitions/nke-laptop-arm64/README.md) select the ARM64
counterpart of the combined M2 candidate plus two source-built providers, not every
module in the NKE repository.

| Output set | Operation |
| --- | --- |
| `nkectl` | Compile the ARM64 CLI from pinned NKE Git |
| `cwe` | Build the controller container image from pinned NKE Git |
| `controller-chart` | Build its chart and bind it to the produced controller image digest |
| `capnico-controller-arm64` | Compile CAPNICo from its pinned public Git release using its Dockerfile |
| `cluster-api-provider-provisioned-arm64` | Compile the provisioned provider from pinned NKE Git using its Dockerfile |
| Twelve dependency images | Acquire the exact locked ARM64 OCI payloads |
| `postgresql`, `spire`, `spire-crds`, `temporal` | Acquire four pinned upstream charts |
| `operator-tools` | Acquire digest-checked native ARM64 Helm and kubectl |
| `nke-node-image-arm64` | Prepare OS materials; invoke Packer for a non-FDE QCOW2 |

That is 15 container-image outputs, five charts, two executable bundles and one disk.
Producing the set does not mean compiling every third-party dependency from source.
The Plan shows exactly which operations are builds and acquisitions.

Fleet profiles and separately published OVN outputs are not selected. OVN
image/chart bytes are OS materials here. AMD64,
FDE, deployment, publication, signing, full-disk SBOM and qualification are not
enabled. Ordinary SBOMs describe composition, not dependency scans. The OS retains
the maintained older-SPIRE exception, omitting the DSX IMDS plugin.

## Prepare the ARM64 execution host

Execution requires Linux ARM64 and rootless Podman or explicitly selected local Docker, with `git`, `jq` and GNU `timeout`. On an
Apple Silicon Mac the launcher enters `podman-machine-default` as `core`, or the
explicit `COLIMA_PROFILE` for Docker; the
repository and optional source clone must be visible through the VM's `/Users` mount.

```sh
podman machine list
# Only if the existing VM is stopped:
podman machine start podman-machine-default
podman --connection podman-machine-default images
df -h .
```

Do not reset a VM to fix an image-listing error. In the laptop trial the rootful
remote API failed, but the rootless connection worked. The launcher uses rootless
Podman without changing your global default connection.

The OS requires 8 CPUs, 10 GiB available memory, at least 64 GiB available workspace
capacity and a six-hour execution ceiling. Runtime checks those requirements;
configured VM size alone is not admission. TCG is selected, not KVM. Allow additional
disk headroom for snapshots, OCI layouts and repeated attempts.
The failed laptop OS preparation alone used about 17 GiB before Packer. Roughly
90 GiB free before a fresh combined attempt is a practical starting allowance,
not a guarantee: the gate still requires 64 GiB remaining when Packer starts.

A capacity rejection reports CPU, memory and workspace as `observed/required`,
with units. Those measurements are not reservations. Free capacity or choose a
host that meets the authored request; do not lower it just to suppress the error.

### If the Plan says no execution class satisfies the VM build

An Environment with only `runtime`, `utilityContainer` and publication settings
does not describe an OS execution host. Use this example's complete
`environment-local.json` together with its `os-tcg.json` catalog overlay. The
Environment declares a Linux ARM64 class, CPU/memory/workspace capacity and timeout;
the overlay selects the source producer's TCG route and exact local toolchain.
The class describes requirements. Linux admission still measures the actual host.
An AMD64 utility image is not an ARM64 execution-class declaration, and changing
the utility image alone cannot fix a missing VM class.

For ordinary images and charts, use the component or provider loop instead of
copying an OS Definition and deleting its execution requirements.

## Build just the two infrastructure providers

This loop compiles both providers from pinned Git and needs no Packer image:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-providers 2 providers
```

CAPNICo uses its public `cluster-api-provider-nico` v0.0.45 source and source-owned
Dockerfile. The provisioned provider uses the maintained NKE controller source
pin. Both are wrapped BuildKit builds with declared network access, not acquired
provider images or hermetic builds. Their compiler/base images and module downloads
remain build inputs; this does not rebuild Linux or the compiler from source.
Local layouts appear under `.nkd/runs/first-providers/packages/`.

## Build the images, charts and tools first

This smaller loop omits only the disk and needs no Packer image:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-components 1 components
```

The launcher downloads the exact Go archive from the toolchain lock, verifies its
SHA-256 inside the pinned utility container and uses it to compile a static Linux ARM64
`nkdctl`, then runs it inside Linux, previews the selection and executes the 22-output
Run. Producer and utility containers still use their pinned images. One job is the conservative
starting point. Omit `NKE_LOCAL_CLONE` to use canonical remote Git URLs:

```sh
env -u NKE_LOCAL_CLONE \
  bash scripts/nke-full-arm64-local.sh remote-components 1 components
```

Private-source Git authentication must work inside the VM, not just in the Mac's
`gh` session. Never put a PAT into catalogs, commands or logs. Charts, images and
declared dependency preparation still need network access.

## Build the complete selection, including the OS

The Podman route reuses the exact recorded local Packer image if present.
Otherwise it builds locked toolchain sources with `--diagnostic-local`; Docker
also builds and natively validates its toolchain, including when executing inside
Colima. Fresh bytes are not relabeled
as the older maintained ID. The launcher freezes the actual image ID through
the existing catalog-overlay contract in `.nkd/laptop-bootstrap/ATTEMPT/`, then
compiles the executable Plan from those inputs. Runtime rechecks the exact image.
Nothing updates maintained locks, pushes images or grants publication/qualification
approval. Native tool validation failures stop execution. See the
[toolchain guide](../toolchains/nkd-packer/README.md).

When prerequisite diagnostics pass:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-full 1 all
# Native Linux Docker alternative:
CONTAINER_RUNTIME=docker \
  bash scripts/nke-full-arm64-local.sh first-full-docker 1 all
```

`all` selects one combined 23-output Definition. `os` selects only the disk and
its material closure. Give every build a fresh attempt name. Earlier attempts are
retained and cannot be resumed or overwritten. A failed Run is not success even
if some payloads exist.

## Find outputs and interpret the result

For `first-components`:

```text
.nkd/runs/first-components/
  lifecycle.json       # required/observed outputs, lane, status, coded failure
  packages/<name>/     # ordinary artifact OCI layouts
  <name>/out/          # source-produced bytes
  <name>/fetch/        # acquired bytes and source snapshots
  <name>/results/      # exact producer results
  .runner/plan.json    # canonical execution Plan
```

For `first-full`, a completed disk export is
`.nkd/runs/first-full/outputs/nke-node-image-arm64.qcow2`.

For the native Colima export, these paths are relative to `$NKD_COLIMA_REPO`
inside Linux, not to your Mac checkout. Inspect them inside the same VM:

```sh
NKD_COLIMA_REPO=/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-$(git rev-parse HEAD)
colima ssh --profile nkd-demo -- jq \
  '{attempt,status,lane,expectedCount:(.expected|length),observedCount:(.observed|length),failure}' \
  "$NKD_COLIMA_REPO/.nkd/runs/first-colima-os/lifecycle.json"
```

Only a completed Run exports the accepted QCOW2 under `outputs/`. Source trees,
partial producer outputs and a toolchain image in a failed attempt are not that
export. Keep the native Run files with the VM; resetting/deleting its disk would
remove them. Export and independently hash the completed disk before moving it
to another environment.

```sh
jq '{attempt,status,lane,expectedCount:(.expected|length),
     observedCount:(.observed|length),failure}' \
  .nkd/runs/first-components/lifecycle.json
jq '.graph.artifacts[] | select(.id|startswith("package/")) |
    {id,kind,path}' .nkd/runs/first-components/.runner/plan.json
```

A completed local Run must observe its entire required output set.
Expect `status: complete`, with equal expected/observed counts. `failed` retains
the coded error. The lane remains `unqualified-local-execution`.
The helper Plan uses `.nkd/factory` inside its container; on the host that prefix
maps to the attempt directory. There is no `nkdctl run show`: inspect the saved
record and use `run plan --plan` for the retained Plan.

`packages` contains OCI layouts, not registry URLs or automatically loaded Podman
images. The bound controller chart is the packaged output, not necessarily the
earlier source-produced `.tgz`. This example does not push images or charts.

## Try the built CLI and load the controller image

The produced `nkectl` is a Linux ARM64 executable, not a macOS binary. On native
Linux ARM64, after the component Run completes:

```sh
.nkd/runs/first-components/nkectl/out/nkectl-arm64/nkectl --help
.nkd/runs/first-components/nkectl/out/nkectl-arm64/nkectl version
```

On macOS, run the same file inside the VM (substitute your repository's absolute path):

```sh
podman machine ssh --username core podman-machine-default \
  /absolute/path/to/nke-recipes/.nkd/runs/first-components/nkectl/out/nkectl-arm64/nkectl --help
```

To import the source-produced controller OCI archive into your rootless Podman
store on macOS:

```sh
podman --connection podman-machine-default load --input \
  .nkd/runs/first-components/cwe/out/nke-controller/nke-controller-image-arm64.oci.tar
podman --connection podman-machine-default images --all
```

On native Linux use `podman load` without the remote connection flag. Loading
changes your local image store, not a registry. Podman's returned image ID is not
the OCI manifest digest recorded by NKD. CLI help/version and image import are
smoke checks, not controller deployment or Kubernetes acceptance.

## Inspect the packaged controller chart

The image-bound chart is the Helm content layer of its local OCI layout. Locate
that layer using the OCI index and manifest, rather than guessing a blob filename:

```sh
NKD_LOCAL_RUN="$(pwd)/.nkd/runs/first-components"
NKD_LOCAL_CHART="$NKD_LOCAL_RUN/packages/controller-chart"
NKD_LOCAL_MANIFEST=$(jq -er '.manifests | select(length == 1) |
  .[0].digest | sub("^sha256:"; "")' "$NKD_LOCAL_CHART/index.json")
NKD_LOCAL_LAYER=$(jq -er '.layers |
  map(select(.mediaType == "application/vnd.cncf.helm.chart.content.v1.tar+gzip")) |
  select(length == 1) | .[0].digest | sub("^sha256:"; "")' \
  "$NKD_LOCAL_CHART/blobs/sha256/$NKD_LOCAL_MANIFEST")

# Native Linux ARM64:
"$NKD_LOCAL_RUN/operator-tools/fetch/helm/helm" show chart \
  "$NKD_LOCAL_CHART/blobs/sha256/$NKD_LOCAL_LAYER"
```

On macOS, replace the last command with:

```sh
podman machine ssh --username core podman-machine-default \
  "$NKD_LOCAL_RUN/operator-tools/fetch/helm/helm" show chart \
  "$NKD_LOCAL_CHART/blobs/sha256/$NKD_LOCAL_LAYER"
```

This uses the acquired native Helm executable and the final packaged chart, not
the earlier source archive. `show chart` should identify `nke-controller` version
`0.1.1` for this pin. `show values` can inspect its bound image values instead.
These are read-only development views, not signed artifact verification or a
deployment. Loading an image into Podman does not provide a registry endpoint
from which a Kubernetes cluster can pull it. The chart retains its source-owned
image repository while NKD binds the new image digest; this local Run does not
publish that digest there. Deployment needs an explicit development registry and
matching chart repository setting, outside this build-only example.

## Rebuild a committed local source change

`NKE_LOCAL_CLONE` changes Git transport only. It supplies the pinned commit;
it does not select uncommitted edits or automatically follow your checked-out branch.

For this CLI example, start a development branch from `cli/v0.1.0-rc.2` in a
dedicated clean NKE clone or worktree. Do not switch a shared dirty checkout.
Make and commit the intended CLI change there, then update only this example's
CLI pin through `catalog pin`:

```sh
NKE_DEV_CLONE=/absolute/path/to/nke
NKE_DEV_COMMIT=$(git -C "$NKE_DEV_CLONE" rev-parse HEAD)
GIT_CONFIG_COUNT=1 \
GIT_CONFIG_KEY_0="url.$NKE_DEV_CLONE.insteadOf" \
GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke \
  bin/nkdctl catalog pin \
  --dir definitions/nke-laptop-arm64/cli-catalog \
  --commit "$NKE_DEV_COMMIT" nke

git diff -- definitions/nke-laptop-arm64/cli-catalog
NKE_LOCAL_CLONE="$NKE_DEV_CLONE" \
  bash scripts/nke-full-arm64-local.sh cli-change-01 1 components
```

The templates derive CLI commit/date/version stamps from the selected pin. Do not
hand-edit lock digests. Exact-commit pinning makes no release-tag claim. Review
the new Plan before building. This command still produces the selected 22-output
set, not an incremental reuse of earlier attempts. For tight code-edit loops use
NKE's own Make/tests, then use NKD to exercise packaging and the artifact graph.
Copy shared catalogs before changing controller/OS selections; do not repin the
maintained release simply to test local changes.

A different baseline may need different producer instructions and toolchains.
The checked local NKE `main` uses Go workspace 1.27.1 without the old committed
vendor tree and changes the CLI linker target; this example's CLI release uses
the older vendored build and its matching image. Repinning to that main commit
alone is not a supported migration. Update the source-owned build interface,
dependency preparation and pinned compiler together before testing that baseline.

To preview a pin change without writing, add `--check` before `nke`. A proposed
change prints its old/new revision and returns `NKD-E-DIFF` (exit 3); that is the
documented difference signal, not a failed Git fetch.

## Understand the Packer handoff

The OS graph prepares `nke-agent.deb`, CNI/Kubernetes/SPIRE/OVN materials, image
inputs and a patched installer ISO. NKD verifies selected results and stages the
material manifest and local payloads. Source-owned NKE Packer consumes those inputs
and assembles the QCOW2; it does not call `nkdctl` to compile the agent.
NKE Make remains the direct development entry point. NKD exercises the pinned
material graph and handoff. Ubuntu installation can still access APT; this is not
a hermetic or byte-reproducible OS claim.

## Validation status

The new container-bootstrap Run `laptop-container-components-arm64-20261007-01`
completed with exit zero, 22 expected and 22 observed outputs, and no failure.
It built the runner inside the pinned utility container using the verified Go
archive, then executed the Linux ARM64 Run in the existing rootless Podman VM.
The produced CLI executed `version`; the packaged controller chart's digest
binding matched the observed controller image. See the
[container-bootstrap evidence](evidence/2026-10-07-local-container-bootstrap.md).
This trial used an existing development VM and a local NKE Git transport override;
it is not a clean-machine or remote-only authentication test.

The separate source-built provider Run `laptop-providers-arm64-20261007-01`
completed with 2/2 observed outputs and both produced images passed offline help
probes. See the [exact provider evidence](evidence/2026-10-07-laptop-provider-builds.md).
That older provider trial is not the complete 22-output component Run above.

The laptop component Run `laptop-dev-arm64-20261007-05` completed with launcher
exit 0, `status: complete`, 20 expected and 20 observed outputs, no failure, and
lane `unqualified-local-execution`. It used rootless Podman, four jobs and the
local Git transport override. The produced ARM64 CLI executed help/version, its
controller OCI archive imported into Podman, and the final packaged chart opened
with the acquired Helm executable. Its bound image digest matched the observed
controller image. The launcher remained unchanged throughout this Run.

Comparing this Run with the preceding completed component Run found matching
build identities for all 20 outputs and matching payload digests for 18. The
controller image and its image-bound chart differed. This trial therefore does
not establish exact-byte reproducibility; neither Run is qualified.

The earlier acquired-image final-observation failure is covered by regression
tests, alongside mixed disk/non-disk utility admission and native Linux ARM64
race checks. The OS trial built its materials and patched ISO, then failed
capacity re-admission before Packer. That historical Run predates the two provider
additions. A complete 22-output component Run or combined 23-output Run is not
established by that historical trial. Raw attempts remain in `.nkd`; repository tests do not prove a disk bake,
remote-only Git access or registry publication. These historical Runs used the
earlier launcher; they do not prove the new container bootstrap, fresh Packer
assembly or a clean-machine Docker run. Record those separately.
