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
| Downstream product and maintained inputs | NVKE, `inputs/` (see the [input layout](input-layout-migration.md)) |
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
  existing maintainer name and email. None is rewritten by the consuming repository.
- Existing immutable Packer/inspector images retain their original runtime
  layout. New source builds use `/nvkr/toolchain/packer/plugins`,
  `/usr/local/bin/nvkr-disk-inspect`, `NVKR_INSPECTION ` and the four
  `nvkr:diskContentClass`, `nvkr:packageFormat`, `nvkr:packageArchitecture` and
  `nvkr:containedIn` report properties. Fixed launchers prefer the new paths and
  support only the exact former paths when the new path is absent. A failed
  command is never retried under another name. The bounded diagnostic reader
  admits both exact prefixes with the same closed schema. After authenticating
  report subject/runtime bindings, the content decoder projects the four exact
  former property keys to NVKR and rejects duplicate semantic properties, including mixed spellings of one key.
  Original report bytes, image pins and subject identities are not rewritten.
- New version-3 network journals use bridge `nvkevm0`, owner prefix `nvke-demo-`,
  staging prefix `.nvke-policy-` and `/tmp/nvke-local-network-<uid>.lock`.
  Version-2 recovery keeps its original bridge, owner, staging paths and journal
  version. Both versions also hold the original per-user lock so older binaries
  cannot enter concurrently. New admission rejects either pre-existing bridge.

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
   admitting its image. Existing Packer/inspector images remain supported through
   the fixed legacy-layout adapters; new source builds use the renamed layout.
   A separately requested image relocation must be verified at its actual
   registry subject without pretending its embedded interfaces changed.
3. Publish and measure any newly built Packer/inspector images before replacing
   maintained image pins. A source layout change does not invent a registry digest.
   Repin producer-owned format changes together with their consumer. The naming
   migration preserves the existing acquisition format;
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

## GitHub environment migration

The production names `nkd-arc` and `nkd-release` identify actual provider objects.
Their replacement is a separate cutover; a repository-only rename must not target
unprovisioned environments. The proposed replacements are `nvkr-arc` and
`nvkr-release`. Development environments keep their separate identities.

1. A repository administrator provisions the replacement environments under
   Settings → Environments. Inventory the existing deployment branch/tag rules,
   protection settings and required environment secrets/variables. Configure and
   read back the intended replacement restrictions before changing any caller;
   do not silently copy obsolete branch exceptions or broaden access. Review any
   authentication policy that binds the environment name, including OIDC subjects.
2. The repository implementer changes `githubARCEnvironment` and
   `githubReleaseEnvironment` in `pkg/nvkr/factory/github.go`, plus the separately
   authored `.github/workflows/nvkr-nightly.yml`. Update current operator setup
   instructions and ADR-18, retaining historical provider readbacks verbatim.
3. Run `make workflows-generate` to regenerate all maintained bundles and ownership
   records. Never search-and-replace generated YAML or Plan/manifest bytes.
   Run `make workflows-check`, `make check` and the applicable workflow checks.
4. After review and cutover, exercise native execution and publication/signing/
   independent verification through the new environments. Record the exact Run
   and provider readback in reconciliation; local tests cannot establish this.
5. The administrator retires the old environments only after active jobs and
   retained dispatch branches no longer need them. Older reviewed branches still
   contain their original environment names and need explicit handling.

## Remaining search matches

| Match | Why it remains | Change boundary |
|---|---|---|
| `nkd-arc`, `nkd-release` | Existing protected GitHub environments | Administrator provisioning followed by the repository cutover above |
| Original toolchain paths, progress prefix and report keys | Compatibility with existing digest-pinned images | New source builds use NVKR; fixed adapters support old images until measured repins |
| `nkd.dev/package-acquisitions/v1` | Exact envelope emitted by the pinned NVKE producer | Coordinated NVKE producer and NVKR consumer change |
| Version-2 bridge/owner/staging names and shared lock | Recovery and exclusion of older sessions | New version-3 sessions use NVKE; legacy coordination remains necessary |
| Embedded Plans, media types, output keys and descriptions of old Runs | Historical bytes and scoped evidence | Preserve records; create new examples for current behavior |
| Old schema/digest literals in rejection tests, including `nkd.recipe` | Prove that obsolete identities are rejected | Preserve negative coverage; never convert historical signed identities |
| `NKDCTL_BIN` and `nkd-m1-registry` diagnostics | Explain migration and refuse unsafe reuse | Keep explicit refusal; these are not accepted aliases |
| Legacy `.gitignore` entries | Keep leftover state, binaries and leases out of source changes | Preserve needed recovery data before operator cleanup |
| `rankdir`, `linkDropIn`, `linkDirectory`, `linkdown`, `linkReleaseEvidence`, checksum text | Substring matches rather than old product names | No rename |

Raw matching-line counts include generated repetition and unrelated substrings;
they are not a defect count. Current task guides using old environment names refer
to the actual protected objects. The acquisition guide describes the upstream
schema; release navigation explains rejection of historical namespaces.

## Developer migration

Use `NVKE_LOCAL_CLONE` instead of `NKE_LOCAL_CLONE`, and `NVKR_BIN` instead of
`NKDCTL_BIN` for the local demo. Other framework `NKD_*` variables become `NVKR_*`;
product `NKE_*` variables become `NVKE_*` except documented producer/provider
contracts. Existing `.nkd` state, root `nkdctl` and helper build output remain
ignored. Preserve required Run/recovery data before deleting legacy local files.
The M1 launcher refuses an existing `nkd-m1-registry` with migration instructions;
it never removes that container automatically.
