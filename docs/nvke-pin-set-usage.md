# Pin and package NVKE deploy charts

NVKR can derive external chart pins from NVKE's `deploy/nico/nvke-deploy.yaml` and the
`release.yaml` of each Helm item. The `nvke-deploy-v1` adapter reads those files from the exact pinned Git tree.
It validates chart identity and acquires package digests. NVKE retains installation order, values and execution.

The [example catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-pin-set/registry.yaml) declares four acquired chart producers and explicitly
accounts for the other seven deploy items. It does not modify the maintained NVKE product Definition.

## Try the example

Copy the example to a writable directory, then pin the NVKE release tag. Flags precede the source name.

```sh
mkdir -p /tmp/nvke-chart-catalog
cp docs/examples/nvke-pin-set/registry.yaml /tmp/nvke-chart-catalog/registry.yaml
go run ./cmd/nvkr catalog pin --dir /tmp/nvke-chart-catalog \
  --tag v0.22.0-beta.1 --check nvke-controller
go run ./cmd/nvkr catalog pin --dir /tmp/nvke-chart-catalog \
  --tag v0.22.0-beta.1 nvke-controller
go run ./cmd/nvkr catalog validate --data /tmp/nvke-chart-catalog
```

`--check` reports the umbrella and all acquired member changes without writing; available changes return
`NVKR-E-DIFF`. The write uses the same resolver and serialization. The example tag selects:

| Deploy item | Chart version | Package source |
|---|---|---|
| spire | 0.30.1 | SPIFFE HTTPS Helm repository |
| spire-crds | 0.6.1 | SPIFFE HTTPS Helm repository |
| temporal | 0.74.0 | Temporal HTTPS Helm repository |
| postgresql | 18.5.15 | Bitnami OCI Helm repository |

The resulting lock records the NVKE commit/tree/epoch, each chart version, its acquisition identity, package
digest and `derivedFrom: nvke-controller@<commit>`. Helm packages are platform-independent: both declared
architectures record the same archive digest. OCI pins identify the chart layer bytes, not the manifest.

## Package locally

The [Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/examples/nvke-pin-set/definition.yaml) selects the four acquired charts as `helm-oci`
outputs. Packaging verifies each locked digest and wraps the original `.tgz` bytes in an OCI layout.
The chart layer retains its pinned digest; the enclosing OCI manifest has a separate digest.

With Docker running and the pinned Go toolchain available:

```sh
mkdir -p .nvkr/acquired-charts
cp docs/examples/nvke-pin-set/*.yaml .nvkr/acquired-charts/
# On ARM64, set utilityPlatform to linux/arm64 in environment-local.yaml.
go run ./cmd/nvkr catalog pin --dir .nvkr/acquired-charts \
  --tag v0.22.0-beta.1 nvke-controller
go run ./cmd/nvkr run local \
  --definition .nvkr/acquired-charts/definition.yaml \
  --environment .nvkr/acquired-charts/environment-local.yaml \
  --attempt acquired-helm-01 --runner-source .
```

Packages are under `.nvkr/runs/acquired-helm-01/packages/`; `lifecycle.json` records the observed
manifest digests. Choose a new attempt name for each run. Source access uses your Git credentials;
a local NVKE clone can be selected with Git's `url.<clone-path>.insteadOf` override without changing the catalog.

[Recorded local execution](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#acquired-helm-charts) packaged all four real charts
and checked their layers against the lock and downloaded bytes. This example performs no publication,
signing or installation. Its SBOMs record composition only; dependency scanning of acquired `raw` charts
is not supported. Acquired charts cannot use image binding, because that would change their pinned bytes.

## Explicit membership

Every deploy item must be listed in `members` or `accounted`. External Helm items become members. The local
controller and NKTP charts are accounted for by their exact paths. Their names must agree with
`release.yaml`; their pinned `Chart.yaml` versions must be exact, and any explicit release version must match.
Local chart paths resolve relative to the deploy root, matching NVKE's deploy format.

Re-pinning fails if an item appears, disappears, changes type, switches between local and external, or changes
its reviewed chart name/repository. The existing lock remains intact. Review the upstream change and update
the catalog mapping before trying again. NVKR does not automatically create producers or omit unknown items.

An acquired member uses `artifact: <producer>` and takes its chart identity from `prebuilt.chart`. A source
member instead uses `source: <source>` plus an explicit `chart: {name: ..., repository: ...}` identity; that
source declares `pinnedBy` and the `tagPrefix` needed to turn the extracted version into an exact upstream tag.
The catalog still owns the source build instructions. Individual members cannot be bumped independently.

## Boundaries

- Chart pins cover package bytes. Images installed by chart values need a separate image-pin contract.
- `accounted` records membership only; it does not package manifests or execute templates.
- Full catalog loading re-reads the pinned Git tree. Git access is required; offline caching is not implemented.
- Public HTTPS and anonymous OCI Helm acquisition are supported. Private OCI credentials are not wired here.
- This adapter consumes the existing NVKE deploy format. It does not generate a CycloneDX BOM, establish a
  qualified set, or add release qualification.

The generic pin-set contract is [ADR-22 section 7](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0022-catalog-pinning-and-the-lock-contract.md#7-derived-pin-sets-one-source-may-curate-other-sources-pins).
