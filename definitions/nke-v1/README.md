# `definitions/nke-v1` — NKE CLI Definition

This production Definition builds `nkectl` from the pinned NKE source, observes its exact bytes, packages the named `nkectl` output, and can publish one signed OCI envelope. The reusable Recipe remains catalog-owned. Runtime, destination, and signing choices live in independently selected Environments, so they do not change the logical Recipe identity.

| File | Purpose |
|---|---|
| `definition.yaml` | Public Definition: Recipe selection and the named `nkectl` executable output. |
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

The attempt record is `.nkd/runs/<attempt>/lifecycle.json`. It records the frozen expected `nkectl` output and the independently reopened payload observation. A dry-run signing Environment remains `static-unqualified`; it does not create a verified Release capability.

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
branch. The GitHub Environment targets `ghcr.io/njhensley/nke-recipes/nkectl` and deliberately keeps signing
in dry-run mode; registry access and NKE source access must be available to the hosted runner. This is not a
production signing workflow until the deferred protected-environment and real-signing acceptance are complete.

The GitHub Environment fetches the canonical `NVIDIA/nke` source through the private `njhensley/nke` fork.
Its `sourceAccess` entry references the repository Actions secret `NKE_SOURCE_TOKEN`, which must grant
read-only Contents access to that fork. Only the matching fetch step receives the token; Git configuration
is process-local, and the pinned commit/tree checks remain mandatory. The local Environment continues to
use the existing command-scoped mirror override described above.
