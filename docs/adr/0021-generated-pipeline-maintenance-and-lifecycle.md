# ADR-21 — Generated-pipeline maintenance & lifecycle

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-21 in the consolidated NVKR ADR set (added to close the day-2 pipeline-maintenance gap).
- **Relates to:** ADR-18 (what is emitted / the runner), ADR-11 (catalog producer overlays), ADR-07
  (runner/profile digest, re-sign), ADR-22 (the pin/lock contract a bump edits), ADR-01 (canonical codec +
  digest provenance), ADR-17 (CLI verbs), ADR-20 (publish phase).
- **Satisfies:** FR-05 (CI portability), FR-21 (standard, replaceable interfaces); supports FR-20 (coded
  errors — the `pipeline-drift` code registers in ADR-03).

## Context

ADR-18 fixes *what* an emitted pipeline is: one IR, provider adapters, and a from-source `nvkr-exec` executing
each node. It does not fix the **lifecycle of those files after they land in an operator's repository**. That is
the day-2 story: a generated pipeline goes stale when pins move, someone hand-edits it, the operator wants a
newer tool, or the operator needs an org-specific step. A stress-test of that journey found no drift gate, no
sanctioned customization seam, no home for the runner/tool upgrade, and an ambiguous owner for "the CI files."
This ADR fixes maintenance.

## Decision

### 1. The emitted pipeline is a generated artifact, not an owned scaffold

`run generate` is the normal owner of committed workflows, generated local actions and invocation data, complete plans and
manifests. Every owned file regenerates byte-for-byte from exact Definition, Environment, catalog pins, generator version and
engine. Drift checks repeat the recorded Definition and Environment inputs without substituting a different
catalog, path or engine. Definition v5 outputs select exact producers directly, with either a native `platform`
or an OCI aggregate `platforms` set. The v5 Plan/artifact graph embeds one producer graph per native build target: a native
output has one target keyed by output name, while an aggregate derives a target for each required platform
and one image-index packaging node that joins every member's exact result and evidence. Run/Release v4
records retain the required aggregate output and its native membership. Separate per-build graph files are retired through
the previous manifest; hosted operations execute the exact complete Plan. The former recipe selection and catalog criteria
workflow are removed. Cluster-composition files and nodes are also removed; regeneration retires former
owned members through the same prior-manifest checks described below. Changed graph/plan and lifecycle records use their new schema majors; old records
cannot be relabeled into current identities.

Execution identity and transaction identity are separate. The Plan binds authored inputs and every executable
operation; the workflow has its own exact-byte digest. The commit manifest lists each owned file's safe relative path, mode and exact whole-file digest,
plus plan and workflow identities. It is written last and never lists itself. No self-referential identity is permitted.

Owned payloads are generated provider/configuration files and canonical graph/plan records. Catalogs, profiles,
vectors, roots and credentials remain external inputs whose exact references/policies are recorded, not copied
and silently claimed as generator-owned. Future artifact/result digests belong in runtime observations; the
generator must not invent them. Changes to input mode, environment or output graph regenerate in one reviewed
change. Generated files are not editable scaffolds.

The GitHub bundle includes shared composite templates beneath `.github/nvkr-run/actions/` and canonical
invocation values in `.github/nvkr-run/action-data.json`. The generator selects fixed semantic interfaces for
execution, preparation, collection and transfer. Grouped jobs repeat those calls; declared optional slots
and further individual transfer calls handle differing output counts without shape-specific or numbered
variant actions. Invocation values use named operation roles rather than step positions. These interfaces
lower Plan-derived steps without introducing another executable Plan or authored extension surface.
The helper-preparation, execution, bootstrap and controlled-transfer
families and their invocation-data member appear in the same commit manifest as the top-level workflow and
Plan. Their exact bytes, safe relative paths and modes participate in regeneration, drift
checks and prior-manifest retirement. The workflow digest still describes the top-level workflow bytes;
it is not a substitute for validating the complete manifest-owned action closure.

