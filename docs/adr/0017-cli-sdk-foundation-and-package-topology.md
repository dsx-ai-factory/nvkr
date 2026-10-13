# ADR-17 — CLI/SDK foundation, seams & package topology

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-17 in the consolidated NVKR ADR set.
- **Relates to:** ADR-03 (exit codes), ADR-18 (runner / ctx), ADR-20 (publish seams), ADR-01 (canonical codec).
- **Satisfies:** FR-21 (standard, replaceable interfaces); supports FR-20 (coded errors — owned by ADR-03).

## Context

NVKR is a **thin CLI over a reusable SDK**. It needs an embeddable facade, context/cancellation discipline,
swappable external-transport seams, and a clean package topology, so later work does not re-accrete orchestration
in `main` or double-author types.

## Decision

### 1. A `client/v1` SDK facade; deep modules remain authoritative

The six public objects are **Definition, Environment, Run, Artifact, Release, and Verification Policy**.
The main lifecycle is **Definition → Run → Release**. Bundles are Artifact formats. Deployment topology
belongs to the downstream deployment owner and is not part of the Definition or Release schema. Plans, graphs, inventories, vectors, release
sets, clearance, publication roots, and qualification records are generated/authenticated internals available
through inspection. Normal callers neither construct nor connect their receipt graph.

Portable test observations and their digest-pinned locators are generated evidence records, not
additional authored lifecycle objects. `evidence emit`, `publish`, `verify-local` and `verify`
delegate through the SDK to `testobservation`; the CLI owns flags and presentation only.
Unsigned self-consistency, authenticated historical observation and qualification remain distinct.

Definition declares named outputs directly. Each native output selects exact catalog components and a concrete target
platform, together with its artifact format. An OCI aggregate instead declares a sorted, unique `platforms`
set; the compiler derives one native producer target per member and one required image-index output
binding every member's exact manifest, Build identity and evidence. An output may explicitly
select another catalog. The compiler derives its complete pinned build graph, routine package envelopes,
required slots, and routes. Byte-changing transformations remain explicit. Missing or duplicate selections,
unsupported platforms and unsupported formats fail with coded errors before work.
Definitions are selected directly by file. There is no authored Recipe, reusable selection group, criteria
matching, mixin, or recipe overlay; legacy fields and layouts are rejected rather than converted implicitly.

`format: disk-image` is an authoring contract for exactly one selected disk root plus every explicitly selected
component in that root's transitive material closure. Resolution derives the root from catalog output metadata
and rejects a missing or second root, an incomplete closure, and unrelated selected components. Disk producer
admission is based on the catalog component's output type, disk metadata and builder contract, never a category
or node role.
Plain and FDE outputs select different catalog components;
Definition has no disk variant, encryption boolean, build command, or Packer field. Catalog owns disk metadata
and production details. Definition schema validation, catalog resolution, and compiler-only Run Plan lowering
are supported. The Plan points the lifecycle output at the root producer's exact disk artifact and carries typed
command/Packer, disk, public-variable, material-edge, execution-class, and host-toolchain inputs. It does not
wrap the QCOW2 as a binary bundle. Local unencrypted direct-Packer execution and held-QCOW2 observation are
supported as described below. The compiler can describe disk publication and its evidence graph, and dry-run
can inspect that Plan. The ordinary local scheduler rejects disk publication with `engine-capability` before
runtime, source, subprocess or workspace work until its grouped live inspector/package executor is connected.
ADR-18 separately admits generated native ARC jobs for unencrypted direct-Packer outputs. The recorded
M2 candidate exercised that pipeline-attested publication route with `build-provenance-v1`, explicitly
deferring inspection, under a validation-branch exception. General hosted VM adapters, FDE and qualified
execution remain capability-gated. The strict live full-disk evidence/publication path has guarded APIs
without a successful real NVKE join; main-only execution, independent full-payload disk consumption,
strict inventory hardening, M3 qualification acceptance and organizational acceptance remain separate gaps in
[reconciliation](reconciliation.md).

