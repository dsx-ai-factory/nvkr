# ADR-21 — Generated-pipeline maintenance & lifecycle

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-21 in the consolidated NKD ADR set (added to close the day-2 pipeline-maintenance gap).
- **Relates to:** ADR-18 (what is emitted / the runner), ADR-11 (merge algebra reused for extensions), ADR-07
  (runner/profile digest, re-sign), ADR-22 (the pin/lock contract a bump edits), ADR-01 (canonical codec +
  digest provenance), ADR-17 (CLI verbs), ADR-20 (publish phase).
- **Satisfies:** FR-05 (CI portability), FR-21 (standard, replaceable interfaces); supports FR-20 (coded
  errors — the `pipeline-drift` code registers in ADR-03).

## Context

ADR-18 fixes *what* an emitted pipeline is: one IR, provider adapters, and a from-source `nkd-exec` executing
each node. It does not fix the **lifecycle of those files after they land in an operator's repository**. That is
the day-2 story: a generated pipeline goes stale when pins move, someone hand-edits it, the operator wants a
newer tool, or the operator needs an org-specific step. A stress-test of that journey found no drift gate, no
sanctioned customization seam, no home for the runner/tool upgrade, and an ambiguous owner for "the CI files."
This ADR fixes maintenance.

## Decision

### 1. The emitted pipeline is a generated artifact, not an owned scaffold

`run generate` is the normal owner of committed workflows, plans, graphs, compositions and manifests. Every
owned file regenerates byte-for-byte from exact Definition, Environment, catalog pins, generator version and
engine. Drift checks repeat the recorded Definition and Environment inputs without substituting a different
catalog, path or engine. The former factory catalog and its indexed-selection workflow are removed.

Execution identity and transaction identity are separate. The execution-bundle identity binds engine, Recipe/
plan identities, canonical nodes, transport plan and rendered provider body, excluding its own provenance header.
The commit manifest lists each owned file's safe relative path, media type, mode and exact whole-file digest,
plus execution identity. It is written last and never lists itself. No self-referential identity is permitted.

Owned payloads are generated provider/configuration files and canonical graph/plan records. Catalogs, profiles,
vectors, roots and credentials remain external inputs whose exact references/policies are recorded, not copied
and silently claimed as generator-owned. Future artifact/result digests belong in runtime observations; the
generator must not invent them. Changes to input mode, environment or output graph regenerate in one reviewed
change. Generated files are not editable scaffolds.

### 2. Drift detection is a fail-closed `--check` twin, run by a separate generated gate

`run generate --check` re-emits in memory using the recorded Definition/Environment selection and compares
against committed files. A mismatch fails closed with a distinct coded error (`pipeline-drift`, ADR-03) naming
missing, extra or divergent owned files. Execution admission verifies the commit manifest before consuming its
graph. The dedicated generated Run directory must contain only expected payloads, its manifest and the
persistent lock inode; unexpected files or directories and symlinks fail the check. Handwritten workflows
outside that directory remain operator-owned. The frozen qualified `bundle --check` adapter applies its own manifest and authority rules.

The deferred qualified `bundle --meta-lint` protocol emits a separate generated gate that checks the build
bundle. Its checker stays outside the object it authenticates, avoiding recursive identity. This is distinct
from the implemented v1 regeneration check and does not claim a current provider meta-lint adapter.

Committed bundle members and `exec-node`'s canonical graph are opened as bounded, nonblocking, no-follow regular
file capabilities and revalidated against their live pathname after reading. A symlink, FIFO, oversized member,
or inode substitution is drift, never an input to hashing or execution.

The provider-specific meta-lint and extension protocols below remain target-state decisions. The current CLI
rejects both before catalog or output I/O until their authenticated input-transport adapters exist; it emits no
editable placeholder workflows.

- `bundle --check --json` **classifies the divergence**: `stale-pins` (the lock moved; remedy: `catalog bump`
  then `bundle`) vs `hand-edit` (remedy: `bundle` to regenerate). Same exit family, different remedy — the
  operator is told which.
- The meta-CI lint job lives **outside** the emitted build pipeline, is committed as a distinct transaction
  member/generation, and must not self-check recursively; `nkd-exec exec-node` remains the only in-pipeline
  executor (ADR-18).

### 3. Churn automation: bump → regenerate → PR, gated by drift

A pin or catalog change moves the IR, which moves the emitted pipeline. The two travel together:

