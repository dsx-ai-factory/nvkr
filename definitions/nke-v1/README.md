# `definitions/nke-v1` — Combined NKE Definition

This Definition builds `nkectl`, the complete upstream CWE runtime image, and the quickstart Helm bundle from
independently pinned CLI and controller module tags in NKE. It observes all three named outputs and generates their OCI publication and signing
jobs. Recipes remain catalog-owned. Runtime, destination, and signing choices live in independently selected
Environments. The [combined hosted run and independent consumers](../../docs/evidence/2026-09-10-combined-run.md)
passed for all three outputs, including real signing and complete Run finalization. That recorded run predates
the controller tag/version correction described below; it does not validate a hosted run of these updated inputs.

| File | Purpose |
|---|---|
| `definition.yaml` | Public Definition: Recipes, CLI/image/Helm outputs, and the Helm-to-image binding. |
| `environment-local.yaml` | Local registry and dry-run signing settings. |
| `environment-github.yaml` | Hosted GitHub destination and signing settings. |
| `catalog/registry.yaml` | Catalog-owned NKE Recipe and source declaration. |
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
export GIT_CONFIG_VALUE_0="https://github.com/NVIDIA/nke"
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

The bundle preserves the complete upstream quickstart tree: three local charts and versioned references to
external charts. It does not vendor those external charts. Packaging binds `007-controller` to the native CWE
manifest digest produced by the same graph and refreshes `checksums.txt`. The adapter currently requires the
plain rendered, single-container Deployment in `templates/controller.yaml`; ambiguous or already-templated
input fails closed.

The chart source is the committed generated quickstart snapshot at the controller pin, not a fresh AICR
generation. Upstream chart `version` and `appVersion` metadata are preserved; they are not the CWE binary's
release version. The source commit and the bound image digest identify this packaged snapshot.

After verifying and extracting the Release, supply its CWE repository as `image.repository` in deployment
values. For this GitHub Environment that repository is `ghcr.io/nvidia/nke-recipes/cwe`. For example, rendering
the extracted controller chart uses:

```sh
helm template controller ./helm/007-controller \
  --set-string image.repository=ghcr.io/nvidia/nke-recipes/cwe
```

The immutable digest is embedded in the template; overriding `image.digest` cannot change it. The default
repository is empty, so missing deployment configuration fails explicitly. When using the upstream `deploy.sh`,
set the repository in the extracted controller `cluster-values.yaml` as a deployment overlay. Registry access
for the target cluster remains deployment configuration. Rendering is not proof of service startup or a
successful cluster deployment.

## Generate hosted CI

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

The CLI source `nke` is pinned to `cli/v0.1.0-rc.2`. The CWE and Helm source `nke-controller` is pinned to
`controller/v0.1.1-rc.1`. Both tags currently resolve to commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`,
tree `309887e8cdb8ee24a39785b864390eda3afce5ec`; selecting the controller tag does not upgrade source bytes.
CWE stamps `v0.1.1-rc.1` from that module tag, matching upstream's prefix stripping, instead of synthesizing
a version from the commit. The sources can be repinned independently, while CWE and its chart snapshot move together:

```sh
nkdctl catalog pin --dir definitions/nke-v1/catalog --tag v0.1.0-rc.2 nke
nkdctl catalog pin --dir definitions/nke-v1/catalog --tag v0.1.1-rc.1 nke-controller
```

Repin only through `nkdctl catalog pin`; never hand-edit the lock. Regenerate CI after changing pins.

The committed generated workflow is `.github/workflows/nkd-run.yml`, with exact inputs under
`.github/nkd-run/`. Pull requests check regeneration only. To execute, dispatch the workflow on the default
branch. The GitHub Environment targets the `nkectl`, `cwe` and `nke-helm` repositories under
`ghcr.io/nvidia/nke-recipes` and uses the
exact pinned cosign executable for keyless signing and provenance attestation. Configure the GitHub deployment
environment `nkd-release` with a deployment branch policy admitting only `main`. Registry access and NKE source
access must be available to the hosted runner. Real operational acceptance is recorded separately in the
[implementation/evidence record](../../docs/adr/reconciliation.md).

The GitHub Environment fetches pinned source directly from the internal `NVIDIA/nke` repository.
Its `sourceAccess` entry references the repository Actions secret `NKE_SOURCE_TOKEN`, which must grant
read-only Contents access to `NVIDIA/nke`. Only the matching fetch step receives the token; Git configuration
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
`publication.repositoryPrefix`; repositories derive from Definition output names. Publication writes the
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
