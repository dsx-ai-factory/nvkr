# `definitions/nke-v1` — Combined NKE Definition

This Definition builds `nkectl`, the complete upstream CWE runtime image, and the quickstart Helm bundle from
one pinned NKE source. It observes all three named outputs and generates their OCI publication and signing
jobs. Recipes remain catalog-owned. Runtime, destination, and signing choices live in independently selected
Environments. Combined hosted acceptance remains pending; earlier signed CLI runs do not prove this graph.

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
their independently reopened payload observations. A dry-run signing Environment remains `static-unqualified`;
it does not create a verified Release capability. The CLI and source-chart archive builds are offline. CWE
uses the pinned rootless BuildKit image with network access for the upstream Dockerfile's base images and
Alpine packages; this wrapped build does not claim hermeticity or exact image reproduction.

## Helm image binding

The bundle preserves the complete upstream quickstart tree: three local charts and versioned references to
external charts. It does not vendor those external charts. Packaging binds `007-controller` to the native CWE
manifest digest produced by the same graph and refreshes `checksums.txt`. The adapter currently requires the
plain rendered, single-container Deployment in `templates/controller.yaml`; ambiguous or already-templated
input fails closed.

After verifying and extracting the Release, supply its CWE repository as `image.repository` in deployment
values. For this GitHub Environment that repository is `ghcr.io/njhensley/nke-recipes/cwe`. For example, rendering
the extracted controller chart uses:

```sh
helm template controller ./helm/007-controller \
  --set-string image.repository=ghcr.io/njhensley/nke-recipes/cwe
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

The pinned NKE source remains `cli/v0.1.0-rc.2`, commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`, tree `309887e8cdb8ee24a39785b864390eda3afce5ec`. Repin only through `nkdctl catalog pin`; never hand-edit the lock.

The committed generated workflow is `.github/workflows/nkd-run.yml`, with exact inputs under
`.github/nkd-run/`. Pull requests check regeneration only. To execute, dispatch the workflow on the default
branch. The GitHub Environment targets the `nkectl`, `cwe` and `helm` repositories under
`ghcr.io/njhensley/nke-recipes` and uses the
exact pinned cosign executable for keyless signing and provenance attestation. Configure the GitHub deployment
environment `nkd-release` with a deployment branch policy admitting only `main`. Registry access and NKE source
access must be available to the hosted runner. Real operational acceptance is recorded separately in the
[implementation/evidence record](../../docs/adr/reconciliation.md).

The GitHub Environment fetches the canonical `NVIDIA/nke` source through the private `njhensley/nke` fork.
Its `sourceAccess` entry references the repository Actions secret `NKE_SOURCE_TOKEN`, which must grant
read-only Contents access to that fork. Only the matching fetch step receives the token; Git configuration
is process-local, and the pinned commit/tree checks remain mandatory. The local Environment continues to
use the existing command-scoped mirror override described above.
