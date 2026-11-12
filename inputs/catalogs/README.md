# Catalog ownership

The shared owner directories contain the maintained catalogs and explicit diagnostic variants. The experimental
public OS catalog is isolated under [`../experiments/public-os-sources`](../experiments/public-os-sources/README.md).
Shared declaration owners live in [`shared/`](shared/README.md). Registries retain explicit selections and
independent lock cohorts; identical source and component instructions are authored once in those fragments.
The 34 catalog entrypoints retain their existing Definition references and resolved producer identities.

Each leaf catalog contains `registry.yaml` and `pins.lock.yaml`. `node-images/variants/` contains
predecessor-bound producer replacements and toolchain substitutions, not additional catalogs.

The [Ubuntu 24 overlay](node-images/variants/ubuntu24.json) applies after the release node-image
catalog's native-toolchain overlay. It selects pinned installers and a compatible agent builder
for the [Ubuntu 24 diagnostic inputs](../definitions/diagnostics/ubuntu24/README.md), retaining
the release catalog's other materials, Packer pins and disk-root patches.

The [native ARM64 SBOM tool](tools/sbom-arm64/README.md) supplies the scanner for the ARM64
quickstart. Its tool pin is separate from the AMD64 scanner in the maintained release catalog;
output-scoped selections retain the same NVKE release producers.

The [native ARM64 AICR CLI](tools/aicr-arm64/README.md) pins AICR's release executable and
delivers its binary attestation beside it as a pinned companion. No maintained Definition selects it:
publication accepts one bundle entry per component, so the companion cannot be published yet.

