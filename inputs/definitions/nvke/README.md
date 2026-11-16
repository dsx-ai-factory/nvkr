# NVKE release candidate and family pilot

The combined [Definition](definition.yaml) is used by the committed `nvkr-run.yml`. It joins independently pinned NVKE CLI,
controller and chart builds, operator executables, native node images and third-party deployment images
into one release with **69 named outputs**. This remains a candidate; completed pipeline delivery and
full product acceptance are separate facts.

## Working Definitions for an NVKE GPU site

An NVKE GPU site needs a site controller cluster, node disks for the site and tenant node pools, and the AICR
deploy bundle for each GPU workload. These Definitions supply them. Status reflects `main`. A pending row names
the issue that adds it.

| Definition | Environment | Produces | Lane when run | Status |
|---|---|---|---|---|
| [Core](core.yaml) | [github-nvke-core.yaml](../../environments/github-nvke-core.yaml) | Site controller: `nvkectl`, operator tools, controller image and chart, `controller-profiles`, CAPNICo, CAPI and cert-manager images | `pipeline-attested` | Generated production bundle; [standalone Run history](../../../docs/adr/reconciliation/maintained-input-and-schema-boundaries.md#maintained-input-and-schema-boundaries) |
| [Services](services.yaml) | [github-nvke-services.yaml](../../environments/github-nvke-services.yaml) | SPIRE, PostgreSQL, Temporal and local-path-provisioner charts and images | `pipeline-attested` | Generated production bundle; no hosted Release recorded |
| [Telemetry](telemetry.yaml) | [github-nvke-telemetry.yaml](../../environments/github-nvke-telemetry.yaml) | NKTP chart and images | `pipeline-attested` | Generated production bundle; no hosted Release recorded |
| [Disks](disks.yaml) | [github-nvke-disks.yaml](../../environments/github-nvke-disks.yaml) | Ubuntu 26.04 `nvke-node-image-{amd64,arm64}` and `nvke-node-fde-{amd64,arm64}` | `pipeline-attested` | Generated production bundle; no hosted Release recorded |
| [Disks Ubuntu 24](disks-ubuntu24.yaml) | [github-nvke-disks-ubuntu24.yaml](../../environments/github-nvke-disks-ubuntu24.yaml) | Ubuntu 24.04 `nvke-node-image-ubuntu24-{amd64,arm64}` and `nvke-node-fde-ubuntu24-{amd64,arm64}` | `pipeline-attested` | Generated production bundle; no hosted Release recorded |
| Offline CAPI repository in Core | — | `clusterctl` provider manifests | — | Pending [#324](https://github.com/dsx-ai-factory/nvkr/issues/324) |
| [GB200 AICR bundle](../aicr/nvke-gb200.yaml) | [aicr-local-arm64.yaml](../../environments/aicr-local-arm64.yaml) (local build only) | `aicr-nvke-gb200-ubuntu-training-kubeflow-{helm,flux,argocd-helm}` | `unqualified-local-execution` | One local Run recorded ([AICR inputs](../aicr/README.md#gb200-nvke-kubeflow-training)); also included in the composite publication below |
| [GPU site composite](gpu-site.composite.yaml) | [github-nvke-gpu-site.yaml](../../environments/github-nvke-gpu-site.yaml) | Every output of the five NVKE families and the GB200 AICR bundle above, 83 in all, in one Release ([GPU site composite](#gpu-site-composite)) | `pipeline-attested` | [Historical 83-output hosted publication](../../../docs/adr/reconciliation/adr-26.md#hosted-gpu-site-publication); current-candidate execution and consumer verification pending |

The [AICR inputs](../aicr/README.md) also hold a `generic` GB300 example, which is not an NVKE recipe and is
not part of an NVKE site.

From the repository root, regenerate or check the five family bundles:

```sh
make nvke-split-generate
make nvke-split-check
```

Each family Run is a manual `workflow_dispatch` of `.github/workflows/nvkr-run-nvke-<family>.yml` on `main`,
with the credentials in [Family pilot](#family-pilot). Each Run publishes one Release under
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/<family>`. To consume them, follow
[Install an NVKE site from the Releases](../../../docs/release-quickstart.md#install-an-nvke-site-from-the-releases).

### GPU site composite

[`gpu-site.composite.yaml`](gpu-site.composite.yaml) is the whole-site entry point. It is an
[ADR-26](../../../docs/adr/0026-definition-composition.md) composite that pins the core, services, telemetry,
disks and disks-ubuntu24 Definitions and the [GB200 AICR bundle](../aicr/nvke-gb200.yaml) by their
`nvkr.definition` digests. One Run builds all 83 outputs and
publishes one Release whose membership is the exact union of the members' outputs. The family Definitions,
Runs and Releases stay as they are; the composite reads the family files and changes none of them.
The composite is the product entry point. Its [first recorded hosted publication](../../../docs/adr/reconciliation/adr-26.md#hosted-gpu-site-publication)
covers an earlier candidate, not current-head or consumer acceptance.

A maintainer can dispatch the existing production workflow on `main`; it has no inputs:

```sh
gh workflow run nvkr-run-nvke-gpu-site.yml --repo dsx-ai-factory/nvkr --ref main
```

Record the Run URL, attempt, commit, complete output count and exact publication root after it succeeds.
Use the root from that same completed Run for subsequent consumer verification.

Both disk families select the same disk, agent, installer and DOCA OVS components, and the Ubuntu 24.04
overlay replaces their producers. The composite declares those eight components as variants of the two disk
families, so the different producers are admitted for those two members only.

After editing a member Definition, re-pin the composite in the same change, then regenerate its bundles:

```sh
bin/nvkr definition pin --definition inputs/definitions/nvke/gpu-site.composite.yaml
make nvke-gpu-site-generate
make nvke-gpu-site-check
```

`make workflows-generate`, `make workflows-check` and NVKE catalog maintenance include both build bundles
and their separate qualify/smoke workflow pairs. The production pair uses
[`nvke-gpu-site-qualification.yaml`](../../policies/nvke-gpu-site-qualification.yaml); the development pair uses
[`nvke-gpu-site-development-qualification.yaml`](../../policies/nvke-gpu-site-development-qualification.yaml).

| Environment | Workflow | Publication | Signer |
|---|---|---|---|
| [github-nvke-gpu-site.yaml](../../environments/github-nvke-gpu-site.yaml) | `nvkr-run-nvke-gpu-site.yml`, partitioned | `ghcr.io/dsx-ai-factory/nvkr/m2-candidate/gpu-site` | That workflow on `main` |
| [github-development.yaml](../../environments/github-development.yaml) | The fixed `nvkr-dev.yml`, partitioned | `ghcr.io/dsx-ai-factory/nvkr/development/runs/<run>/attempts/<attempt>` | That workflow on any admitted branch |

The production Environment starts from the combined candidate's `github-release.yaml`. The composite owns the
repository's fixed development bundle, `nvkr-dev.yml`, generated from the unchanged `github-development.yaml`.
In both, disks run on the native ARC KVM pools and every other job on hosted runners. The combined candidate no
longer has a development bundle. The fixed `nvkr-dev-qualify.yml` and `nvkr-dev-smoke.yml` pair now
qualifies the complete GPU-site development Release. Development jobs use the `nvkr-dev-release` and `nvkr-dev-arc` GitHub environments, which admit
only allowlisted branches. Dispatch a clean pushed branch of this repository that those environments admit:

```sh
make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr
```

Every composite Run rebuilds every output, including eight native disk bakes; it does not reuse a family
Release. Hosted qualification binds the complete composite and all its outputs; AICR bundles receive strict
artifact validation, with no deployment or application-compatibility claim. Local qualification rehearsal,
member Release reuse and composite runtime Plans remain refused with `engine-capability`. Verifying a composite Release establishes nothing about the family Releases, and the
reverse also holds.

The GB200 AICR Definition selects no `sbomTool`, so its standalone ARM64 local build needs no ARM64 scanner.
Inside the composite it inherits the families' `syft` (ADR-26), so its bundles get SBOMs there. The
composite's finalizer fits ADR-18's 16 result-collector buckets.

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

## Family pilot

Five independent Definitions partition 68 of the combined candidate's 69 named outputs, and together add 12
family-only outputs, for 80 in all. The core family delivers the 69th, `nico-reference-profiles-fixture`, as `controller-profiles`:
one Release cannot publish the same image under two output names. The core family also
selects the ARM64 operator tools and the `controller-profiles` image, and the services family selects local-path-provisioner, which the combined Definition omits; they were added while its flat workflow had no remaining YAML
parsing headroom. The Disks Ubuntu 24 family's four outputs are also absent from the combined Definition. They reuse the
shared catalogs and pins; they do not import each other or change the combined Definition into a parent.

| Definition | Outputs | Membership | Environment |
|---|---:|---|---|
| [Core](core.yaml) | 21 | CLI/operator tools, controller with its bound chart and `controller-profiles` image, providers, CAPI and cert-manager images | [github-nvke-core.yaml](../../environments/github-nvke-core.yaml) |
| [Services](services.yaml) | 34 | SPIRE, PostgreSQL, Temporal and local-path-provisioner charts and native images | [github-nvke-services.yaml](../../environments/github-nvke-services.yaml) |
| [Telemetry](telemetry.yaml) | 17 | NKTP chart and native runtime images | [github-nvke-telemetry.yaml](../../environments/github-nvke-telemetry.yaml) |
| [Disks](disks.yaml) | 4 | Ubuntu 26.04 native AMD64/ARM64 plain and public-bootstrap FDE disks, each with its complete material closure | [github-nvke-disks.yaml](../../environments/github-nvke-disks.yaml) |
| [Disks Ubuntu 24](disks-ubuntu24.yaml) | 4 | The same four disks on Ubuntu 24.04 through the [Ubuntu 24 overlay](../../catalogs/node-images/variants/ubuntu24.json) | [github-nvke-disks-ubuntu24.yaml](../../environments/github-nvke-disks-ubuntu24.yaml) |

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

The two disk families are sibling Ubuntu series, not a default and an exception; consumers choose the
series. Each family has its own Run, signer and publication namespace, so one series can release without
rebuilding the other. The Ubuntu 24.04 disks stay out of the combined candidate: adding them would make every
combined release and development dispatch wait on four more native disk bakes. The
[GPU site composite](#gpu-site-composite) releases both series together, as separate members with declared
variants.

Each Environment selects `githubBundle: nvke-<family>`, the corresponding
`.github/workflows/nvkr-run-nvke-<family>.yml`, an explicit main-only signer identity, and publication prefix
`ghcr.io/dsx-ai-factory/nvkr/m2-candidate/<family>`. Its Release publishes by digest beneath that prefix's
`releases` repository. Source-access and disk settings are scoped to the family's selected producers.
All five families retain the maintained production registry policy: `NVKR_REGISTRY_WRITE` handles
publication, and `NVKR_REGISTRY_READ` with `NVKR_REGISTRY_READ_USER` handles publication verification.
Only the two disk families also use that reader for private native toolchain preparation. These credentials
require the same operator provisioning as the combined candidate; generation does not verify package access.

From the repository root:

```sh
make nvke-split-generate
make nvke-split-check
```

The repository-wide `make workflows-generate` and `make workflows-check` also include these bundles.
Generated Plans, actions and manifests retain their normal ownership; edit the authored inputs and
regenerate. Generation and drift checking do not build, publish or qualify any family.

This is a reversible pilot alongside the combined production, development and qualification workflows.
Named production bundles have no generated qualification pair, and the family Definitions cannot have named
development bundles; ADR-26 admits those for composites only. The existing combined qualification policies do not cover five independent family Runs.
Keep the combined path until the consumer and qualification cutover has its own accepted contract.

Consumers select each exact digest-pinned family Release and verify it under independently chosen policy.
Any record associating those references belongs to the downstream consumer; this pilot emits no automatic
association file, native parent Release or cross-Release compatibility verdict. Family output publication
still requires every output and its exact evidence within that Definition. The
[family evidence ledger](../../../docs/adr/reconciliation/maintained-input-and-schema-boundaries.md#maintained-input-and-schema-boundaries)
owns standalone Run and consumer-acceptance status. The
[partitioning strategy](../../../docs/definition-partitioning-strategy.md) records the rationale,
historical capacity measurements and remaining cutover work.

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
| Controller profiles (core family only) | `../../catalogs/nvke/release-components` | `controller-profiles`, the same `nvke-nico-reference-profiles` build as `nico-reference-profiles-fixture`, which the core family therefore omits: the two NICO test config profiles and their runbooks, not fleet configuration. NVKE's `--release` deploy path requires this artifact name for the controller item, so the Release must carry it even when a site sets `images.profiles` to override the image. The combined Definition does not select it, so a `--release` render needs the core family Release |
| Provisioned infrastructure provider | `../../catalogs/nvke/release` | Existing source-built `cluster-api-provider-provisioned`, independently delivered for both architectures; not CAPNICo or CAPI core |
| Reference-profile fixture (combined Definition only) | `../../catalogs/nvke/release-components` | `nico-reference-profiles-fixture`, one AMD64/ARM64 content image containing the two NICO test config profiles and their runbooks, not production fleet configuration |
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
| Reference-profile fixture | The selected NVKE fixture Dockerfile, `nico-vms-cp.yaml`, `nico-vms-worker.yaml` and sibling runbooks | Explicitly named `nico-reference-profiles-fixture`; the core family also delivers the same build as `controller-profiles`. The control-plane profile skips its pre-join reboot. It must not be installed as production fleet configuration. No empty/failure-test config profile, placeholder example, copied OSProfile or new Dockerfile/patch is added |
| CAPI and cert-manager images | Selected NVKE `make/tool-versions.mk` chooses CAPI `v1.13.6`; its published provider manifests name the three controllers, and its clusterctl config selects cert-manager `v1.21.1` | Acquired runtime images only. Provider manifests/metadata and installation remain a separate consumer contract |

These version selections are explicit mirrors of the pinned upstream files, not live imports. A future
upstream version bump requires a reviewed catalog/lock update; automatic source-to-catalog version drift
detection is not implemented.

### Setting NICo image values from the Release

No maintained Definition selects the NICo outputs yet ([Family pilot](#family-pilot)); these steps apply once
they return to the core family.

The `nico` chart is not image-bound. It renders `<repository>:<tag>`, so the consumer sets the values.
First authenticate the Release under your identity and issuer policy with `nvkr release verify --scope
artifact-publication` ([Lifecycle](../../../docs/lifecycle.md)); `nvkr release browse` does not verify
signatures or payloads. The maintained candidate publishes to private GHCR packages. Without
`--anonymous`, `release verify` reads signature, provenance and SBOM bundles with the same registry
keychain as payloads, so log in first. That private read is contract-tested only; no private-registry
verification has been re-run since it changed ([consumer acceptance scopes](../../../docs/adr/reconciliation/consumer-acceptance-scopes.md#consumer-acceptance-scopes)).
`release verify` writes the verified Release to stdout, so save it:

```sh
nvkr release verify \
  --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$BUILD_IDENTITY_REGEXP" --issuer "$OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST" > release.json
```

Take each digest from the verified Release's `.artifacts[]` record with the matching `name`; with the
exact-input flags, the Release is under `.release`, so read `.release.artifacts[]`. `nico` and `nico-flow` are
chart manifest digests, and `nico-core-image` and `nico-flow-image` are their two-platform OCI image
index digests, not the Release root or a per-platform child manifest. Then pass a digest-qualified tag,
because Kubernetes pulls `name:tag@sha256:…` by digest. Upstream's
[setup](https://github.com/dsx-ai-factory/infra-controller/blob/abc446b33dc773d9b8c7f41b54cccf812758b165/helm-prereqs/setup.sh)
installs Core as `nico` in namespace `nico-system`, which its prerequisite phases prepare and Flow addresses
as `nico-api.nico-system.svc.cluster.local:1079`. It installs Flow as its own `flow` release in
namespace `flow`, where `nico-prereqs` delivers its database credentials and the site agent expects
`flow.flow.svc.cluster.local:50051`; the umbrella no longer contains Flow. Install it from the separate
`nico-flow` chart and set Flow's own repository, which otherwise derives
`<global.image.repository>/nico-flow`. The chart renders its own `flow` Namespace, labeled so the site CA
syncs into it, so `--create-namespace` would leave a namespace Helm refuses to adopt. As upstream's setup
does, apply the chart's Namespace first and mark it for adoption by the `flow` release:

```bash
digest() { jq -er --arg n "$1" '[.artifacts[] | select(.name == $n)] | if length == 1 then .[0].digest else error("want one \($n)") end' release.json; }
CHART_DIGEST="$(digest nico)"
FLOW_CHART_DIGEST="$(digest nico-flow)"
CORE_DIGEST="$(digest nico-core-image)"
FLOW_DIGEST="$(digest nico-flow-image)"
helm pull "oci://$PREFIX/nico@$CHART_DIGEST" --untar
helm pull "oci://$PREFIX/nico-flow@$FLOW_CHART_DIGEST" --untar
helm install nico ./nico --namespace nico-system --create-namespace \
  --set global.image.repository="$PREFIX/nico-core-image" \
  --set global.image.tag="v2.3.0@$CORE_DIGEST"
FLOW_ARGS=(--namespace flow
  --set images.flow.repository="$PREFIX/nico-flow-image"
  --set images.flow.tag="v2.3.0@$FLOW_DIGEST")
helm template flow ./nico-flow "${FLOW_ARGS[@]}" --show-only templates/namespace.yaml | kubectl apply -f -
kubectl annotate namespace flow meta.helm.sh/release-name=flow meta.helm.sh/release-namespace=flow --overwrite
kubectl label namespace flow app.kubernetes.io/managed-by=Helm --overwrite
helm install flow ./nico-flow "${FLOW_ARGS[@]}"
```

`PREFIX` is the Environment's publication prefix. Each output is published at `$PREFIX/<output>`;
`$PREFIX/releases-<output>` holds only its internal metadata root (ADR-20). Each chart's `2.3.0` tag is
only a discovery hint; use `helm pull "oci://$PREFIX/<chart>" --version 2.3.0` only when your Helm version
rejects `oci://…@sha256:…`, and then compare the pulled chart's manifest digest with its recorded digest before installing. The Core image
serves the umbrella chart's services that read `global.image`. The default-enabled `nico-ntp` subchart uses
its own upstream image (`dockurr/chrony`), which this Release does not build; consumers must assess or
relocate it.

NKTP image-reference binding remains a nonblocking follow-up, not a completed deployment contract.
The signed chart still contains upstream tags, whereas the separately delivered native images have exact
digests. Follow-up acceptance requires binding every enabled parent/subchart image to the corresponding
published digest and testing rendered manifests for both architectures without mutable image tags.
Until then, consumers must explicitly relocate/bind those references; chart and image delivery alone
does not prove offline installation or that a deployed workload uses the signed image outputs.

The controller and CLI now select NVKE `v0.23.0`. The config-only reference image is not a complete
provisioning configuration. Fleet payload selection and delivery of the Kubernetes release resources
remain separate consumer work; updating source pins does not establish deployment acceptance.

The catalogs and their machine-managed locks own exact commits, versions, archive/member hashes and
native image digests. Do not maintain a second digest table in documentation. PostgreSQL and SPIRE Bash
retain their source-owned index digests as well as the selected native child pins; acquisition checks
actual parent-to-child membership before using the native image.

OS assembly, agent, Calico, kubeadm, SPIRE packaging and DOCA OVS acquisition all select NVKE `v0.23.0` at
`2cfd00081b468d85a4894ec17e9deac13c9d8169`. The `nvke-doca-ovs` producer fetches the source-locked DOCA OVS
`3.5.0` debs for Ubuntu `26.04` (`24.04` under the Ubuntu 24 overlay) and supplies them as the sixth
image-inputs material, which the source's composer requires. NVIDIA publishes these packages without source. SPIRE stages `1.14.1` and TPM plugin `1.9.0`,
then packages Debian revision `4` offline before image-input composition and Packer.
That source-owned packaging excludes DSX IMDS. SPIRE server/container outputs retain their
separately pinned chart-selected versions. The agent package stamps `v0.23.0`.
These updated inputs do not establish a new build, source-review acceptance or runtime result.
The pinned commit's source-owned direct-Packer contract receives `capture_apt_acquisitions=true` and exports
`non-fde/apt-acquisitions.tar` or `fde/apt-acquisitions.tar`, which each producer declares through `packer.acquisitionArchive`.
The source's composition command receives the required explicit native `--architecture` argument.
The selected material projection binds `image-inputs` as `archive` and `installer-iso` as `disk-image`
under `nvke.nvidia.com/os-image-materials/v1`; every selected material and disk target retains its own architecture.

This is a candidate Definition, not completed M2 acceptance. The production target Environment reuses
the existing registry credential, source access and workflow signing policy, but publishes only under
the separately authored `ghcr.io/dsx-ai-factory/nvkr/m2-candidate` prefix. The maintained signer identity
admits only `main`; the temporary PR #97 branch-test exception has been removed after the
cancelled attempt. Candidate execution is not organizational publication acceptance. Native disk
execution classes select the two ARC KVM pools under `githubARC: native-kvm-v1`. They describe requested
capacity, not observed allocation. `../../catalogs/node-images/variants/native-toolchains.yaml` substitutes the exact published native
Packer pin for the nine plain-image AMD64 producers plus the FDE root, retaining the separate BuildKit pin.
The OS catalog directly retains its published ARM64 Docker Packer pin.
The two inspector profiles use measured executable hashes and published native image manifests.
Their bounded budgets cover up to an 8 GiB subject and 16 GiB filesystem: 28 GiB inspection memory,
224 GiB private scratch and a ten-minute deadline. Execution classes request 36 GiB memory to
include the held subject snapshot. These are admission budgets, not measured full-M2 usage.
The inspector and Packer source locks retain their outstanding organizational approval gates;
the [candidate exception and publication evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md)
records the narrower authorization and exact registry read-back.
The retained measurements came from developer-host preparation and publication. A hosted toolchain build,
its matching measurements and provenance, and completed organizational clearance are not recorded here;
native preparation commands alone do not supply that evidence.
The candidate exception's organizational acceptance owner and expiry are not recorded. Leaving that exception
requires recorded license/security clearance and the hosted closure/measurement/provenance evidence; a
developer-host image push does not supply those approvals.
The candidate Environment selects Docker through the existing typed runtime controls;
it declares no storage-driver field or ambient runtime-variable workaround. Storage behavior
is admitted and observed by the existing runtime owner. The historical explicit-VFS probe
is diagnostic evidence, not this Environment's configuration or production approval.
Runner labels do not supply runtime-capacity or full-inspector profile authority.
The current candidate selects `diskEvidence: build-provenance-v1`, authenticating all four held disks and their
material producers without deep inspection. Profiles remain available for plain-image strict mode but are not pulled or
executed in this mode; its signed evidence reports inspection as `not-performed`. The approved October 12
M3 MVP retains this producer-provenance floor. Full disk inspection and material reconciliation are deferred
beyond that MVP ([ADR-13](../../../docs/adr/0013-supply-chain-evidence.md#6-pipeline-sbom-and-acquisition-scope)
and [NKX-18052](https://nvidia.atlassian.net/browse/NKX-18052)).
The product acceptance floor remains AMD64; this candidate retains the separately selected ARM64
extension, and all four disk outputs remain mandatory for completion of this Definition.
Omitting producer `diskEvidence` still selects the strict contract. A complete producer-provenance
M2 candidate requires publication, signatures and consumer verification for every selected output.
The [failed delivery attempt](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md)
records the earlier aggregate transport failure; the per-output route below avoids aggregate payload downloads.
Per-output storage peaks still require measured admission.
The signed disk mode is descriptive. `VerificationPolicy.minimumDiskEvidence` defaults to `build-provenance-v1`
for pipeline-attested verification. Explicit `strict` rejects producer-only evidence; qualified policy defaults
to strict and rejects the producer-only minimum. The selected minimum is
policy-digest-bound. The historical validation run below predates these consumer-policy and timeout changes
and does not validate this updated head, current main execution, strict inspection or independent full-payload
consumer acceptance.

The [customer choices guide](../../../docs/nvke-customer-choices.md) explains supplier replacement through
the existing catalog interfaces and the precise scope of its graph tests. The
[release transport evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records the
failed candidate's storage exhaustion and why correcting carrier selection alone does not complete delivery.

The [toolchain preparation guide](../../../toolchains/nvkr-packer/README.md)
lists repository-owned native preparation and measurement commands, their current
toolchain license/security gates, and the missing production inputs. The inspector's source
README gives separate AMD64 and ARM64 assembly commands. The generated workflow uses isolated
read-only image preparation with `NVKR_REGISTRY_READ` and `NVKR_REGISTRY_READ_USER`; provision access
independently of the repository token, as described in the
[production credential setup](../../../docs/generate-pipeline.md). Maintained manual dispatch and
signing admit only `main`. The October 7 branch attempt at `562a609` used a temporary exact
`codex/m2-native-arc-foundation` authored exception; it was cancelled after GitHub environment
protection rejected that branch before attestation, disk execution and finalization steps ran
([run 37661490433](https://github.com/dsx-ai-factory/nvkr/actions/runs/37661490433)).
That exception is removed from the authored inputs and regenerated bundle. No deployment-policy
entry was added, and existing unrelated entries are not changed here. Any further branch test
needs a separate non-merge candidate and actual environment admission; this maintained bundle
does not authorize it.
The equivalent validation-branch bundle completed all jobs in
[run 37533806963](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963);
that run used a different exact branch exception and is not evidence for this PR's head.

Each disk selects its root and complete material closure: agent, Kubernetes distribution, Calico, OVN,
SPIRE, installer ISO and aggregate inputs. The source composer receives an explicit native architecture.
The direct-Packer contract opts into APT capture and declares the matching plain or FDE acquisition archive under the
source-owned material projection. Capture byte correspondence alone does not authenticate repository
origin or establish a complete installed-software inventory.

The Environment explicitly selects `diskEvidence: build-provenance-v1`. M2 authenticates the held disk
and its live producer/material subjects and signs evidence describing the deferred inspection scope.
Deep content inspection and reconciliation are deferred beyond the approved M3 MVP. The existing strict
inspection profiles remain available for plain disks, but this mode neither prepares nor invokes the inspector.
A successful M2 pipeline still
requires every named output, exact-subject evidence, publication, signing and verification; deferral
cannot substitute for those gates or establish qualified reproduction.

## Native execution and publication

`nvke-node-fde-amd64` and `nvke-node-fde-arm64` select the pinned source's
`nvke-node-fde.qemu.nvke-fde` target and `fde/nvke-node.qcow2` output. They reuse each architecture's existing
material producers and native toolchain. Each explicitly declares the public bootstrap `luks_passphrase: changeit`;
no private passphrase or new secret channel is admitted. Build-provenance publication binds `luks2` through
the wrapper and signed release metadata. Strict FDE inspection/publication and qualified reproduction remain
unsupported. The [historical source audit](../diagnostics/fde-audit.md) records the prior pin's shared-key
limitations; it is not an audit of the beta update. These diagnostic images make no deployed confidentiality
claim, even after TPM enrollment. `swtpm` is not required for the bake.

The Disks Ubuntu 24 family's `nvke-node-image-ubuntu24-<arch>` and `nvke-node-fde-ubuntu24-<arch>` outputs add the
[Ubuntu 24 overlay](../../catalogs/node-images/variants/ubuntu24.json) to the same catalog stack. It replaces the
installers, the agent, DOCA OVS and the four disk producers with their 24.04 declarations; the targets,
passphrase, evidence mode and every other material producer are unchanged. The
[Ubuntu 24 inputs](../diagnostics/ubuntu24/README.md) record the agent's libc contract and installer pins.
The family's Environment keeps this candidate's native ARC route, execution classes, inspection profiles and
`build-provenance-v1` disk evidence. No Ubuntu 24.04 disk has a hosted bake, publication or guest boot recorded.

The [recorded AMD64 diagnostic Run](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035)
completed a standalone FDE bake and raw artifact retention. It did not exercise FDE publication, an ARM64
FDE bake, or this combined 69-output pipeline. Those operational acceptance checks remain outstanding.

`../../environments/github-release.yaml` admits only ADR-18's `githubARC: native-kvm-v1` route on
`arc-amd64-kvm` and `arc-arm64-kvm`, with required native KVM. Each execution class requests 8 CPUs,
36 GiB memory, 224 GiB workspace capacity and a six-hour disk-producer budget. Declarations are admission
budgets, not observed allocation or general runner-capacity guarantees. Docker uses the typed runtime
and producer-declared build-network controls; no ambient storage-driver workaround is selected.

`../../catalogs/node-images/release` directly retains the historical published ARM64 Docker Packer candidate.
`../../catalogs/node-images/variants/native-toolchains.yaml` substitutes the measured published AMD64 Packer pin for its nine M2
source-map consumers and the FDE root, preserving OVN's separate BuildKit pin. These historical images do not establish
reproduction of the current Dockerfile or bootstrap compiler. Internal Packer/inspector images were
published under the user's scoped candidate exception. Their source locks retain unresolved general
license/security approval gates; the exception does not mark those reviews complete. See
[Packer preparation and selection](../../../toolchains/nvkr-packer/README.md#native-preparation-and-catalog-selection)
and [inspector measurement](../../../toolchains/nvkr-disk-inspect/README.md#local-measurement-not-publication-admission).

Strict profiles contain actual native image/executable pins and conservative bounds: up to an 8 GiB
subject, 16 GiB filesystem, 28 GiB inspection memory, 224 GiB private scratch and a ten-minute deadline.
These retained profiles do not demonstrate a successful full M2 inspection or measured peak usage.

Publication uses the separate `ghcr.io/dsx-ai-factory/nvkr/m2-candidate` prefix and
`publication.mode: per-output-v1`. Each output is packaged, published, signed and verified independently.
Assembly authenticates same-Plan, same-workflow metadata and evidence; it does not download payload layers.
The release remains one signed entry point. Retry assembly may retain earlier attempt identities;
[reconciliation](../../../docs/adr/reconciliation/m3-mvp-scope-and-acceptance-boundary.md#m3-mvp-scope-and-acceptance-boundary) records a successful
producer completion with mixed original attempts. Successful independent retry qualification remains unproved.

Source retrieval and pinned-source revalidation read the public NVKE and OVN repositories anonymously;
native build jobs receive verified source bytes. Internal toolchain preparation uses separate ephemeral read-only package access, removed
before producer execution. Grant the dedicated reader account only its required package roles and retire
source-repository Actions access after migrating and validating the new workflow, as described in the
[production credential setup](../../../docs/generate-pipeline.md). Restrict `nvkr-arc`, `nvkr-release`
and the signing identity to the selected refs. Maintained inputs
admit `main` only and contain no validation-branch dispatch exception. Ephemeral runner isolation and
provider access restrictions remain operator obligations.

## Generate and check the maintained workflow

The committed feature-branch development workflow comes from the [GPU site composite](#gpu-site-composite)
through `../../environments/github-development.yaml`, not from this Definition. It has 83 outputs, including
the three GB200 AICR bundles and plain and public-bootstrap FDE disks for both Ubuntu series on both native routes, signing and verification.
Only execution/publication policy differs from production: separate development environments and credentials, the
`nvkr-dev.yml` signer identity, and run-attempt-scoped repositories beneath
`ghcr.io/dsx-ai-factory/nvkr/development`. `make pipeline-dev-generate` regenerates it;
`make pipeline-dev-run PIPELINE_DEV_REPO=dsx-ai-factory/nvkr` dispatches a clean pushed branch.
See [pipeline development](../../../DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch) for operator
setup, full-attempt retries and the default-branch cleanup owner. This lane does not establish production
or qualified acceptance, and no hosted development execution is recorded yet.
Development emits its Run record without a qualification handoff and does not generate the production
qualifier/smoke-worker pair.

Run from the repository root with the pinned Go toolchain and authorized access to the exact NVKE tree.
Catalog loading re-derives source-owned pin sets and may need Git access even for generation.

```sh
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition inputs/definitions/nvke/definition.yaml \
  --environment inputs/environments/github-release.yaml \
  --qualification-policy inputs/policies/nvke-qualification.yaml \
  --engine github --root .
make workflows-check
```

These commands regenerate the producer workflow, Plan and manifest plus the separately owned qualifier
and smoke worker. The worker is the generated reusable GitHub Actions workflow `nvkr-smoke.yml`, invoked
by `nvkr-qualify.yml` at the same commit; it is not a separately authored pipeline. The qualification manifest
records both workflow files. Generation and drift checks do not execute any producer or qualification job.
The generator rejects a workflow above 500,000 bytes before writing. YAML compaction preserves the
expanded job graph and credential boundaries. Changes to authored inputs must regenerate these files;
never edit the bundle by hand.
Measure the generated production and development files after regeneration. The maintained rendering
regression rejects growth above 95% of the provider limit so
further membership expansion must first preserve compaction headroom. This is a maintenance guard,
not a different provider or runtime limit.

Source/artifact maintenance belongs to reviewed catalog changes using `catalog pin`, `catalog bump`
and `catalog pin-artifacts`. SPIRE intentionally retains the temporary selection described above;
review [NVKE #1641](https://github.com/dsx-ai-factory/nvke/issues/1641) before changing that contract.
The opt-in full graph integration runs with
`NVKR_TEST_M2_ALL_OUTPUT_GRAPHS=1` and authorized pinned-source access; ordinary tests receive no
private-source credentials. Pinning and graph tests establish their own contracts, not production launch.

## Qualification readiness

The simplified [artifact check](../../../docs/lifecycle.md#authenticate-a-qualification-candidate) takes a Run URL and a
reusable policy. Upstream embedded-component manifests are not a qualification prerequisite. The maintained
catalogs omit the optional declarations, so their metadata checks are `not-applicable`. Any declaration added
later must strictly match its referenced map at the pinned source revision, including exact membership,
versions and known source pins. That comparison reads raw upstream metadata; it cannot establish effective
composition after patches, configuration or material substitutions, or verify installed contents.

Source, patch, configuration and selected material identities remain bound to production evidence. Exact
producer Run, original attempt identities, output membership, signatures, provenance and applicable
delivered-tool smoke remain required. Authenticated partial retries within the same producer or qualification
run preserve successful output/worker evidence when its exact inputs and policy remain unchanged. No source
or artifact pin changes are needed merely to satisfy the optional metadata check.

The [candidate policy](../../policies/nvke-qualification.yaml) records concrete cosign version/executable hashes, native
smoke-image manifest digests, three tool profiles and 56 exact deployment-image scope decisions. These
independently authored trust selections are reviewed with the generated workflow pair; they are not inherited
from the producing Run. The [hosted setup guide](../../../docs/lifecycle.md#publish-qualification-evidence)
explains the policy and generated worker. Concrete pins and repository validation do not establish
organizational approval or hosted acceptance.

The verifier is [cosign v3.1.3](https://github.com/sigstore/cosign/releases/tag/v3.1.3); both Linux
executables were downloaded and their hashes matched the release asset metadata and checksum file. Smoke
uses Docker Official Debian `trixie-20261005-slim`, pinned to the native
[AMD64 manifest](https://github.com/debuerreotype/docker-debian-artifacts/blob/a3751996111ce2c1c904050c84440184535e23b1/trixie/slim/oci/index.json)
and [ARM64 manifest](https://github.com/debuerreotype/docker-debian-artifacts/blob/cf1f4a45447842b45e9952e0f018ae734a7341c7/trixie/slim/oci/index.json).
Both passed the required utility, environment and container-isolation probes. Catalog-pinned Helm and
kubectl bytes passed their fixed version commands. ARM64 utility execution was native; AMD64 probes used
local emulation. No fresh nvkectl artifact or hosted qualification was exercised by these checks.

The reviewed implementation, policy and generated workflows may merge before the real acceptance runs.
After merge, complete a producer run with its original evidence and successful-attempt handoff, followed by native delivered-tool
checks, hosted positive/negative qualification and independent complete-set consumer verification. Those
results remain unproved and required before production-readiness or operational-acceptance claims. Earlier
successful builds and local contract tests do not replace them. Runtime authority checks remain unchanged.

## Recorded delivery and remaining acceptance

[Run 37533806963](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-06-team-demo-pipeline.md) completed all 34 outputs and aggregate
publication, signing, verification and finalization. It used an exact validation-branch dispatch/signing
exception absent from the maintained main-only inputs. Independent full-payload signed disk consumption,
main-only acceptance and successful hosted mixed-attempt qualification remain unproved. Earlier deep inspections
timed out; the successful candidate used the explicit build-provenance mode.

CAPNICo, the provisioned provider, NKTP and the reference config-profile image are explicit required
members, not substitutes for each other or claims of deployed acceptance. Production fleet-profile
selection and provider/resource delivery remain separate gaps described above. Embedded SPIRE and
chart-side SPIRE containers are distinct artifacts; one cannot satisfy the other's evidence row.
M3 build/evidence qualification, delivered-tool smoke and organizational toolchain reviews remain product
acceptance work. Full disk inspection is a separate deferred capability, not an M3 MVP prerequisite.

[Reconciliation](../../../docs/adr/reconciliation/README.md) owns current implementation/evidence status;
[the epic specification](../../../docs/epics-v1-spec.md) owns product acceptance. This README owns the
candidate input selections and the maintained workflow instructions.