The entry workflow references the fixed Plan and manifest paths without repeating the Plan digest. Its
provider source-fetch IDs and scheduling order describe the target and actual consumer cohort rather than
changing source pins. Exact executable identities remain in the Plan and generated invocation data.
Templates contain fixed helper operations and pinned provider actions; the runtime descriptor operation
admits the Plan and regenerates the selected values rather than trusting the checked-in data as authority.
Routine pin or Plan changes can therefore leave the workflow bytes unchanged while the complete bundle
and ownership manifest change. This stability is conditional on unchanged topology, labels, action
interfaces and visible execution policy; checkout pin and inline operator-setup changes remain workflow
changes. ADR-18 §8 defines the full boundary. Every input update still requires complete regeneration and
the same all-member drift check; an unchanged workflow file does not establish an up-to-date bundle.

### 2. Drift detection is a fail-closed `--check` twin, run by a separate generated gate

`run generate --check` re-emits in memory using the recorded Definition/Environment selection and compares
against committed files. A mismatch fails closed with a distinct coded error (`pipeline-drift`, ADR-03) naming
missing, extra or divergent owned files. Execution admission verifies the commit manifest before consuming its
graph. The dedicated generated Run directory must contain only expected payloads, its manifest and the
persistent lock inode; unexpected files or directories and symlinks fail the check. Handwritten workflows
outside that directory remain operator-owned. The unused qualified `bundle` command, bundle manifest and
transport-plan writer are removed; no parallel generated format is retained.

Committed bundle members and `exec-node`'s diagnostic canonical graph are opened as bounded, nonblocking,
no-follow regular file capabilities and revalidated against their live pathname after reading. A symlink, FIFO,
oversized member or inode substitution fails closed before hashing or execution.

A separately generated meta-lint workflow and automatic semantic explanation inside `--check` remain deferred.
The maintained repository gate invokes `run generate --check` directly. Read-only `run diff` separately compares
two fully validated canonical Plans: it reports changed named Build identities, classifies every changed field,
keys named collections without array-shift noise, and preserves command/patch order. Concise text counts all
changes; detailed text and descriptive JSON retain the full comparison. `run graph` exports complete linked
producer/artifact graphs, and `run plan --pretty` emits a display-only view. These views remain outside the
generator-owned directory and do not change stored canonical bytes, Plan identity, or the byte-exact drift gate.

### 3. Churn automation: bump → regenerate → PR, gated by drift

A pin or catalog change moves the IR, which moves the emitted pipeline. The two travel together:

- `catalog bump [--all] --check` previews the lockfile diff without writing (ADR-22).
- `run generate` after `catalog bump` regenerates the pipeline in the **same
  change** as the lock edit. The drift gate (§2) makes a lock change **unmergeable without the matching pipeline
  regen**, so "forgot to regenerate" is structurally impossible to merge.
- `catalogmaintenance` owns an explicitly configured review-candidate refresh: check the existing bundle,
  atomically bump selected independent roots and their declared pin sets, regenerate, and repeat the drift
  check. No-op preserves bytes. `--check` previews only the complete pin transaction, without writes or a
  claim that candidate production was exercised. Locks and generated files retain separate transactions;
  failure after pin commit leaves a disposable checkout for diagnosis, never a publishable candidate.
  Recovery context preserves the generating owner's coded cause, including retryable cancellation and
  non-retryable generated-file drift during either generation pass.
- `catalog update-ci` renders a separate GitHub workflow with optional reviewed scheduling, default-branch
  admission, exact base checkout, bounded non-canceling concurrency and read-only repository permissions.
  Credentials are passed only to the fixed private-source checkout, not resolution or producer execution.
  Its Git prefix override rejects distinct repository URL collisions before workflow output or pin mutation;
  admission covers selected maintenance source closures, every revalidated catalog pin set and compiled
  fetches. Repeated roles and ordinary `.git`/trailing-slash aliases for the same repository remain supported.
  Changed candidates pass the repository gate and deterministic regeneration; `local-run` additionally
  requires execution and independent observation of every required output. Unsupported capabilities fail
  without reducing the Definition. Static mode emits only a review patch. Automatic review-branch/PR
  publication remains capability-gated, and no scheduled external acceptance is claimed.
- Local candidate verification is diagnostic, not qualified reproduction or hosted release acceptance.
  Qualified changes require reproduction; v1 production uses its explicit evidence contract. Merge to the
  default branch is the v1 release decision (ADR-16), without a reproduction claim. Release-manifest issuance
  remains deferred. Regeneration is a reviewable diff.


