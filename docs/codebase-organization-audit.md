# Codebase organization audit

Date: 2026-09-18. Baseline: `a525fe2912cec12c99f22736e0d39937cd1bc6e7`.

The audit identified nine meaningful improvements, led by consolidating output observation, moving durable
status acceptance and catalog maintenance out of the CLI, and organizing compiler/runner code by operation.
All nine are implemented. The existing domain package map remains the foundation: private
helpers and coherent files keep each invariant with its owner without adding broad package splits.

The findings below describe the baseline audit. The implementation record later in this report maps all nine
recommendations to their current code and contracts. Priorities describe maintenance value, not defect severity.
The refactor preserves wire schemas and generated bundle identities; its deliberate binary-admission change
and typed maintenance/diagnostic API additions are called out explicitly. It adds no operational or qualification claim.

## Scope and method

The review inventoried Go source under `cmd`, `client`, `internal` and `pkg`, then traced representative paths
through command/SDK adapters, preparation, compilation, execution, observation, publication and authentication.
It examined implementation and existing contract tests, using [ADR-17](adr/0017-cli-sdk-foundation-and-package-topology.md),
the relevant execution/publication/pinning contracts, [reconciliation](adr/reconciliation.md), and the recent
[cleanup disposition](repository-cleanup.md) to distinguish necessary separation from accidental fragmentation.

The inventory contains 199 production Go files with 50,259 physical lines and 230 test files with 40,895 lines.
Counts include comments and blank lines, across platform variants; they are navigation signals rather than
complexity scores. Generated workflow payloads and fixture data are outside these counts. Deep reading focused
on the areas below; this was not a line-by-line correctness review of every file or an external execution audit.

| Area | Production files | Production lines | Navigation signal |
|---|---:|---:|---|
| `factory` | 36 | 8,131 | Compilation, admission, execution and provider generation share one owner |
| `pipeline` | 27 | 7,161 | `local.go` combines scheduling, process cleanup, source preparation and builds |
| `cmd/nkdctl` | 16 | 4,833 | `commands.go` contains 1,934 lines across unrelated operations |
| `delivery` | 14 | 3,786 | Binary format logic is spread between general layout and inspection files |
| `catalog` | 10 | 3,613 | `load.go` contains 1,395 lines of loading, resolution and validation |
| `sign` | 10 | 3,481 | Separate authentication lanes already reuse held-executable support |

Source line references and inventories describe the baseline; links resolve to the current files where retained.
Moved tests are linked at their new locations. Proposed filenames were illustrative; naming should follow the
existing operation vocabulary. Effort is relative: small means primarily mechanical moves; medium means
changing internal call structure or ownership; higher care means preserving durable or concurrent behavior.

## Prioritized findings

| ID | Priority | Opportunity | Effort / care |
|---|---|---|---|
| 1 | High | One factory-owned implementation of typed payload observation | Medium / authority-sensitive |
| 2 | High | Move the durable status acceptance operation into `status` | Medium / higher care |
| 3 | High | Put catalog pin maintenance behind catalog operations | Medium / I/O and commit semantics |
| 4 | Medium | Separate catalog decoding, resolution and producer validation | Small initially / preserve error order |
| 5 | Medium | Separate factory packaging adapters from release assembly | Medium / exact membership and cleanup |
| 6 | Medium | Expose local runner phases and isolate scheduler state | Medium / concurrency and lifetimes |
| 7 | Medium | Share process-group termination mechanics | Medium / platform and lifetime behavior |
| 8 | Medium | Organize compiler and Plan validation by the contract they enforce | Medium / deterministic admission |
| 9 | Lower | Gather binary-bundle implementation by artifact format | Small initially / byte stability |

### 1. Consolidate typed payload observation in factory

**Evidence.** [Lifecycle execution](../pkg/nkd/lifecycle/execute.go), `observeOutput` at line 319, contains its own
producer-result comparison, binary-bundle inspection at line 373, and APT producer lookup/rehashing at line 389.
[Factory output observation](../pkg/nkd/factory/outputs_observed.go), `observeProducerPayload` at line 27 and
`ObserveLifecyclePayload` at line 64, already implements the exact producer-instruction/result/payload join.
Lifecycle calls that observer for APT, Helm and image outputs, while handling binary bundles separately and
performing additional APT checks first. A reader must compare both paths to understand an observed output.

