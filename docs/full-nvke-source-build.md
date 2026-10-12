# Full source-built NVKE on ARM64: implementation gap map

The requested deliverable is a complete local ARM64 NVKE artifact set: CLI,
controller, infrastructure providers, fleet profiles, runtime images, Helm charts
and node OS image. Publicly available application components must be built from
pinned Git, not substituted with downloaded upstream application binaries.
Unavailable-source inputs need an explicit, reviewed binary exception; they cannot
be omitted and still called a full build. This does not mean rebuilding Ubuntu,
the kernel distribution or the compiler from Git.

**Not complete.** The current laptop example is an executable subset, not that
deliverable. Adding output names or producing a Plan does not close the missing
producer, packaging, source-access and runtime work.

## Evidence and remaining work

The catalogs introduced by
[PR #113](https://github.com/dsx-ai-factory/nvkr/pull/113) provide
[public-source producers](../definitions/nvke-os-source/README.md) and
[OVN outputs](../definitions/nvke-ovn-source/README.md). The local demo selects
the OVN fork producers for the disk's chart/image packages. The other direct-source
OS producers are not yet integrated into that selection, and the maintained M2
OS graph is unchanged. The remaining migration is not completed by merging those
candidate catalogs.

| Required area | Existing implementation | Missing for the requested build |
| --- | --- | --- |
| CLI/controller/chart | Local source producers and controller image binding | Exercise the complete final source-built selection; migrating to a different NVKE baseline also requires its matching build interface/toolchain |
| CAPNICo | This example selects a source-owned BuildKit producer at public v0.0.45, commit `30882520888ab7bb75d125cb7bb9aa89e48acd6a` | Include it in the complete Run and validate provider reconciliation; a producer declaration is not runtime acceptance |
| Provisioned provider | Existing `nvke-controller/catalog` source producer, selected locally | Include it in the complete Run and validate provider reconciliation; the NICo demo does not use this provider |
| Fleet profiles | NVKE `image.yml` identifies `nvke-site-deploy` as the production source; checked-in profiles are an example fixture | Accessible Git URL and approved revision, producer and image output. Do not publish the fixture as production profiles |
| Kubernetes/containerd/crictl/CNI/Helm/TPM | PR #113 has fourteen direct-source producers across supported versions | Integrate the candidate catalog and package the authenticated outputs for actual consumers. Its static containerd subset is not feature-equivalent to NVKE's upstream release archive; held Helm preparation remains a separate hosted-route limitation, not a blocker to the local developer-fetch route |
| Remaining OS application dependencies | PR #113's inventory maps released inputs and missing source producers | SPIRE, runc with seccomp, CoreDNS, etcd, pause, Calico/Tigera and embedded OCI images; preserve package dependencies, paths, units, hooks and import behavior |
| OVN image/chart/packages | The local demo selects PR #113's pinned DSX fork producers for its OS material closure, with matching profile and tenant chart stream | Fresh Packer/boot acceptance and maintained M2 integration. Independently published outputs are separate from embedded-disk bytes unless original-owner reuse is established. The fork's standard Ubuntu Dockerfile still uses APT; not every embedded subcomponent is Git-built |
| Twelve deployment runtime images | Current M2 image catalog acquires pinned native images | Source-build/image-package producers for the selected PostgreSQL, Temporal, SPIRE, CSI and related inputs. OS source producers alone do not replace these acquired images |
| Upstream charts/operator tools | Current laptop catalog acquires four charts and Helm/kubectl | Source chart producers and source-built operator tools; retain correct values/image bindings and chart dependencies |
| DSX IMDS | Release inventory retains payload digests but no established source revision | Accessible source with an approved pin, or a declared unavailable-source exception and accessible digest-matching payload. An older plugin-free SPIRE package is not completion |
| OS material assembly | Existing OS graph produces image-inputs and stages them for source-owned Packer | Connect direct-source binary/image packages, remove package-folder calls including indirect agent packaging, and preserve the source-owned material contract |
| Shared published/embedded image/chart bytes | [Issue #116](https://github.com/dsx-ai-factory/nvkr/issues/116) records the missing generic handoff | An authenticated original-owner cross-output reuse contract. Independent rebuilds cannot be relabeled as identical published bytes |
| Local OS runtime | Native Colima Docker/TCG can execute the declared disk graph; Docker Desktop on macOS is not supported by this launcher | Complete source smoke, direct-source material integration and tenant/workload acceptance. A disk build does not prove these gates |

Do not use an upstream release SBOM hash as the expected hash of a new source
build. New bytes receive new identities. The release inventory guides membership
and versions; it does not prove the source-to-binary relationship or qualification.
See the [staged-installer handoff](nvkr-to-nvke-demo.md) for installer alignment and E2E blockers.

## Offline deployment inventory

Source-build completeness and offline-install completeness are separate requirements. The following
inventory was inspected on 2026-10-10 against NKE commit
[`b506565e698a3f0f1a64b2072476ba928c2e799e`](https://github.com/dsx-ai-factory/nvke/tree/b506565e698a3f0f1a64b2072476ba928c2e799e).
It describes that consumer's dependencies, not an NVKR export schema or a completed installation.
The pinned source links require access to the private NVKE repository, renamed from NKE.

The [M2 image catalog](../definitions/nvke-m2/image-catalog/registry.yaml) selects twelve non-test runtime
images per architecture: PostgreSQL, eight SPIRE images and three Temporal images. Their pins reflect the
selected NKE deployment values and do not cover every installation mode. The bootstrap CLI's embedded
PostgreSQL selection differs from the NICo deployment's Bitnami chart image.

The [local site catalog](../definitions/nvke-local-dev-arm64/site-catalog/registry.yaml) bundles the NKTP
dependency charts `opentelemetry-kube-stack` 0.22.0, `prometheus-node-exporter` 4.56.1 and
`metrics-server` 3.13.1. Rendering the inspected NKE source with NICo values and those exact archives
selected these additional runtime images; the archive hashes matched the catalog pins during inspection:

| Runtime role | Rendered image reference |
| --- | --- |
| Collector | `otel/opentelemetry-collector-k8s:0.162.0` |
| Operator | `ghcr.io/open-telemetry/opentelemetry-operator/opentelemetry-operator:0.159.0` |
| Target allocator | `ghcr.io/open-telemetry/opentelemetry-operator/target-allocator:0.159.0` |
| Node exporter | `quay.io/prometheus/node-exporter:v1.12.1` |
| Kubernetes state metrics | `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.20.0` |
| Metrics server | `registry.k8s.io/metrics-server/metrics-server:v0.8.1` |
| Development telemetry backend | `grafana/otel-lgtm:0.32.0` |

The charts also use `rancher/kubectl:v1.34.1` for a pre-delete hook and `busybox:latest` for Helm test hooks.
The Operator declares a fallback collector at 0.159.0 while NKE's rendered collector resources specify
0.162.0. The downstream inventory must state whether tests, uninstall, fallback resources and an
operator-selected production telemetry backend are in scope. These tags are findings, not admitted digest pins.

NKE's [bootstrap code](https://github.com/dsx-ai-factory/nvke/blob/b506565e698a3f0f1a64b2072476ba928c2e799e/cli/internal/bootstrap/controllerdeploy/capi.go)
requests CAPI core, kubeadm bootstrap and kubeadm control-plane v1.12.3 through `clusterctl init`.
The [clusterctl default](https://github.com/kubernetes-sigs/cluster-api/blob/v1.12.3/cmd/clusterctl/client/config/cert_manager_client.go)
is cert-manager v1.19.3 unless configuration overrides it. Their release manifests identify these images:

| Prerequisite | Images |
| --- | --- |
| CAPI v1.12.3 | `registry.k8s.io/cluster-api/cluster-api-controller:v1.12.3`, `registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:v1.12.3`, `registry.k8s.io/cluster-api/kubeadm-control-plane-controller:v1.12.3` |
| cert-manager v1.19.3 | `quay.io/jetstack/cert-manager-controller:v1.19.3`, `quay.io/jetstack/cert-manager-cainjector:v1.19.3`, `quay.io/jetstack/cert-manager-webhook:v1.19.3` |

Offline bootstrap also needs `core-components.yaml`, `bootstrap-components.yaml`,
`control-plane-components.yaml` and `metadata.yaml` from the [CAPI release](https://github.com/kubernetes-sigs/cluster-api/releases/tag/v1.12.3),
plus [`cert-manager.yaml`](https://github.com/cert-manager/cert-manager/releases/download/v1.19.3/cert-manager.yaml).
The consumer must stage provider components and metadata in clusterctl's local provider repository layout.
Publishing these files in an artifact does not configure clusterctl to use them.

Complete offline installation remains unimplemented. It requires per-platform digest acquisition for the
additional images, provider manifests and local configuration, chart values that name available images, and
loading each selected image into every relevant node. The selected mode also needs Kind/bootstrap node
images, CNI and storage dependencies, installation tools and site profiles. Acceptance requires an
installation from empty caches with external network access disabled. Metadata export, chart rendering and
existing local demos do not establish that result.

## What needs external input

Two source/access inputs cannot be invented: the fleet-profile repository/revision
and the DSX IMDS source or accepted retained payload. A plugin-free SPIRE package
does not satisfy that membership; complete source smoke is not established here.
A successful TPM attestation does not satisfy that different contract.
Fresh OS builds also need
physical host and native workspace headroom; follow the resource checks in the
[developer guide](local-development.md). The developer launcher never deletes
attempts, container images or user caches automatically to obtain that space.

## Acceptance

One final immutable Run must produce every required named ARM64 output, including
the QCOW2. Its observed set must exactly match its frozen required set. Inspect the
Plan to ensure all source-required application producers are builds rather than
acquisitions; enumerate accepted unavailable-source exceptions explicitly.

Run the produced CLI, import and probe each image, inspect the final bound charts,
and boot the actual disk with the intended agent/CNI/package configuration. Record
that result separately from repository tests. A green partial Run, Plan, source
diagnostic or package tar is not a full-build result. Publication/signing and
qualified reproduction are separate acceptance claims, not implied by local success.