Definition v5, Run/Release v4, and compiled Run Plan/artifact graph v5 records reject the
retired cluster, role, category and composition fields. Signer policy roles remain a distinct security concept.

Environment separately supplies runtime, runner, publication namespace, credential references and signing policy
(ADR-20). Output-affecting tools stay in the build identity they affect.
Changing transport or signing configuration cannot silently change logical Build identity.

`pkg/nvkr/definition` owns authored Definition/Environment loading, validation and identity. `factory.Compile`
accepts those exact inputs and owns the complete Plan, output joins and evidence requirements. It returns no
partially completed plan and exposes no independently authored CompileSpec or ExecutionSettings handoff.
`lifecycle` owns preparation, immutable attempt persistence and consumer workflow. Preparation retains an owned
snapshot; subsequent caller mutation cannot change execution. `factory` owns Run scheduling/execution and
provider generation. Its typed payload observer rechecks binary, APT, Helm and image outputs against their
exact planned producer inputs; lifecycle retains frozen requirements and outer publication membership.
Disk outputs use factory's concrete in-process `LocalRunResult` and held-QCOW2 observer. Lifecycle stages the
observed bytes beneath its immutable attempt workspace while the held capability rechecks their digest, syncs
the read-only file, binds the observation, and closes the held result. An explicit attempt transaction then
commits the QCOW2 and successful Run record together. After successful rollback, a commit failure records a
failed Run. Its durable marker makes interruption
distinct from completion on a reused attempt. Marker removal plus attempt-directory sync is the success commit
point; handle cleanup after it is non-authoritative. Serialized descriptions cannot restore the held capability,
and callers cannot select an export path. This local retention contract does not enable disk publication.
`delivery` owns checked publication membership and canonical release-content identity. A consumer may optionally
prepare an exact Definition, Environment and compiled Plan expectation before external work, then require the held
verified publication to match its authenticated input identities and complete output binding. Lifecycle performs
that compiler-owned join and issues an opaque receipt bound to the publication subject, consumer policy and matched
identities. Its defensive description exposes those matches without serializing or restoring the receipt; neither
the receipt nor its description grants a stronger lane or scope. Consumers
do not reconstruct bindings from caller-provided digests or publication fields. Existing domain modules retain their
canonical, trust, evidence, status and release invariants. `client/v1` is a context-first facade,
with process configuration in functional options and per-call inputs in method arguments. CLI commands parse
flags, call those use cases, format results and map ADR-03 errors. Neither layer mints authority from fields.

For M3-A, `run check-artifacts --run <exact-attempt-URL> --policy <path>` delegates to
`lifecycle.CheckQualificationRun`; the only optional command input is the private workspace. Lifecycle owns
strict policy loading and the full handoff orchestration. `qualificationorigin` discovers the commit from
the exact successful GitHub attempt, applies the independent repository/workflow/default-branch policy and
issues a closeable source receipt. It downloads only that attempt's bounded, provider-digest-bound archive,
whose sole member is the generated `handoff.json`. An artifact name or decoded description grants no authority.

Factory captures the complete compiler-read authored closure and replays it from memory through the existing
compiler. Original logical paths, base and output catalog overrides, overlays, registries, locks, patches and
directory facts are preserved. Capture uses no-follow source reads; each file is bounded to 4 MiB, the
aggregate to 32 MiB and file/directory/listing facts to 1,024 entries, with the catalog traversal-depth limit.
Replay cannot fall back to a checkout. Catalog retains its bounded pinned-upstream Git authentication and embedded
declaration checks, so replay is not offline. Lifecycle compares the recompiled Plan with the saved original.
The generated signed `per-output-v1` producer exports original predicates only after successful finalization.
Unsigned/dry-run and legacy aggregate producers cannot emit a qualification handoff; aggregate publication
remains available without M3 qualification. Consumer policy and verifier pins remain separately selected.