GitHub actions are catalog sources with `kind: github-action`. The registry owns the GitHub repository
and semver constraint; `catalog pin` and `catalog bump` write exact release commits into the source lock.
The resolved catalog snapshot and generated Run bind those pins. Regenerate after bumping; the generator
binary does not need rebuilding. The GitHub adapter requires its supported repositories and major versions
before emitting bytes. Missing pins, mutable refs, and unsupported interfaces fail closed. Handwritten
repository CI has its own reviewed action pins; it is not part of the generated product Run.
Native action runtime declarations and transport behavior require external checks; a commit pin alone does
not establish compatibility.

### 4. Deferred customization: extension overlay and callable form

Generated shared composite templates and invocation data are internal lowering artifacts owned by the existing
generator. They accept only generated input bindings and do not admit operator-supplied actions, arbitrary commands or
replacement job policies. Their callers retain visible topology, permissions and credential references;
validation checks the expanded steps and exact bytes of every generated member (ADR-18 §8).

Authored extensions and reusable callable workflows remain deferred design options. They have no current command,
renderer or injected extension implementation. Introducing either requires explicit input ownership,
deterministic identity, credential admission and transport contracts before implementation. They cannot restore
removed Recipe layers or mixins; catalog overlay v3 remains whole-producer replacement.
Matrix scheduling likewise needs an explicit dependency and complete provider-result contract before
admission; it is not enabled by extracting existing steps into generated actions.

Editing generated files directly remains out of contract. Current pipeline changes belong in the Definition,
Environment or owning compiler/renderer and pass the same byte-exact regeneration gate.

### 5. Runner/bootstrap self-upgrade is a profile change, not a catalog change

Helper/runtime upgrades belong to Environment and, for qualified execution, the signed builder profile.
Component toolchains and output-affecting inputs belong to producer/catalog identity. Neither owner can override
the other's pins. From-source helper bootstrap measures the actual helper and bootstrap toolchain; a preexisting
helper must not claim a bootstrap that did not participate (ADR-07/18).

For the qualified source-bootstrap path, `profile repin --runner|--bootstrap-toolchain` writes new expected pins
and re-signs; regeneration accompanies the change and actual execution must reproduce the expected helper and
pass stage two. A pinned helper image is an alternative transport, not automatic qualified admission. Its
qualified path remains unavailable until the exact executable/runtime observation contract is implemented.
The legacy `--toolchain` spelling cannot alter component producer pins.

### 6. Serialized local writes and exact retirement of generated members

Catalog lock writes use original-byte compare-and-swap, a persistent kernel lock inode, same-filesystem
staging, fsync and atomic replacement (ADR-22). Generated Run writes acquire their per-bundle kernel lock,
stage complete files and fsync them, publish payloads, retire obsolete members, and replace the manifest last.
The current generated Run writer serializes same-checkout writers; it does not promise an optimistic CAS
conflict for two separately compiled bundles.

Retirement reads the previous generated-run/v1 manifest as a bounded ownership description. It validates
its schema, engine, workflow path, sorted member paths, digests and modes. A member may be retired only
inside the generator-owned directory and only when it is a regular file whose current bytes and mode still
match that manifest. Missing obsolete members are tolerated for retry after an interrupted retirement.
Symlinks, changed obsolete members and unlisted directory members fail closed before payload publication.
A missing prior manifest grants no ownership over unexpected files. Empty directories made obsolete by
retired members are removed nonrecursively; unknown files are never swept by a recursive cleanup.

Payload replacement and retirement are not atomic across the directory. The writer rechecks retired bytes
before removal, writes the new manifest last, and fsyncs generated directories. A process crash may leave
mixed payloads or incomplete retirement that fails the byte-exact check; regeneration is the recovery path
when the prior ownership record remains available. The writer does not claim a crash-wide transaction or
protection from an arbitrary same-user editor outside its kernel lock. If an older generator already replaced
the ownership manifest while leaving old files behind, recover the authentic prior manifest from version
control before regeneration; the current writer does not guess which unlisted files it may delete.

The persistent owner-only lock inode is opened without following symlinks and is never removed. Process death
releases kernel ownership automatically. Acquisition uses nonblocking attempts with context-aware retries;
unsupported lock platforms fail closed. Staging honors cancellation; after publication begins the small
commit finishes without voluntary cancellation. No daemon or distributed lock is required. Separate branches
and worktrees retain Git's ordinary review and merge-conflict behavior.