- `catalog bump [--pins|--artifacts|--all] --check` previews the lockfile diff without writing (ADR-22).
- `catalog bump --regen` (or a `bundle` invocation immediately after) regenerates the pipeline in the **same
  change** as the lock edit. The drift gate (§2) makes a lock change **unmergeable without the matching pipeline
  regen**, so "forgot to regenerate" is structurally impossible to merge.
- A scheduled job runs bump → regeneration → build → verification → PR. Qualified changes require reproduction; v1 production uses its explicit evidence contract. Merge to the default branch is the v1 release decision (ADR-16), without a reproduction claim. Scheduled automation and release-manifest issuance remain deferred. Regeneration is a reviewable diff.


GitHub action pins are maintained by `nkdctl actions bump`. An explicit `--track NAME=vN` selects a new
major release line; ordinary bumps stay on each recorded line. Unknown actions, malformed or duplicate
overrides, and overrides excluded by `--only` fail before resolution. Track-only changes still update the
machine-managed lock even when the resolved commit is unchanged. After a bump, rebuild the generator and
regenerate the committed Run workflow in the same change. Handwritten repository CI pins are reviewed
alongside the shared lock. Native action runtime declarations and transport behavior require external checks;
a commit pin alone does not establish compatibility.

### 4. Deferred customization: extension overlay and callable form

The generator-is-canonical rule (§1) needs a sanctioned seam for "add a Slack notify" or "add an approval gate".
There are **two**, and an operator may use either or both:

- **Declared extension overlay.** The `bundle` config declares named hook points (`pre-build`, `post-verify`,
  `pre-publish`) whose contents the operator supplies; `bundle` **merges them deterministically using ADR-11's
  merge algebra**, so extensions are part of `bundleDigest` and stay drift-checkable. In-pipeline steps that must
  be gated or audited belong here.
- **Emit-callable form.** `bundle --emit-callable` emits the pipeline as a reusable unit (reusable workflow /
  `include:` / composed Pipeline) so an operator's **outer, operator-owned** workflow can invoke the pristine
  generated pipeline and wrap it with org logic, leaving the generated file untouched.

Editing the generated file directly remains out of contract; the drift gate rejects it. These are proposed
alternatives, unavailable at the current CLI boundary until their admission and transport contracts exist.

### 5. Runner/bootstrap self-upgrade is a profile change, not a catalog change

Helper/runtime upgrades belong to Environment and, for qualified execution, the signed builder profile.
Component toolchains and output-affecting inputs belong to Recipe/catalog identity. Neither owner can override
the other's pins. From-source helper bootstrap measures the actual helper and bootstrap toolchain; a preexisting
helper must not claim a bootstrap that did not participate (ADR-07/18).

For the qualified source-bootstrap path, `profile repin --runner|--bootstrap-toolchain` writes new expected pins
and re-signs; regeneration accompanies the change and actual execution must reproduce the expected helper and
pass stage two. A pinned helper image is an alternative transport, not automatic qualified admission. Its
qualified path remains unavailable until the exact executable/runtime observation contract is implemented.
The legacy `--toolchain` spelling cannot alter component Recipe pins.

### 6. Concurrency: Git serializes branches; local writers use atomic compare-and-swap

A bump is a pure function of immutable inputs producing a staged lock/bundle change (ADR-22), but Git serializes
only actors operating in **separate branches or worktrees**. Two processes writing the same checkout can both
read the old lock, and one update is lost before Git observes anything.

Every mutating command therefore:

1. records the original digest/version of each input and destination;
2. writes and fsyncs complete replacements in same-filesystem temporary paths;
3. acquires a short-lived **per-target commit lock** through a persistent owner-only regular lock inode and a
   kernel advisory lock, then rechecks all original digests while holding it and returns a distinct coded
   `write-conflict` if any changed; and
4. atomically renames outputs, writing the bundle manifest/transaction marker last, fsyncs the directory, and
   only then releases the kernel lock.

The lock is scoped to one lock or bundle root and is held only for the local commit, never during network
resolution or builds. Acquisition uses non-blocking kernel attempts plus context-aware retry, never an
uninterruptible wait. The owner-only inode is opened without following symlinks and is intentionally never
removed: process death releases kernel ownership automatically, and the unlocked inode and any stale PID metadata
remain safely reusable. File contents are diagnostic only and never establish ownership. Platforms without
equivalent kernel-lock semantics must fail closed. This closes the compare-then-rename race without an `O_EXCL`
sentinel whose crash recovery would require unsafe stale-owner guessing or operator deletion.