`lifecycle.Consumer.CheckQualification` joins the authenticated source to the verified publication, exact
Definition/Environment/Plan, every named output and authenticated build evidence. Factory owns
production-predicate semantics; signing owns cryptographic verification. The immutable artifact-check receipt
retains the verified publication and frozen Plan. Its idempotent closure is shared by shallow copies: it
revokes admission, cancels and joins registered operations, then releases held payloads. The receipt grants
no smoke-execution or signing authority. The direct consumer and `CheckQualificationHandoff` operations borrow
the caller's source receipt; source closure also revokes the check receipt. The Run adapter and retained
`CheckQualificationFiles` diagnostic adapter transfer their source owner to the returned receipt. All
qualification adapters share independent canonical registry-host admission for explicit pull credentials;
the candidate cannot select the credential destination. Anonymous and host-scoped keychain reads remain
available. Defensive
diagnostics cannot restore authority after closure. The independent `qualification` publisher contract
consumes authenticated check and smoke receipts; its public descriptions are not receipt constructors.
`run qualify --policy <path>` delegates to `qualification.PublishTriggeredRun`, which derives its source
from the authenticated hosted event. The generated hidden native worker command also takes only that
policy; it is not an independently authored lifecycle input. Pipeline owns isolated held-byte execution,
lifecycle owns fixed version evaluation and authenticated complete worker aggregation, and qualification
owns worker report signing and final subject-bound publication. Native and aggregate receipts borrow A and
the qualifier invocation, preserve shared revocation, and expose only defensive descriptions. The old
file-based publishing adapter is capability-gated before I/O; A's file diagnostics remain supported.
`run generate --qualification-policy <path>` composes the producer generator with the qualification-owned
workflow pair and manifest. `lifecycle.ValidateQualificationSmokePlan` owns the pure policy/Plan scope
join shared by generation and runtime; adapters cannot choose a different applicability rule. Generation
selects exactly the executable native platforms, retaining the final AMD64 signer requirement. The CLI
only selects the use case and presents its result.
ADR-16 owns verdict and publication rules, and reconciliation records the separate A/B/C implementation and
acceptance boundaries.

`status.AcceptDiagnosticCheckpoint` owns protected minimum reads, exact document authentication, checkpoint
verification and receipt-derived byte-CAS persistence before interpreting withdrawal. Its caller-supplied epoch
and returned decision remain diagnostic; this operation adds no trusted-time or release authority.
`catalog.UpdateSourcePins` and `UpdateArtifactPins` own selection, resolution, change detection and the existing
lock transaction. Commands map flags, select bounded contexts and present structured change summaries; they do
not own the persisted status schema or reconstruct pin-update transactions. `catalogmaintenance` composes
that pin owner with normal lifecycle compilation/generation for review-only candidates and renders the
explicitly configured maintenance workflow. `catalog refresh` and `catalog update-ci` remain thin adapters;
these maintenance descriptions cannot grant execution or release authority.

`planview` owns immutable descriptive snapshots after factory validates the complete Plan. It renders readable
Plans, joins producer and artifact graphs by exact instruction references, and compares every field while
separating per-output Build Digest changes from execution and transport changes. It issues no authority.

Normal commands are `definition validate|inspect`, `run plan|graph|diff|local|generate|check-artifacts`, and `release verify|inspect|browse|export`, with
`--definition` and `--environment`; catalog maintenance and exact component/platform diagnostics remain
available. The SDK resolves direct selections into generated graphs and Build Digests. Backward compatibility
is not required. Remove superseded authoring APIs and wire fixtures; retain internal mechanics only when the
current lifecycle uses them. SDK callers obtain an opaque prepared Run and execute/generate it; decoded
JSON cannot restore executable authority. Consumer verification takes one pinned Release reference and explicit
policy (ADR-04), returning inspectable views plus a closeable capability. The reference selects the sole
ADR-20 release index: delivery owns its metadata/descriptor/evidence join, checked named outputs, exact sibling
artifact references, exact Definition, Environment, Plan and content identities, and release content projection.
Lifecycle applies consumer policy and assembles the Release
view. `VerificationPolicy.minimumDiskEvidence` is part of the policy digest. Pipeline-attested policy defaults
to `build-provenance-v1`; explicitly selecting `strict` requires inspected evidence. Qualified policy defaults
to strict and rejects the producer-only minimum. Inspected evidence satisfies either minimum; producer metadata
cannot weaken the selected consumer policy. The signed mode remains
descriptive and does not grant authority.
Transport locators are excluded from
the content subject and bound by the release metadata. A second published lifecycle-binding object is not retained. An external SDK test proves embedding
without `main`. Low-level command orchestration must serve a current diagnostic or execution use case, not preserve an obsolete workflow.

