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

## Inspect the result

The launcher prints the Definition and Plan before building. At completion it
prints commands to inspect the Run, view the chart's dependencies and look up
`nvkectl` in the catalog. Keep the whole attempt directory together: the Run
record, payloads and SBOMs describe the same build.

The local Run is unsigned and publishes nothing. For hosted builds, signing and
publication, continue with [pipeline generation](../../generate-pipeline.md).

The separate [module-tags example](../nvke-module-tags/README.md) retains the
older `cwe` and `nvke-helm` output names for scratch hosted generation.
