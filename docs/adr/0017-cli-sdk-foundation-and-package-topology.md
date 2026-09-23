# ADR-17 — CLI/SDK foundation, seams & package topology

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-17 in the consolidated NKD ADR set.
- **Relates to:** ADR-03 (exit codes), ADR-18 (runner / ctx), ADR-20 (publish seams), ADR-01 (canonical codec).
- **Satisfies:** FR-21 (standard, replaceable interfaces); supports FR-20 (coded errors — owned by ADR-03).

## Context

NKD is a **thin CLI over a reusable SDK**. It needs an embeddable facade, context/cancellation discipline,
swappable external-transport seams, and a clean package topology, so later work does not re-accrete orchestration
in `main` or double-author types.

## Decision

### 1. A `client/v1` SDK facade; deep modules remain authoritative

The six public objects are **Definition, Environment, Run, Artifact, Release, and Verification Policy**.
The main lifecycle is **Definition → Run → Release**. Bundles are Artifact formats. Deployment topology
belongs to the downstream deployment owner and is not part of the Definition or Release schema. Plans, graphs, inventories, vectors, release
sets, clearance, publication roots, and qualification records are generated/authenticated internals available
through inspection. Normal callers neither construct nor connect their receipt graph.

Definition declares named outputs directly. Each output selects exact catalog components and a concrete target
platform, together with its artifact format. An output may explicitly
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
wrap the QCOW2 as a binary bundle. Local or hosted execution, generated jobs, observation, evidence, and
publication still fail with `engine-capability` before source, subprocess, or output work.

Definition v4, Run/Release v3, and compiled Run Plan/artifact graph v4 records reject the
retired cluster, role, category and composition fields. Signer policy roles remain a distinct security concept.

Environment separately supplies runtime, runner, publication namespace, credential references and signing policy
(ADR-20). Output-affecting tools stay in the build identity they affect.
Changing transport or signing configuration cannot silently change logical Build identity.

`pkg/nkd/definition` owns authored Definition/Environment loading, validation and identity. `factory.Compile`
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
`delivery` owns checked publication membership and canonical release-content identity;
Release consumers do not reconstruct compiler bindings. Existing domain modules retain their
canonical, trust, evidence, status and release invariants. `client/v1` is a context-first facade,
with process configuration in functional options and per-call inputs in method arguments. CLI commands parse
flags, call those use cases, format results and map ADR-03 errors. Neither layer mints authority from fields.

`status.AcceptDiagnosticCheckpoint` owns protected minimum reads, exact document authentication, checkpoint
verification and receipt-derived byte-CAS persistence before interpreting withdrawal. Its caller-supplied epoch
and returned decision remain diagnostic; this operation adds no trusted-time or release authority.
`catalog.UpdateSourcePins` and `UpdateArtifactPins` own selection, resolution, change detection and the existing
lock transaction. Commands map flags, select bounded contexts and present structured change summaries; they do
not own the persisted status schema or reconstruct pin-update transactions.

Normal commands are `definition validate|inspect`, `run plan|local|generate`, and `release verify|inspect`, with
`--definition` and `--environment`; catalog maintenance and exact component/platform diagnostics remain
available. The SDK resolves direct selections into generated graphs and Build Digests. Backward compatibility
is not required. Remove superseded authoring APIs and wire fixtures; retain internal mechanics only when the
current lifecycle uses them. SDK callers obtain an opaque prepared Run and execute/generate it; decoded
JSON cannot restore executable authority. Consumer verification takes one pinned Release reference and explicit
policy (ADR-04), returning inspectable views plus a closeable capability. The reference selects the sole
ADR-20 release index: delivery owns its metadata/descriptor/evidence join, checked named outputs, exact sibling
artifact references, and release content projection. Lifecycle applies consumer policy and assembles the Release
view. Transport locators are excluded from
the content subject and bound by the release metadata. A second published lifecycle-binding object is not retained. An external SDK test proves embedding
without `main`. Low-level command orchestration must serve a current diagnostic or execution use case, not preserve an obsolete workflow.

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
  `execution`, `ir`, `pipeline`, `attest`, `release`, `status`, `provenance`, `sbom`, `vuln`, and `trust`.
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
- Swappable transports keep NKD integrating only via standard interfaces, while missing authority adapters fail
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

Current contract evidence in `client/v1/*_test.go`, `cmd/nkdctl/*_test.go`, `pkg/nkd/pipeline/local*_test.go`,
`pkg/nkd/pipeline/container_lifecycle_test.go`, and
`pkg/nkd/internal/heldfile` covers context-first facade calls, authoritative-module equivalence, injected
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
insufficient. The organization audit records these ownership changes and their validation scope.

The Recipe-removal migration changes public requests, generated graphs and their wire versions. Native and
Linux race tests provide contract evidence; repository checks are recorded in reconciliation.

End-to-end acceptance requires a separate consumer module using only `client/v1` to load inputs, prepare and
execute a Run, and verify its published Release under explicit policy, plus cancellation tests against every
real subprocess and network adapter proving no child, container, temporary output, or partial publication remains.
Qualified diagnostic commands remain explicitly scoped consumers of their owning modules. They do not constitute
an alternative supported publication workflow, and current command orchestration is not described as fully thin.