`lifecycle.InspectRelease` is the bounded, context-first read-only entry point for registry or local release
navigation. Delivery validates the bounded metadata and evidence closure before projecting declared output
and evidence membership, without downloading or reading artifact payload layers. Local inspection accepts a
complete publication layout or a metadata-only layout. Presentation may then focus on an exact output or
render text, JSON, or Mermaid; platform labels describe checked config metadata. Reference and transport
admission precede filesystem/network work. Registry inspection owns and removes a system temporary directory
by default; an explicit caller-owned workspace is retained while its private staging child is removed.
Each format labels metadata inspection explicitly. These detached descriptions establish no payload,
signature or policy verification, and cannot restore a verified Release capability. Explicit Release
verification retains its full payload and policy checks. The existing compact local `release inspect` view remains.

`lifecycle.ExportRelease(ctx, reference, options)` owns complete release-description export (ADR-19/20).
It uses the same bounded inspection and versioned JSON description as `release browse --format json`.
The `nvkr.dev/release-description/v1` schema is a detached read model, not another public lifecycle object.
Export takes one immutable OCI reference; an optional local layout caches that exact publication. It needs
no Plan, mapping, requested platform, producer checkout or build workspace. Every named output and platform
is retained, including aggregate image-index children and exact evidence/source bindings. Consumer-specific
roles, source compatibility and installation requirements remain outside this interface. The CLI writes the
complete description after admission and metadata validation. Export creates no identity, Run completion,
payload or signature acceptance, or publication authority. Unpublished workspace export is not an interface.

The SDK exposes document loading, Run preparation/execution/generation and scoped Release verification.
`ResolveBuild` and `Run` remain for exact producer diagnostics; optional `AtomicOutput` commits a diagnostic
graph export. It does not expose a second publication workflow or qualified-evidence orchestration. The unused
SDK validation-vector/profile authentication, qualified rebuild, vector creation, release authorization and
publication methods and their injected authority options are removed. Existing diagnostic commands use the
owning trust, profile, attest and release modules directly; their receipt checks and capability
gates remain. The unreachable qualified transport package and publication helper are removed. Qualified
publication remains unavailable until its authenticated commit protocol exists.

### 2. Context threading + subprocess cleanup-on-cancel

`ctx` is the **first argument** on every facade method. The deep `pipeline.Runner` is the documented
low-level runner convention: its method carries context in `RunOptions.Ctx`, and every public local
execution or terminal-verification entry point rejects a nil value at admission, before validation or I/O. A
future breaking runner-interface revision should move it to the first argument.

Every subprocess MUST use context-aware execution with bounded **SIGTERM→SIGKILL reaping**. The scheduler MUST
keep an owned cleanup registry and force-remove started containers on cancel or exit, without holding its
registry lock across runtime I/O. Build outputs are written to a temp path and **atomically renamed on success
only**. A conforming cancel returns the `timeout` code, leaves **zero orphaned containers and zero partial
artifacts**, and writes no manifest. The local pipeline and the Linux descriptor-held subprocess boundary
implement these owned, bounded cancellation mechanics; the acceptance section below deliberately does not claim
exhaustive zero-orphan proof for the remaining daemon/adapter matrix.

Catalog I/O follows the same rule. Authority-bearing loads admit a deep context-aware provider rather than
placing checkpoints around a legacy blocking call. The stock `--data` provider descriptor-resolves every path
component without following symlinks, accepts regular files only, caps each file and the aggregate load, and
bounds recursive listing count and depth. `client/v1.WithProvider` rejects a legacy provider before invoking it.
Catalog parsing and I/O use the same context-aware loading API.

Finite generated workflows follow a value-returning interface: `factory.Render(ctx, plan, engine)`
returns the complete bundle after cancellation-aware rendering. It does not accept an arbitrary `io.Writer`,
because a callback that blocks inside `Write` makes cancellation unenforceable.