**Proposed organization.** Route all four formats through the factory observer, passing the complete Plan,
exact named output and resolved workspace paths. Extend its descriptive observation only with information
the caller needs: the binary bundle's semantic digest is currently retained by lifecycle but absent from
`LifecyclePayloadObservation`. Keep frozen requirements, observed-to-required binding, immutable Run state
and outer publication membership in lifecycle; delivery continues to own representation checks.

**Value and limits.** A producer-binding change gains one implementation and one primary contract suite.
Consolidate the code, while continuing to observe bytes independently wherever the lifecycle requires it;
do not cache an earlier observation as authority. Preserve output-name separation when Build Digests match.
The binary paths are not equivalent today: factory also rechecks raw producer bytes and the exact IR/result
binding, while lifecycle compares packaged entries with frozen producer records. Characterize raw-payload
availability, malformed producer metadata and coded errors before adopting the stronger factory admission.

**Validation.** Use [factory observation contracts](../pkg/nkd/factory/outputs_observed_test.go) and
[lifecycle execution contracts](../pkg/nkd/lifecycle/execute_test.go): exact producer evidence, binary/publication
membership, APT substitution, relocation and sibling-output rejection. The binary observation fixture at line 19
currently permits a nil Plan; change it to use a compiled Plan when removing the alternate observation path.
Check both descriptor and semantic digests and retain hosted-finalization coverage.

A related small cleanup fits this work: `recordHostedFailure` and `recordFailedAttempt` in lifecycle execution
(lines 133 and 503) repeat the cancellation-detached five-second immutable commit protocol. One private failure
commit helper can take the destination and diagnostic context. Preserve destination paths, coded errors and
the contract that cancellation cannot erase a failed attempt. Put preparation, local execution, hosted
finalization, observation and attempt persistence in separately named files as this area is touched.

### 2. Move durable status acceptance out of the command package

**Evidence.** [The status command](../cmd/nkdctl/status_qualified.go) owns `statusHighWaterFile` at line 43,
receipt-to-state projection at line 245, protected reads at line 334, byte-CAS persistence at line 420, and
kernel-released locking/directory fsync at line 495. Its `consumeQualifiedStatus` operation also owns the crucial
ordering at lines 179–190: commit the authenticated checkpoint before interpreting withdrawal.
[The status module](../pkg/nkd/status/checkpoint.go) owns checkpoint verification, leaving the durable half
of the operation in the command adapter.

**Proposed organization.** Give `status` a diagnostic acceptance operation that reads the protected minimum,
verifies the checkpoint, commits receipt-derived state against the exact snapshot, then exposes the decision.
Keep the concrete store private in status state/platform files. The command retains flag mapping, transport
configuration and presentation. Callers should not have to reconstruct the persistence sequence or supply
arbitrary bytes as an accepted checkpoint.

**Value and limits.** Authentication and durable acceptance become reviewable in one module and testable
without a CLI context. This is an ownership change requiring an ADR-17/reconciliation update if implemented:
the `HighWaterMark` comment currently delegates persisted-state authentication to the root adapter.
Preserve the caller-supplied diagnostic epoch and existing capability limits. This extraction grants no
trusted-time authority, release clearance or new qualified SDK workflow.

**Validation.** Move the substantive [CLI status contracts](../cmd/nkdctl/qualified_lifecycle_test.go) beside the
owner while retaining command behavior tests: withdrawal persistence (line 334), stale-writer CAS (495), symlink
rejection (510), cancellation/FIFO rejection (536), crash lock release (607) and concurrent ownership (649).
Retain [checkpoint rollback/equivocation contracts](../pkg/nkd/status/checkpoint_test.go). Commit ordering,
no-follow reads, permissions, fsync and post-commit cancellation semantics are part of the interface.

### 3. Consolidate catalog maintenance behind catalog-owned operations

**Evidence.** [Command registration and handlers](../cmd/nkdctl/commands.go) mix resolution, builds, diagnostics,
SBOM, attestation, profiles, AICR and catalog maintenance in 1,934 lines. `pinSources` at line 1607 and
`pinArtifacts` at line 1690 select targets, resolve pins, mutate the lock, detect changes and sequence commits.
Binary digest resolution, image-manifest parsing and bounded HTTP also live here, starting at lines 1804,
1818 and 1859. Meanwhile [catalog pin logic](../pkg/nkd/catalog/pin.go) owns loading the lock snapshot,
resolving source pins and performing the CAS write.

