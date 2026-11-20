## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 22 Pins | Catalog v3/lock v1, per-platform tool declarations and complete sorted pin lists with legacy single-platform compatibility, per-module tags, source/artifact/action/tool pin maintenance, no-op/check/CAS semantics, derived pin sets including NVKE beta local-chart version omission and descriptive artifact mappings, same-release maintenance groups, whole-producer overlays, exact toolchain substitutions and authored embedded-version metadata authentication; digest-pinned release-tool companions delivered beside the executable in its executable output | Pin-set loading needs pinned Git access. Curated graph/SBOM edges remain deferred. Optional metadata consistency is not disk-content inspection, external acquisition or production acceptance. Companion acquisition, observation and packaging are contract-tested; the AICR v1.1.2 lock was pinned against the real release asset, but no hosted Run has delivered a companion, and a companion attestation is delivered, not verified |

Opt-in `sourceRecipe` renders command argv and artifact paths from the selected source pin. Local contracts
cover leading `v`, prereleases, prefixed tags, explicit commit-only packaging streams, unknown tokens and
stale rendered values. Two controller chart recipes and the Ubuntu24 agent use this binding; source-owned chart
versions and unrelated chart contracts remain unchanged. These tests do not execute Helm or prove a release.
Complete pin-free producer templates may bind one explicitly named catalog producer's source identity.
Binding tests preserve fetch, patch, toolchain and packaging fields; overlay tests advance the source pin
and regenerate predecessor bindings. Eight maintained Plan digests change in the authoring migration;
all 481 Build Digests and resolved producer contracts remain unchanged. No migrated producer was executed.
The agent uses the exact Tag operand, with no unused semantic packaging stream. Its commit-only rendering
has a contract test; no Ubuntu24 package build at a substituted commit was exercised.

Opt-in `sourceMetadata` bindings derive command environment values from each producer's own source pin,
including complete resolved copies. Reserved names, collisions, unknown tokens and unsupported producer
environments fail with coded errors; `ReleaseVersion` refuses commit-only pins. The NVIDIA and upstream
candidate OVN catalogs derive image tags, Debian revisions, chart checks, generated values and installer
constants without per-pin registry literals. Local two-revision Git and controlled shell fixtures cover
lock-only propagation, chart disagreement and deferred installer expressions. These are contract tests,
not OVN builds or deployment acceptance. Existing catalogs without bindings retain their prior Build bytes;
the candidate OVN command/environment migration intentionally changes affected Build Digests.
See `source_metadata_test.go`, `ovn_pin_propagation_test.go` and the
[copy inventory](../../catalog-maintenance.md#maintain-producer-copies). The native NVIDIA overlay copies
its three maintained OVN producers from the release catalog through the same generator, preserving the resolved overlay identity and consuming Plans. The release catalog and
excluded KVM variants remain separately authored; profile stream coordination remains downstream-owned.

Digest-pinned, explicitly selected catalog fragments share source and component declarations before ordinary
lock resolution. Catalog tests cover aliasing, collisions, unknown inputs, content mutation, pin ownership,
provider admission and resource bounds; factory snapshot tests cover complete capture and replay. These
contracts preserve resolved producer values and do not establish fresh build or runtime acceptance.

Multi-platform release-tool sources use sorted platform declarations and complete per-platform lock entries.
The ordinary bounded selector resolves every asset before an atomic source-pin update; utility selection and
release-executable graph resolution select one exact native record. Catalog and buildgraph tests cover missing,
extra and duplicate platforms, URL/member templating, failed acquisition and unchanged native graphs. This
is a catalog/native-selection capability; it does not implement executable image-index publication or prove
fresh upstream builds or hosted runtime acceptance.

Offline root discovery, exact-name search and the generated catalog index share a description-only
inventory path. `TestInventoryMaintainedCatalogsOffline` covers maintained catalog membership, imported
component locations and deterministic rendering without executable tools. Failure contracts cover missing
locks, invalid imports and unsafe directory entries. Pin sets are not re-derived in discovery; executable
loading and pin updates retain their upstream validation. These checks establish local description
behavior, not producer execution or release authenticity.

File operands in build/vendor argv expand through bounded context-aware catalog providers.
`TestCommandFilesIdentityGoldens` freezes equal inline/file Build and VendorSpec identities;
catalog contracts cover byte preservation, platform overrides, confinement and resource limits.
`TestCompileSnapshotCapturesCommandFiles` covers registry/fragment script capture and replay after
working-file mutation, with missing captured files rejected. These are local contracts, not hosted
execution evidence. Authored YAML command operands under `inputs/catalogs/` of at least
3,000 UTF-8 bytes now use adjacent command-template files: 25 expanded operands share six bodies. Byte comparison
reconstructs the complete original decoded catalogs, and fresh compilation of all 11 maintained
Plans preserves all 481 Build records, including their digests and resolved argv. These checks
do not execute the migrated commands or establish hosted acceptance.
Before migrating sibling-fragment scripts, toolchain repinning's edited-fragment provider must capture
those files as well; its current tracked edit set contains YAML/JSON declarations only and fails closed
for other sibling inputs. Directory loading and factory snapshot capture/replay support these files.

## NVIDIA Workload Identity agent

The [independent WLI selection](../../../inputs/definitions/nvidia-workload-identity/README.md)
declares two source-built Linux DSX IMDS NodeAttestor agent executables from its own
pinned catalog. It owns the manual-only `nvkr-run-wli-imds-agent` bundle beside the
three NVKE composite bundles; it adds no NVKE product output or derived-inventory member.
The shared maintenance policy explicitly includes its generated closure to keep
shared action declarations, generator interfaces and Plan-store ownership coherent.
It does not repin the WLI source, select a server plugin, or add signing/publication.

Catalog, graph and generation contracts cover exact source/toolchain selection,
unsigned build-only topology and invalid selections. Controlled Linux fixtures use
catalog-loaded command bytes, held source/vendor transport, receipt reuse and
boundary-specific closure-tamper refusals with untampered controls. Sealed-execution
fixtures distinguish helper and interpreter digests. These are contract tests,
not an upstream WLI build or hosted acceptance record. No hosted WLI Run, paired
SPIRE execution or hardware/node-attestation behavior is established here.
Raw executable producer descriptions now use octet-stream media types; actual Debian
outputs retain their media type. Complete uniform historical executable descriptions
remain valid without rewriting frozen Plan bytes; mixed encodings and unrelated
artifact changes fail compiler validation. Native execution and full-gate evidence
remain with their separate custodians.