### 3. Standard, replaceable external-interface seams

The maintained SDK admits catalog I/O, diagnostic execution and a scoped Release consumer. The owning
modules retain their external I/O seams and authenticated receipts. Missing authority-bearing verifiers,
evidence sources and publishers fail closed before I/O. There is no permissive production default that can
manufacture authority. Unused qualified transport adapters and their speculative injected backends are removed;
future qualified publication needs an authenticated commit protocol before exposing orchestration or SDK options.

### 4. Package topology and single-owner rule

- A documented **single-owner package map** — `definition`, `lifecycle`, `factory`, `delivery`, `canonical`, `catalog`, `buildgraph`, `target`,
  `execution`, `ir`, `pipeline`, `attest`, `release`, `status`, `provenance`, `sbom`, `vuln`, `trust`,
  `qualificationorigin`, and the separate M3-C `qualification` publisher.
  Catalog owns producer admission and snapshots; buildgraph owns exact selection and graph compilation;
  target owns concrete supported target platforms. Each owns its schema, returns typed coded
  errors, and marshals through the canonical codec (ADR-01). `client/v1` composes those owners; `main` must not
  own a competing schema, verifier, receipt constructor, or policy decision. Remaining multi-step command
  orchestration is migration debt tracked independently rather than declared absent.

The standalone query/diff selector workflow is removed; inspect the current lifecycle views directly.

### 5. Same-process authority has an explicit lifecycle

An opaque same-process capability exposes an idempotent `Close` operation that revokes authority across shallow
copies. If it also retains file descriptors, subprocesses, locks, or other resources, `Close` releases them.
Ownership transfers are stated on the returning method. Facade results either close on every failure or transfer
one clear close obligation to their caller. Garbage-collector cleanup may be defense in depth, never the
correctness or resource-reclamation mechanism.

The non-owning installed-inventory receipt in ADR-13 borrows the issuing Run's
lifetime; it has no separate close obligation or resource ownership. The Run's
idempotent `Close` revokes every borrowed receipt and shallow copy. Live receipt
validation requires that original open owner; a description cannot restore it.

The current diagnostic `client/v1.LocalRunOptions` facade aliases the lifecycle options, including
`DiskInspection`; callers therefore open and own the concrete `sbom.DiskInspectionRuntime` using the
pipeline selection type. `Run.InstalledDiskInventories` exposes factory subject descriptions and SBOM package
metadata, never a receipt. This deliberately retains the existing deep-owner types for the optional diagnostic
instead of adding a second runtime or inventory schema. A facade-only construction API and independent
`client/v1` acceptance harness remain deferred; direct deep-package tests do not establish that acceptance.

The producer `NodeSession` cancels admitted call contexts synchronously when `Close` starts, before exposing
session cancellation, then joins active producers and releases source snapshots. Calls retain their caller's
context values and deadline. An asynchronous cancellation callback alone cannot enforce this close ordering.

### 6. One Run is one immutable attempt

Preparation freezes admitted Definition/Environment inputs and required slots before execution or reservation
of attempt storage. Execution binds independently observed bytes and evidence; missing/conflicting/extra members
fail completeness (ADR-05). A failed Run retains its original requirements and coded failure. Retry requires a
new explicit attempt and cannot overwrite the previous observation. A completed descriptive Run is not clearance.

Each named output is an independent required use. Equal Build Digests do not merge producer jobs, restore
results, terminal observations or evidence. Joins bind the exact output and selected target as well as the
Build Digest; a successful sibling cannot satisfy an output whose production or evidence is missing. Output
names remain authenticated by the owning Plan and catalog producer records retain their own snapshot
identity, even though output labels and non-output-affecting metadata are excluded from logical Build identity.

Persisted Run descriptions cannot resume execution. Any future resume path must own locking, byte-CAS/no-replace,
deterministic recovery and fsync; until implemented, reject resume. Dry-run admits the plan without creating an
attempt workspace. Hosted finalization separately requires producer-job success, successful restore, terminal
observations, exact payload/evidence closure and production predicate semantics. Surviving payloads alone cannot
hide a failed job or restore. If bootstrap/finalization cannot run, provider state may exist without any Run
record; absence of a record proves neither success nor absence of remote effects.

