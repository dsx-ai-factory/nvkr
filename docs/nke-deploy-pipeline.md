# NKE v0.21.0 deploy pipeline

[Definition](../definitions/nke-deploy/definition.yaml) → build/acquire → SBOMs → package → one signed Release.
The committed GitHub workflow now uses this Definition.

| Output | Input |
|---|---|
| `cwe` | Build the shared controller producer at `v0.21.0` |
| `controller-chart` | Package that controller's chart; set `image.digest` to the built CWE digest |
| `postgresql` | Acquire chart `18.5.15` |
| `spire` | Acquire chart `0.30.1` |
| `spire-crds` | Acquire chart `0.6.1` |
| `temporal` | Acquire chart `0.74.0` |

## Release pins

The umbrella source is **`v0.21.0`**, commit `33fb71b2f4224b470ebc628a9165fc4b92870406`.
Its `controller/deploy/nke-sbom/nke-deploy.yaml` determines the four external chart versions.
The [catalog](../definitions/nke-controller/catalog/registry.yaml) maps each chart explicitly; the
[machine-generated lock](../definitions/nke-controller/catalog/pins.lock.yaml) records its exact downloaded digest.
Re-pinning fails if upstream adds an unaccounted member or changes an accounted item's type/path.

The deploy Definition reuses the catalog introduced by upstream #69. Both controller sources
(`nke` and `nke-helm`) remain pinned to `v0.21.0`; the existing Helm dependency/package commands,
BuildKit image, and AMD64/ARM64 producer support are preserved. Git URLs use HTTPS for the existing
GitHub source-credential contract. The deploy Definition selects only controller/CWE and the five charts.
The separate controller Definitions retain their provider-image output and now bind each chart to its
matching architecture's controller image.

This builds the umbrella tag's source tree. It does not reconstruct the module-tag composition in NKE's
release-asset BOM, which names an older `controller/v0.1.1` commit. Automatic release-asset BOM derivation
remains unimplemented.

The source chart's templates and repository defaults stay unchanged. Installation supplies `image.repository`
for the destination registry. External chart archives retain their upstream bytes. SBOMs inspect chart metadata;
they do not scan or acquire the container images referenced by those external charts.

## Local

Use Linux AMD64 with Docker and source access. From the repository root:

```sh
# Optional: use an existing NKE clone without changing catalog identity.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url./absolute/path/to/nke/.insteadOf"
export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke

# Inspect the complete graph first.
go run ./cmd/nkdctl run local \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-local.yaml \
  --attempt nke-021-preview --dry-run

# Build/acquire/package all six outputs. Use a new attempt name for each run.
go run ./cmd/nkdctl run local \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-local.yaml \
  --attempt nke-021-001 --jobs 1
```

Local execution has no publication or signing. Its record is `.nkd/runs/nke-021-001/lifecycle.json`.
CWE uses upstream Dockerfile network dependencies; this is not a hermetic or qualified build.

## GitHub

The [GitHub Environment](../definitions/nke-deploy/environment-github.yaml) publishes under
`ghcr.io/dsx-ai-factory/nke-recipes` and selects keyless signing. Configure:

- `NKE_SOURCE_TOKEN`: read-only Contents access to `dsx-ai-factory/nke`.
- The `nkd-release` deployment environment: admit `main` and `feat/nke-deploy-pipeline`.
- GHCR package write access for this repository's `GITHUB_TOKEN`.

The Environment explicitly adds `feat/nke-deploy-pipeline` through `githubDispatchBranches`, and its
signing identity accepts that branch or `main`. The repository deployment policy must allow the same
branches. Source-token permissions and GHCR access still need runtime verification.

Dispatch the full pipeline from the branch:

```sh
gh workflow run nkd-run.yml --repo dsx-ai-factory/nke-recipes --ref feat/nke-deploy-pipeline
```

It builds, inventories, packages, publishes, signs, verifies, and finalizes the six required outputs.
Branch dispatch is opt-in: remove the exception and narrow the signing/deployment policies when branch
validation is finished. Generated wiring is not evidence of a completed release.

Catalog revalidation needs private Git access in bootstrap and finalization. Credentials are scoped to those
steps and source fetches, with repository-specific Git helpers. The merge gate acquires a fixed NKE checkout
before running the generator, with no persisted token. Fork PRs without private-source access fail closed;
no privileged PR trigger is used.

To regenerate, with Git access configured:

```sh
go run ./cmd/nkdctl run generate \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-github.yaml \
  --engine github --root .
make workflows-check
```

Installation stays with NKE. Accounted manifests/templates are not delivered as standalone outputs.
This pipeline does not package an installer/deploy bundle, build NKTP or the provider image, mirror all
runtime images, or establish NKE release qualification.

## Validation

Passed on this branch: `make check notices-check workflows-check build`, uncached `go test ./... -count=1`,
`actionlint`, relaxed `yamllint`, and `git diff --check`. Dry-runs passed for the six-output deploy Definition and both upstream controller Definitions, with
zero observed artifacts (`static-unqualified`). Docker was stopped; no complete build or hosted publication
was exercised. Contract tests cover acquired-chart SBOMs and repository-scoped validation credentials.