**Proposed organization.** First split command families into operation files without changing behavior.
Then move source/artifact maintenance into catalog operations accepting typed requests and returning structured
change summaries. Catalog should own request admission, resolution and the existing lock transaction;
the CLI selects its timeout, maps arguments and prints results. Place artifact download/runtime adapters
beside the pinning implementation. Preserve distinct source-toolchain and per-architecture artifact resolution
contracts even where they both inspect container images.

**Value and limits.** A maintainer can understand a pin update in the same module as its lock invariants.
Embedding the operation no longer requires duplicating command loops or constructing a CLI context. Keep
no-op lock bytes, atomic updates, bounded I/O and cancellation. Source pin/bump `--check` must keep its no-write
behavior and `CodeDiff`; do not accidentally broaden flags on artifact pinning during extraction. Preserve
artifact selection validation before resolver calls, and characterize multi-source failure ordering before
changing admission across the whole request.

**Validation.** The current behavior is covered in [pinning commands](../cmd/nkdctl/pinning_commands_test.go),
[HTTP pinning](../pkg/nkd/catalog/pin_artifact_http_test.go), [image selection](../pkg/nkd/catalog/pin_artifact_image_test.go)
and [catalog lock contracts](../pkg/nkd/catalog/lock_contract_test.go). Move domain assertions into catalog,
retain CLI output/exit assertions, and verify check/no-op/write paths against the same prior lock snapshot.

### 4. Separate catalog loading, resolution and producer admission

**Evidence.** [Catalog loading](../pkg/nkd/catalog/load.go) contains strict decoding and I/O orchestration
(`LoadContext`, line 102), raw declarations, source/lock resolution (`resolveComponent`, 306), patch loading,
template rendering (471/517), catalog admission (587), VM requirements (664), Packer validation (708), material
graph validation (819), a 239-line component validator (909), and transport/reference helpers. These are
different reasons to open a file, all behind a filename suggesting only loading.

**Proposed organization.** Keep the `catalog` package and existing public entry points. Use a small set of
concern files: `load.go` for the bounded loading sequence; `resolve.go` for pin/component/patch resolution and
template expansion; `validate.go` for shared declaration and resolved-catalog admission; `validate_vm.go`
and `validate_materials.go` for their substantial specific contracts. Only split the long component validator
into named phases after mechanical moves are reviewed. Similarly, `pin.go` can later separate lock persistence
from upstream tag resolution when finding 3 is implemented.

**Value and limits.** A Packer authoring change becomes local to a discoverable contract instead of a general
loader. Keep authored and resolved validation distinct: update-only readers deliberately validate declarations
without requiring complete artifact pins. Keep deterministic first-error ordering and coded errors. This is
a file organization improvement and does not justify a generic validation framework or new public packages.

**Validation.** Preserve [load contracts](../pkg/nkd/catalog/load_test.go), especially update-reader validation
and patch-byte checks, [Packer](../pkg/nkd/catalog/packer_contract_test.go),
[material](../pkg/nkd/catalog/material_contract_test.go), [provider](../pkg/nkd/catalog/provider_context_test.go)
and lock contracts. Existing tests already separate many of these concerns more clearly than the implementation.

### 5. Separate package-format execution from release assembly

**Evidence.** [Factory execution](../pkg/nkd/factory/execute.go), `executePackage` at lines 343–617, combines
declared-input admission, five packaging paths, temporary carrier ownership, SBOM/evidence collection,
release-content identity and per-output build-subject projection. The Helm branch at lines 420–480 and OCI
publication branch at 481–613 are substantial workflows. Supporting code already has useful homes in
`helm.go`, `helm_binding.go`, `image.go`, `release_evidence.go` and `sbom_publication.go`.

**Proposed organization.** Retain a small package dispatcher with shared admission, then explicit private
format handlers grouped with their helpers, for example `package_helm.go`, `package_apt.go` and
`package_publication.go`. Within publication, separate collecting checked objects from binding their release
metadata. Keep delivery's representation/content-identity implementation in delivery.

