# AICR deployment bundles

This focused example builds pinned AICR source and delivers its rendered directories
as ordinary `bundle-oci` artifacts through Definition → Run → Release. One GB300
`generic / gb300 / ubuntu / training` selection demonstrates Argo CD, Argo CD Helm,
Flux, Helm, Helmfile and Terraform without adding a hardware-profile model. A second
Definition selects the [GB200 NVKE Kubeflow training](#gb200-nvke-kubeflow-training) bundles.

The [catalog](../../catalogs/aicr/registry.yaml) owns the producers and
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

The pinned source is AICR `v1.2.0` (`935e0746dc54da890a23c1ecca998244b3a25de7`),
the first stable release that writes an OCI layout without pushing. Other coordinates
are ordinary catalog selections, not NVKR capabilities or hardware guarantees.
The pinned upstream lacks a generic GB200 recipe and labels VR200 Preview.
Referenced charts, images and providers are not vendored: these are not air-gap
payloads. GitOps repository settings and cluster acceptance belong downstream.

## GB200 NVKE Kubeflow training

[`nvke-gb200.yaml`](nvke-gb200.yaml) selects the AICR coordinate `service: nvke,
accelerator: gb200, os: ubuntu, intent: training, platform: kubeflow` in three formats:
`helm`, `flux` and `argocd-helm`. Output names carry the coordinate; the Definition
carries no hardware model. The producers share the GB300 example's source, lock and
layout handling, and add `--platform` to the recipe.

AICR resolves a coordinate without a curated leaf by merging broader overlays. The
producer therefore requires the last `metadata.appliedOverlays` entry of the rendered
recipe to be `gb200-nvke-ubuntu-training-kubeflow`. Any other result, or a recipe whose
list it cannot read, fails the build before `aicr bundle` runs.

```sh
bin/nvkr run local \
  --definition inputs/definitions/aicr/nvke-gb200.yaml \
  --environment inputs/environments/aicr-local-arm64.yaml \
  --attempt aicr-nvke-gb200-local-1 --jobs 1
```

AICR's GB200 NVKE recipes require Ubuntu `24.04` nodes, so these bundles pair with
the Ubuntu 24.04 ARM64 node image. AICR `v1.2.0` has no recipe for a 26.04 GB200 node.
It also has no `gb200-nvke-ubuntu-inference-dynamo` leaf, so this Definition has no
Dynamo inference output. NVKE does not apply these bundles: install them out of band
with Helm, Flux or Argo CD.

This selection is the interim form above. Moving to ADR-27 pinned-tool producers and
AICR's own manifest changes the published digests, as it does for GB300.

[Reconciliation](../../../docs/adr/reconciliation.md#aicr-deployment-bundles)
records the local six-output Run and its limits. Changes to the catalog
change build identity; that Run does not validate the inputs against hardware or
hosted services.