The lockfile is a single-file atomic compare-and-swap. A multi-file bundle is a crash-consistent transaction: all
files are staged first, all destinations pass CAS, payloads are replaced, and the manifest is replaced last. A
crash can leave a detectable incomplete generation, but cannot leave a new valid manifest authenticating old or
partial payloads. Recovery is deterministic regeneration. Commands never truncate a previously valid target
before verification succeeds.

NKD requires no daemon and no global distributed lock. Scheduled automation uses a dedicated branch or worktree;
Git PR merge serializes separate actors, while the local commit lock plus CAS prevents same-checkout lost
updates. This distinction is part of the interface, not an implementation detail.

### 7. Single-owner rule for "the CI files"

`run generate` is the normal owner of the emitted build pipeline; the legacy `bundle` adapter retains its own frozen format. `catalog bump` may regenerate only the *pin-bot*
workflow — the scheduled bump job — never the build pipeline. Conflating two "CI" artifacts under one verb is
disallowed; that ambiguity is what makes forgotten-regen drift possible.

## Consequences

**Positive**
- The committed pipeline provably corresponds to a specific IR, and staleness or hand-edits self-detect in CI.
- Bump and regen travel together; forgotten-regen drift is unmergeable.
- Operators get two sanctioned customization paths that both survive regeneration.
- The tool/runner upgrade has a home that preserves the signed-profile boundary.

**Negative / costs**
- The extension-overlay merge is new surface that must stay deterministic and drift-checkable.
- The meta-CI lint job is an extra job on every run — cheap, but present.
- Operators must learn that the generated file is off-limits and use the overlay or emit-callable form instead.

**Neutral**
- The `pipeline-drift` code and its classification are surfaced through ADR-03's coded-error table.
- The pin/lock mechanics a bump operates on are owned by ADR-22; this ADR consumes them.

## Alternatives considered

- **One-time scaffold the operator then owns.** Rejected — silent drift breaks "the pipeline builds this
  recipe"; the byte-identity claim evaporates.
- **Route the runner/bootstrap bump through `catalog bump`.** Rejected — it puts environment identity in the
  definition and skips the required profile re-sign (§5).
- **Overlay OR emit-callable, pick one.** Rejected — in-pipeline gated steps need the drift-checkable overlay
  and org-owned wrapping needs the callable form; they serve different needs.
- **Rely on Git for concurrent writers in one checkout.** Rejected — Git sees only the last filesystem state and
  cannot detect a pre-commit lost update. Local optimistic CAS is required; a daemon or global lock is still
  unnecessary (§6).
- **Digest recheck without a commit lock.** Rejected — two writers can both pass the check before either rename.
  The narrowly scoped kernel lock makes check-and-commit atomic without introducing a service.

## De-risking spikes

1. **Deterministic extension merge** — an overlay-augmented `bundle` reproduces byte-identically and
   `bundle --check` still detects drift over the merged result.
2. **Drift classification** — `bundle --check --json` reliably distinguishes `stale-pins` from `hand-edit`.
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

Current contract evidence in `pkg/nkd/pipeline/emitters_test.go` and `cmd/nkdctl/bundle_test.go` covers
deterministic bundle identity, graph/body substitution detection, extension rendering, fail-before-output source
capability gates, and CLI rejection when a provider does not implement `--extensions` or `--meta-lint`. Focused
bundle tests also prove that an unlocked crash-stale lock inode is reusable, an active cross-process owner blocks
until context cancellation, cancellation publishes no member and removes staged files, and the same inode works
after the owner exits. There is no end-to-end acceptance evidence for a provider-executed bundle transaction or
the full same-checkout kill-point/concurrency matrix.

Acceptance requires a golden bundle containing every owned generated file; edits to `nodes.json`, the transport
plan, provider body, rendered extensions, callable form, and a separately emitted meta-lint bundle must each fail
`bundle --check` and identify the path. Substitution of an external expected-digest, profile, or trust input must
fail at its authenticated consumer boundary rather than being misclassified as generator drift. A process-race
test must start two different lock updates from one checkout and prove no lost update. Kill-point tests around
stage/fsync/CAS/rename/manifest commit must prove an interrupted write is recoverable and never appears valid.
Finally, two worktree branches updating the same lock must demonstrate the documented Git merge-conflict path.
The CLI contract test must also prove the legacy `--toolchain` option cannot be mistaken for a component
toolchain update.