**Value and limits.** Readers changing a Helm carrier or release membership can inspect one coherent workflow.
Existing format variation is enough to justify private handlers; no plugin registry is needed. Admission must
remain ahead of handler I/O, cleanup must remain owned on every exit, and semantic/blob digests must retain
their separate meanings.

**Validation.** Use [execution admission](../pkg/nkd/factory/execution_boundary_test.go),
[APT/Helm execution](../pkg/nkd/factory/execute_test.go), [image execution](../pkg/nkd/factory/image_test.go)
and [SBOM publication](../pkg/nkd/factory/sbom_publication_test.go) contracts. Preserve relocation byte equality,
carrier reclamation, exact producer membership and failure before output. Recheck lifecycle consumption and
both maintained NKE/AICR generated bundles.

### 6. Make the local runner a readable sequence of phases

**Evidence.** [The local pipeline runner](../pkg/nkd/pipeline/local.go) combines subprocess wrappers (line 79),
the container reaper (129), `Local.Run` (339), source fetch/vendor/tree verification (793/843/861), build
execution (890), packaging (1061) and graph-selection helpers. `Local.Run` itself spans roughly 300 lines.
Its scheduler tracks dependency counts, readiness, worker slots, prepared/preparing source keys, results and
the first failure inside one operation that also handles qualification and terminal receipts.

**Proposed organization.** Begin with same-package moves into `local_schedule.go`, `container_lifecycle.go`,
`source_fetch.go` and `local_build.go`. Then consider a private scheduler state with methods for readiness,
launch and completion accounting. `Local.Run` should expose admission, the qualified fetch barrier, execution,
terminal receipt construction and cleanup as explicit phases. Group scheduler and cleanup tests accordingly.

**Value and limits.** This reduces the amount of unrelated implementation a reader must understand to change
scheduling. Preserve ordering and ownership before attempting algorithm simplification: every started worker
must be joined, source preparation must not waste all worker slots, and the reaper must close before releasing
the held runtime it needs for cleanup. The public runner interface need not change.

**Validation.** [Scheduling contracts](../pkg/nkd/pipeline/local_schedule_test.go) cover deterministic concurrent
scheduling (formerly line 152); [container lifetime contracts](../pkg/nkd/pipeline/container_lifecycle_test.go)
cover reaper stop/join, early exits and bounded cleanup without holding the registry lock (formerly 272–347).
[Source-cache contracts](../pkg/nkd/pipeline/source_cache_test.go) cover shared preparation
and distinct ready source keys. Retain [qualification ordering](../pkg/nkd/pipeline/execution_identity_integration_test.go)
and run native and Linux race tests for any implementation change.

### 7. Share process-group termination mechanics while keeping execution policy explicit

**Evidence.** `configureCommandCancellation` and `killCommandGroup` are effectively duplicated in
[pipeline termination](../pkg/nkd/pipeline/command_termination_unix.go) and
[heldfile termination](../pkg/nkd/internal/heldfile/termination_unix.go), at lines 13 and 31/33.
[Owned command execution](../internal/ownedcmd/ownedcmd_unix.go) repeats the process-group setup and signaling.
Policies differ: pipeline normally gives cancellation ten seconds, `ownedcmd` uses 250 milliseconds, and
post-wait descendant cleanup has different graceful/forced behavior.

**Proposed organization.** Put the common platform operations under one internal owner, potentially by extending
`internal/ownedcmd`: configure process-group cancellation with an explicit grace and signal the group.
Keep each caller's current post-wait policy explicit during the first consolidation. Executable authentication,
environment policy, command construction, domain errors and lifecycle orchestration remain with their owners.

**Value and limits.** A signal-handling or platform fix gets one implementation and a common edge-case suite.
This small duplication merits attention because it controls child lifetime. A generic runner must not replace
`heldfile.RunContext`: [heldfile execution](../pkg/nkd/internal/heldfile/heldfile.go), lines 548–598, deliberately
coordinates descriptor locks with `Start`/`Wait` and retains input locks through completion. Preserve unsupported
platform behavior and error precedence as well as grace periods.

