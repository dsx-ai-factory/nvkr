# Catalog ownership

The shared owner directories contain the maintained catalogs and explicit diagnostic variants. The experimental
public OS catalog is isolated under [`../experiments/public-os-sources`](../experiments/public-os-sources/README.md).
This is an ownership relocation, preserving registry, lock and patch contents. It does not yet reduce
the distinct registries to one per producer owner or reconcile their differing producer baselines.

Each leaf catalog contains `registry.yaml` and `pins.lock.yaml`. `node-images/variants/` contains
predecessor-bound producer replacements and toolchain substitutions, not additional catalogs.

The [Ubuntu 24 overlay](node-images/variants/ubuntu24.json) applies after the release node-image
catalog's native-toolchain overlay. It selects pinned installers and a compatible agent builder
for the [Ubuntu 24 diagnostic inputs](../definitions/diagnostics/ubuntu24/README.md), retaining
the release catalog's other materials, Packer pins and disk-root patches.

The [native ARM64 SBOM tool](tools/sbom-arm64/README.md) supplies the scanner for the ARM64
quickstart. Its tool pin is separate from the AMD64 scanner in the maintained release catalog;
output-scoped selections retain the same NVKE release producers.

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
| `nvke-local-dev-arm64/capnico-catalog` | [`capnico/local-arm64`](capnico/local-arm64/registry.yaml) | Local/nightly native CAPNICo producer |
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

## Remaining consolidation

The target is one owner for each producer and pin, with explicitly named variants where contracts
differ. Consolidate in this order:

1. Split the mixed release-components catalog into CAPNICo, NVKE/NKTP, controller-profiles and runtime
   dependency owners. Preserve both native platforms and each exact acquired-image subject.
2. Reconcile CLI/controller/site versions deliberately; a shared repository URL does not require all
   independently versioned modules to use one commit. Keep chart pin-set membership authenticated.
3. Bring runnable fixture, release-appropriate and diagnostic profile producers under
   controller-profiles with distinct payload names. The config-only reference fixture cannot stand in
   for an OSProfile set.
4. Consolidate the plain/FDE OS materials and their OVN sources, then regenerate every predecessor
   binding affected by the new catalog snapshots. Correct the documented NVIDIA nightly OVN drift
   as an explicit content change.
5. Share identical installer producers while keeping intentional source-built/acquired Helm and
   architecture/version choices explicit. Move shared workflow tool/action pins into the tools owner
   together with a real producer catalog; an empty producer catalog is invalid.

A catalog move preserves its resolved snapshot because its filesystem location is excluded from
catalog identity. Splitting or merging a catalog changes that snapshot and may invalidate a producer
replacement's required predecessor digest. Locks also require exact source and artifact key sets;
copying a full lock beside a trimmed registry is invalid. Use the existing pin owner and catalog
validation for those changes, then regenerate dependent bundles.

See the [full migration map](../../docs/input-layout-migration.md) for Definition/Environment
dispositions, parity gaps, and acceptance checks.

## Native diagnostic additions

The native demo uses `tools/native` for shared Darwin and Linux host tools.
The local NVIDIA KVM Definitions use the shared release node-image catalog with
ordered NVIDIA and native KVM overlays. Their other selections are:

| Catalog | Contract |
|---|---|
| [`node-images/variants/native-local-kvm.json`](node-images/variants/native-local-kvm.json) | Native demo selection over the shared release catalog: agent stamp, MAC-stable DHCP staging and native KVM settings |
| [`nvke/native-site-arm64`](nvke/native-site-arm64/registry.yaml) | Source-aligned CLI, controller, provisioned provider and chart producers; acquired deployment charts derived from the same source pin set |
| [`controller-profiles/native-nvidia-arm64`](controller-profiles/native-nvidia-arm64/registry.yaml) | Unchanged upstream config profiles and runbooks; releases are registered by the consumer |
| [`runtime-dependencies`](runtime-dependencies/registry.yaml) | Shared release and native demo runtime acquisitions from the same NVKE deployment selection |

[`node-images/variants/native-nvidia.json`](node-images/variants/native-nvidia.json)
binds its complete producer replacements to the shared release catalog's exact
snapshot. The native KVM overlay binds the resulting NVIDIA stack. The release,
upstream, TCG and hosted nightly selections retain their source pin cohorts.
Package metadata checks and Plan compilation establish input
alignment; a fresh package/disk build and full native runtime acceptance remain required.
