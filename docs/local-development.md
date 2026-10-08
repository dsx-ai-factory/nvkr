# Build and develop NKE locally with NKD

Inspect the ARM64 NKE artifact set, build it locally, and rebuild a committed source
change. No previous NKD experience is required. Run commands from the `nke-recipes`
repository root and keep the same terminal open.

NKD is the project; `nkdctl` is its command-line tool. You provide three inputs:

| Input | What you edit |
| --- | --- |
| Definition | Which named artifacts to produce and their platform |
| Catalog and machine-managed pins | How to build/acquire components and their exact sources/tools |
| Environment | Where to execute, with which runtime and resources |

`run plan` describes the work. `run local` executes it as a new immutable attempt.
Local builds are unqualified diagnostics, not signed releases or cluster deployment.

## Set up the CLI

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

You need Git, Make and the exact Go version printed by `cat .go-version`.
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

Validation prints the Definition name, digest and `outputs: 21`. Plan inspection
prints expected outputs, producer instructions, operations and publication routes.
This Environment has no publication route. The preview ends with `pending`,
`static-unqualified`, and `observed outputs: 0`: those are expected preview results,
not a successful build. Keep canonical `--json` bytes unchanged;
`--pretty` is a display-only view. Plan loading may access pinned Git objects
needed by selected pin sets even though it does not build artifacts.

## Understand the selected artifact set

The [example inputs](../definitions/nke-laptop-arm64/README.md) select the ARM64
counterpart of the combined M2 candidate, not every module in the NKE repository.

| Output set | Operation |
| --- | --- |
| `nkectl` | Compile the ARM64 CLI from pinned NKE Git |
| `cwe` | Build the controller container image from pinned NKE Git |
| `controller-chart` | Build its chart and bind it to the produced controller image digest |
| Twelve dependency images | Acquire the exact locked ARM64 OCI payloads |
| `postgresql`, `spire`, `spire-crds`, `temporal` | Acquire four pinned upstream charts |
| `operator-tools` | Acquire digest-checked native ARM64 Helm and kubectl |
| `nke-node-image-arm64` | Prepare OS materials; invoke Packer for a non-FDE QCOW2 |

That is 13 container-image outputs, five charts, two executable bundles and one disk.
Producing the set does not mean compiling every third-party dependency from source.
The Plan shows exactly which operations are builds and acquisitions.

CAPNICo, the provisioned-provider image, fleet profiles and separately published
OVN outputs are not selected. OVN image/chart bytes are OS materials here. AMD64,
FDE, deployment, publication, signing, full-disk SBOM and qualification are not
enabled. Ordinary SBOMs describe composition, not dependency scans. The OS retains
the maintained older-SPIRE exception, omitting the DSX IMDS plugin.

## Prepare the ARM64 execution host

Execution requires Linux ARM64 and rootless Podman, with `git` and `jq`. On an
Apple Silicon Mac the launcher enters `podman-machine-default` as `core`; the
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

## Build the images, charts and tools first

This smaller loop omits only the disk and needs no Packer image:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-components 1 components
```

The launcher uses the required host Go version to compile a static Linux ARM64
`nkdctl`, then runs it inside Linux, previews the selection and executes the 20-output
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

The OS inputs require the exact local Packer image in
`toolchains/nkd-packer/toolchain.lock.json`. On macOS check it with:

```sh
podman machine ssh --username core podman-machine-default \
  podman image inspect \
  sha256:03a8b35467adfd1fc3ca496adf87ac0cd23ce35782290409d2832578eaad7d51
```

Fresh Packer bootstrap is currently blocked: rebuilding the current Dockerfile
did not match that older local pin. The launcher refuses a replacement rather
than weakening the check. Until that native pin is refreshed and validated, the OS
route requires the already-present exact image; this is not a fresh-machine full
build quickstart. See the [toolchain guide](../toolchains/nkd-packer/README.md).

When the exact image and required capacity are available:

```sh
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-full 1 all
```

`all` selects one combined 21-output Definition. `os` selects only the disk and
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
the new Plan before building. This command still produces the selected 20-output
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
capacity re-admission before Packer. No new completed combined 21-output Run is
claimed. Raw attempts remain in `.nkd`; repository tests do not prove a disk bake,
remote-only Git access, registry publication or a fresh-machine Packer bootstrap.