**Validation.** Retain [pipeline termination](../pkg/nkd/pipeline/command_termination_unix_test.go),
[ownedcmd](../internal/ownedcmd/ownedcmd_unix_test.go),
[Linux heldfile cancellation](../pkg/nkd/internal/heldfile/heldfile_cancel_linux_test.go) and
[shared-input lifetime](../pkg/nkd/internal/heldfile/heldfile_test.go) contracts. Cover successful parents leaving
descendants, cancellation, escalation, start failure and input serialization; run Linux sign/pipeline race tests.

### 8. Group compiler construction and validation by their governing concern

**Evidence.** [Compilation](../pkg/nkd/factory/compile.go) spans 877 lines and
[validation](../pkg/nkd/factory/validate.go) spans 702. Execution binding is constructed in
`execution_binding.go:21`, while its selection checks sit in `validate.go:183–234`. Signing graph construction
is in `compile.go:510`, with its checker in `validate.go:413`. Structural `ArtifactGraph.Validate` and exact
compiled-graph rederivation also share the general validation file despite answering different questions.

**Proposed organization.** Keep `Compile` and `Plan.Validate` as readable sequences of named phases.
Co-locate execution-binding checks with binding construction and signing dataflow checks with signing graph
construction. Give artifact-graph structural validation its own file. Keep complete Plan admission distinct
from these local checks, and use private helpers rather than exported intermediate models.

**Value and limits.** Readers can find where one contract is established and rechecked without searching two
large files. Preserve the existing shared derivations: `compileBuildGraph` is reused by catalog/build checking;
`compileArtifactGraph` is reused by complete graph checking. Preserve full canonical-byte comparison, rejection
order and graph admission at executable entry points. Similar-looking validation is often intentional;
Build Digest equality cannot replace transport/execution binding checks.

**Validation.** Retain [Plan mutation contracts](../pkg/nkd/factory/plan_validation_contract_test.go),
[graph-edge contracts](../pkg/nkd/factory/graph_edges_contract_test.go),
[execution requirements](../pkg/nkd/factory/execution_requirements_test.go) and
[transport binding](../pkg/nkd/factory/build_transport_contract_test.go). Require byte-exact regeneration of
both maintained bundles. This work must not recreate an independent compiler handoff or second validator compiler.

### 9. Group binary-bundle implementation by format

**Evidence.** [Delivery layout](../pkg/nkd/delivery/layout.go) owns binary input/capture types at lines 67–89,
`BuildBinaryBundle` at 139 and tar production at 462. [Delivery inspection](../pkg/nkd/delivery/inspect.go)
owns bundle extraction at 329, archive membership enforcement at 510 and `InspectBinaryBundle` at 574.
Both files also carry generic OCI/publication logic. APT and native images already have recognizable format files.

**Proposed organization.** Move binary build/inspect/extract behavior into `binary_bundle.go` and, if needed,
`binary_bundle_archive.go`. Leave generic OCI blob/descriptor traversal and publication evidence with their
existing owner. Start with file moves; consolidate manifest decoding only after confirming matching contracts.

**Value and limits.** One discoverable format implementation makes both the binary lifecycle and generic OCI
code easier to read. Preserve the difference between standalone index binding and published-member binding.
A descriptive `Inspection` must not become authority through reuse. Keep held source capture and exact bytes.

**Validation.** Use [binary round trips and archive mutations](../pkg/nkd/delivery/binary_bundle_test.go),
[construction admission](../pkg/nkd/delivery/construction_contract_test.go),
[inspection/archive mutation](../pkg/nkd/delivery/inspection_contract_test.go) and
[registry closure](../pkg/nkd/delivery/registry_test.go) contracts, comparing output bytes/digests before and after.

## Structure to preserve

- The `definition` → `buildgraph`/`factory` → `lifecycle` ownership model and the small `client/v1` facade.
  Recent locality work deliberately removed duplicated producer instructions and independent compiler handoffs.
- Separate qualified, pipeline-attested and diagnostic evidence paths. `sign` already shares held cosign capture
  and environment handling; its distinct authentication protocols are not redundant implementations to flatten.
- Separate source snapshots, terminal artifact receipts, descriptive observations and durable release/status
  state. Shared filesystem calls do not establish identical authority or commit semantics.
- Complete graph validation at execution/rendering entry points and exact GitHub rerender comparison after
  semantic checks. These serve different contracts and should survive a file reorganization.
