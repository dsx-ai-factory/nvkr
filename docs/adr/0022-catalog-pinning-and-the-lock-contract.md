# ADR-22 — Catalog pinning & the lock contract (declare vs. resolve)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-22 in the consolidated NKD ADR set (added to give the pin/lock model a decided home).
- **Relates to:** ADR-02 (what enters `buildDigest`), ADR-10 (toolchain / builder pins), ADR-11 (the catalog
  the lock feeds resolution), ADR-12 (the output/composition shape a curated set takes), ADR-13 (the aggregate
  SBOM dependency graph a curated set induces), ADR-15 (mirrorable pinned inputs), ADR-16 (digest-first
  selection), ADR-07 (the profile boundary the lock must NOT cross), ADR-21 (bump → regen), ADR-10 (a wrapped
  build's `embeds` are derived from a curated set), ADR-01 (canonical codec).
- **Satisfies:** FR-01/FR-02 (digest-pinned, fail-closed inputs). Supports the `buildDigest` determinism of
  ADR-02.

## Context

NKD aggregates a dozen-plus upstream components that each move independently as releases and CVE patches land.
If every pin lived in a file compiled into the `nkdctl` binary, keeping current would mean cutting a tool
release constantly and the repository would churn forever. Multiple ADRs already *assume* a lockfile exists
(ADR-02 "source-date derived from a lock", ADR-10 flake locks, ADR-15 locked closures), but the pin/lock
contract itself — the declare-vs-resolve split, what feeds `buildDigest`, what a bump is, and where the
lock stops and the builder profile begins — was never decided in an ADR. This ADR fixes it. It is upstream of
both `resolve` and `catalog bump`.

## Decision

### 1. Two files: a hand-authored registry, a machine-managed lock (like `go.mod` / `go.sum`)

- **`registry.yaml`** — hand-authored, changes rarely. Declares **sources** (an upstream repo + a version
  *constraint* + the **digest-pinned** toolchain image) and the **components** built from each source (build
  command, output form, CGO/flags, version stamps). **No commit SHAs live here.**
- **`pins.lock.yaml`** — machine-generated, changes often. One entry per source: the resolved
  `{tag, commit, treeHash, sourceDateEpoch}`, plus an **`artifacts:`** section of per-arch content digests for
  prebuilt (no-compile) components. This is the **only file a version bump touches**.

Both files carry explicit `apiVersion` and `kind`. Readers dispatch to an exact versioned schema, reject unknown
versions/kinds/fields, and never interpret a future lock as the current version. A missing lock is an input error,
not an implicit empty lock.

Registry v3 contains producer components with explicit supported `platforms`.
It contains no recipes, criteria, recipe layers, mixins, `versionFrom`, placement categories or node roles.
Catalog overlay v3 can replace complete component producers under ADR-11; it is independent of the removed
recipe merge system. Catalog snapshots use v3 and strict decoding rejects removed classification fields even
when the caller supplies the current version. Registry v3 accepts the explicit pin-set declarations in §7.
Lock v1 retains its version and adds optional `derivedFrom` provenance to source and artifact pins; older strict
readers reject these new fields rather than ignoring them.

Multiple components may share one source and therefore **bump atomically** (e.g. the Kubernetes-core binaries
move together on a single lock entry).

**Per-module tag prefixes.** A source MAY declare a **`tagPrefix`** (for example `cli/`) when the upstream is a
monorepo whose modules tag independently. Pin and bump then consider **only** tags under that prefix, and the
`constraint` matches the version part after it — a sibling module's newer tag can never satisfy this source. The
lock stores the **full tag git can resolve** (`cli/v0.1.0-rc.2`, not `v0.1.0-rc.2`), so a checkout is a pure
`git` operation with no reassembly. Rendered version stamps are **prefix-aware**: `Version`, `Major`, `Minor`,
`Patch`, and `Date` derive from the version part, while `ModuleTag` renders the full prefixed tag. A source
without a `tagPrefix` behaves exactly as before, and a declared prefix that matches no tag fails closed rather
than falling back to unprefixed tags.

**A repository URL is not a source identity.** Several sources may name **one repository at different tags** —
a monorepo's CLI module and its node-images module, or one repository deliberately pinned at two releases. The
declared source key is the identity. The checkout/fetch node is therefore keyed by **commit, tree hash, and
vendor spec**: distinct commits get distinct checkouts, identical commits share exactly one, and no de-duplication
is attempted on the URL. In the other direction a **from-source component has exactly one source** — one source
closure per hermetic build. A component needing bytes from two repositories is two components, or one source
that vendors the other.

### 2. The lock feeds logical build identity for selected producers

Load-time resolution combines `registry.yaml` and `pins.lock.yaml` into a fully pinned catalog. Each Definition
output selects exact producer IDs and a concrete platform from that catalog. Compilation generates its
executable closure and canonical BuildDigest. A selected source pin, prebuilt artifact or producer toolchain
change moves that logical build identity when it changes the selected graph. An unrelated catalog edit may
change catalog and Plan identity without changing another output's BuildDigest. Output names, packaging,
and transport do not enter this logical identity.

Templated version stamps such as `KUBE_GIT_VERSION` and `KUBE_GIT_COMMIT` are rendered from source pins at
load time (`{{.Tag}}`, `{{.Commit}}`, …). Unknown template tokens fail closed. There is no cluster context,
criteria axis or `versionFrom` projection into build identity.

### 3. Fail closed on any unpinned or under-resolved input

An **unpinned source ref**, or a **toolchain pinned by tag instead of digest**, **fails resolution** — it never
builds (a core SRD invariant). The lock must be **complete** — every declared source resolved, every required
`artifacts:` digest present for every architecture declared by each prebuilt producer's `platforms` — or resolution fails closed with a distinct coded error. The
validator accepts either digest form for a toolchain (manifest-list `golang@sha256:…` or a per-arch image ID)
and every per-arch entry, but fails closed on a tag. Completeness is an exact key-set contract: orphan source
pins, artifacts for undeclared components/architectures, and duplicate semantic entries are rejected.
Completeness is checked from actual producer platform declarations even when no Definition selects that producer. Stale data cannot silently survive a bump.

A **derived member** (§7) whose lock pin disagrees with the version its umbrella declares **at the umbrella's
pinned tree** is likewise an **under-resolved input**: resolution fails with `CodePinSetMismatch`
(`NKD-E-PIN-SET-MISMATCH`, exit 54). Re-derivation, not the recorded `derivedFrom` string, checks this agreement.

Authority-bearing catalog reads are context-aware and resource-bounded. The on-disk provider holds a real root
and opens every component descriptor-relative with `O_NOFOLLOW`; it accepts regular files only, limits one file
to 4 MiB and one deep load to 32 MiB, and limits recursive catalog-overlay discovery to 1,024 entries and 16
directory levels. Cancellation is checked before any path I/O and between bounded chunks. Legacy providers that
lack the deep context interface remain usable only through the synchronous diagnostic compatibility API;
`client/v1` rejects them before a read.

### 4. `catalog pin` / `bump` machine-manage the lock; `--check` is the dry-run

`catalog.UpdateSourcePins` and `catalog.UpdateArtifactPins` own selection, resolution, change detection and
commit. They accept typed maintenance requests and return descriptive change summaries; the lock's original
snapshot remains private. Source selections retain request order, so an earlier source may resolve before a
later unknown source fails, but no partial update commits. Artifact selections and complete architecture sets
are admitted before upstream calls. No-op updates preserve existing lock bytes. Source check mode returns
`CodeDiff` when changes are available without writing; artifact pinning does not acquire a new check mode.

- `catalog pin --dir <catalog> --tag vX.Y.Z <source>` — resolve that tag to
  `{tag, commit, treeHash, sourceDateEpoch}` and update the lock.
- `catalog bump --dir <catalog> [--all] [source...]` — resolve the newest tag matching each selected source's
  `constraint` (e.g. `1.32.x`). `--all` selects independent roots and visits each explicitly nested set once.
- `catalog pin-artifacts --dir <catalog> [--all] [component...]` — resolve the complete declared architecture
  set of prebuilt digests through the existing URL or image acquisition path.
- `catalog pin` and `catalog bump` accept `--check`: resolve and print the complete update without writing.
- **Derived pins (§7).** `pin`/`bump` must **refuse** an independent bump of a member marked `pinnedBy` with a coded
  error naming its umbrella; bumping the umbrella is **one operation** that re-derives and rewrites the umbrella
  entry and **every** member entry in the set together (an aborted partial set is never committed); and `--check`
  prints the **whole set** — the umbrella plus every member it moves — never a member in isolation.

Every write follows ADR-21's same-filesystem stage, fsync, original-digest compare-and-swap, and atomic rename.
Lock snapshots and writes share the bounded context-aware file boundary. Cancellation before rename aborts and
removes the stage; after rename, commit wins and durability work completes. Concurrent same-checkout bumps never
use last-writer-wins; one returns the coded write conflict. `--check` runs the identical resolver/serializer and
differs only by withholding the commit.

### 5. The lock is DATA and ships on its own train, decoupled from the tool

Because the lock is data, a newer lock ships as a **catalog release**, consumed via `--data` / a catalog
reference, on its **own cadence independent of `nkdctl` releases**. `nkdctl` (the tool) is released only when
its *logic* changes; the embedded default lock is a **periodically-refreshed known-good default**, not the
mechanism operators rely on to stay current. An operator tracking security fixes pulls a **newer catalog, not a
newer binary**.

| Changes | Touches | Cadence | Ships as |
|---|---|---|---|
| A component version / CVE patch | `pins.lock.yaml` | often | catalog release (data) |
| A producer build definition / new component | `registry.yaml` | occasional | catalog release (data) |
| Resolver / IR / engine logic | Go code | rare | `nkdctl` release (tool) |

`derivedFrom` (§7) is **pin provenance carried in the lock as data**: it records which umbrella pin produced a
member entry, so a reviewer reads the curation out of an ordinary lockfile diff. It is not a signature, not a
receipt, and not an authority on its own — the authority is the re-derivation check in §3.

### 6. Catalog data pins DEFINITION inputs only — never the builder profile

Catalog data pins **definition** inputs; the selected executable subset enters `buildDigest`: resolved sources and prebuilt
artifacts live in `pins.lock.yaml`; the directly digest-pinned **component build toolchain** lives in
`registry.yaml`. The signed builder profile does **not** override either. It owns environment identity instead:
the `nkd-runner`, its bootstrap toolchain, host-environment facts, and builder capabilities (ADR-07), all excluded
from `buildDigest` by construction.

This boundary is load-bearing: changing a component toolchain is a reviewed catalog producer change and moves
`buildDigest`; changing the runner/bootstrap uses `profile repin --runner|--bootstrap-toolchain` and does not.
Putting runner/bootstrap identity in the lock would corrupt logical build identity; putting a source/component-toolchain
pin in the profile would make the definition overridable by its environment.

### 7. Derived pin sets: one source may curate other sources' pins

An upstream repository can declare exact versions that belong together. A catalog **pin set** maps that
declaration to existing sources or acquired producers. The generic `values` format reads bounded YAML/JSON;
it does not discover producers, select outputs or establish release qualification.

```yaml
# registry.yaml fragment; ordinary source build/toolchain declarations omitted
sources:
  platform:
    repo: https://github.com/example/platform
    constraint: 1.4.x
    pinSet:
      file: versions.yaml                     # from platform's pinned tree
      format: values                         # also the default
      members:
        controller: {source: controller, path: 'dependencies[name=controller].version'}
        helper: {artifact: helper, path: 'dependencies[name=helper].version'}
  controller:
    repo: https://github.com/example/controller
    tagPrefix: controller/v
    pinnedBy: platform
components:
  helper:
    platforms: [linux/amd64, linux/arm64]
    pinnedBy: platform
    outputType: raw
    prebuilt:
      url: 'https://downloads.example/helper/{{.Version}}/helper-{{.Arch}}'
      evidence: {none: true}
```

```yaml
# versions.yaml in the upstream platform repository
dependencies:
  - {name: controller, version: "0.9.3"}
  - {name: helper, version: "2.0.0"}
```

Pinning `platform` resolves `controller/v0.9.3` and pins helper version `2.0.0` through its existing
artifact-pin path, recording a digest for each declared architecture. Every resulting member pin records
`derivedFrom: platform@<full-commit>`. The umbrella and all affected pins commit together.
`catalog pin` and `catalog bump` accept `--runtime` for acquired image manifest inspection
(default `docker`), including images whose versions come through derived sources.

**Explicit membership.** Each member names exactly one source or acquired artifact, with a reciprocal
`pinnedBy` declaration. An acquired member has no `source`. Every record in a selector-addressed collection
must be mapped; new, missing, duplicate or ambiguous records fail closed before a lock update. Unrelated
object metadata is not membership. Nested sets require their own explicit declarations; cycles, repeated
ownership and nesting beyond 64 sources are rejected. Independent pin, bump or artifact-pin updates of a
derived member fail with an error naming its umbrella.

**Bounded extraction.** Paths use dotted fields and literal `[key=value]` selectors. Values must be exact
semantic-version strings, optionally prefixed with `v`; ranges and numeric YAML scalars are rejected.
Version syntax is owned by `internal/versionpolicy`, using pinned Masterminds SemVer. Pin sets require
three numeric segments; Helm metadata uses Helm-compatible parsing. Validation never rewrites the input
string used for lookup or identity.
Source tags use the declared `tagPrefix` without guessing a `v`: `0.9.3` with `controller/v` becomes
`controller/v0.9.3`. Unsupported selectors, duplicate keys, extra documents, YAML anchors and aliases fail.
Documents are limited to 4 MiB, 64 levels and 65,536 nodes; a set declares at most 1,024 members.

**Pinned-tree validation.** NKD fetches the exact umbrella commit into a private bare Git repository and
checks its tree hash and source date epoch against the lock. Linux/macOS acquisition applies a kernel
file-size ceiling of at most 64 MiB to Git and its children before fetching. Fetch retains packed objects,
disables auxiliary pack downloads and automatic maintenance, and admits exactly one pack with its index
within a 128 MiB object-store budget. Unrelated content counts toward the packed-byte ceiling, even
when the selected version file is small. Other host platforms fail capability admission. Only regular blobs at canonical relative paths
are readable; symlinks, submodules and path escapes are rejected. Reads are context-bound and limited to
4 MiB per file, 32 MiB and 1,024 reads per tree. No checkout, source program, hook, template or dependency
resolver runs. Extraction itself has no network access; obtaining pinned Git objects requires source access.

**Atomic updates and provenance.** The catalog extracts a complete member-version map before resolving that
umbrella's members. Acquired producers whose version comes from a derived source move in the same transaction.
It validates and serializes the complete candidate lock within the byte limit before `--check` or commit; any
failure leaves the original lock intact. Full catalog loads and lock writes re-derive versions from pinned trees and check
`derivedFrom`; provenance strings alone cannot authorize a stale member. A mismatch returns exit 54.
Catalogs with pin sets therefore require access to pinned Git objects when loaded; ordinary catalogs retain
local loading. Offline pin-set transport remains deferred.

**NKE deploy adapter.** `format: nke-deploy-v1` reads `nke-deploy.yaml` and each Helm item's
`release.yaml` from the same pinned tree. Every deploy item must appear in `members` or `accounted`;
unknown, missing, duplicate or reclassified items fail before chart acquisition. External members bind the
reviewed chart name/repository through `prebuilt.chart` (acquired) or member `chart` (source). Accounted local
charts bind their relative path and must match their `Chart.yaml` name/version. Order, values and installation
remain NKE-owned. See the [example catalog](../examples/nke-pin-set/registry.yaml) and
[usage guide](../nke-pin-set-usage.md). CycloneDX ingestion and qualified-set acceptance remain deferred.

**Chart acquisition.** HTTPS Helm indexes and anonymous OCI repositories resolve one exact chart version.
The lock records chart identity, transport URL and the SHA-256 of the unchanged chart archive, identical for
every declared architecture. OCI pins identify the chart-content layer, not the manifest. Downloads and archive
inspection are bounded; unsafe paths, links and conflicting duplicates fail. Identical regular-file duplicates
under vendored `charts/` are allowed, with every record counted against the limits. Runtime OCI acquisition
uses an explicit digest-only `oci-blob://` transport and preserves the compressed bytes. Private OCI credentials,
chart dependency resolution and pins for images referenced inside charts are outside this contract.

Direct output selection remains unchanged. `curatedBy` build-graph identity and aggregate SBOM curation edges
remain follow-ups owned by ADR-11/13; this pinning contract does not implement them.

## Consequences

**Positive**
- Staying current is a **data** operation (a catalog release), not a tool release — the repo does not churn.
- A selected producer pin changes its logical build identity; unrelated catalog inputs remain outside that closure.
- Shared-source components bump atomically; the hand-authored file stays quiet via templated stamps.
- The definition/environment boundary is explicit, so catalog edits and `profile repin` cannot overlap or
  override one another.
- A monorepo's modules pin independently without a source-per-repository fiction, and identical commits still
  share one checkout.
- Drift between a member and the upstream's declared set fails validation instead of surviving as an
  unnoticed lockfile edit.

**Negative / costs**
- Two files to understand (declare vs. resolve) and a load-time resolution step.
- `catalog bump` needs network + git to resolve tags/commits and prebuilt digests; loading a pin set also
  requires its pinned Git objects.
- A shared source means a bump moves several components at once (atomicity is intended, but coarse).
- A pin set is a second, coarser bump unit: an umbrella bump can move many members in one reviewed change, and a
  member cannot be patched alone without first changing the declaration.
- Each curated set costs a reviewed extractor declaration, and an upstream that reshapes its manifest breaks
  derivation loudly (correctly, but it is maintenance).

**Neutral**
- The reviewable lock file is strict, deterministically emitted YAML input; its raw bytes are not a semantic
  trust-object identity. After strict parsing, its normalized resolved values enter ADR-01's canonical build
  graph. ADR-15 may hash the exact lock file as a transported blob, which must not be confused with
  `buildDigest` or a canonical `LockSnapshot` object.

## Alternatives considered

- **Pins live in tool code (one file, compiled in).** Rejected — every CVE patch forces a tool release; the
  repository churns forever. This ADR exists to avoid exactly that.
- **Hardcode resolved SHAs and version stamps in `registry.yaml`.** Rejected — the hand-authored file would
  churn on every bump; templated stamps rendered from the pin keep it quiet.
- **One combined file (no declare/resolve split).** Rejected — conflates the rare hand edit with the frequent
  machine edit, and loses the go.mod/go.sum-style review clarity.
- **Pin the runner/environment in the lock.** Rejected — it is not a definition input; it belongs in the
  signed builder profile (§6, ADR-07).
- **One source pinned at several tags (a multi-tag source).** Rejected — it would make "the source's commit"
  ambiguous inside one hermetic build and in `buildDigest`. Declare two sources against the same repository
  instead (§1); the checkout node de-duplicates by commit anyway.
- **A repository-level pin shared by every source naming that repository.** Rejected — it forces unrelated
  modules of a monorepo to move together and re-introduces the churn `tagPrefix` exists to avoid.
- **Resolve upstream version ranges at pin time.** Rejected — NKD would re-decide what the upstream already
  decided, and two pins of the same tree could differ. A pin set reads exact declared versions (§7).
- **Hand-author each member pin and diff it against the upstream set by review.** Rejected — an unchecked
  convention. Re-derivation at validation time makes it a property the tooling enforces.

## Follow-ups

- **ADR-02:** the fully-resolved pins are the `buildDigest` inputs; source-date comes from the lock.
- **ADR-10:** toolchain and prebuilt-artifact digests the lock carries.
- **ADR-15:** the lock and its pinned inputs are mirrorable for air-gap.
- **ADR-21:** `catalog bump` → regen; the `--check` dry-run; the definition/environment boundary vs.
  `profile repin`.
- **ADR-11 (deferred):** define `curatedBy` graph binding and its identity/conflict rules.
- **ADR-12 §3:** a future curated set can inform explicit output membership; it must not introduce a selector
  axis, deployment topology or reusable recipe group.
- **ADR-13 (deferred):** define aggregate SBOM curation edges from an umbrella to its members.
- **ADR-10 (deferred):** bind a wrapped build's `embeds` to its pinned upstream declaration.

## Acceptance evidence

Contract evidence in `pkg/nkd/catalog/load_test.go`, `provider_context_test.go`, `pin_context_test.go`,
`prebuilt_test.go`, `lock_contract_test.go`, `pin_update_test.go`, `pin_artifact*_test.go` and
`client/v1/catalog_io*_test.go` covers strict schemas, complete source/artifact keys and architectures,
bounded no-follow I/O, cancellation, deterministic output and atomic CAS writes. `pin_extract_test.go`,
`pin_tree_test.go`, `pin_set_integration_test.go` and `pin_serialization_test.go` cover explicit source/acquired
and nested sets, pinned-tree re-derivation, membership/drift rejection, rollback and shared check/write bounds.
These are contract tests. Operational acceptance still requires real-upstream bump modes, process kill points
on supported filesystems and publication of a separately versioned catalog-data release.

## Tool release assets

A source may declare `tool` with `format` (`raw` or `tar-gzip`), a version-rendered HTTPS `url`,
`platform` (`linux/amd64` or `linux/arm64`), and `executablePath`. Tool names are source names; they do not
include release versions. The same source constraint and tag-prefix selection used by other sources governs
`catalog pin` and `catalog bump`. Each declared tool currently selects one platform; multiple platforms use
distinct source names. No platform fallback is inferred.

Pinning resolves the upstream tag to commit/tree/epoch, downloads the release asset with bounded I/O, checks
archive structure and computes both archive and selected-executable SHA-256 digests without running the tool.
The source lock's nested `tool` record stores the exact URL, format, member, platform and digests. A changed
version or acquisition declaration with an old tool pin fails catalog loading. Tool updates use the existing
lock snapshot/CAS writer, including `--check` no-write behavior; a failed download produces no lock update.
These hashes pin acquired bytes; they do not establish an upstream signature or reproducible build.

The resolved catalog snapshot includes tool pins. Definition selects tools by purpose, and compilation
requires each selected catalog tool to match the utility execution platform. Missing pins, invalid formats
or ambiguous declarations fail before execution. Regenerate the Run bundle after any tool bump.

## GitHub action dependencies

A source with `kind: github-action` declares a canonical `https://github.com/owner/repository` URL and a
semver constraint. It has no tool download, build toolchain, vendor command, or tag prefix. Pinning requires
an exact stable release tag and resolves its commit/tree/epoch through the existing source resolver and
CAS lock writer. Moving major tags are not release pins. Unknown source kinds fail closed.

Resolved action references are included in the catalog snapshot and Run plan. The GitHub adapters own
supported repository/major-version contracts; they contain no release commit constants or embedded locks.
A compatible patch/minor bump is a catalog lock change followed by regeneration. A new major requires an
adapter compatibility change and validation. Local generation does not require GitHub actions.