### 7. Single-owner rule for "the CI files"

`run generate` is the sole owner of the emitted build pipeline; the legacy `bundle` adapter is removed. `catalog bump` may regenerate only the *pin-bot*
workflow — the scheduled bump job — never the build pipeline. Conflating two "CI" artifacts under one verb is
disallowed; that ambiguity is what makes forgotten-regen drift possible.

## Consequences

**Positive**
- The committed pipeline provably corresponds to a specific IR, and staleness or hand-edits self-detect in CI.
- Bump and regen travel together; forgotten-regen drift is unmergeable.
- Future customization must preserve exact regeneration before it is admitted.
- The tool/runner upgrade has a home that preserves the signed-profile boundary.

**Negative / costs**
- A future extension overlay or meta-lint workflow would add an admission and maintenance contract.
- Generated files are off-limits to direct edits; current changes go through authored inputs or the renderer.

**Neutral**
- The `pipeline-drift` code and its classification are surfaced through ADR-03's coded-error table.
- The pin/lock mechanics a bump operates on are owned by ADR-22; this ADR consumes them.

## Alternatives considered

- **One-time scaffold the operator then owns.** Rejected — silent drift breaks "the pipeline builds these
  selected producers"; the byte-identity claim evaporates.
- **Route the runner/bootstrap bump through `catalog bump`.** Rejected — it puts environment identity in the
  definition and skips the required profile re-sign (§5).
- **Overlay OR emit-callable, pick one.** Rejected — in-pipeline gated steps need the drift-checkable overlay
  and org-owned wrapping needs the callable form; they serve different needs.
- **Rely on Git for concurrent writers in one checkout.** Rejected — Git sees only the last filesystem state and
  cannot detect a pre-commit lost update. Catalog lock CAS and bundle serialization are required; a daemon or global lock is still
  unnecessary (§6).
- **Digest recheck without a commit lock.** Rejected — two writers can both pass the check before either rename.
  The narrowly scoped kernel lock makes check-and-commit atomic without introducing a service.

## De-risking spikes

1. **Deferred extension contract** — any future extension must reproduce byte-identically and remain covered
   by the generation drift check.
2. **Deferred drift classification** — distinguish moved pins from hand edits without weakening exact comparison.
3. **Concurrent writer/crash matrix** — same-checkout racers produce one success plus one coded conflict, and
   interruption at every commit step leaves either the prior valid generation or detectable drift.

## Follow-ups

- **ADR-03:** register `pipeline-drift` in the coded-error table.
- **ADR-03:** allocate and freeze `write-conflict` before the CAS interface is published.
- **ADR-22:** the lockfile diff a bump produces and the `catalog bump --check` dry-run.
- **ADR-07:** `profile repin --runner|--bootstrap-toolchain` writes and re-signs the corresponding environment
  identity attribute.
- **ADR-17:** migrate the legacy ambiguous `profile repin --toolchain` spelling to `--bootstrap-toolchain` with
  an explicit compatibility/deprecation path; it must never alter component T1 pins.
- **ADR-18:** the emitted pipeline whose lifecycle this governs.

## Acceptance evidence

Current generated Run contract evidence in `pkg/nvkr/factory/bundle_io_test.go` covers idempotent generation,
byte-exact drift checks, unchanged obsolete-member retirement, empty-directory cleanup, and preservation of
modified or unlisted files. These tests do not establish a complete crash/kill-point matrix.

Retired qualified renderer and bundle-transaction tests were removed with their unused implementation. The
maintained Run writer keeps its ownership, retirement, cancellation and drift tests. Retired-engine admission
is checked before authored-input or output I/O. No new provider-executed or qualified acceptance is claimed.

Acceptance requires every owned generated file to participate in the byte-exact check. Substitution of external
profile, evidence or trust inputs must fail at the owning authenticated consumer. A complete process-race and
kill-point matrix for stage/fsync/rename/manifest commit remains deferred; it must prove interruption cannot
appear valid. Future extensions, callable workflows and meta-lint generation require their own evidence before
admission. Two worktree branches updating the same lock must preserve the ordinary Git merge-conflict path.
The CLI contract test must also prove the legacy `--toolchain` option cannot be mistaken for a component
toolchain update.
