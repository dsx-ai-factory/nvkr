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

The normal public objects are **Definition, Recipe, Environment, Run, Artifact, Release, and Verification
Policy**. The main lifecycle is **Definition → Run → Release**. A cluster is a named view inside Definition and
its exact realization inside Release; bundles are Artifact formats. Plans, graphs, inventories, vectors, release
sets, clearance, publication roots, and qualification records are generated/authenticated internals available
through inspection. Normal callers neither construct nor connect their receipt graph.

Definition declares reusable recipe selections and named outputs once. Each output selects exact components and
format; the compiler derives routine package envelopes, required slots, and routes. Byte-changing transformations
remain explicit. Ambiguous/duplicate selections, missing components, conflicting roles, and unsupported formats
fail with coded errors. Existing catalog criteria remain explicit; recipe reuse does not require general product
imports. Definitions are selected directly by file.

Environment separately supplies runtime, runner, destinations, credential references and signing utility pins
(ADR-20). Output-affecting tools such as the AICR generator stay in the Recipe/composition identity they affect.
Changing transport or signing configuration cannot silently change logical Recipe identity.

`pkg/nkd/lifecycle` owns preparation, required/observed joins, Run persistence, generated release relationships,
and consumer workflow. `factory` owns compilation/execution mechanics; existing domain modules retain their
canonical, trust, evidence, status, release and transport invariants. `client/v1` is a context-first facade,
with process configuration in functional options and per-call inputs in method arguments. CLI commands parse
flags, call those use cases, format results and map ADR-03 errors. Neither layer mints authority from fields.

Normal commands are `definition validate|inspect`, `run plan|local|generate`, and `release verify|inspect`, with
`--definition` and `--environment`; catalog and Recipe maintenance remain available. Backward compatibility is not required. Remove superseded authoring APIs and wire fixtures; retain
internal mechanics only when the current lifecycle uses them. SDK callers obtain an opaque prepared Run and execute/generate it; decoded
JSON cannot restore executable authority. Consumer verification takes one pinned Release reference and explicit
policy (ADR-04), returning inspectable views plus a closeable capability. An external SDK test proves embedding
without `main`. Low-level command orchestration must serve a current diagnostic or execution use case, not preserve an obsolete workflow.

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

Finite generated workflows follow a value-returning interface: `pipeline.Emitter.Render(ctx, graph, digest)`
returns the complete bytes after cancellation-aware rendering. It does not accept an arbitrary `io.Writer`,
because a callback that blocks inside `Write` makes cancellation unenforceable. The writer-shaped `Emit` and
`WriteGeneratedPipeline` entry points are fail-before-output migration tombstones.

### 3. Standard, replaceable external-interface seams

Operator-swappable seams — **`OCIRegistry` (push + referrers)**, **`PackageRepo` (publish)**, and **`ObjectStore`
(put/get)** — are injected via facade options and used by the publish phase (ADR-20). No seam references a
vendor-internal package. Diagnostic and read-only conveniences may have local defaults, but an absent
authority-bearing verifier, evidence source, trusted-time source, or publisher **fails closed before I/O**. There
is no permissive production default that can manufacture authority.

### 4. Package topology and single-owner rule

- A documented **single-owner package map** — `canonical`, `ir`, `recipe`, `pipeline`, `attest`, `release`,
  `status`, `provenance`, `sbom`, `vuln`, and `trust`. Each owns its schema, returns typed coded
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

### 6. One Run is one immutable attempt

Preparation freezes admitted Definition/Environment inputs and required slots before execution or reservation
of attempt storage. Execution binds independently observed bytes and evidence; missing/conflicting/extra members
fail completeness (ADR-05). A failed Run retains its original requirements and coded failure. Retry requires a
new explicit attempt and cannot overwrite the previous observation. A completed descriptive Run is not clearance.

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

Current contract evidence in `client/v1/*_test.go`, `cmd/nkdctl/*_test.go`, `pkg/nkd/pipeline/local_test.go`, and
`pkg/nkd/internal/heldfile` covers context-first facade calls, authoritative-module equivalence, injected
I/O/runner/publication seams, local cancellation propagation, an explicitly stopped and joined container-reaper
waiter, bounded lock-free cleanup I/O, held-child SIGTERM→SIGKILL escalation, atomic output, exact-byte
ValidationVector and profile receipts, rejection of unauthenticated inputs, and absence of implicit publication I/O. Public
diagnostic values cannot be promoted into authority by the facade. Qualified report and ValidationVector result
lifetimes are explicitly closeable, and tests prove that closing them invalidates their retained same-process
authority. Direct runner entry points also reject a nil `RunOptions.Ctx` before inspecting their graph, node, or
results path. The stock measure-only verifier rejects a missing producer-results source before trust, profile, or
catalog I/O; the independent disk-envelope decode lane does not require producer results. Catalog and SDK tests
additionally prove pre-canceled/no-I/O admission, legacy-provider rejection, no-follow regular-file reads,
file/aggregate byte ceilings, deterministic directory count/depth ceilings, and context-aware lock
snapshot/update behavior.

End-to-end acceptance requires a separate consumer module using only `client/v1` to perform the complete
authenticated resolve/results/evidence/release/publication flow, plus cancellation tests against every real
subprocess and network adapter proving no child, container, temp output, or partial publication remains. The
remaining authenticated verify/attest/bundle/status/release command orchestration must also move behind facade
use cases, or be justified as presentation-only, before the CLI can be called thin.
