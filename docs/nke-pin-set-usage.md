# Pin and package NKE deploy charts

NKD can derive external chart pins from NKE's `controller/deploy/nke-sbom/nke-deploy.yaml` and the
`release.yaml` of each Helm item. The `nke-deploy-v1` adapter reads those files from the exact pinned Git tree.
It validates chart identity and acquires package digests. NKE retains installation order, values and execution.

The [example catalog](examples/nke-pin-set/registry.yaml) declares four acquired chart producers and explicitly
accounts for the other six deploy items. It does not modify the maintained NKE product Definition.

## Try the example

Copy the example to a writable directory, then pin the controller tag. Flags precede the source name.

```sh
mkdir -p /tmp/nke-chart-catalog
cp docs/examples/nke-pin-set/registry.yaml /tmp/nke-chart-catalog/registry.yaml
go run ./cmd/nkdctl catalog pin --dir /tmp/nke-chart-catalog \
  --tag controller/v0.1.1-rc.9 --check nke-controller
go run ./cmd/nkdctl catalog pin --dir /tmp/nke-chart-catalog \
  --tag controller/v0.1.1-rc.9 nke-controller
go run ./cmd/nkdctl catalog validate --data /tmp/nke-chart-catalog
```

`--check` reports the umbrella and all acquired member changes without writing; available changes return
`NKD-E-DIFF`. The write uses the same resolver and serialization. The example tag selects:

| Deploy item | Chart version | Package source |
|---|---|---|
| spire | 0.30.1 | SPIFFE HTTPS Helm repository |
| spire-crds | 0.6.1 | SPIFFE HTTPS Helm repository |
| temporal | 0.74.0 | Temporal HTTPS Helm repository |
| postgresql | 18.5.15 | Bitnami OCI Helm repository |

The resulting lock records the NKE commit/tree/epoch, each chart version, its acquisition identity, package
digest and `derivedFrom: nke-controller@<commit>`. Helm packages are platform-independent: both declared
architectures record the same archive digest. OCI pins identify the chart layer bytes, not the manifest.

## Package locally

The [Definition](examples/nke-pin-set/definition.yaml) selects the four acquired charts as `helm-oci`
outputs. Packaging verifies each locked digest and wraps the original `.tgz` bytes in an OCI layout.
The chart layer retains its pinned digest; the enclosing OCI manifest has a separate digest.

With Docker running and the pinned Go toolchain available:

```sh
mkdir -p .nkd/acquired-charts
cp docs/examples/nke-pin-set/*.yaml .nkd/acquired-charts/
# On ARM64, set utilityPlatform to linux/arm64 in environment-local.yaml.
go run ./cmd/nkdctl catalog pin --dir .nkd/acquired-charts \
  --tag controller/v0.1.1-rc.9 nke-controller
go run ./cmd/nkdctl run local \
  --definition .nkd/acquired-charts/definition.yaml \
  --environment .nkd/acquired-charts/environment-local.yaml \
  --attempt acquired-helm-01 --runner-source .
```

Packages are under `.nkd/runs/acquired-helm-01/packages/`; `lifecycle.json` records the observed
manifest digests. Choose a new attempt name for each run. Source access uses your Git credentials;
a local NKE clone can be selected with Git's `url.<clone-path>.insteadOf` override without changing the catalog.

[Recorded local execution](https://github.com/dsx-ai-factory/nke-recipes/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#acquired-helm-charts) packaged all four real charts
and checked their layers against the lock and downloaded bytes. This example performs no publication,
signing or installation. Its SBOMs record composition only; dependency scanning of acquired `raw` charts
is not supported. Acquired charts cannot use image binding, because that would change their pinned bytes.

## Explicit membership

Every deploy item must be listed in `members` or `accounted`. External Helm items become members. The local
controller chart is accounted for by its exact path, and its name/version must agree with its `release.yaml`.
Local chart paths resolve relative to the deploy root, matching NKE's deploy format.

Re-pinning fails if an item appears, disappears, changes type, switches between local and external, or changes
its reviewed chart name/repository. The existing lock remains intact. Review the upstream change and update
the catalog mapping before trying again. NKD does not automatically create producers or omit unknown items.

An acquired member uses `artifact: <producer>` and takes its chart identity from `prebuilt.chart`. A source
member instead uses `source: <source>` plus an explicit `chart: {name: ..., repository: ...}` identity; that
source declares `pinnedBy` and the `tagPrefix` needed to turn the extracted version into an exact upstream tag.
The catalog still owns the source build instructions. Individual members cannot be bumped independently.

## Boundaries

- Chart pins cover package bytes. Images installed by chart values need a separate image-pin contract.
- `accounted` records membership only; it does not package manifests or execute templates.
- Full catalog loading re-reads the pinned Git tree. Git access is required; offline caching is not implemented.
- Public HTTPS and anonymous OCI Helm acquisition are supported. Private OCI credentials are not wired here.
- This adapter consumes the existing NKE deploy format. It does not generate a CycloneDX BOM, establish a
  qualified set, or add release qualification.

The generic pin-set contract is [ADR-22 section 7](adr/0022-catalog-pinning-and-the-lock-contract.md#7-derived-pin-sets-one-source-may-curate-other-sources-pins).