## Consequences

**Positive**
- Downstream integrators embed the SDK to prepare and execute Runs or verify Releases, branching on coded failures.
- ctx and cancellation are explicit on every public operation; the options-carried deep-runner context
  remains visible migration debt and fails closed when omitted.
- Swappable transports keep NVKR integrating only via standard interfaces, while missing authority adapters fail
  honestly instead of falling back to declarations.
- The package map prevents `main` re-accretion and double-authored types.

**Negative / costs**
- Extracting the facade is a broad refactor gated by golden-CLI tests to prevent output and exit-code drift.
  During migration some commands remain thicker than the target boundary and must be listed as such.
- ctx plus subprocess cleanup changes kill/cleanup semantics; the cleanup registry must be exercised.
- Resource-bearing result types require callers to adopt `defer Close`, and copying such a value does not create
  an independent authority lifetime.

**Neutral**
- Coded errors and their exit-code table are owned by ADR-03; this ADR wires the CLI/SDK to them.

## Alternatives considered

- **Keep orchestration in `main`, export helpers.** Rejected — blocks embedding and re-accretes logic.
- **Options carry everything (no per-call request structs).** Rejected — conflates per-call inputs with
  process-lifetime config.


## Follow-ups

- **ADR-18:** the runner shares the ctx/cleanup discipline and is a mode/subset of the tool.
- **ADR-20:** the publish phase uses the external-interface seams.

## Acceptance evidence

Current contract evidence in `client/v1/*_test.go`, `cmd/nvkr/*_test.go`, `pkg/nvkr/pipeline/local*_test.go`,
`pkg/nvkr/pipeline/container_lifecycle_test.go`, and
`pkg/nvkr/internal/heldfile` covers context-first facade calls, authoritative-module equivalence, injected
catalog/runner/consumer seams, local cancellation propagation, an explicitly stopped and joined container-reaper
waiter, bounded lock-free cleanup I/O, held-child SIGTERM→SIGKILL escalation, atomic output, exact-byte
receipt checks in the owning modules, rejection of unauthenticated inputs, and absence of implicit publication I/O.
Public diagnostic values cannot be promoted into authority by the facade. Qualified reports and evidence receipts
retain explicit close lifetimes in their owning modules; they are not separate SDK workflows. Direct runner entry points also reject a nil `RunOptions.Ctx` before inspecting their graph, node, or
results path. The stock measure-only verifier rejects a missing producer-results source before trust, profile, or
catalog I/O; the independent disk-envelope decode lane does not require producer results. Catalog and SDK tests
additionally prove pre-canceled/no-I/O admission, legacy-provider rejection, no-follow regular-file reads,
file/aggregate byte ceilings, deterministic directory count/depth ceilings, and context-aware lock
snapshot/update behavior.

Status operation and state contracts cover commit-before-withdrawal, authentication and CAS failures, replay,
protected reads and kernel-released locks. Catalog maintenance contracts cover no-op bytes, source check mode,
whole-update CAS and artifact selection before upstream calls. Lifecycle binary observation now requires its
complete Plan and retained raw producer bytes, including exact IR/result metadata; packaged bytes alone are
insufficient. The owning package tests and [reconciliation](reconciliation.md) record these current
ownership and contract boundaries; completed organization reports are not a second status owner.

The Recipe-removal migration changes public requests, generated graphs and their wire versions. Native and
Linux race tests provide contract evidence; repository checks are recorded in reconciliation.

End-to-end acceptance requires a separate consumer module using only `client/v1` to load inputs, prepare and
execute a Run, and verify its published Release under explicit policy, plus cancellation tests against every
real subprocess and network adapter proving no child, container, temporary output, or partial publication remains.
Qualified diagnostic commands remain explicitly scoped consumers of their owning modules. They do not constitute
an alternative supported publication workflow, and current command orchestration is not described as fully thin.
