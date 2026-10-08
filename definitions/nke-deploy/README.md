# NKE deploy example

This eight-output Definition builds CWE and its controller chart and acquires the four external charts
selected by NKE `v0.21.0`. It remains available for local execution. The committed GitHub workflow uses
[the combined M2 Definition](../nke-m2/README.md), which includes these outputs and additional artifacts.

| Output | Selected input |
|---|---|
| `nkectl-linux-amd64` | Native AMD64 CLI binary bundle |
| `nkectl-linux-arm64` | Native ARM64 CLI binary bundle |
| `cwe` | Source-built controller image |
| `controller-chart` | Source controller chart, with its `controller` slot bound to the exact `cwe` image |
| `postgresql` | Acquired chart `18.5.15` |
| `spire` | Acquired chart `0.30.1` |
| `spire-crds` | Acquired chart `0.6.1` |
| `temporal` | Acquired chart `0.74.0` |

## Source and package membership

The [shared controller catalog](../nke-controller/catalog/registry.yaml) pins the umbrella NKE tag
`v0.21.0`. Its machine-managed lock authenticates the source commit/tree and acquired chart bytes.
The source-owned `controller/deploy/nke-sbom/nke-deploy.yaml` supplies the four external chart versions;
every other deploy member is explicitly accounted for. Re-pinning fails on unreviewed membership,
type, path or chart-identity changes. See the [pin-set adapter guide](../../docs/nke-pin-set-usage.md).

Both controller sources select the umbrella tag. This does not reconstruct the different module-tag
composition in NKE's release-asset BOM; automatic derivation from that BOM remains unimplemented.
The separate controller Definitions retain their provider-image output, which this example does not select.

The source chart's templates and repository defaults stay unchanged. Installation supplies
`image.repository` for the destination registry. External chart archives retain their pinned upstream
bytes. Their SBOMs inspect chart metadata; they do not scan or acquire referenced container images.
Installation order, deployment values and topology remain with NKE.

## Execute locally

Use Docker with support for both selected Linux platforms, the pinned Go toolchain and source access. Run from the repository root.
To use an existing NKE clone, scope a Git transport override to this shell; the canonical catalog stays unchanged:

```sh
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url./absolute/path/to/nke/.insteadOf"
export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke

# Admission and planning only; no build or attempt workspace.
go run ./cmd/nkdctl run local \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-local.yaml \
  --attempt nke-deploy-preview --dry-run

# Execute all eight outputs; use a new attempt name for every Run.
go run ./cmd/nkdctl run local \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-local.yaml \
  --attempt nke-deploy-001 --jobs 1
```

The executed record is `.nkd/runs/nke-deploy-001/lifecycle.json`. Local execution has no publication or
signing. CWE uses upstream Dockerfile network dependencies; it is a wrapped, unqualified build.

## Hosted workflow ownership

Use [the M2 README](../nke-m2/README.md#generate-and-check-the-maintained-workflow) for the maintained bundle
and its source, runner, registry and signing requirements. Generating this deploy example into the repository
root would replace that bundle and fail the M2 drift check. A dispatch of the committed `nkd-run.yml` executes M2; dispatch does not select a different Definition.
This is the checked-in candidate configuration, not a ratified canonical-product/path or namespace decision.
No parallel generated deploy workflow is maintained at that fixed path.

Historical static validation checked this eight-output Definition and both controller Definitions with the
repository gate, workflow lint and dry-runs. Those dry-runs observed zero artifacts and remained
`static-unqualified`; no complete eight-output build or hosted publication was exercised at that revision.
Current M2 candidate publication is separate evidence with its own exact inputs and limits.

This example does not package an installer/deploy bundle, build NKTP or the provider image, mirror all
runtime images, or establish release qualification.
