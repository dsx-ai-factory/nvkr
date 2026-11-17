# NVKE composite release inputs

The [composite Definition](gpu-site.composite.yaml) is the maintained NVKE product entry point.
It pins five NVKE family files and the [GB200 AICR member](../aicr/nvke-gb200.yaml), producing
83 named outputs through one Plan, Run and Release. Family files remain authored selection owners;
there are no maintained standalone family workflows.

## Working Definitions for an NVKE GPU site

| Member | Outputs | Scope |
|---|---:|---|
| [Core](core.yaml) | 21 | Controller, CLI/operator tools, profiles and infrastructure images |
| [Services](services.yaml) | 34 | SPIRE, PostgreSQL, Temporal and local-path charts/images |
| [Telemetry](telemetry.yaml) | 17 | NKTP chart and images |
| [Disks](disks.yaml) | 4 | Ubuntu 26.04 plain/public-bootstrap FDE, AMD64 and ARM64 |
| [Disks Ubuntu24](disks-ubuntu24.yaml) | 4 | Ubuntu 24.04 plain/public-bootstrap FDE, AMD64 and ARM64 |
| [GB200 AICR](../aicr/nvke-gb200.yaml) | 3 | Kubeflow training Helm, Flux and Argo CD bundles |

The [site consumption guide](../../../docs/release-quickstart.md#install-an-nvke-site-from-the-releases)
owns consumer verification and downstream installation. The historical hosted publication covers its exact
candidate; current-head execution, independent verification, qualification and guest boots remain separate
acceptance work in [reconciliation](../../../docs/adr/reconciliation/adr-26.md).

### GPU site composite

| Purpose | Environment | Workflow | Publication prefix |
|---|---|---|---|
| Pinned production | [github-nvke-gpu-site.yaml](../../environments/github-nvke-gpu-site.yaml) | `nvkr-run-nvke.yml` | `ghcr.io/dsx-ai-factory/nvkr/m2-candidate/gpu-site` |
| Branch development | [github-development.yaml](../../environments/github-development.yaml) | `nvkr-dev.yml` | `ghcr.io/dsx-ai-factory/nvkr/development/runs/<run>/attempts/<attempt>` |
| Floating-source candidate | [github-nvke-nightly-gpu-site.yaml](../../environments/github-nvke-nightly-gpu-site.yaml) | `nvkr-run-nvke-nightly.yml` | `ghcr.io/dsx-ai-factory/nvkr/nightly/gpu-site` |

All three producers are manually dispatched. The separate [AMD64 nightly E2E](../../../docs/nvke-nightly.md)
is scheduled and builds/tests locally. Floating-source NVKE nightly freezes its declared upstream main refs
once per Run; it does not replace pinned production. Development retains isolated credentials and admitted
branches. Production and nightly signing identities name their exact new workflows on main; consumers must
explicitly select the corresponding trusted identity. Existing historical roots retain their original signer.

```sh
gh workflow run nvkr-run-nvke.yml --repo dsx-ai-factory/nvkr --ref main
gh workflow run nvkr-run-nvke-nightly.yml --repo dsx-ai-factory/nvkr --ref main
```

Every composite Run rebuilds every output, including eight native disk bakes. Member Release reuse and local
composite qualification rehearsal remain unsupported. Complete-composite qualification uses independent
[production](../../policies/nvke-gpu-site-qualification.yaml),
[development](../../policies/nvke-gpu-site-development-qualification.yaml) and
[nightly](../../policies/nvke-nightly-gpu-site-qualification.yaml) policies. The production/nightly qualifier
runs after its exact producer completes; its native smoke worker is reusable only by the qualifier.
AICR archive checks establish artifact validity, not deployment or application compatibility.

## Generate and check the maintained workflow

After editing a member, update its digest pin and regenerate through the owner:

```sh
bin/nvkr definition pin --definition inputs/definitions/nvke/gpu-site.composite.yaml
make nvke-generate
make nvke-check
make workflows-generate
make workflows-check
```

`make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr` dispatches a clean pushed admitted branch.
See [development setup](../../../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch).
Source resolution may need network access even during generation. No generated file is hand-edited.

## Family pilot

The core family no longer selects the five NICo outputs or the ARM64 AICR CLI; their catalogs remain. A
development Run of the [GPU site composite](#gpu-site-composite) at `ee5d61d1`, before this change, failed
both ([41789718662](https://github.com/dsx-ai-factory/nvkr/actions/runs/41789718662)): the
[NICo `nico-chart` fetch](https://github.com/dsx-ai-factory/nvkr/actions/runs/41789718662/job/126440623326)
and the [AICR publication bundle](https://github.com/dsx-ai-factory/nvkr/actions/runs/41789718662/job/126445930388).
Source admission refuses infra-controller `v2.3.0` because
`pxe/mkosi.profiles/scout-oss-aarch64/mkosi.extra/etc/systemd/system/multi-user.target.wants/forge-scout.service`
is an absolute symlink, so no NICo output can be fetched
([#375](https://github.com/dsx-ai-factory/nvkr/issues/375)). Publication requires one bundle entry per subject,
and the AICR executable output carries its `aicr-attestation.sigstore.json` companion as a second entry. The
GB200 AICR bundle builds AICR from its own pinned source and does not need this CLI.


The standalone family bundles and the old combined production/qualification bundle are retired by the
explicit 2026-11-17 [ADR-26 U10 decision](../../../docs/adr/0026-definition-composition.md).
The former [combined Definition](definition.yaml) and standalone family Environments remain comparison and
authoring fixtures; they are not enrolled releases. The decision supersedes the former hosted/consumer/output-
equivalence retirement prerequisites without claiming that missing acceptance evidence now exists.
The composite keeps the same member selections and Build identities; production/nightly rename their workflow,
Environment, Plan and qualification-policy identities. Existing registry prefixes remain unchanged.

## Selected artifacts and their owners

Rows marked "not selected" are catalogs that stay in the repository although no maintained Definition
selects their outputs.

| Output group | Owning inputs | Selection |
|---|---|---|
| CLI | `../../catalogs/nvke/module-tags` | AMD64 `nvkectl` and ARM64 `nvkectl-linux-arm64`, from their shared NVKE v0.23.0 source pin |
| Controller and charts | `../../catalogs/nvke/release` | AMD64/ARM64 aggregate `cwe`, image-bound `controller-chart`, PostgreSQL, SPIRE, SPIRE CRDs and Temporal chart packages |
| Operator tools | `../../catalogs/tools/operator-amd64`, `../../catalogs/tools/operator-arm64` | AMD64 `operator-tools` and, in the core family only, ARM64 `operator-tools-linux-arm64` Helm and kubectl executables, at the same pinned versions |
| Node images | `../../catalogs/node-images/release`, `../../catalogs/node-images/variants/native-toolchains.yaml` | Separate native AMD64 and ARM64 plain and public-bootstrap FDE QCOW2s, with explicit material producers; the Disks Ubuntu 24 family adds `../../catalogs/node-images/variants/ubuntu24.json` |
| Deployment images | `../../catalogs/runtime-dependencies` | Twelve non-test runtime images for each native architecture: PostgreSQL, eight SPIRE images and three Temporal images |
| CAPNICo | `../../catalogs/nvke/release-components` | Source-built `capnico-controller`, one OCI index requiring AMD64 and ARM64 |
| Default StorageClass (services family only) | `../../catalogs/local-path-provisioner` | Source-packaged `local-path-provisioner` chart from Rancher `v0.0.37`, one chart per native architecture with its acquired provisioner and BusyBox helper images bound by digest |
| NICo charts (not selected) | `../../catalogs/nico` | Source-packaged `nico` umbrella, `nico-prereqs` and standalone `nico-flow` charts from one infra-controller `v2.3.0` pin, each stamped `2.3.0` |
| NICo images (not selected) | `../../catalogs/nico` | Source-built `nico-core-image` and `nico-flow-image`, each one OCI index requiring AMD64 and ARM64, from the same infra-controller pin |
| Controller profiles (core family only) | `../../catalogs/nvke/release-components` | `controller-profiles`, the same `nvke-nico-reference-profiles` build as `nico-reference-profiles-fixture`, which the core family therefore omits: the two NICO test config profiles and their runbooks, not fleet configuration. NVKE's `--release` deploy path requires this artifact name for the controller item, so the Release must carry it even when a site sets `images.profiles` to override the image. The composite selects this name through the core member |
| Provisioned infrastructure provider | `../../catalogs/nvke/release` | Existing source-built `cluster-api-provider-provisioned`, independently delivered for both architectures; not CAPNICo or CAPI core |
| Telemetry | `../../catalogs/nvke/release-components` | Source-packaged `nktp` with its three vendored dependencies; eight required/optional runtime images for each native architecture, including the enabled Target Allocator and the uninstall hook's kubectl |
| Bootstrap runtime images | `../../catalogs/nvke/release-components` | CAPI core, kubeadm bootstrap/control-plane and three cert-manager images, each with separate AMD64 and ARM64 outputs |

### Additional release members and their source contracts

These are declarative catalog additions, not NVKE-specific compiler behavior. The existing generic
BuildKit, source-vendor, Helm and acquired-image routes build or acquire them. Each OCI index requires
both native subjects; acquired images retain separate native outputs because aggregation currently admits
built OCI exports only. The architecture-independent NKTP chart is packaged once. Existing outputs remain required.

| Member | Selection mirrored from pinned upstream inputs | Boundary |
|---|---|---|
| CAPNICo | NVKE's selected `deploy/nico/capnico/capnico.yaml` names controller `v0.0.45`; CAPNICo's own Dockerfile builds it | Independent source pin and source build, not the provisioned infrastructure provider |
| NICo charts | Not selected by NVKE's pin set; the site installs `nico`, `nico-prereqs` and `nico-flow` from infra-controller's `helm`, `helm-prereqs` and `helm/nico-flow` directories. `v2.3.0` is the latest non-candidate release; subcharts are vendored as local directories and the Flow chart has no dependencies, so packaging is offline | Charts plus the Core and Flow images. Since `v2.3.0` Flow is a standalone release that the umbrella excludes, so it is packaged as its own chart. The prerequisite chart's helmfile-installed operators and third-party images are not selected |
| NICo images | infra-controller `v2.3.0`'s own Dockerfiles. Flow: `rest-api/docker/production/Dockerfile.nico-flow` with `golang:1.26.4` and `nvcr.io/nvidia/distroless/go:v4.0.8`, both bound by index digest. Core: `dev/docker/Dockerfile.release-container-{x86_64,aarch64}` on bases built from the native `Dockerfile.build-container-*` and `Dockerfile.runtime-container-*`, bound by digest. Those bases' `FROM` lines carry upstream's own digest pins, and both receive the Kea package version recorded in `dev/docker/kea.version` | Flow is the only REST image the packaged charts reference, through the standalone `nico-flow` chart; the standalone REST charts are excluded from the umbrella package and not selected. A cold Core Rust build may exceed the generated hosted build-job budget; a hosted Run must prove it. Apart from Kea, both bases still install unpinned public packages and tools at build time. Core stamps version `v2.3.0`, revision `abc446b3` and the pinned commit's date from build arguments derived from the source lock; the date argument is upstream [#6896](https://github.com/dsx-ai-factory/infra-controller/pull/6896)'s `CI_COMMIT_TIMESTAMP`, rendered in UTC rather than with upstream CI's committer offset. Flow stamps the same version, but its Dockerfile declares no revision argument |
| Default StorageClass | Rancher publishes `rancher/local-path-provisioner` images but no Helm repository; the chart is packaged from `deploy/chart/local-path-provisioner` at `v0.0.37`, whose values select image `v0.0.37`. The chart's `busybox:latest` helper is replaced by an explicit `1.37.0` selection | A hashed source patch adds empty `image.digest` and `helperImage.digest` values that render `@digest` when set; unset renders are unchanged. Acquired images are native outputs, so each chart binds one architecture's digests; a mixed-architecture cluster needs a source-built image index |
| NKTP chart and dependencies | NKTP's own repository, `dsx-ai-factory/nvke-telemetry-pipeline`, at tag `nktp/v0.1.0`, the version that matches NVKE `v0.23.0`'s site values; `nktp/Chart.yaml` and `Chart.lock` select three exact dependencies; packaging keeps the chart's own version | Fetch verifies Helm `v4.2.4` and all three acquired chart archives; packaging uses the retained closure with no network |
| NKTP images | NKTP `0.1.0`'s values and authenticated dependency charts, pinned under the NKTP source: collector `0.164.0`, operator `0.161.0` (the OpenTelemetry kube-stack `0.25.1` subchart's app version), LGTM `0.38.0`, kube-state-metrics `v2.20.0`, node-exporter `v1.12.1`, optional metrics-server `v0.8.1`, uninstall kubectl `registry.k8s.io/kubectl:v1.35.10`; the NKTP values select Target Allocator `0.161.0` | Acquired images, not source builds; native subjects and parent indexes are lock-owned. Helm test-only BusyBox is excluded. Images are delivered separately; chart references are not automatically rewritten to publication repositories |
| Reference-profile fixture | The selected NVKE fixture Dockerfile, `nico-vms-cp.yaml`, `nico-vms-worker.yaml` and sibling runbooks | The composite delivers it as `controller-profiles`; `nico-reference-profiles-fixture` is only in the former combined comparison input. The control-plane profile skips its pre-join reboot. It must not be installed as production fleet configuration. No empty/failure-test config profile, placeholder example, copied OSProfile or new Dockerfile/patch is added |
| CAPI and cert-manager images | Selected NVKE `make/tool-versions.mk` chooses CAPI `v1.13.6`; its published provider manifests name the three controllers, and its clusterctl config selects cert-manager `v1.21.1` | Acquired runtime images only. Provider manifests/metadata and installation remain a separate consumer contract |

These version selections are explicit mirrors of the pinned upstream files, not live imports. A future
upstream version bump requires a reviewed catalog/lock update; automatic source-to-catalog version drift
detection is not implemented.

## Native execution and publication

Disks use ADR-18's native ARC KVM routes (`arc-amd64-kvm`, `arc-arm64-kvm`); other jobs use generic hosted
runners. `build-provenance-v1` authenticates producer/material subjects and explicitly reports deferred deep
inspection. It does not establish strict disk inspection, guest boots or deployed acceptance.
FDE images use the shared public bootstrap passphrase `changeit`; they make no confidentiality claim,
including after TPM enrollment. The [FDE diagnostic](../diagnostics/fde.md) owns diagnostic details.
Registry access, scoped writers/readers and protected environments remain operator prerequisites in
[the pipeline guide](../../../docs/generate-pipeline.md). The namespace rename does not provision them.
Toolchain [preparation](../../../toolchains/nvkr-packer/README.md) and qualification evidence retain their
separate approval and operational boundaries.

### GPU system matrix

Definitions select outputs, not hardware: targets are platforms, and downstream systems own placement
([ADR-12](../../../docs/adr/0012-criteria-model-and-aicr-activation.md#1-concrete-target-platforms),
[ADR-19](../../../docs/adr/0019-clusterdefinition-and-provisioning-adapter-boundary.md#3-downstream-systems-own-deployment)).
This table maps each GPU system to the outputs it uses. NVKE's node image excludes GPU drivers and NVIDIA fabric
([NVKE `os-image/SPEC.md`](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/os-image/SPEC.md?plain=1#L192-L195)),
so the disks are per Ubuntu series and architecture, not per GPU system. Each row's Ubuntu series, kernel and
recipe requirements decide which disk a GPU system can use. Where a row names an AICR bundle, that bundle
installs the GPU stack.

| GPU system | CPU/arch | Worker disks | AICR bundle | Upstream status |
|---|---|---|---|---|
| GB200 | Grace, arm64 | `nvke-node-image-ubuntu24-arm64` (24.04, from the Disks Ubuntu 24 family) and its FDE variant. The 26.04 `nvke-node-image-arm64` and `nvke-node-fde-arm64` are built, but the GB200 AICR recipe does not admit 26.04 | `aicr-nvke-gb200-ubuntu-training-kubeflow-*` from the [GB200 Definition](../aicr/README.md#gb200-nvke-kubeflow-training); hosted Release pending #325. Its recipe requires Ubuntu 24.04 ([leaf](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/recipes/overlays/gb200-nvke-ubuntu-training-kubeflow.yaml#L25-L34), [`os-ubuntu`](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/recipes/mixins/os-ubuntu.yaml#L23-L24)). AICR v1.2.0 has no NVKE inference leaf | NVKE's golden path is Ubuntu 24.04 on GB200, with streams on both series ([policy](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/release/policy.yaml#L23-L24), [streams](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/release/policy.yaml#L68-L75)). NVKE qualifies GB200 only to nodes Ready, with no GPU driver check ([overview](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/qualification/overview.md?plain=1#L185-L187)). NICo lists GB200 trays ([HCL](https://github.com/dsx-ai-factory/infra-controller/blob/abc446b33dc773d9b8c7f41b54cccf812758b165/docs/hcl.md?plain=1#L24-L25)) |
| GB300 | Grace, arm64 | Same as GB200 | None: AICR v1.2.0 has no GB300 NVKE leaf ([overlays](https://github.com/NVIDIA/aicr/tree/935e0746dc54da890a23c1ecca998244b3a25de7/recipes/overlays)) | NVKE roadmap ([ROADMAP](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/ROADMAP.md?plain=1#L26)). NICo lists only the Lenovo GB300 tray ([HCL](https://github.com/dsx-ai-factory/infra-controller/blob/abc446b33dc773d9b8c7f41b54cccf812758b165/docs/hcl.md?plain=1#L27)) |
| VR200 | Vera, arm64 | Same disk outputs; AICR's VR200 recipes require Ubuntu 26.04 and kernel 7.0 or later ([recipe](https://github.com/NVIDIA/aicr/blob/935e0746dc54da890a23c1ecca998244b3a25de7/recipes/overlays/vr200-rke2-ubuntu-training.yaml#L47-L65)), which NVKR does not check against the disk | None: AICR v1.2.0 VR200 leaves are RKE2-only | NVKE roadmap ([ROADMAP](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/ROADMAP.md?plain=1#L27)). Not in the NICo v2.3.0 [HCL](https://github.com/dsx-ai-factory/infra-controller/blob/abc446b33dc773d9b8c7f41b54cccf812758b165/docs/hcl.md) |
| Site controller | amd64 or arm64 | The management nodes' architecture: Ubuntu 26.04 or 24.04, plain or FDE | n/a | NVKE management nodes are all one architecture, either one ([supported platforms](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/overview/supported-platforms.md?plain=1#L79)). NICo documents x86_64 site controller hardware on Ubuntu 24.04 ([hardware](https://github.com/dsx-ai-factory/infra-controller/blob/abc446b33dc773d9b8c7f41b54cccf812758b165/docs/getting-started/prerequisites/hardware.md?plain=1#L12-L21)) |

NVKE links are at `v0.23.0`, which `main` pins, and NICo links at `v2.3.0`. The `nvke-node-fde-*` disks use
a public bootstrap passphrase, so every node built from one shares its volume key and gets no data-at-rest
protection ([Native execution and publication](#native-execution-and-publication)). NVKR does not build NICo's boot artifacts or the `qcow-imager.efi` that iPXE chainloads to write a disk; the site
takes them from its NICo deployment or builds them as
[NVKE describes](https://github.com/dsx-ai-factory/nvke/blob/2cfd00081b468d85a4894ec17e9deac13c9d8169/docs/operate/build-node-images.md?plain=1#L22).

NVKR's role ends at building, signing and publishing these outputs. GPU driver, fabric and runtime acceptance
belongs to AICR and NVKE evidence, and NVKR records none ([reconciliation](../../../docs/adr/reconciliation/maintained-input-and-schema-boundaries.md#maintained-input-and-schema-boundaries)).

## Qualification readiness

Use [artifact checks and hosted qualification](../../../docs/lifecycle.md#authenticate-a-qualification-candidate)
with the exact producing Run and independently selected policy. Complete output membership, signatures,
provenance and applicable native smoke remain required. The maintainer's bundle-retirement decision supplies
no qualification evidence. [Reconciliation](../../../docs/adr/reconciliation/adr-26.md) records the historical
publication and outstanding current-candidate, consumer and runtime acceptance.
