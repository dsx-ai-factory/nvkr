# NVKR and NVKE naming cutover

This repository is now **NVKR**, targeting `dsx-ai-factory/nvkr`. Its downstream
product is **NVKE**, targeting `dsx-ai-factory/nvke`. The change is a breaking
namespace migration, not a compatibility alias or a claim that the future
registries and renamed hosted configuration are already provisioned.

| Surface | New name |
|---|---|
| Framework and Go module | NVKR, `github.com/dsx-ai-factory/nvkr` |
| Public executable and Go command | `nvkr`, `cmd/nvkr` |
| SDK implementation packages | `pkg/nvkr` |
| Framework configuration, environment and schema namespace | `.nvkr`, `NVKR_*`, `nvkr.dev` |
| Structured digest families | `nvkr.*` |
| Generated workflow and ownership records | `nvkr-run.yml`, `.github/nvkr-run` |
| Downstream product and maintained inputs | NVKE, `definitions/nvke-*` |
| Local downstream demo | `nvke-demo`, `internal/nvkedemo` |

## Identities and generated files

Changing a schema namespace or digest-family prefix changes the canonical
identity. Old signed records are never edited, reauthenticated or treated as
NVKR records. Strict readers reject unsupported namespaces. Generated Plans and
workflow ownership records are regenerated from the renamed authored inputs;
catalog locks retain their pinned source, tool and material bytes.

The embedded, recorded publication and release examples in the demo retain
their original bytes and historical scope. Their old identifiers describe those
records, not the current product's naming or acceptance evidence. Historical
provider readbacks likewise retain the environment and branch names actually
observed at the time.

Demo links into those records retain the recorded output keys. Renaming a link
without changing its immutable record would make the saved output unavailable;
current authored examples and exported Plans use the new names.

## Exact upstream contracts

A Git repository rename does not rewrite a pinned source tree or rename its
operational interfaces. The maintained catalog preserves the following exact
source-owned contracts. Retaining these names is not unfinished rebranding:

- CLI source directory `cli/cmd/nkectl`, linker module
  `github.com/NVIDIA/nke/cli`, Make inputs `NKECTL_*`, and Make output
  `cli/bin/nkectl`.
- Packer selector `nke-node-non-fde.qemu.nke-non-fde`, producer manifest
  `nke.dev/os-image-materials/v1`, and output `non-fde/nke-node.qcow2`.
- The deploy pin-set's `controller/deploy/nke-sbom/nke-deploy.yaml`, its
  `nke-base-config` item, and the upstream controller chart filename.
- Installed packages, services, deployment selectors and helper files supplied
  by the pinned upstream tree. These describe producer bytes, not NVKR aliases.
- Guest acquisition envelope `nkd.dev/package-acquisitions/v1`, OVN's
  `NKE_CTR_BIN`, `/usr/lib/nke/cni-ovn/` and post-install log, and the producer's
  existing maintainer email. None is rewritten by the consuming repository.
- Pinned runtime ABI `/nkd/toolchain/packer/plugins` and
  `/usr/local/bin/nkd-disk-inspect`, including its `NKD_INSPECTION ` diagnostic
  wire prefix. Renaming NVKR does not require changing this runtime ABI or
  rebuilding an otherwise valid, pinned Packer/inspector image.
- Version-2 host-network bridge, owner/staging prefixes and shared per-user lock.
  Keeping the complete durable namespace allows existing journal recovery and
  coordination without silently reinterpreting recovery state.

Public component names, caller-owned output paths and documentation use NVKE.
Captured upstream test fixtures retain the actual upstream bytes.
Node packages, services, Kubernetes identifiers and Packer contracts keep their
existing names. A future independent interface change requires an explicit
producer change and matching consumer validation; it is not a naming-cutover
prerequisite. Repinning to NVKE's renamed CLI is likewise a separate source
update, not permission to rewrite the CLI paths in an older pinned checkout.

## Deployment cutover

Existing provider identities remain in use, rather than silently creating
unprotected replacements: `nkd-arc`, `nkd-release`, `NKE_SOURCE_TOKEN` and
`@dsx-ai-factory/nvke-write`. The team has been renamed and its NVKR repository
write access is verified; the old `nke-write` slug no longer exists.
Published immutable Packer/inspector references
remain under `ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/`;
a Git repository rename does not move GHCR packages.

Before migrating those external names:

1. Provision or rename the two repositories, preserving Git history, tags and
   access. Verify external repository permissions and consumers of the Go module.
   Verify replacement CODEOWNERS write access before changing the existing team.
2. Build, measure and publish the renamed standalone execution helper before
   admitting its image. Keep existing Packer/inspector images and their measured
   interfaces; no rebuild is required merely to change the framework name.
   A separately requested image relocation must be verified at its actual
   registry subject without pretending its embedded interfaces changed.
3. If a producer-owned format or runtime ABI changes, repin the producer/closure
   together with its consumer. This PR preserves the existing acquisition format;
   it does not require an upstream schema migration or convert captured evidence.
4. Migrate hosted environments, source-secret names, branch restrictions,
   package access, signing identities and external documentation domains to the
   authored new names. This PR does not change provider settings or secrets.
5. Regenerate the Run after those measured repins. Exercise both native
   architectures, publication and independent consumer verification against the
   actual new repository and registry identities.

Local compilation and contract tests validate the rename, not those external
cutover steps. Upstream material and published Packer/inspector SHA-256 pins are
retained. The standalone renamed execution helper was rebuilt and measured on both
architectures; its executable pins changed while its image pins remain unpublished.
No pin is invented to imply that a renamed registry subject or rebuilt image exists.

## Developer migration

Use `NVKE_LOCAL_CLONE` instead of `NKE_LOCAL_CLONE`, and `NVKR_BIN` instead of
`NKDCTL_BIN` for the local demo. Other framework `NKD_*` variables become `NVKR_*`;
product `NKE_*` variables become `NVKE_*` except documented producer/provider
contracts. Existing `.nkd` state, root `nkdctl` and helper build output remain
ignored. Preserve required Run/recovery data before deleting legacy local files.
The M1 launcher refuses an existing `nkd-m1-registry` with migration instructions;
it never removes that container automatically.
