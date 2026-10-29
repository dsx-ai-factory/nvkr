# AICR deployment bundles

This focused example builds pinned AICR source and delivers its rendered directories
as ordinary `bundle-oci` artifacts through Definition → Run → Release. One GB300
`generic / gb300 / ubuntu / training` selection demonstrates Argo CD, Argo CD Helm,
Flux, Helm, Helmfile and Terraform without adding a hardware-profile model.

The [catalog](../../catalogs/aicr/registry.yaml) owns the six producers and
[source lock](../../catalogs/aicr/pins.lock.yaml). Source preparation downloads,
verifies and vendors dependencies without changing upstream module pins. Builds
compile and render offline, verify the generated bundle and normalize tar/gzip
metadata before NVKR packages the exact archive bytes in OCI.

```sh
make build
bin/nvkr run local \
  --definition inputs/definitions/aicr/definition.yaml \
  --environment inputs/environments/aicr-local-arm64.yaml \
  --attempt aicr-gb300-local-1 --jobs 1
```

This local Docker example does not publish or sign. Publication uses the normal
[Environment and pipeline workflow](../../../docs/generate-pipeline.md), with
consumer-owned destinations, credentials and policy.

`helm` contains values and scripts; `argocd-helm` contains a chart tree. Both are
transported as generic bundles here. Native `helm-oci` remains available through
its existing genuine-chart contract. Consumers authenticate the artifact before
extracting it and following upstream deployment instructions; NVKR does not apply it.
Admission and integrity preserve exact archive names. Extraction owners must handle
case-insensitive, Unicode-normalization or reserved-name collisions on their destination filesystem.

The pinned source is `fa705ec263a54eeb0ca25cb24c9f20c5d8504dca`. Other coordinates
are ordinary catalog selections, not NVKR capabilities or hardware guarantees.
The pinned upstream lacks a generic GB200 recipe and labels VR200 Preview.
Referenced charts, images and providers are not vendored: these are not air-gap
payloads. GitOps repository settings and cluster acceptance belong downstream.

[Reconciliation](../../../docs/adr/reconciliation.md#aicr-deployment-bundles)
records the earlier local six-output Run and its limits. Changes to catalog
membership change build identity; that historical Run does not validate this
trimmed catalog or the current branch against hardware or hosted services.
