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
deferring inspection, under a validation-branch exception. A separate native ARC build-only route omits
publication and signing, retains exact raw QCOW2 bytes and producer records, and finalizes a static-unqualified
Run. It admits ADR-10's explicitly public-bootstrap FDE producer as well as plain disks. Native ARC also admits
public-bootstrap FDE publication with explicit `build-provenance-v1` evidence and exact encryption metadata
binding. General hosted VM adapters, secret-valued FDE, encrypted inspection, strict FDE publication and
qualified execution remain capability-gated.
The strict live full-disk evidence/publication path has guarded APIs
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

For the in-progress local rehearsal, `lifecycle.PrepareCapturedFiles` delegates to factory's existing
bounded compiler-read capture and retains the snapshot privately with the prepared Run. Real local
execution commits `compile-inputs.json` before starting producers; dry-run and failed capability
admission create no snapshot or attempt storage. Later edits to authored files cannot change that
snapshot. The snapshot remains descriptive: it grants no completed-Run, payload or signing authority.

The distinct `lifecycle.CheckQualificationReference` entrypoint discovers that exact source attempt from
an authenticated selected publication's signed `InvocationID`, retaining the same independent policy and
Run/handoff authentication. The receipt must bind the selected root (ADR-16 §5). `Client.Validate` delegates
to `validation.RunFiles` and its concrete `conformance` adapter; [ADR-16 §6](0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns its descriptive execution boundary. This does not require a customer-supplied Run URL.

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
`run qualify-local` delegates to `lifecycle.QualifyLocalRun`. It selects a completed captured attempt,
independent local policy, three explicit private-key files, native runtime, pinned cosign, registry
namespace, scratch and new evidence destination. Lifecycle owns the complete check/sign/smoke/publish/
read-back sequence and cleanup; the command emits only its descriptive canonical result. This separate
local lane accepts no hosted invocation and supplies no hosted qualification or release authority.
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

`progress` owns bounded descriptive snapshots of admitted operations and named
outputs, supplied by factory scheduling and lifecycle output-observation boundaries.
It also carries detached public command-result descriptions (titles, fields, items
and navigation links). These values are presentation contracts, not additional
Run membership or SDK inspection methods; CLI adapters project recorded metadata.
It calls no renderer or caller-controlled writer from producer updates. Lossy
wakeups and bounded public activity cannot change frozen membership or finalization.
`internal/runpresenter` owns terminal admission, rendering, cancellation requests and
joined screen/event/worker lifetimes. It has no receipt or deployment ownership.
Live CLI result/error pairs independently select final reporting and exit status;
complete retained output plus cleanup failure remains a complete Run and a failed
command. These transient descriptions add no field to signed/canonical identities.
The CLI captures a bounded tail of interactive producer stdout/stderr in memory,
without producer-side filesystem or renderer callbacks. A failed command replays
that tail to ordinary stderr after terminal restoration and owner joining, with
truncation marked. Raw diagnostics never enter safe snapshots or panes. This
does not create durable general-purpose logs or replay successful console output;
existing Packer diagnostic retention remains separately owned.

Lifecycle's `InspectRun` owns bounded, no-follow canonical record inspection and
projects safe file-recorded metadata and attempt-marker descriptions through the
SDK. A record must exist and name its containing attempt directory; absence
fails with a coded verification error and never establishes interruption or
excludes a live owner. Inspection admits only currently producible lanes, not
qualified lifecycle records. It validates the existing output slot bindings, without opening payload
locators, restoring execution authority or inferring current availability/health.
The CLI may show these views as plain/JSON or an explicitly selected read-only
terminal explorer. Of the 27 public command leaves, 23 admit an explicitly selected result browser;
`validate`, `run check-artifacts`, `run qualify` and `run qualify-local` retain canonical machine-readable diagnostics. The hidden native
worker is not a public command.
Owning operations, including maintenance transactions
and verification, finish and release receipts before this read-only view opens.
Typed names, identity fields, counts and navigation links are descriptions; they
exclude transport locations, private paths and raw error/value text. Closing a
result view neither repeats nor reverses the operation, and owning status codes
remain authoritative; Ctrl+C closes a read-only viewer and cancels only live work.
Existing non-interactive and explicitly plain output bytes, including exact Plan
JSON, are preserved. Interactive `run local` defaults to the dashboard and does
not replay successful producer output. Nonempty `CI` keeps auto plain; explicit
terminal mode remains available. Hidden CI/execution helpers retain their existing interface.

Release browsing reuses that shared terminal explorer and its session ownership.
Its output overview supplies compact metadata summaries when they fit;
full fields remain available in Details. Command-item search covers only the supplied public names,
kinds and projected fields. Release browse and export share the sorted description projection so chart,
native-image and exact evidence memberships stay consistent. Explicit JSON or Mermaid output forces plain
presentation in auto mode and conflicts with explicit terminal presentation before acquisition.

Normal commands are `validate`, `definition validate|inspect`, `run plan|graph|diff|inspect|local|generate|check-artifacts|qualify|qualify-local`,
and `release verify|inspect|browse|export|pull`. Producer operations select authored inputs with `--definition` and
`--environment`; artifact checking and qualification use the Run/policy interfaces above. Catalog maintenance
and exact component/platform diagnostics remain available. The SDK resolves direct selections into generated graphs and Build Digests. Backward compatibility
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

`Consumer.Authenticate` admits only pipeline-attested `authenticity` policy and issues a distinct opaque
`AuthenticatedRelease` after checking the complete metadata/evidence closure and its required signatures,
production provenance and SBOM bundles. It never opens the full-payload publication owner. Its description
explicitly reports `payloadIntegrityVerified: false`; it supplies no payload export or exact-input receipt.
`Consumer.Verify` retains the full-payload artifact-publication contract. These capabilities cannot substitute
for one another, and their serialized descriptions cannot restore live authority. The CLI selects the narrow
operation through `release verify --scope authenticity`; exact-input flags are rejected at admission.

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
signature or policy verification, and cannot restore an authenticated or verified Release capability. Explicit
`artifact-publication` verification retains its full payload and policy checks; `authenticity` adds policy-bound
signature/evidence checks to metadata acquisition. The existing compact local `release inspect` view remains.

`lifecycle.ExportRelease(ctx, reference, options)` owns complete release-description export (ADR-19/20).
It uses the same bounded inspection and versioned JSON description as `release browse --format json`.
The `nvkr.dev/release-description/v1` schema is a detached read model, not another public lifecycle object.
Export takes one immutable OCI reference; an optional local layout caches that exact publication. It needs
no Plan, mapping, requested platform, producer checkout or build workspace. Every named output and platform
is retained, including aggregate image-index children and exact evidence/source bindings. Consumer-specific
roles, source compatibility and installation requirements remain outside this interface. The CLI writes the
complete description after admission and metadata validation. Export creates no identity, Run completion,
payload or signature acceptance, or publication authority. Unpublished workspace export is not an interface.

`lifecycle.PullReleaseOutput`, exposed as `release pull`, is a diagnostic use case for one exact named output
under a pinned registry Release reference. Lifecycle owns the overall deadline and delegates to `delivery.PullOutput`.
Delivery owns reference, selection, destination and transport admission, complete metadata/evidence validation,
selected format and payload-closure checks, and private temporary storage and cleanup. Its optional workspace
is the parent for owned temporary data. Executables retain their bundle names and modes; disks and Helm charts
retain exact payload bytes in named files, including declared LUKS2 disk ciphertext without decryption. Images
retain their native OCI/Docker manifests, configs and layers in a standard OCI layout; APT repositories retain
their repository trees. An aggregate OCI output includes every declared platform. Only the selected output's
payload closure is downloaded. The destination parent must already exist; existing destinations are rejected,
and installation suggestions cannot redirect writes. Failure or cancellation before commit exposes no
destination; a failed directory sync after commit can leave the complete destination while reporting failure.
Malformed or unsupported format metadata fails with a coded error before payload or destination work. This
operation runs on macOS and Linux and executes no downloaded file.
The CLI retains plain output by default and admits its optional read-only result browser before acquisition;
the successful snapshot describes files already written and cannot repeat or reverse the operation.
Its result describes integrity-checked bytes and confers no signature, policy or Release acceptance;
authenticated consumption remains the explicit verification use case.

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
  `qualificationorigin`, `testresults`, `validation`, `conformance`, and the separate M3-C `qualification` publisher.
  Catalog owns producer admission and snapshots; buildgraph owns exact selection and graph compilation;
  target owns concrete supported target platforms. Each owns its schema, returns typed coded
  errors, and marshals through the canonical codec (ADR-01). `client/v1` composes those owners; `main` must not
  own a competing schema, verifier, receipt constructor, or policy decision. Remaining multi-step command
  orchestration is migration debt tracked independently rather than declared absent.

`testresults` owns output descriptions, wrapper validation and deterministic serialization in lane
**none (descriptive)**. [ADR-16 §6](0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
owns the external-producer boundary; the [output reference](../external-test-output-contract.md)
owns descriptive fields, format pins and limits. This adds no public lifecycle object or receipt. `validation` owns diagnostic profile admission and
check dispatch; `conformance` owns the supported suite/harness protocol, subject observations, capture
and cleanup. Catalog, lifecycle, delivery and held-file owners retain their existing responsibilities.

The standalone query/diff selector workflow is removed; inspect the current lifecycle views directly.

### 5. Same-process authority has an explicit lifecycle

An opaque same-process capability exposes an idempotent `Close` operation that revokes authority across shallow
copies. If it also retains file descriptors, subprocesses, locks, or other resources, `Close` releases them.
Ownership transfers are stated on the returning method. Facade results either close on every failure or transfer
one clear close obligation to their caller. Garbage-collector cleanup may be defense in depth, never the
correctness or resource-reclamation mechanism.

`ArtifactCheckReceipt.WithDiagnosticOperation` registers a bounded callback and defensive description
under the live receipt; it grants no payload access on its own. `WriteExecutableMember` delegates held
member streaming to delivery after exact output/member/platform, publication and build-subject checks.
Export grants byte access, not execution or qualification authority. The diagnostic adapter separately
admits the user's explicit execution profile and owns capture, child processes and cleanup (ADR-18 §3).
Product and tools receipts remain live through that work; closure stays outside the registered callback
and cancels/joins operations before releasing payloads. Serialization restores none of these capabilities.

The public `client/v1.ValidationOptions` currently aliases `lifecycle.QualificationRuntimeOptions`.
This deliberately reuses the existing GitHub token, private workspace and registry credential/transport
options without another authentication contract; it grants no qualification relationship or authority.
The CLI supplies the explicit anonymous-registry default; SDK callers must select anonymous access or
complete host-scoped credentials themselves. The alias exposes future changes to that owner and remains
an explicit SDK coupling, like `LocalRunOptions` below.

The provisional diagnostic methods `ListValidators`, `DescribeValidator` and `PrepareValidatorProfile`
delegate to `validation` for catalog discovery and frozen local-plan admission (ADR-24).
`ValidatorDescription`, `ValidatorInvocation` and `ValidatorRuntime` alias that package's types, so changes
to the provisional catalog/profile surfaces also affect `client/v1`. They provide no authenticated receipt,
artifact membership or qualification authority. The corresponding hidden CLI group is `validators`.

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

The provisional downstream diagnostic `demo.WithSite` delegates to the existing native site owner.
`Config`, `RunInputs` and `Resources` alias its descriptive configuration;
`Site` and `Tenant` alias its opaque callback-scoped handles. This deliberate coupling adds no
public lifecycle object, artifact authentication rule, deployment schema or qualification authority. Every
native admission and cleanup invariant remains with that owner. Callback exit cancels and joins operations;
handles cannot be detached or restored from paths. The adapter requires the existing matching `nvkr` and
`nvke-demo` helper executables and host prerequisites; importability does not imply binary-free execution.
The native CLI default has a distinct retained lifetime: its fixed child owns the
resource stack through an authenticated private control and explicit joined cleanup.
Returning from the presenter or canceling it grants no teardown authority. Successful
return requires the same live owner to acknowledge detachment and completed requested
checks. Linux observes that owner through SO_PEERPIDFD; macOS retained controls bind
to the original self audit token and a generation-validated kqueue registration.
The generic Darwin peer API remains unavailable. Unsupported builds fail before work.
External-module compile and no-work admission tests establish only that boundary. Real native site reuse
and complete conformance remain separately evidenced operational acceptance.

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