- Small domain owners such as `target`, `material` and `execution`. Their size does not justify folding them
  into a miscellaneous utilities package. Likewise, large files such as Packer execution and transport archives
  deserve later focused reading, but length alone is insufficient grounds for another abstraction.

## Suggested implementation sequence

1. Make mechanical navigation changes first: split command families, catalog concerns and binary-bundle files.
   Keep functions, tests, error ordering and bytes unchanged; review moves separately from semantic extraction.
2. Consolidate the payload observer and lifecycle failure commits. This removes verified duplication from an
   active end-to-end path, with existing cross-format and immutable-attempt contracts.
3. Extract status acceptance and catalog maintenance in separate changes. Record the precise new owner and
   move substantive tests to that owner while keeping CLI contract coverage.
4. Refactor factory package execution and compiler phases. Keep one derivation and verify generated bytes.
5. Consolidate process-group primitives, then simplify local scheduler organization. Treat lifetime and
   concurrency changes as separate reviews requiring Linux validation.

For each change, evaluate readability by whether a maintainer can locate the operation, its invariant and its
tests together. Count removed competing implementations and reduced caller obligations rather than targeting
a line limit. Update affected package comments and existing navigation documentation when the move happens.
Keep the source-scanning purity guard in `internal/architecture` aligned with any moved identity-producing files.

## Implementation record

All nine recommendations are implemented. The package map remains intact; the changes
concentrate ownership and organize private operations inside the existing modules.

| ID | Current implementation | Primary contract coverage |
|---|---|---|
| 1 | [Lifecycle observation](../pkg/nkd/lifecycle/observations.go) delegates binary, APT, Helm and image payloads to [factory observation](../pkg/nkd/factory/outputs_observed.go), retaining frozen slots and outer membership. Disk outputs use the separate [factory-held QCOW2 observer](../pkg/nkd/factory/disk_observed.go). Preparation, hosted finalization/evidence and [attempt persistence](../pkg/nkd/lifecycle/attempt_store.go) have their own files; failure commits share one helper. | [Payload observation](../pkg/nkd/lifecycle/observations_test.go), cross-format execution and hosted finalization |
| 2 | [Diagnostic checkpoint acceptance](../pkg/nkd/status/diagnostic.go) owns authentication through commit and decision; [private status state](../pkg/nkd/status/state_unix.go) owns protected reads, locking and CAS. The CLI maps transport/policy and prints results. | [Diagnostic operation](../pkg/nkd/status/diagnostic_test.go), [state](../pkg/nkd/status/state_unix_test.go), retained CLI behavior |
| 3 | [Typed pin maintenance](../pkg/nkd/catalog/pin_update.go) owns selection/resolution and one lock transaction. CLI command families are separate files; [catalog commands](../cmd/nkdctl/catalog.go) present returned changes. | [Pin transactions](../pkg/nkd/catalog/pin_update_test.go), HTTP/image adapter contracts, CLI output/check tests |
| 4 | Catalog declarations, [load](../pkg/nkd/catalog/load.go), [resolve](../pkg/nkd/catalog/resolve.go), common/VM/material validation, [lock persistence](../pkg/nkd/catalog/lock_io.go) and source resolution are separate concerns. | Existing catalog loader, provider, VM/material and lock contracts |
| 5 | A small [package dispatcher](../pkg/nkd/factory/package.go) admits inputs before private format handlers; [publication assembly](../pkg/nkd/factory/package_publication.go) separates checked evidence, objects and release metadata. | Execution admission, Helm/APT/image, relocation, cleanup and SBOM publication contracts |
| 6 | [Local.Run](../pkg/nkd/pipeline/local.go) exposes admission, qualified fetch barrier, scheduling and terminal observation. A private [scheduler](../pkg/nkd/pipeline/local_schedule.go) owns mutable accounting; process/container, source and build operations have focused files. | [Scheduler](../pkg/nkd/pipeline/local_schedule_test.go), [container lifetime](../pkg/nkd/pipeline/container_lifecycle_test.go), source-cache and execution-identity contracts |
| 7 | [Owned command primitives](../internal/ownedcmd/ownedcmd_unix.go) implement common process-group setup/signaling; pipeline and heldfile retain their grace periods, cleanup policy and Start/Wait ownership. | Ownedcmd, pipeline termination and Linux heldfile cancellation/input-lifetime contracts |
| 8 | [Compile](../pkg/nkd/factory/compile.go) and [Plan.Validate](../pkg/nkd/factory/validate.go) expose named phases. Build/output/execution/signing contracts are grouped with their construction; artifact structure has its own file. | Complete-Plan mutation, graph-edge, transport-binding and generated-bundle checks |
| 9 | [Binary bundle](../pkg/nkd/delivery/binary_bundle.go) build/inspect/extract and [archive handling](../pkg/nkd/delivery/binary_bundle_archive.go) are grouped by format, with shared private manifest decoding. | [Binary format contracts](../pkg/nkd/delivery/binary_bundle_test.go), publication inspection and byte comparison |

