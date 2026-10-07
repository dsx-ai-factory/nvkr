# `definitions/nke-v1` — Combined NKE Definition

This Definition builds `nkectl`, the complete upstream CWE runtime image, and the source controller Helm chart from
independently pinned CLI and controller module tags in NKE. It observes all three named outputs and generates one shared OCI release publication and signing
jobs. Producer instructions remain catalog-owned; outputs select them directly. Runtime, destination, and signing choices live in independently selected
Environments. The [combined hosted run and independent consumers](../../docs/evidence/2026-09-10-combined-run.md)
passed for all three outputs, including real signing and complete Run finalization. That recorded run predates
the controller tag/version correction described below; it does not validate a hosted run of these updated inputs.

| File | Purpose |
|---|---|
| `definition.yaml` | Public Definition: direct producers, CLI/image/Helm outputs, and the Helm-to-image binding. |
| `environment-local.yaml` | Local registry and dry-run signing settings. |
| `environment-github.yaml` | Hosted GitHub destination and signing settings. |
| `catalog/registry.yaml` | Catalog-owned NKE components, platforms, and source declarations. |
| `catalog/pins.lock.yaml` | Machine-managed exact source pins. |

## Inspect and run

```sh
nkdctl definition validate --definition definitions/nke-v1/definition.yaml

nkdctl run plan \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml

# Admission and node planning only; creates no attempt storage.
nkdctl run local \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml \
  --attempt local-check --dry-run

# One immutable local attempt. Publication remains explicit.
nkdctl run local \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-local.yaml \
  --attempt local-001 --jobs 1 --publish
```

To use a local NKE clone without changing catalog identity, scope Git's `insteadOf` setting to the command process:

```sh
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$HOME/dev/code/github/NVIDIA/nke.insteadOf"
export GIT_CONFIG_VALUE_0="https://github.com/dsx-ai-factory/nke"
```

The attempt record is `.nkd/runs/<attempt>/lifecycle.json`. It records all three frozen expected outputs and
their independently reopened payload observations. Planning with dry-run signing is `static-unqualified`; executed local Run records use
`unqualified-local-execution`. Neither creates a verified Release capability. The CLI and source-chart archive builds are offline. CWE
uses the pinned rootless BuildKit image with network access for the upstream Dockerfile's base images and
Alpine packages; this wrapped build does not claim hermeticity or exact image reproduction. The Docker host
must support unprivileged overlayfs (Linux 5.11+ or an Ubuntu kernel); native snapshot fallback is disabled
because full-tree copies can exhaust hosted-runner disk space.

## Dependency SBOMs

The Definition enables dependency inspection with `sbomTool: syft`; the catalog lock supplies exact archive
and executable digests shared by both Environments. The CLI and image SBOMs inventory packages discovered in their produced bytes. The Helm SBOM inventories
bundled chart metadata and external chart references. See [dependency SBOMs](../../docs/dependency-sboms.md) for
scope, limits and offline checks on actual artifacts. These populated documents supersede the source-metadata-only
SBOMs emitted by earlier runs; existing signed publications are not modified.

## Helm image binding

The producer packages `controller/deploy/charts/controller` from the pinned controller source. The Definition
binds its `controller` image slot to `cwe`; the catalog maps that slot to `image.digest`. Packaging writes the
verified native image manifest digest into `values.yaml`. Root charts preserve upstream checksum files;
nested wrappers refresh binder-owned checksums outside the chart. Templates and other values remain
unchanged. This chart must already support digest-based image selection.

After verifying and extracting the Release, supply the repository containing its CWE image:

```sh
helm template controller ./controller \
  --set-string image.repository=ghcr.io/nvidia/nke-recipes/cwe
```

Registry selection does not change the packaged chart bytes. Normal Helm values overrides still apply;
overriding or clearing `image.digest` changes image selection. Deployment enforcement is outside NKD's
packaging contract. A new complete build/publication using this source chart remains unexercised.

## Generate hosted CI

