# NVKE CLI, controller image and chart

The [quickstart](../../quickstart.md) builds three outputs using the maintained NVKE release catalog:
`nvkectl`, `nvke-controller` and `nvke-controller-chart`. The chart binds to the image built in the same Run.
The commands select the input file automatically for your processor architecture.

| File | Platform |
|---|---|
| `definition-amd64.yaml` | AMD64 Linux; produces Linux AMD64 artifacts. |
| `definition-arm64.yaml` | Apple Silicon and ARM64 Linux; produces Linux ARM64 artifacts. |
| `environment-local-amd64.yaml`, `environment-local-arm64.yaml` | Matching native utility execution, with publication disabled unless explicitly requested. |

Native artifact builds have been exercised on Apple Silicon macOS and Ubuntu ARM64/AMD64.
Intel Mac input selection and SDK checksums are present, but native Intel Mac execution remains unverified.

Both selections use the existing NVKE producers and source pins from
[`inputs/catalogs/nvke/release`](../../../inputs/catalogs/nvke/release). The ARM64 selection uses the
[native ARM64 scanner](../../../inputs/catalogs/tools/sbom-arm64/README.md) at the same Syft version.
Each Environment runs utility helpers on its matching architecture. No catalog edits or NVKE source
checkout are required. The host NVKR CLI runs directly on macOS or Linux; Docker runs the build tools.

## Preview the native build

From an NVKR checkout, copy this block to build the CLI and preview the selected work.
You need Git, the Go version in `.go-version` and network access; Docker and Make are not required.
Compiler installation is separate from this preview; automatic compiler downloads stay disabled.

```sh
(
  set -eu
  export GOTOOLCHAIN=local
  NVKR_VERSION="$(git describe --tags --always --dirty)"
  NVKR_COMMIT="$(git rev-parse --short HEAD)"
  go build -ldflags "-X main.version=$NVKR_VERSION -X main.commit=$NVKR_COMMIT" -o bin/nvkr ./cmd/nvkr
  bin/nvkr run local \
    --definition "docs/examples/nvke-tutorial/definition-$(go env GOHOSTARCH).yaml" \
    --environment "docs/examples/nvke-tutorial/environment-local-$(go env GOHOSTARCH).yaml" \
    --attempt quickstart-preview --dry-run
)
```

A preview reports `pending`, `static-unqualified` and zero observed outputs.
It builds no NVKE artifacts and creates no attempt workspace. Follow the
[quickstart](../../quickstart.md) for the real three-artifact build.

## Retained AMD64 example

The separate `definition.yaml` is retained for existing demos and scratch hosted generation.
Its output names are `nvkectl`, `cwe` and `nvke-helm`. The instructions below describe that selection.
Use the architecture-specific files above for the quickstart.

This retained example builds `nvkectl`, the NVKE controller image (output identifier `cwe`), and the
source controller Helm chart from NVKE `v0.22.0`. The chart depends on the exact built controller image. Producer
instructions live in the catalog; the Definition selects and packages outputs; the Environment selects
runtime, destination and signing.

The repository's committed production and full development workflows use [M2](../../../inputs/definitions/nvke/README.md).
This three-output Definition retains a smaller unsigned development example for scratch generation.
Its [historical hosted run](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-10-combined-run.md)
passed real signing, complete Run finalization and independent consumption in a personal namespace.
That record predates the current schema, tool pins, Helm binding and namespace; it does not validate
a hosted run of these updated inputs. A complete publication using this source chart remains unexercised.

| File | Purpose |
|---|---|
| `definition.yaml` | Public Definition: direct producers, CLI/image/Helm outputs, and the Helm-to-image binding. |
| `environment-local.yaml` | Local Docker execution, optional registry destination and dry-run signing settings. |
| `environment-github.yaml` | Hosted GitHub destination and signing settings. |
| `environment-github-dev.yaml` | Build-only branch dispatch with one-day output retention and no signing or publication. |
| `../../../inputs/catalogs/nvke/module-tags/registry.yaml` | Catalog-owned NVKE components, platforms, and source declarations. |
| `../../../inputs/catalogs/nvke/module-tags/pins.lock.yaml` | Machine-managed exact source pins. |

## Inspect and run

