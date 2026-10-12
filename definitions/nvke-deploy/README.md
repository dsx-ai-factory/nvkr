# NVKE deploy example

This eight-output Definition builds CWE and its controller chart and acquires the four external charts
selected by NVKE `v0.21.0`. It remains available for local execution. The committed GitHub workflow uses
[the combined M2 Definition](../nvke-m2/README.md), which includes these outputs and additional artifacts.

| Output | Selected input |
|---|---|
| `nvkectl-linux-amd64` | Native AMD64 CLI binary bundle |
| `nvkectl-linux-arm64` | Native ARM64 CLI binary bundle |
| `cwe` | Source-built controller image |
| `controller-chart` | Source controller chart, with its `controller` slot bound to the exact `cwe` image |
| `postgresql` | Acquired chart `18.5.15` |
| `spire` | Acquired chart `0.30.1` |
| `spire-crds` | Acquired chart `0.6.1` |
| `temporal` | Acquired chart `0.74.0` |

## Source and package membership

The [shared controller catalog](../nvke-controller/catalog/registry.yaml) pins the umbrella NVKE tag
`v0.21.0`. Its machine-managed lock authenticates the source commit/tree and acquired chart bytes.
The source-owned `controller/deploy/nke-sbom/nke-deploy.yaml` supplies the four external chart versions;
every other deploy member is explicitly accounted for. Re-pinning fails on unreviewed membership,
type, path or chart-identity changes. See the [pin-set adapter guide](../../docs/nvke-pin-set-usage.md).

Both controller sources select the umbrella tag. This does not reconstruct the different module-tag
composition in NVKE's release-asset BOM; automatic derivation from that BOM remains unimplemented.
The separate controller Definitions retain their provider-image output, which this example does not select.

The source chart's templates and repository defaults stay unchanged. Installation supplies
`image.repository` for the destination registry. External chart archives retain their pinned upstream
bytes. Their SBOMs inspect chart metadata; they do not scan or acquire referenced container images.
Installation order, deployment values and topology remain with NVKE.

## Execute locally

Use Docker with support for both selected Linux platforms, the pinned Go toolchain and source access. Run from the repository root.
To use an existing NVKE clone, scope a Git transport override to this shell; the canonical catalog stays unchanged:

```sh
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url./absolute/path/to/nvke/.insteadOf"
export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nvke

# Admission and planning only; no build or attempt workspace.
go run ./cmd/nvkr run local \
  --definition definitions/nvke-deploy/definition.yaml \
  --environment definitions/nvke-deploy/environment-local.yaml \
  --attempt nvke-deploy-preview --dry-run

# Execute all eight outputs; use a new attempt name for every Run.
go run ./cmd/nvkr run local \
  --definition definitions/nvke-deploy/definition.yaml \
  --environment definitions/nvke-deploy/environment-local.yaml \
  --attempt nvke-deploy-001 --jobs 1
```

The executed record is `.nvkr/runs/nvke-deploy-001/lifecycle.json`. Local execution has no publication or
signing. CWE uses upstream Dockerfile network dependencies; it is a wrapped, unqualified build.

## Hosted workflow ownership

Use [the M2 README](../nvke-m2/README.md#generate-and-check-the-maintained-workflow) for the maintained bundle
and its source, runner, registry and signing requirements. Generating this deploy example into the repository
root would replace that bundle and fail the M2 drift check. A dispatch of the committed `nvkr-run.yml` executes M2; dispatch does not select a different Definition.
This is the checked-in candidate configuration, not a ratified canonical-product/path or namespace decision.
No parallel generated deploy workflow is maintained at that fixed path.

Historical static validation checked the earlier six-output Definition and both controller Definitions with the
repository gate, workflow lint and dry-runs. Those dry-runs observed zero artifacts and remained
`static-unqualified`; no complete six-output build or hosted publication was exercised at that revision.
The current eight-output Definition has separate native-route and generation contract coverage;
those checks do not extend the historical record or establish a complete eight-output build.
Current M2 candidate publication is separate evidence with its own exact inputs and limits.

This example does not package an installer/deploy bundle, build NKTP or the provider image, mirror all
runtime images, or establish release qualification.