Navigation improved without introducing intermediate public compiler models or a general plugin framework.
At the initial implementation, command registration was 65 lines (formerly 1,934), catalog loading 136 (1,395),
compiler entry points 155 (877), Plan validation 159 (702), and the local runner 224 (1,370). These counts describe
smaller entry points, not deleted functionality; operation files hold the implementation.

Binary lifecycle observation now requires a compiled Plan, retained raw producer bytes and exact producer
metadata. Previously, its alternate path could accept a bundle and matching result record without rechecking
all of those inputs. Missing raw bytes and substituted metadata now fail with coded errors. Tests cover this
stronger admission, frozen sibling-result rejection, publication substitution, relocation, and both descriptor
and semantic digests. Hosted missing-evidence fixtures retain raw bytes at their planned paths so they still
exercise the intended evidence failure.

Status acceptance remains diagnostic at an explicit caller epoch. Its private state schema and commit point
are unchanged; a withdrawal advances the high-water mark before returning its rejection. Catalog maintenance
preserves source request order, complete artifact admission before upstream resolution, no-op bytes, source
check mode, and a single CAS commit. Public typed requests/results are descriptive. ADR-06/17/22,
reconciliation, SADD and package comments identify these owners; domain vocabulary and public wire versions
remain unchanged. The source-scanning purity guard still covers the same files: none of its guarded identity
implementations moved.

Full Linux validation also exposed a preexisting node-session cancellation race. `Close` previously depended on
an asynchronous callback to cancel each producer; a resumed producer could reach runtime lookup before that
callback ran. [NodeSession](../pkg/nkd/pipeline/node_session.go) now registers active call contexts and cancels
them synchronously before exposing session cancellation, then joins producers before releasing snapshots.
[Session contracts](../pkg/nkd/pipeline/node_session_test.go) cover cancellation, close/revocation ordering,
and preservation of caller values and deadlines. This correction enforces the existing lifetime contract.

### Implementation validation

Recorded 2026-09-18 against the working tree at that date, with no commit or publication:

- Native macOS arm64, Go 1.27.0: uncached `go test ./... -count=1` and `make check` passed for the refactor.
  After the session cancellation correction, the full `go test -race ./... -count=1`, formatting, vet,
  golangci-lint 2.13.1 and ShellCheck passed again; lint reported zero issues. Session race contracts also
  passed 50 repetitions.
- Linux arm64, Go 1.27.1, kernel 6.12.76-linuxkit: full `go test -race -json ./... -count=1` passed all 31
  tested packages from a source snapshot matching the working tree's Go/module files. Docker ran with
  `--init --network none` and a read-only module cache. Session race contracts passed 25 repetitions.
  Linux held-executable and sandbox contracts ran. External BuildKit, cosign, downloaded SBOM/Helm fixtures,
  a platform-specific case and the subprocess helper retained their explicit skips.
- The committed NKE bundle passed `run generate --check`. AICR generation at the baseline commit and current
  tree produced four byte-identical files, and both regeneration checks passed. The AICR Plan digest remained
  `sha256:2c301624223847016c3c54288ea225a64c8a205f08f1f62c53a3903e17996ece`.
- A fixed binary-bundle fixture (linux/amd64, epoch 1748736000, one executable installed at `/usr/bin/tool`)
  produced an identical index, layout marker and every blob before/after the delivery move. Its manifest
  remained `sha256:ec5270171da6b56d91f81df8602c8c264683014ad43f49ce09084c5a0e08e754`.
- Local links in all changed Markdown documents, tracked/untracked whitespace and `git diff --check` passed.
  Independent reviews of status acceptance, catalog maintenance and factory extraction found no additional
  actionable regressions.