Build `bin/nvkr` using [CLI setup](../../quickstart.md#build-only-the-cli), then run
these commands from the repository root. The [quickstart](../../quickstart.md#before-you-start)
owns the platform and Docker prerequisites. Local Runs compile a helper and still need the pinned Go compiler.
For another native three-output build, rerun `bash scripts/nvkr-quickstart.sh`; it automatically reuses its local SDK.

If the launcher installed your only Go compiler, activate that SDK before custom commands in this terminal:

```sh
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64) NVKR_SDK_PLATFORM=darwin-arm64 ;;
  Darwin/x86_64) NVKR_SDK_PLATFORM=darwin-amd64 ;;
  Linux/aarch64|Linux/arm64) NVKR_SDK_PLATFORM=linux-arm64 ;;
  Linux/x86_64|Linux/amd64) NVKR_SDK_PLATFORM=linux-amd64 ;;
  *) echo 'Unsupported quickstart SDK platform'; false ;;
esac &&
export PATH="$PWD/.nvkr/quickstart-sdk/go$(cat .go-version).$NVKR_SDK_PLATFORM/go/bin:$PATH" &&
export GOTOOLCHAIN=local
```

This changes only the current terminal, without modifying a shell profile. With an existing matching Go,
set `GOTOOLCHAIN=local` and keep that compiler on `PATH`.

```sh
bin/nvkr definition validate --definition docs/examples/nvke-tutorial/definition.yaml

bin/nvkr run plan \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml

# Admission and node planning only; creates no attempt storage.
bin/nvkr run local \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml \
  --attempt local-check --dry-run

# Build all three selected outputs locally, without a registry or publication.
NVKR_ATTEMPT="tutorial-$(date -u +%Y%m%d-%H%M%S)-$$"
bin/nvkr run local \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-local.yaml \
  --attempt "$NVKR_ATTEMPT" --jobs 1 --ui plain
```

To use a local NVKE clone without changing catalog identity, scope Git's `insteadOf` setting to the command process:

```sh
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$HOME/dev/code/github/dsx-ai-factory/nvke.insteadOf"
export GIT_CONFIG_VALUE_0="https://github.com/dsx-ai-factory/nvke"
```

The attempt record is `.nvkr/runs/<attempt>/lifecycle.json`. It records all three frozen expected outputs and
their independently reopened payload observations. Planning with dry-run signing is `static-unqualified`; executed local Run records use
`unqualified-local-execution`. Neither creates a verified Release capability. The CLI and source-chart archive builds are offline. The controller producer
uses the pinned rootless BuildKit image with network access for the upstream Dockerfile's base images and
Alpine packages; this wrapped build does not claim hermeticity or exact image reproduction. The Docker host
must support unprivileged overlayfs (Linux 5.11+ or an Ubuntu kernel); native snapshot fallback is disabled
because full-tree copies can exhaust hosted-runner disk space.

## Dependency SBOMs

The Definition enables dependency inspection with `sbomTool: syft`; the catalog lock supplies exact archive
and executable digests shared by both Environments. The CLI and image SBOMs inventory packages discovered in their produced bytes. The Helm SBOM inventories
bundled chart metadata and external chart references. See [dependency SBOMs](../../dependency-sboms.md) for
scope, limits and offline checks on actual artifacts. These populated documents supersede the source-metadata-only
SBOMs emitted by earlier runs; existing signed publications are not modified.

## Helm image binding

The producer packages `controller/deploy/charts/controller` from the pinned controller source. The Definition
binds its `controller` image slot to `cwe`; the catalog maps that slot to `image.digest`. Packaging writes the
verified native image manifest digest into `values.yaml`. Root charts preserve upstream checksum files;
nested wrappers refresh binder-owned checksums outside the chart. Templates and other values remain
unchanged. This chart must already support digest-based image selection.

After verifying and extracting the Release, supply the repository containing its controller image:

```sh
helm template controller ./controller \
  --set-string image.repository=ghcr.io/dsx-ai-factory/nvkr/cwe
```

Registry selection does not change the packaged chart bytes. Normal Helm values overrides still apply;
overriding or clearing `image.digest` changes image selection. Deployment enforcement is outside NVKR's
packaging contract.

## Generate hosted CI

For feature-branch generator changes, use `make workflows-generate` and `make workflows-check`, then
commit and push your reviewed branch. `make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr`
dispatches the current pushed commit after preflight using the full M2 development Environment.
Development Environments generate `.github/workflows/nvkr-dev.yml` and `.github/nvkr-dev/`, independently
of the production paths below. Generate this smaller unsigned example only into a scratch root.
See [pipeline development](../../../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) for one-time
registration, source access, development signing/publication, downloads and cleanup.

The repository commits the [M2 candidate](../../../inputs/definitions/nvke/README.md), selected by `make workflows-check`.
This checked-in configuration is not a ratified canonical-product decision.

Generate this alternative into a scratch directory. Generating it into the repository root would
replace the maintained M2 bundle and fail `make workflows-check`. Pinned-tree access may be
needed during catalog loading; configure your existing Git credentials or the transport override above.

```sh
NVKR_V1_BUNDLE=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-v1-bundle.XXXXXX")
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_V1_BUNDLE"

bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_V1_BUNDLE" --check
```

Generated CI recompiles these repository-relative inputs, executes the internal graph, and finalizes an immutable Run record from exact restored outcomes. Static generation and contract tests do not establish that GitHub, a registry, OIDC, or cosign ran.

The CLI source `nvke` and the shared controller/chart source `nvke-controller` are pinned to
`v0.22.0` (`400c24fb6465b042be883d6b62472528520d6291`). The controller pin supplies the
source chart's existing digest support. Each source role has its own lock entry;
update both for a shared NVKE release:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/nvke/module-tags --tag v0.22.0 nvke
bin/nvkr catalog pin --dir inputs/catalogs/nvke/module-tags --tag v0.22.0 nvke-controller
```

Repin only through `nvkr catalog pin`; never hand-edit the lock. Regenerate CI after changing pins.

The generated workflow path is `.github/workflows/nvkr-run.yml`, with exact inputs under
`.github/nvkr-run/`. Its current repository selection is M2, not this three-output alternative; use
`make workflows-check` for that maintained selection. Dispatch runs whichever reviewed bundle is committed
at the selected ref and does not choose a different Definition. This alternative's GitHub Environment targets
the `nvkectl`, `cwe` and `nvke-helm` repositories under `ghcr.io/dsx-ai-factory/nvkr` and uses the
exact pinned cosign executable for keyless signing and provenance attestation. Configure the GitHub deployment
environment `nvkr-release` with a deployment branch policy admitting only `main`. Registry access and NVKE source
access must be available to the hosted runner. Real operational acceptance is recorded separately in the
[implementation/evidence record](../../adr/reconciliation.md).

The GitHub Environment fetches pinned source directly from the internal `dsx-ai-factory/nvke` repository.
Its `sourceAccess` entry references the repository Actions secret `NKE_SOURCE_TOKEN`, which must grant
read-only Contents access to `dsx-ai-factory/nvke`. Only admitted source retrieval and pinned-source revalidation
steps receive the token; Git configuration
is process-local, and the pinned commit/tree checks remain mandatory. The local Environment continues to
use the existing command-scoped mirror override described above.

## Catalog tools and publication

`sbomTool: syft` and `cosignTool: cosign` select catalog sources. Their release declarations live in
`../../../inputs/catalogs/nvke/module-tags/registry.yaml`; `../../../inputs/catalogs/nvke/module-tags/pins.lock.yaml` binds source and executable bytes. Update through:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/nvke/module-tags --tag v1.51.1 syft
bin/nvkr catalog bump --dir inputs/catalogs/nvke/module-tags syft cosign
```

Regenerate the workflow after changing the lock. Each Environment configures one
`publication.repositoryPrefix`; the release index and SBOMs live under `<repositoryPrefix>/releases`, while each output has its own sibling repository. Publication writes the
root by digest and records `<prefix>/<output>@sha256:…`. No mutable tag is updated automatically.

## GitHub action pins

Workflow dependencies are catalog sources with `kind: github-action`. Their constraints select supported
release lines; their exact commits are in the same lock as NVKE and tool pins.

```sh
bin/nvkr catalog bump --dir inputs/catalogs/nvke/module-tags --check checkout download-artifact setup-go upload-artifact
bin/nvkr catalog bump --dir inputs/catalogs/nvke/module-tags checkout download-artifact setup-go upload-artifact
```

Regenerate the Run workflow after applying an update. No generator rebuild is needed. The adapter rejects
missing pins, unexpected action repositories and unsupported major versions before producing a workflow.

The current input contract is Definition v4 with catalog v3 and generated IR v3. Definition outputs describe
produced artifacts and package dependencies. Deployment topology, node roles and cluster validation belong to
the downstream deployment system; the release content subject still binds every exact named output.