[`nico`](nico/registry.yaml) owns the source-packaged NICo, NICo prerequisite and NICo Flow charts and the
source-built NICo Core and Flow images, pinned independently of NVKE to one infra-controller release.
Both images use the generic rootless BuildKit route with digest-pinned bases. Core chains the source's
native build, runtime and release Dockerfiles in one producer per architecture. No maintained Definition
currently selects the five NICo outputs; the [family pilot](../definitions/nvke/README.md#family-pilot)
explains their withdrawal. A cold Core build may exceed the generated hosted build-job budget; no hosted Run has built it.

[`local-path-provisioner`](local-path-provisioner/registry.yaml) packages Rancher's chart from
source, patched to accept image digests, and binds its acquired provisioner and helper images.

The native local Go smoke inputs select AMD64 and ARM64 from the existing NVKE, CAPNICo,
controller-profiles and native-tools catalogs. Architecture-specific tool acquisitions remain
explicit members of the shared native-tools catalog; no copied AMD64 site catalog is required.

## Current-to-shared mapping

Former paths below were relative to `definitions/` at upstream commit
`f260c48570eacc0e2112b6a74eb07150ff0e828e`. Current paths are relative to this directory.

| Former catalog | Current catalog | Responsibility |
|---|---|---|
| `nvke-v1/catalog` | [`nvke/module-tags`](nvke/module-tags/registry.yaml) | Module-tag CLI and historical tutorial producers; shared action/Syft/Cosign pins |
| `nvke-controller/catalog` | [`nvke/release`](nvke/release/registry.yaml) | Release controller/provider, runnable fixture profiles and deployment chart cohort |
| `nvke-local-dev-arm64/cli-catalog` | [`nvke/module-cli-arm64`](nvke/module-cli-arm64/registry.yaml) | Historical ARM64 CLI selection and local utility tools |
| `nvke-local-dev-arm64/site-catalog` | [`nvke/local-site-arm64`](nvke/local-site-arm64/registry.yaml) | Local CLI, controller and chart source/interface baseline |
| `nvke-nightly-arm64/site-catalog` | [`nvke/nightly-site-arm64`](nvke/nightly-site-arm64/registry.yaml) | Nightly site producers aligned with the selected downstream suite |
| `nvke-m2/release-catalog` | [`nvke/release-components`](nvke/release-components/registry.yaml) | Mixed CAPNICo, NKTP, reference profiles and runtime-image cohort awaiting a deliberate split |
| `nvke-local-dev-arm64/capnico-catalog` | [`capnico/local`](capnico/local/registry.yaml) | Local/nightly native CAPNICo producer |
| `nvke-m2/os-catalog` | [`node-images/release`](node-images/release/registry.yaml) | Plain/FDE disk roots and complete material closure |
| `nvke-os/catalog` | [`node-images/diagnostic`](node-images/diagnostic/registry.yaml) | Existing plain-disk diagnostic baseline |
| `nvke-ovn-source/catalog` | [`ovn-nvidia`](ovn-nvidia/registry.yaml) | NVIDIA OVN standalone image/chart and package producers |
| `nvke-local-dev-arm64/upstream-ovn-catalog` | [`ovn-upstream`](ovn-upstream/registry.yaml) | Upstream OVN diagnostic alternatives |
| `nvke-local-dev-arm64/profiles-catalog` | [`controller-profiles/nvidia-arm64`](controller-profiles/nvidia-arm64/registry.yaml) | NVIDIA local/nightly diagnostic profile payload |
| `nvke-local-dev-arm64/upstream-ovn-profiles-catalog` | [`controller-profiles/upstream-arm64`](controller-profiles/upstream-arm64/registry.yaml) | Matching upstream OVN diagnostic payload |
| `nvke-m2/image-catalog` | [`runtime-dependencies`](runtime-dependencies/registry.yaml) | Acquired native runtime images |
| `nvke-m2/catalog` | [`tools/operator-amd64`](tools/operator-amd64/registry.yaml) | Acquired AMD64 operator tools |
| `nvke-local-dev-arm64/operator-catalog` | [`tools/operator-arm64`](tools/operator-arm64/registry.yaml) | Acquired ARM64 operator tools |
| `nvke-local-dev-arm64/tools-catalog` | [`tools/installer-local-arm64`](tools/installer-local-arm64/registry.yaml) | Local source-built installer tools |
| `nvke-nightly-arm64/installer-tools-catalog` | [`tools/installer-hosted-arm64`](tools/installer-hosted-arm64/registry.yaml) | Hosted installer-tool packaging and acquired Helm |
| `nvke-local-dev-arm64/simulator-catalog` | [`tools/native`](tools/native/registry.yaml) | Shared simulator tools and runtime assets |

## Shared declarations and retained variants

The shared owners consolidate 21 identical source-declaration groups and 13 component recipe groups.
This removes 25 repeated source declarations and 14 repeated component declarations, including the seven
node-image materials, local/nightly CLI and controller, module-tag/release CLI, hosted/native acquired Helm,
the two identical native init-image names, and the common kubectl recipe across version cohorts.
The ARM64 operator Helm recipe also shares the acquired Helm declaration while retaining its different pin. Explicit aliases preserve existing selections. Pins and
artifact digests stay in the consuming locks; none are updated by this authoring migration.

The [shared ownership map](shared/README.md) is the starting point for edits. Fragments contain no locks or
nested imports. Registries select exact named declarations with a file hash; duplicate names fail rather than
override. A fragment edit requires updating each consuming hash and validating every consumer.

Variants remain local when commands, output packaging, patches, platforms, toolchains, constraints, or pins
differ. In particular source-built installer executables and raw hosted tools have different output contracts;
controller profile patches and OVN/kernel cohorts remain explicit. Sharing an upstream URL does not authorize
changing those contracts. The mixed release-components catalog still selects distinct CAPNICo/NKTP and image
variants; its duplicated CAPNICo source is shared.

See the [full migration map](../../docs/input-layout-migration.md) for Definition/Environment dispositions,
parity gaps, and acceptance checks. No handwritten catalog, lock, or patch file was deleted.

## Native diagnostic additions

The native demo uses `tools/native` for shared Darwin and Linux host tools.
The local NVIDIA KVM Definitions use the shared release node-image catalog with
ordered NVIDIA and native KVM overlays. Their other selections are:

| Catalog | Contract |
|---|---|
| [`node-images/variants/native-local-kvm.json`](node-images/variants/native-local-kvm.json) | Native demo selection over the shared release catalog: agent stamp, MAC-stable DHCP staging and native KVM settings |
| [`nvke/native-site`](nvke/native-site/registry.yaml) | Source-aligned CLI, controller, provisioned provider and chart producers; acquired deployment charts derived from the same source pin set |
| [`controller-profiles/native-nvidia`](controller-profiles/native-nvidia/registry.yaml) | Unchanged upstream config profiles and runbooks; releases are registered by the consumer |
| [`runtime-dependencies`](runtime-dependencies/registry.yaml) | Shared release and native demo runtime acquisitions from the same NVKE deployment selection |
| [`kubernetes-conformance/`](kubernetes-conformance/hydrophone/registry.yaml) | Hydrophone (with its bounded-download patch), kubectl and conformance/BusyBox image producers shared by the [conformance tool Definitions](../definitions/kubernetes-conformance-arm64/README.md) and the native conformance demo |

[`node-images/variants/native-nvidia.json`](node-images/variants/native-nvidia.json)
binds its complete producer replacements to the shared release catalog's exact
snapshot. Its three NVIDIA fork 2.4.2 OVN producers are generated from the release catalog
through `inputs/policies/ovn-producer-overlays.json`, so the release catalog owns their instructions
and pins. This overlay leaves the snapshot unchanged. The native KVM overlay binds
the resulting NVIDIA stack. The upstream, TCG and hosted nightly selections retain
their source pin cohorts.
Package metadata checks and Plan compilation establish input
alignment; a fresh package/disk build and full native runtime acceptance remain required.

## NVSentinel development catalog

[`nvsentinel/`](nvsentinel/README.md) separately owns the pinned NVSentinel image, chart, executable and
Debian producers. It uses existing format handlers and adds no upstream script patches. Its local
Definitions do not change the maintained NVKE release. The separate
[NVKE OS material experiment](../experiments/nvsentinel/README.md) retains its existing consumer source
cohort and stages the package through the ordinary material folder.