The first full Linux harness ran Go as PID 1 and left killed descendant processes as zombies. Inspection
confirmed they were terminated; using Docker's init process corrected that harness issue. The next run exposed
the separate node-session defect documented above, which was fixed before the successful final suite.

This is implementation and contract-test evidence. No new hosted build, registry publication, signing-service
exercise, qualified reproduction or operational acceptance run was performed.

### Rebase integration (2026-09-21)

The uncommitted refactor was reapplied onto `upstream/main` at
`0f71926b65d3177e1b20e1a95cae8e392931dfdc`. Six overlapping files were resolved while retaining upstream's
explicit disk material closures, IR v4/compiler-only VM lowering, exact host toolchain selection and local VM
admission primitive. The new rules live in the refactored catalog/compiler owners; raw disk outputs retain
their compiler-issued artifact IDs. Disk execution, observation and publication remain capability-gated.
The session conflict retains synchronous Close cancellation plus caller values/deadlines.

The September 18 digests above remain historical evidence. Upstream itself changed generated plans;
comparison for this rebase uses the new upstream commit. NKE regeneration is current, and all four AICR
generation files match clean upstream byte for byte. The AICR Plan digest is now
`sha256:bda412d2c39d8f75d2525cd0f30b53c23208a2e94615101c1dd4fcbe20aa88d1`.

The rebased tree passed uncached native unit and race suites, formatting, vet, lint, ShellCheck and whitespace
checks. The complete Linux arm64 race suite passed all 31 tested packages, including new VM/cgroup contracts;
its seven environment-dependent skips are unchanged. An independent compiler-integration review found no
missing upstream behavior. At that stage the refactor was uncommitted and unstaged; no external execution was claimed.

### Stack refresh integration (2026-09-22)

The ten review branches are rebased on `2d6022b8933156c32061b5fad1aca2079f877947`, incorporating the subject-scoped
reproduction documentation and held-QCOW2 lifecycle observation from upstream. The first nine refactor patches
retain their existing code changes. The final lifecycle branch integrates the new disk path into the moved
observation and preparation files rather than retaining duplicate implementations in the execution entry point.

Disk observation requires factory's concrete in-process execution result before pathname projection. Ordinary
package observation still rechecks raw producer bytes and metadata. Execution results close before committing
successful observations or observation failures, and Run copies retain independent disk metadata. Exact QCOW2
delivery-byte observation does not establish decoded RAW reproduction, full-disk content validation, an SBOM,
compatibility, publication, or production VM execution. The earlier rebase's disabled-path statements describe
its September 21 base; current support is recorded in reconciliation and ADR-14.

The refreshed tip passed uncached native unit and race suites, formatting, vet, lint with zero issues,
ShellCheck and whitespace checks. The full Linux arm64 race suite passed all 31 tested packages, including
the held-QCOW2 observation and result-lifetime contracts; the seven explicit environment-dependent skips
remain. Each of the first nine intermediate commits separately passed all-package compilation and focused
factory/lifecycle/delivery disk and result-lifetime race tests in an isolated source snapshot.

NKE regeneration passed, and all four AICR output files match the new upstream base byte for byte with both
regeneration checks passing. Their Plan digests remain the September 21 values. New lifecycle regressions
cover missing or closed disk capabilities before path handling and independent disk metadata in Run copies.
An independent lifetime review found no actionable integration defects. No new operational execution or
qualified reproduction evidence is claimed.

## Baseline audit validation

The original audit changed documentation only. Native environment: macOS arm64, Go 1.27.0. Formatting, `go vet ./...`,
ShellCheck, `go test ./... -count=1`, `go test -race ./... -count=1`, and golangci-lint 2.13.1 passed. The first
sandboxed gate could not access the Go cache; rerunning with cache/listener access resolved that limitation.
The default PATH linter was built
with Go 1.26 and rejected the Go 1.27 target. The installed Go 1.27-built linter passed with zero issues when
selected explicitly; no toolchain or repository configuration was changed.

Local Markdown links in this report and the documentation index resolve, and whitespace checks pass.
No Linux or external hosted/registry/signing acceptance run was performed for this documentation audit.
That audit gate did not validate the subsequent implementation. Refactor validation is recorded separately
in the implementation record.
