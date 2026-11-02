# AICR deployment bundles

This focused example builds pinned AICR source and delivers its rendered directories
as ordinary `bundle-oci` artifacts through Definition → Run → Release. One GB300
`generic / gb300 / ubuntu / training` selection demonstrates Argo CD, Argo CD Helm,
Flux, Helm, Helmfile and Terraform without adding a hardware-profile model.

The [catalog](../../catalogs/aicr/registry.yaml) owns the six producers and
[source lock](../../catalogs/aicr/pins.lock.yaml). Source preparation downloads,
verifies and vendors dependencies without changing upstream module pins. Builds
compile and render offline with `aicr bundle --output oci-layout:<dir>`, check that
the layout holds one manifest with one layer, and verify the unpacked bundle. The
artifact is that layer's exact bytes, so its digest is the archive AICR itself
packaged; NVKR does not repack it. NVKR still wraps it in its own `bundle-oci`
manifest; importing AICR's manifest unchanged is follow-up work under ADR-27.

OCI output renders AICR's registry variant of each bundle. Argo CD requires a
repository URL, so the catalog passes the fixed literal `oci://registry.invalid/aicr-bundle`.
It never resolves: consumers set the Argo CD `repoURL` to wherever they host the
bundle. `argocd-helm` takes `repoURL` at install time and Flux uses AICR's default
OCIRepository name `aicr-bundle`; neither embeds a registry.

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

The pinned source is AICR `v1.2.0-rc1` (`0a4d1a2fe2fa9245fc298b924b9825aaac09d2a3`),
the first release that writes an OCI layout without pushing. Other coordinates
are ordinary catalog selections, not NVKR capabilities or hardware guarantees.
The pinned upstream lacks a generic GB200 recipe and labels VR200 Preview.
Referenced charts, images and providers are not vendored: these are not air-gap
payloads. GitOps repository settings and cluster acceptance belong downstream.

[Reconciliation](../../../docs/adr/reconciliation.md#aicr-deployment-bundles)
records the local six-output Runs and their limits. Changes to the catalog
change build identity; those Runs do not validate the inputs against hardware or
hosted services.
