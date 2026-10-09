# Full source-built NKE on ARM64: implementation gap map

The requested deliverable is a complete local ARM64 NKE artifact set: CLI,
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

Point-in-time inventory checked on October 7, 2026, against main
`105bb738521c9dbd7f1326ab15e78a699200f4f1`,
[PR #113](https://github.com/dsx-ai-factory/nke-recipes/pull/113) head
`bb28f56614c71c04522c7be091f784f54b1c3794`, the local example and NKE's
source-owned workflows. PR #113 is a separate draft, not merged support on main.

| Required area | Existing implementation | Missing for the requested build |
| --- | --- | --- |
| CLI/controller/chart | Local source producers and controller image binding | Exercise the complete final source-built selection; migrating to a different NKE baseline also requires its matching build interface/toolchain |
| CAPNICo | This example adds a source-owned BuildKit producer at public v0.0.45, commit `30882520888ab7bb75d125cb7bb9aa89e48acd6a`; local source build and offline help passed | Include it in the complete Run; provider reconciliation is not exercised |
| Provisioned provider | Existing `nke-controller/catalog` source producer, now selected locally; local source build and offline help passed | Include it in the complete Run; provider reconciliation is not exercised |
| Fleet profiles | NKE `image.yml` identifies `nke-site-deploy` as the production source; checked-in profiles are an example fixture | Accessible Git URL and approved revision, producer and image output. Do not publish the fixture as production profiles |
| Kubernetes/containerd/crictl/CNI/Helm/TPM | PR #113 has fourteen direct-source producers across supported versions | Integrate the candidate catalog, resolve held Helm preparation admission, and package the authenticated outputs for actual consumers. Its static containerd subset is not feature-equivalent to NKE's upstream release archive |
| Remaining OS application dependencies | PR #113's inventory maps released inputs and missing source producers | SPIRE, runc with seccomp, CoreDNS, etcd, pause, Calico/Tigera and embedded OCI images; preserve package dependencies, paths, units, hooks and import behavior |
| OVN image/chart/packages | PR #113 builds the pinned DSX fork and declares independent OCI image and bound Helm outputs | Local selection, maintained OS material replacement, and actual Packer/boot acceptance. The image's upstream Dockerfile still uses APT; not every embedded subcomponent is Git-built |
| Twelve deployment runtime images | Current M2 image catalog acquires pinned native images | Source-build/image-package producers for the selected PostgreSQL, Temporal, SPIRE, CSI and related inputs. OS source producers alone do not replace these acquired images |
| Upstream charts/operator tools | Current laptop catalog acquires four charts and Helm/kubectl | Source chart producers and source-built operator tools; retain correct values/image bindings and chart dependencies |
| DSX IMDS | Release inventory retains payload digests but no established source revision | Accessible source with an approved pin, or a declared unavailable-source exception and accessible digest-matching payload. An older plugin-free SPIRE package is not completion |
| OS material assembly | Existing OS graph produces image-inputs and stages them for source-owned Packer | Connect direct-source binary/image packages, remove package-folder calls including indirect agent packaging, and preserve the source-owned material contract |
| Shared published/embedded image/chart bytes | [Issue #116](https://github.com/dsx-ai-factory/nke-recipes/issues/116) records the missing generic handoff | An authenticated original-owner cross-output reuse contract. Independent rebuilds cannot be relabeled as identical published bytes |
| Local OS runtime | Native Colima Docker/TCG can execute the declared disk graph; Docker Desktop on macOS is not supported by this launcher | Complete source smoke, direct-source material integration and tenant/workload acceptance. A disk build does not prove these gates |

Do not use an upstream release SBOM hash as the expected hash of a new source
build. New bytes receive new identities. The release inventory guides membership
and versions; it does not prove the source-to-binary relationship or qualification.
See the [staged-installer handoff](nkd-to-nke-demo.md) for installer alignment and E2E blockers.

## What needs external input

Two source/access inputs cannot be invented: the fleet-profile repository/revision
and the DSX IMDS source or accepted retained payload. The retained package lacks
DSX IMDS and has not passed the complete source smoke. A successful TPM
attestation does not satisfy that different contract. Fresh OS builds also need
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