The repository now commits the [M2 candidate](../nke-m2/README.md), selected by `make workflows-check`.
This is the checked-in configuration, not a ratified canonical-product decision. The commands below
replace it with this older three-output Definition; use a separate checkout
when exploring this alternative.

```sh
nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root .

nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root . --check
```

Generated CI recompiles these repository-relative inputs, executes the internal graph, and finalizes an immutable Run record from exact restored outcomes. Static generation and contract tests do not establish that GitHub, a registry, OIDC, or cosign ran.

The CLI source `nke` is pinned to `cli/v0.1.0-rc.2`. CWE and the chart share `nke-controller`, pinned to
`controller/v0.1.1-rc.9` (`340009f1e15d8e26940f1e03c9696f1ad251c0ae`). The controller pin supplies the
source chart's existing digest support. CLI and controller can be repinned independently; CWE and its chart
move together:

```sh
nkdctl catalog pin --dir definitions/nke-v1/catalog --tag v0.1.0-rc.2 nke
nkdctl catalog pin --dir definitions/nke-v1/catalog --tag v0.1.1-rc.9 nke-controller
```

Repin only through `nkdctl catalog pin`; never hand-edit the lock. Regenerate CI after changing pins.

The generated workflow path is `.github/workflows/nkd-run.yml`, with exact inputs under
`.github/nkd-run/`. Its current repository selection is M2, not this three-output alternative; use
`make workflows-check` for that maintained selection. Dispatch runs whichever reviewed bundle is committed
at the selected ref and does not choose a different Definition. This alternative's GitHub Environment targets
the `nkectl`, `cwe` and `nke-helm` repositories under `ghcr.io/dsx-ai-factory/nke-recipes` and uses the
exact pinned cosign executable for keyless signing and provenance attestation. Configure the GitHub deployment
environment `nkd-release` with a deployment branch policy admitting only `main`. Registry access and NKE source
access must be available to the hosted runner. Real operational acceptance is recorded separately in the
[implementation/evidence record](../../docs/adr/reconciliation.md).

The GitHub Environment fetches pinned source directly from the internal `dsx-ai-factory/nke` repository.
Its `sourceAccess` entry references the repository Actions secret `NKE_SOURCE_TOKEN`, which must grant
read-only Contents access to `dsx-ai-factory/nke`. Only admitted source retrieval and pinned-source revalidation
steps receive the token; Git configuration
is process-local, and the pinned commit/tree checks remain mandatory. The local Environment continues to
use the existing command-scoped mirror override described above.

## Catalog tools and publication

`sbomTool: syft` and `cosignTool: cosign` select catalog sources. Their release declarations live in
`catalog/registry.yaml`; `catalog/pins.lock.yaml` binds source and executable bytes. Update through:

```sh
nkdctl catalog pin --dir definitions/nke-v1/catalog --tag v1.51.1 syft
nkdctl catalog bump --dir definitions/nke-v1/catalog syft cosign
```

Regenerate the workflow after changing the lock. Each Environment configures one
`publication.repositoryPrefix`; the release index and SBOMs live under `<repositoryPrefix>/releases`, while each output has its own sibling repository. Publication writes the
root by digest and records `<prefix>/<output>@sha256:…`. No mutable tag is updated automatically.

## GitHub action pins

Workflow dependencies are catalog sources with `kind: github-action`. Their constraints select supported
release lines; their exact commits are in the same lock as NKE and tool pins.

```sh
nkdctl catalog bump --dir definitions/nke-v1/catalog --check checkout download-artifact setup-go upload-artifact
nkdctl catalog bump --dir definitions/nke-v1/catalog checkout download-artifact setup-go upload-artifact
```

Regenerate the Run workflow after applying an update. No generator rebuild is needed. The adapter rejects
missing pins, unexpected action repositories and unsupported major versions before producing a workflow.

The current input contract is Definition v4 with catalog v3 and generated IR v3. Definition outputs describe
produced artifacts and package dependencies. Deployment topology, node roles and cluster validation belong to
the downstream deployment system; the release content subject still binds every exact named output.
