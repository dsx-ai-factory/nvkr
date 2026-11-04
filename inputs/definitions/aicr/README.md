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
The pinned upstream has no `generic` GB200 recipe and labels VR200 Preview.
Referenced charts, images and providers are not vendored: these are not air-gap
payloads. GitOps repository settings and cluster acceptance belong downstream.

## GB200 NVKE Kubeflow training

[`nvke-gb200.yaml`](nvke-gb200.yaml) selects the AICR coordinate `service: nvke,
accelerator: gb200, os: ubuntu, intent: training, platform: kubeflow` in three formats:
`helm`, `flux` and `argocd-helm`. Output names carry the coordinate; the Definition
carries no hardware model. The producers share the GB300 example's source, lock and
layout handling, add `--platform` to the recipe, and build `helm` and `argocd-helm`
with `--readiness-hooks` as AICR's NVKE guide directs. GPU Operator installs the NVKE driver,
so every format also passes `--dra-eviction-node-label nvidia.com/dra-kubelet-plugin=true`
to opt in to AICR's
[DRA driver upgrade eviction](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/docs/user/cli-reference.md#dra-driver-upgrade-eviction).
AICR labels this coordinate Preview.

AICR `v1.2.0` refuses criteria that no applied overlay covers. The producer also
requires the last `metadata.appliedOverlays` entry of the rendered recipe to be
`gb200-nvke-ubuntu-training-kubeflow`, so an AICR pin that resolves this coordinate
through any other overlay fails the build before `aicr bundle` runs. A recipe whose
list it cannot read fails the same way.

```sh
bin/nvkr run local \
  --definition inputs/definitions/aicr/nvke-gb200.yaml \
  --environment inputs/environments/aicr-local-arm64.yaml \
  --attempt aicr-nvke-gb200-local-1 --jobs 1
```

AICR's recipe constrains nodes to Ubuntu `24.04`, and its NVKE guide assumes NVIDIA's
`-nvidia-64k` kernel. No NVKR Run has paired these bundles with a node image. AICR `v1.2.0` has no GB200 recipe
for Ubuntu 26.04 and no `gb200-nvke-ubuntu-inference-dynamo` leaf, so this Definition has
no Dynamo inference output. NVKE does not apply these bundles; install them out of band
following AICR's pinned
[NVKE GB200 setup guide](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/docs/integrator/nvke-gb200-setup.md).
Flux has no readiness hooks, so `sriov-ib-networks` can race the IPPool CRD; the guide
gives the wait-and-reset steps. The DRA eviction opt-in adds AICR's `dra-node-labeler`
DaemonSet, which labels every node carrying `nvidia.com/gpu.present=true`, NVKE control-plane
nodes included, and limits the DRA kubelet plugin to labeled nodes. AICR documents the eviction
as best-effort: under `k8s-driver-manager` v0.12, ordering against DRA claim holders and completion
of the plugin's teardown are not guaranteed. A GPU node without the label runs no DRA kubelet
plugin, and `deploy.sh` still reports success. The pinned
[DRA driver upgrade eviction](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/docs/user/cli-reference.md#dra-driver-upgrade-eviction)
section gives the checks to run after applying.

These outputs use the source-built producer and NVKR wrapper described above. The ADR-27
pinned-tool migration changes their published digests and media type, and #288 may move
`argocd-helm` to native `helm-oci`. The leaf check needs a non-shell owner after that
migration.

[Reconciliation](../../../docs/adr/reconciliation/aicr-deployment-bundles.md#aicr-deployment-bundles)
records the local GB300 and GB200 Runs and their limits. Changes to the catalog
change build identity; that Run does not validate the inputs against hardware or
hosted services.
