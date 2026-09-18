# ADR-21 — Generated-pipeline maintenance & lifecycle

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-21 in the consolidated NKD ADR set (added to close the day-2 pipeline-maintenance gap).
- **Relates to:** ADR-18 (what is emitted / the runner), ADR-11 (catalog producer overlays), ADR-07
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

`run generate` is the normal owner of committed workflows, plans, graphs and manifests. Every
owned file regenerates byte-for-byte from exact Definition, Environment, catalog pins, generator version and
engine. Drift checks repeat the recorded Definition and Environment inputs without substituting a different
catalog, path or engine. Definition v4 outputs select exact producers and a concrete platform directly;
generated plans and graph paths are keyed by output name. The former recipe selection and catalog criteria
workflow are removed. Cluster-composition files and nodes are also removed; regeneration retires former
owned members through the same prior-manifest checks described below. Changed graph/plan and lifecycle records use their new schema majors; old records
cannot be relabeled into current identities.

Execution identity and transaction identity are separate. The execution-bundle identity binds engine, build/
plan identities, canonical nodes, transport plan and rendered provider body, excluding its own provenance header.
The commit manifest lists each owned file's safe relative path, mode and exact whole-file digest,
plus plan and workflow identities. It is written last and never lists itself. No self-referential identity is permitted.

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


GitHub actions are catalog sources with `kind: github-action`. The registry owns the GitHub repository
and semver constraint; `catalog pin` and `catalog bump` write exact release commits into the source lock.
The resolved catalog snapshot and generated Run bind those pins. Regenerate after bumping; the generator
binary does not need rebuilding. Both GitHub adapters require their supported repositories and major versions
before emitting bytes. Missing pins, mutable refs, and unsupported interfaces fail closed. Handwritten
repository CI has its own reviewed action pins; it is not part of the generated product Run.
Native action runtime declarations and transport behavior require external checks; a commit pin alone does
not establish compatibility.

### 4. Deferred customization: extension overlay and callable form

The generator-is-canonical rule (§1) needs a sanctioned seam for "add a Slack notify" or "add an approval gate".
There are **two**, and an operator may use either or both:

- **Declared extension overlay.** The `bundle` config declares named hook points (`pre-build`, `post-verify`,
  `pre-publish`) whose contents the operator supplies; `bundle` must compose them under a separately specified deterministic hook contract, so extensions are part of `bundleDigest` and stay drift-checkable. In-pipeline steps that must
  be gated or audited belong here.
- **Emit-callable form.** `bundle --emit-callable` emits the pipeline as a reusable unit (reusable workflow /
  `include:` / composed Pipeline) so an operator's **outer, operator-owned** workflow can invoke the pristine
  generated pipeline and wrap it with org logic, leaving the generated file untouched.

These deferred pipeline hooks do not restore removed recipe layers or mixins. Catalog overlay v3 remains
a separate whole-producer replacement operation.

Editing the generated file directly remains out of contract; the drift gate rejects it. These are proposed
alternatives, unavailable at the current CLI boundary until their admission and transport contracts exist.

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

Current generated Run contract evidence in `pkg/nkd/factory/bundle_io_test.go` covers idempotent generation,
byte-exact drift checks, unchanged obsolete-member retirement, empty-directory cleanup, and preservation of
modified or unlisted files. These tests do not establish a complete crash/kill-point matrix.

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
