# NKE v0.21.0 OS dependency inventory and source-build migration

Reviewed 2026-10-07 against the exact published release asset, not NKE main.
This document inventories the release and the remaining source-build migration.
The experimental [public-source catalog](../definitions/nke-os-source/README.md) adds 14 binary producers from 13 pinned Git sources, separate from the maintained M2 inputs.
The [recorded ARM64 integration test](evidence/2026-10-07-direct-os-source-builds.md) passed all 14 declared producers, not a complete OS-image migration or release qualification.

## Authoritative inputs

- [NKE v0.21.0 release](https://github.com/dsx-ai-factory/nke/releases/tag/v0.21.0).
- [SBOM.cdx.json asset](https://github.com/dsx-ai-factory/nke/releases/download/v0.21.0/SBOM.cdx.json), asset ID 601972512, 248,989 bytes.
- Downloaded asset SHA-256: `546477fbb07f5915aa425a0b633a279de7c62dd884da7aeb9d7f6270551ac626`. This is measured download integrity, not signature authentication.
- SBOM metadata release source: `33fb71b2f4224b470ebc628a9165fc4b92870406`.
- NKeR comparison: `d6cdf8dabb6bbda3af925ddb49eb16cde3eae8d9`, current main at read-back.
- [Machine-readable inventory](nke-v0.21.0-dependency-inventory.json) preserves every component record and all dependency edges from the asset, including evidence labels and source/distribution references. This file is descriptive evidence, not a replacement lock or authored lifecycle object.

## What the asset does and does not establish

The asset is CycloneDX 1.6, document version 2. It has 211 component records, 11 dependency rows and composition `aggregate: incomplete`.
159 records have the `os-source:` prefix. They comprise 83 distinct name/version pairs: 27 application records, 64 file records and 68 container records.
132 OS records contain hashes and distribution references. The remaining 27 are source-bundle groupings or Ubuntu package declarations.
19 OS records have no version: both DSX IMDS binary entries and the 17 Ubuntu package declarations.

The metadata explicitly says the asset is not a complete OS package or container-layer inventory.
Source-declared input digests identify upstream archives or native container manifests, not locally rebuilt outputs.
Qualification rows describe separately scoped RC evidence; they do not certify a new NKeR build or identify every installed guest package.
The recorded source pins are enough to identify declared inputs, but not a complete transitive compiler, package, container-base or OS closure.
Source reconstruction must derive new producer identities and evidence. It must not require locally built bytes to equal these upstream download digests or inherit v0.21.0 qualification.

## Required source-built groups

| Group | Versions present in the OS material records | Proposed direct source |
|---|---|---|
| Kubernetes binaries and control-plane images | 1.34.2, 1.35.3, 1.36.3 | kubernetes/kubernetes |
| containerd, containerd-shim-runc-v2 and ctr | 2.1.7, 2.2.3, 2.3.0 | containerd/containerd |
| runc | 1.3.5, 1.4.2 | opencontainers/runc |
| crictl | 1.34.0, 1.35.0, 1.36.0 | kubernetes-sigs/cri-tools |
| CNI plugins | 1.6.0 | containernetworking/plugins |
| Helm | 3.20.2, 4.1.4 | helm/helm |
| CoreDNS images | 1.12.1, 1.13.1, 1.14.2 | coredns/coredns |
| etcd images | 3.6.5-0, 3.6.6-0, 3.6.8-0 | etcd-io/etcd plus the owning image packaging; image suffix is not an upstream source tag |
| pause images | 3.10.1, 3.10.2 | Verify owning Kubernetes image-release source revision independently; do not infer one from the image tag |
| OVN image and chart | 1.4.0 / release-1.4 | ovn-kubernetes/ovn-kubernetes; current NKeR already builds the image and composes its chart |
| Calico images and manifests | 3.30.1 | projectcalico/calico, with each external image's actual source ownership verified |
| Tigera operator image | 1.38.1 | tigera/operator |
| SPIRE agent | 1.14.1 | spiffe/spire |
| TPM agent plugin and EK helper | 1.9.0 | spiffe/spire-tpm-plugin |
| DSX IMDS agent plugin | No version | NKE source calls the neighboring checkout dsx-identity; exact Git URL, approved commit and license remain unresolved |
| NKE agent | Source-selected 0.2.1; qualification record separately labels an RC-derived build version | NKE agent source selected by the Definition; do not conflate RC observation and rebuilt version |

These source mappings are proposed acquisition/build owners, not source identities derived from the release SBOM.
Current GitHub checks resolved all 25 source tags listed above for Kubernetes, containerd, runc, crictl, Helm, CoreDNS, etcd, CNI, SPIRE, TPM, Calico, Tigera and OVN.
They have not established transitive closures, permitted licenses, build compatibility or production pins. Pause image-release sources still require separate resolution.
Use ADR-22 catalog pinning to record commit, tree and source epoch once producer declarations and toolchains are complete. Never hand-populate a guessed source lock.

## Current NKeR comparison

Current M2 uses NKE `ff21ba8c07cafff74b68e238a8876af11e5beded` for most image preparation.
SPIRE alone uses `726f76944404e1106626b8333d3576bdf67b575b`, which omits the DSX plugin.
Those are not the release source `33fb71b2f4224b470ebc628a9165fc4b92870406`.
NKeR compiles the NKE agent and builds the upstream OVN image, but other OS-package producers currently invoke NKE package preparation or packaging.
Retaining the current older SPIRE pin is not complete v0.21.0 membership.

The native-toolchain overlay is digest-bound to the current catalog and an exact producer list.
Changing that catalog requires recomputing the overlay and regenerating generated inputs through their owning commands, not hand-patching generated workflows.

## Source-build PR boundaries

The user selected source compilation for publicly available sources, with an explicit prebuilt exception when public source is unavailable. This is not automatic fallback on a source-build failure.

1. Declare independently pinned sources/producers for binaries and images, for both AMD64 and ARM64 and each preserved Kubernetes matrix row.
2. Realize each producer's dependency closure before compilation. Do not reuse a downloaded release binary as its alleged source-built result.
3. Supply exact toolchain images with the build inputs each producer needs. The current Packer toolchain contains gcc/libc development inputs but no libseccomp development package; runc's default Makefile enables seccomp and CGO.
4. Use admitted isolated execution. Calico's upstream top-level image target invokes Docker; calling it unchanged from an ordinary offline build is not supported by the current material boundary. Model the individual builds and image assembly explicitly.
5. Package only authenticated producer outputs with pinned nfpm. Preserve package names, file layout, Depends/Conflicts/Provides, scripts, service units, configuration and image-import behavior.
6. Preserve image reference names or explicitly patch every consumer manifest/configuration to the rebuilt image names. A new digest alone does not update kubeadm/Calico image selection.
7. Keep the existing image-inputs material handoff and source-owned Packer project. The composer currently expects agent plus four package archives.
8. Remove every active invocation of `os-image/packages`, including the indirect agent Debian target's packaging path. Relocating download scripts is not source compilation.
9. Preserve the standalone NKE Make path unless separately authorized to migrate it. This NKeR change does not delete the NKE folder.
10. Validate package content/hooks and executable graphs locally, then perform fresh full image builds and cluster-time Kubernetes/CNI selection on both architectures. A plan, source-tag lookup or unit test is not runtime proof.

## Declared unavailable-public-source exceptions

For a component whose public source cannot be established, retain its existing acquired input and declare the exception explicitly beside its catalog entry. Record the component, reason, exact acquisition URL, per-architecture payload digests in the machine-managed artifact lock, and external evidence status. An unavailable source is not an excluded component: the bytes are still required inputs to the image.

The existing catalog acquired lane is `prebuilt`, not `fromSource: true` or `exclusionReason`. A catalog comment can document why this lane was selected without inventing a new executable schema field. Upstream evidence references must still follow ADR-13; absence of provenance, a source revision or a complete SBOM must remain explicit. This source-build exception is not an authenticated evidence waiver or qualification approval.

Preparation should display a warning naming each retained prebuilt exception. Acquisition still fails on inaccessible, missing or digest-mismatching bytes. A public-source compilation failure must not select a prebuilt input automatically. A successful rebuilt component must be reported as source-built; an exception remains acquired in its evidence.

### DSX IMDS agent plugin

The v0.21.0 SBOM declares these existing payloads, but does not identify their source repository, source revision or binary version:

| Architecture | Existing acquisition URL | Declared SHA-256 |
|---|---|---|
| AMD64 | `https://nke-prod-assets.s3.us-west-2.amazonaws.com/dsx-imds-node-attestor-agent/linux-amd64/dsx-imds-node-attestor-agent` | `fa134f877968550e33f683ed2b1acb05aa64028ce3eaeaf85603e05bbbef1350` |
| ARM64 | `https://nke-prod-assets.s3.us-west-2.amazonaws.com/dsx-imds-node-attestor-agent/linux-arm64/dsx-imds-node-attestor-agent` | `88226ee3a848e09c620a99aad9c99c64e6c04e7a862836eb206d6e6f9fae2bf5` |

Both URLs returned HTTP 403 to anonymous HEAD requests on 2026-10-07. These are release-declared hashes, not payloads reauthenticated by this analysis. Retaining the exception does not solve access: an accessible existing mirror or held package must supply bytes matching the declared digest before execution can succeed. Do not replace the plugin with an empty file, omit it, or label the older plugin-free M2 SPIRE package as v0.21.0-complete.

NKE's development code names a neighboring `dsx-identity` checkout. GitHub search and probes of the two guessed repository locations below did not establish the actual source location or its public availability. Classify this as unresolved public source, not proof that no repository exists. Replace the prebuilt producer with a pinned source producer when the public source becomes established and reviewed.

## Explicit unresolved inputs

- DSX IMDS: retain the explicitly declared prebuilt exception above. GitHub repository/code search returned no accessible matching repository; probes of NVIDIA/dsx-identity and dsx-ai-factory/dsx-identity returned 404. Source URL/revision and existing payload access remain unresolved independently.
- Build toolchains: compiler/vendor/container-base closures and their source/security/license review are not established for the newly proposed producers. Do not silently give existing Packer images broader unmeasured build authority.
- Ubuntu: package versions/digests and transitive APT dependencies are absent. For this package-folder migration, retain and capture the source-owned installer/APT behavior. A fully source-rebuilt Ubuntu distribution is a separate scope.
- Image release sources: pause tags and etcd image packaging suffixes cannot be converted to source commits by string substitution.

No source-build implementation, refreshed generated bundle, new full OS bake or implementation PR is claimed by this inventory.

## Complete OS-material name/version inventory

Repeated rows from both architectures and Kubernetes matrix rows are grouped only for display.
The JSON preserves the original records and exact dependency relationships.
A hash column means the SBOM declares a hash; it does not assert upstream signature verification.

| Name | Version | Architecture labels | Records | Hashes on every record |
|---|---|---|---|---|
| clevis | Not declared | Not declared | 1 | No |
| clevis-initramfs | Not declared | Not declared | 1 | No |
| clevis-luks | Not declared | Not declared | 1 | No |
| clevis-tpm2 | Not declared | Not declared | 1 | No |
| cni-plugins | 1.6.0 | amd64, arm64 | 6 | Yes |
| conntrack | Not declared | Not declared | 1 | No |
| containerd | 2.1.7 | amd64, arm64 | 2 | Yes |
| containerd | 2.2.3 | amd64, arm64 | 2 | Yes |
| containerd | 2.3.0 | amd64, arm64 | 2 | Yes |
| crictl | 1.34.0 | amd64, arm64 | 2 | Yes |
| crictl | 1.35.0 | amd64, arm64 | 2 | Yes |
| crictl | 1.36.0 | amd64, arm64 | 2 | Yes |
| cryptsetup | Not declared | Not declared | 1 | No |
| cryptsetup-initramfs | Not declared | Not declared | 1 | No |
| custom-resources.yaml | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/apiserver | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/cni | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/csi | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/goldmane | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/kube-controllers | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/node-driver-registrar | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/node | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/pod2daemon-flexvol | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/typha | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/whisker-backend | v3.30.1 | amd64, arm64 | 2 | Yes |
| docker.io/calico/whisker | v3.30.1 | amd64, arm64 | 2 | Yes |
| dsx-imds-node-attestor-agent download | Not declared | amd64, arm64 | 2 | Yes |
| ethtool | Not declared | Not declared | 1 | No |
| get_tpm_pubhash download | 1.9.0 | amd64, arm64 | 2 | Yes |
| ghcr.io/ovn-kubernetes/ovn-kubernetes/ovn-kube-ubuntu | release-1.4 | amd64, arm64 | 2 | Yes |
| helm | 3.20.2 | amd64, arm64 | 4 | Yes |
| helm | 4.1.4 | amd64, arm64 | 2 | Yes |
| initramfs-tools | Not declared | Not declared | 1 | No |
| iptables | Not declared | Not declared | 1 | No |
| kubeadm | v1.34.2 | amd64, arm64 | 2 | Yes |
| kubeadm | v1.35.3 | amd64, arm64 | 2 | Yes |
| kubeadm | v1.36.3 | amd64, arm64 | 2 | Yes |
| kubectl | v1.34.2 | amd64, arm64 | 2 | Yes |
| kubectl | v1.35.3 | amd64, arm64 | 2 | Yes |
| kubectl | v1.36.3 | amd64, arm64 | 2 | Yes |
| kubelet | v1.34.2 | amd64, arm64 | 2 | Yes |
| kubelet | v1.35.3 | amd64, arm64 | 2 | Yes |
| kubelet | v1.36.3 | amd64, arm64 | 2 | Yes |
| lvm2 | Not declared | Not declared | 1 | No |
| nke-cni-calico source bundle | v3.30.1 | amd64, arm64 | 2 | No |
| nke-cni-ovn source bundle | 1.4.0 | amd64, arm64 | 2 | No |
| nke-kubeadm-distro source bundle | v1.34.2 | amd64, arm64 | 2 | No |
| nke-kubeadm-distro source bundle | v1.35.3 | amd64, arm64 | 2 | No |
| nke-kubeadm-distro source bundle | v1.36.3 | amd64, arm64 | 2 | No |
| openssh-server | Not declared | Not declared | 1 | No |
| openvswitch-switch | Not declared | Not declared | 1 | No |
| operator-crds.yaml | v3.30.1 | amd64, arm64 | 2 | Yes |
| ovn-kubernetes-src-v1.4.0.tar.gz | 1.4.0 | amd64, arm64 | 2 | Yes |
| python3 | Not declared | Not declared | 1 | No |
| python3-apt | Not declared | Not declared | 1 | No |
| quay.io/tigera/operator | v1.38.1 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/coredns/coredns | v1.12.1 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/coredns/coredns | v1.13.1 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/coredns/coredns | v1.14.2 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/etcd | 3.6.5-0 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/etcd | 3.6.6-0 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/etcd | 3.6.8-0 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-apiserver | v1.34.2 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-apiserver | v1.35.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-apiserver | v1.36.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-controller-manager | v1.34.2 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-controller-manager | v1.35.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-controller-manager | v1.36.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-proxy | v1.34.2 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-proxy | v1.35.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-proxy | v1.36.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-scheduler | v1.34.2 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-scheduler | v1.35.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/kube-scheduler | v1.36.3 | amd64, arm64 | 2 | Yes |
| registry.k8s.io/pause | 3.10.1 | amd64, arm64 | 4 | Yes |
| registry.k8s.io/pause | 3.10.2 | amd64, arm64 | 2 | Yes |
| runc | 1.3.5 | amd64, arm64 | 4 | Yes |
| runc | 1.4.2 | amd64, arm64 | 2 | Yes |
| socat | Not declared | Not declared | 1 | No |
| spire-agent download | 1.14.1 | amd64, arm64 | 2 | Yes |
| tigera-operator.yaml | v3.30.1 | amd64, arm64 | 2 | Yes |
| tpm_attestor_agent download | 1.9.0 | amd64, arm64 | 2 | Yes |
| tpm2-tools | Not declared | Not declared | 1 | No |
