# ADR-22 — Catalog pinning & the lock contract (declare vs. resolve)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-22 in the consolidated NVKR ADR set (added to give the pin/lock model a decided home).
- **Relates to:** ADR-02 (what enters `buildDigest`), ADR-10 (toolchain / builder pins), ADR-11 (the catalog
  the lock feeds resolution), ADR-12 (the output/composition shape a curated set takes), ADR-13 (the aggregate
  SBOM dependency graph a curated set induces), ADR-15 (mirrorable pinned inputs), ADR-16 (digest-first
  selection), ADR-07 (the profile boundary the lock must NOT cross), ADR-21 (bump → regen), ADR-10 (a wrapped
  build's optional `embeds` authenticate pinned upstream metadata only; curated-set derivation is deferred),
  ADR-01 (canonical codec).
- **Satisfies:** FR-01/FR-02 (digest-pinned, fail-closed inputs). Supports the `buildDigest` determinism of
  ADR-02.

## Context

NVKR aggregates a dozen-plus upstream components that each move independently as releases and CVE patches land.
If every pin lived in a file compiled into the `nvkr` binary, keeping current would mean cutting a tool
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

The independent qualification policy selects its own verifier executable and native smoke-image pins; these
are consumer trust inputs, not producer catalog pins or permissions inherited from the producing Run.
Review and commit those concrete selections with their generated workflows. Hosted acceptance follows merge
under ADR-16; a recorded pin or successful generation alone establishes neither production readiness nor
organizational approval.

Registry v3 contains producer components with explicit supported `platforms`.
It contains no recipes, criteria, recipe layers, mixins, `versionFrom`, placement categories or node roles.
Catalog overlay v3 can replace complete component producers under ADR-11. Its only narrow field-level operation
is an exact old-to-new digest-pinned execution-toolchain substitution for a named, sorted producer set and one
host architecture. The predecessor catalog digest and every old component pin must match before the new catalog
snapshot is admitted; there is no wildcard or general deep merge. Catalog snapshots use v3 and strict decoding
rejects removed classification fields even when the caller supplies the current version. Registry v3 accepts
the explicit pin-set declarations in §7.
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
(`NVKR-E-PIN-SET-MISMATCH`, exit 54). Re-derivation, not the recorded `derivedFrom` string, checks this agreement.

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

Acquired `prebuilt.image` declarations may retain an exact parent index or single-manifest digest,
or use a tag. Image maintenance authenticates the actual parent, unique baseline native descriptors and
native manifest/config bytes with the existing pinned OCI library. The artifact lock retains optional
`imageParentDigest` plus the independently selected native `digests`. The parent is lock-owned, not a
second authored pin field. A digest-qualified declaration must equal that retained parent; the legacy
single-native lane may omit it only when the authored digest equals every selected native subject.
Missing or substituted parent descriptions fail `CodeUnverifiedRef` before executable work. Templates
remain owned by their existing render boundary. Lock fields describe constraints, never a receipt.

The frozen full IR retains the exact parent acquisition reference and parent/native/platform binding.
`ImageParentDigest` is also a logical content-selection constraint: changing the immutable index closure
that the selected source requires changes Build identity even when its native child is unchanged.
Repository, tag spelling and registry credentials remain transport-only. This optional field extends
IR v4; empty legacy nodes preserve their prior canonical logical bytes, while old strict readers reject
the unknown field rather than silently executing a weaker contract.

Migrating a legacy tag pin that lacks `imageParentDigest` authenticates and records the current parent.
That first maintenance operation intentionally changes Build identity, even if every native child
digest is unchanged. Subsequent maintenance at the same parent and children preserves the exact lock
bytes and Build identity. Maintained catalogs should complete this migration before establishing a
release identity. A same-version republish policy compares the parent as well as native subjects.
The pinned OCI library canonicalizes `repository:tag@digest` as `repository@digest`; the digest, not
the redundant tag spelling, constrains selection. The original declaration remains descriptive transport.

The built-in acquisition owner authenticates the exact parent again before output work and consumes its
original selected native image through an original-owner, callback-scoped single-use handle. It rejects
wrong parent/child/platform, duplicate or ambiguous native membership, constrained variants, nested
indexes and substituted manifest/config bytes. Callback exit cancels before joining consumption;
copied, expired and replayed handles fail. Unsupported injected OCI transports fail capability admission
before creating output. Tag-only legacy transports remain compatible; new tag pins with a retained
parent acquire that exact immutable parent, not a possibly moved tag. Native manifests are preserved,
including Docker schema-2 media, and complete native closures are verified before atomic publication.

Metadata responses remain bounded to 8 MiB and acquisition to five minutes, including membership,
config and complete layer materialization within the callback. Large images that cannot finish within
this fixed acquisition budget fail closed; extending it requires an owning capability change, not a
caller bypass. Only authenticated native
layer descriptors widen a compressed blob response; HTTPS redirect continuations retain that exact bound.
The existing OCI library additionally limits and hashes each layer stream against its descriptor.
No second fetch by a caller-supplied child, standalone unbounded layer client, or ambient credential
store substitutes for the parent's selected image. This is pipeline-attested acquisition-origin evidence,
not an upstream signature, build reproduction, source approval or production delivery claim.

- `catalog pin --dir <catalog> --tag vX.Y.Z <source>` — resolve that tag to
  `{tag, commit, treeHash, sourceDateEpoch}` and update the lock.
- `catalog pin --dir <catalog> --commit <full-commit> <source>` pins an exact Git
  source revision through the same acquisition and atomic lock writer. It cannot
  be combined with `--tag` or bump. The held Git commit, tree and epoch are checked;
  the lock's descriptive `tag` is that full commit, not an invented release tag.
  GitHub actions and release-tool sources retain their exact release-tag contract
  and reject this option before source acquisition. This permits candidate source
  integration without making a release or governance approval claim.
  `bump` rejects selected commit-mode pins (`tag == commit`) before upstream work;
  moving one requires explicit `pin --commit` or `pin --tag` intent, not a routine
  automatic conversion to the newest release tag.
- `catalog bump --dir <catalog> [--all] [source...]` — resolve the newest tag matching each selected source's
  `constraint` (e.g. `1.32.x`). `--all` selects independent roots and visits each explicitly nested set once.
- `catalog pin-artifacts --dir <catalog> [--all] [component...]` — resolve the complete declared architecture
  set of prebuilt digests through the existing URL or image acquisition path.

- `catalog pin` and `catalog bump` accept `--check`: resolve and print the complete update without writing.
- **Derived pins (§7).** `pin`/`bump` must **refuse** an independent bump of a member marked `pinnedBy` with a coded
  error naming its umbrella; bumping the umbrella is **one operation** that re-derives and rewrites the umbrella
  entry and **every** member entry in the set together (an aborted partial set is never committed); and `--check`
  prints the **whole set** — the umbrella plus every member it moves — never a member in isolation.

Image pin maintenance records authenticated parent and exact native subjects. Fixed source-reviewed
tag references remain ordinary acquired producers, not automatically extracted chart pin-set members.
The native profile requires explicit body `mediaType` matching served image/index media; omitted or
mismatched markers fail closed. Republishing a tag does not authorize changing an already frozen parent
or native subject. Moving a pin still requires explicit owning maintenance and review.

Maintenance callers may additionally supply explicit `SameRelease` groups of selected independent sources.
Each group must share the exact repository, tag prefix and constraint before upstream resolution; the complete
candidate must share tag, commit, tree and source epoch before the lock commit. Toolchain and vendoring
settings may differ. Disagreement aborts the entire transaction, including derived pins. This is an
invocation-scoped consistency assertion, not `pinnedBy` or `derivedFrom` provenance and not a new catalog
schema. Maintenance also opts into rejecting republished versions before preview or CAS: unchanged
versions may not acquire different source identities or acquired payload bytes/chart identities. Derived
provenance may advance to a new umbrella with unchanged member bytes. Interactive pin/bump semantics
remain explicit and unchanged. No constraint is widened automatically. The maintenance workflow owner is
defined in ADR-21.

Every write follows ADR-21's same-filesystem stage, fsync, original-digest compare-and-swap, and atomic rename.
Lock snapshots and writes share the bounded context-aware file boundary. Cancellation before rename aborts and
removes the stage; after rename, commit wins and durability work completes. Concurrent same-checkout bumps never
use last-writer-wins; one returns the coded write conflict. `--check` runs the identical resolver/serializer and
differs only by withholding the commit.

### 5. The lock is DATA and ships on its own train, decoupled from the tool

Because the lock is data, a newer lock ships as a **catalog release**, consumed via `--data` / a catalog
reference, on its **own cadence independent of `nvkr` releases**. `nvkr` (the tool) is released only when
its *logic* changes; the embedded default lock is a **periodically-refreshed known-good default**, not the
mechanism operators rely on to stay current. An operator tracking security fixes pulls a **newer catalog, not a
newer binary**.

| Changes | Touches | Cadence | Ships as |
|---|---|---|---|
| A component version / CVE patch | `pins.lock.yaml` | often | catalog release (data) |
| A producer build definition / new component | `registry.yaml` | occasional | catalog release (data) |
| Resolver / IR / engine logic | Go code | rare | `nvkr` release (tool) |

`derivedFrom` (§7) is **pin provenance carried in the lock as data**: it records which umbrella pin produced a
member entry, so a reviewer reads the curation out of an ordinary lockfile diff. It is not a signature, not a
receipt, and not an authority on its own — the authority is the re-derivation check in §3.

### 6. Catalog data pins DEFINITION inputs only — never the builder profile

Catalog data pins **definition** inputs; the selected executable subset enters `buildDigest`: resolved sources and prebuilt
artifacts live in `pins.lock.yaml`; the directly digest-pinned **component build toolchain** lives in
`registry.yaml`. The signed builder profile does **not** override either. It owns environment identity instead:
the `nvkr-runner`, its bootstrap toolchain, host-environment facts, and builder capabilities (ADR-07), all excluded
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
Image pinning uses the already pinned OCI library against anonymous HTTPS registries,
not a runtime CLI's manifest projection. Index selection requires exactly one baseline
Linux native descriptor for each requested architecture (unvariant AMD64; unvariant or
baseline `v8` ARM64), authenticates the native manifest/config bytes and platform, and
preserves OCI or Docker schema-2 native manifest identity. Nested indexes, ambiguous
matches, constrained OS variants and substituted subjects fail closed. Metadata I/O is
bounded to 8 MiB per response and five minutes. These pin read-backs do not authenticate
layers or grant image-delivery, scanner or publication acceptance.
`catalog pin`, `catalog bump` and `catalog pin-artifacts` retain `--runtime` (and the
artifact command's `--docker` alias) only for maintenance API compatibility; it no longer
selects a process or ambient runtime credentials for image pinning.
Only the empty/default `docker` spelling is admitted; non-default values return
`CodeUsage` before catalog reads or upstream work rather than silently ignoring
a requested runtime's credential store.
This anonymous native client owns acquired `prebuilt.image` artifact pins only.
Source `toolchainBaseImage` resolution remains a separate existing Docker path;
it does not inherit these native selection or anonymous registry guarantees.

**Explicit membership.** Each member names exactly one source or acquired artifact, with a reciprocal
`pinnedBy` declaration. An acquired member has no `source`. Every record in a selector-addressed collection
must be mapped; new, missing, duplicate or ambiguous records fail closed before a lock update. Unrelated
object metadata is not membership. Nested sets require their own explicit declarations; cycles, repeated
ownership and nesting beyond 64 sources are rejected. Independent pin, bump or artifact-pin updates of a
derived member fail with an error naming its umbrella.

**Bounded extraction.** Paths use dotted fields and literal `[key=value]` selectors. The shared
`internal/documentpath` module also resolves declarative Helm values bindings (ADR-20); only pin sets
require complete membership of each selected collection. Values must be exact
semantic-version strings, optionally prefixed with `v`; ranges and numeric YAML scalars are rejected.
Version syntax is owned by `internal/versionpolicy`, using pinned Masterminds SemVer. Pin sets require
three numeric segments; Helm metadata uses Helm-compatible parsing. Validation never rewrites the input
string used for lookup or identity.
Source tags use the declared `tagPrefix` without guessing a `v`: `0.9.3` with `controller/v` becomes
`controller/v0.9.3`. Unsupported selectors, duplicate keys, extra documents, YAML anchors and aliases fail.
Documents are limited to 4 MiB, 64 levels and 65,536 nodes; a set declares at most 1,024 members.

**Pinned-tree validation.** NVKR fetches the exact umbrella commit into a private bare Git repository and
checks its tree hash and source date epoch against the lock. Linux/macOS acquisition applies a kernel
file-size ceiling of at most 64 MiB to Git and its children before fetching. Fetch retains packed objects,
disables auxiliary pack downloads and automatic maintenance, and admits exactly one pack with its index
within a 128 MiB object-store budget. Unrelated content counts toward the packed-byte ceiling, even
when the selected version file is small. Other host platforms fail capability admission. Only regular blobs at canonical relative paths
are readable; symlinks, submodules and path escapes are rejected. Reads are context-bound and limited to
4 MiB per file, 32 MiB and 1,024 reads per tree. No checkout, source program, hook, template or dependency
resolver runs. Extraction itself has no network access; obtaining pinned Git objects requires source access.

Resolved catalog snapshots reuse the catalog owner's intrinsic pin-set declaration checks: supported
format, canonical relative file path, bounded and unambiguous member declarations, and adapter-specific
selectors. Compilation and Plan validation reject malformed descriptions before emitting credentialed
catalog-revalidation steps. These checks and a matching snapshot digest establish semantic validity and
byte identity, not upstream authentication. Reciprocal registry membership, lock completeness and exact
pinned-tree re-derivation remain owned by catalog loading; resolved overlays do not recreate those
authored registry records.

**Atomic updates and provenance.** The catalog extracts a complete member-version map before resolving that
umbrella's members. Acquired producers whose version comes from a derived source move in the same transaction.
It validates and serializes the complete candidate lock within the byte limit before `--check` or commit; any
failure leaves the original lock intact. Full catalog loads and lock writes re-derive versions from pinned trees and check
`derivedFrom`; provenance strings alone cannot authorize a stale member. A mismatch returns exit 54.
Catalogs with pin sets therefore require access to pinned Git objects when loaded; ordinary catalogs retain
local loading. Offline pin-set transport remains deferred.

**NVKE deploy adapter.** `format: nvke-deploy-v1` reads the declared deploy document and each Helm item's
`release.yaml` from the same pinned tree. Every deploy item must appear in `members` or `accounted`;
unknown, missing, duplicate or reclassified items fail before chart acquisition. External members bind the
reviewed chart name/repository through `prebuilt.chart` (acquired) or member `chart` (source), and always
require an exact version in `release.yaml`. Accounted local charts bind their relative path and authenticate
their name and exact version against the same pinned tree's `Chart.yaml`. An optional accounted
`chartName` explicitly binds a reviewed upstream chart rename; otherwise the release name must match. A local release may omit its version;
when supplied it must be an exact string matching `Chart.yaml`. Empty, null, numeric and ranged versions fail.
The local chart's optional `artifact`, `imageArtifact` and `profilesArtifact` fields accept plain names only.
They describe downstream consumer mappings; they neither select catalog producers nor authorize Release
membership. Other unknown fields and these mappings on external members remain rejected. Order, values and
installation remain NVKE-owned. This is declared-metadata consistency for the admitted evidence lanes,
not deployed compatibility or qualification. See the [example catalog](../examples/nvke-pin-set/registry.yaml) and
[usage guide](../nvke-pin-set-usage.md). CycloneDX ingestion and qualified-set acceptance remain deferred.

**Chart acquisition.** HTTPS Helm indexes and anonymous OCI repositories resolve one exact chart version.
Source-owned acquired charts use the exact declared chart version, including any leading `v`, for
lookup and archive validation; their lock tag retains the source’s qualified Git tag.
The lock records chart identity, transport URL and the SHA-256 of the unchanged chart archive, identical for
every declared architecture. OCI pins identify the chart-content layer, not the manifest. Downloads and archive
inspection are bounded; unsafe paths, links and conflicting duplicates fail. Acquisition, packaging and publication SBOM inspection share the duplicate-record rule. Identical regular-file duplicates
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
- **Resolve upstream version ranges at pin time.** Rejected — NVKR would re-decide what the upstream already
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
- **ADR-10:** bind any explicitly authored `embeds` to its exact pinned upstream declaration;
  automatic curated-set derivation remains deferred.

## Acceptance evidence

Contract evidence in `pkg/nvkr/catalog/load_test.go`, `provider_context_test.go`, `pin_context_test.go`,
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

An acquired `host-package` producer may set `prebuilt.releaseExecutable: true`
with one matching-platform source tool declaration. The source tool lock owns
both transport and selected executable pins; a second component artifact lock,
authored URL/image/chart or resolved executable record is rejected. Raw assets
retain their bytes. A tar-gzip asset uses the standard-library codecs to select
one exact bounded regular member after archive verification, then rechecks the
executable digest. Archive selection is byte-preserving acquisition, not a build
or an upstream authenticity claim. The selected executable and transport/member
pins participate in logical Build identity; transport locations do not.
The shared source-tool selector admits at most 4,096 members and 1 GiB of both
physical expanded stream bytes and aggregate logical member bytes; sparse holes
cannot evade the latter. Reads of synthesized member bytes remain cancellation-aware.
The selector replaces the former 65,536-entry/256 MiB expanded policy with this
4,096-entry/1 GiB policy shared by catalog pinning and executable acquisition;
transport and selected-executable bounds remain 256 MiB. A focused contract test
freezes the selector limits at the shared `internal/releaseasset` owner; catalog
pinning and executable acquisition consume that owner's immutable policy.
Production archive acquisition uses the existing
anonymous immutable held-file owner to authenticate the already-open HTTP stream,
then selects from that descriptor rather than reopening a pathname. The bounded
carrier is never created beside the published executable and is closed before
the selected executable's atomic digest-checked publication. Linux uses a sealed
memfd; Darwin supports diagnostic anonymous read-only holding, not Linux execution
authority. These finite byte bounds do not constitute runner capacity reservation.
Production provenance records archive and selected-member digests independently (ADR-09).

## GitHub action dependencies

A source with `kind: github-action` declares a canonical `https://github.com/owner/repository` URL and a
semver constraint. It has no tool download, build toolchain, vendor command, or tag prefix. Pinning requires
an exact stable release tag and resolves its commit/tree/epoch through the existing source resolver and
CAS lock writer. Moving major tags are not release pins. Unknown source kinds fail closed.

Resolved action references are included in the catalog snapshot and Run plan. The GitHub adapters own
supported repository/major-version contracts; they contain no release commit constants or embedded locks.
A compatible patch/minor bump is a catalog lock change followed by regeneration. A new major requires an
adapter compatibility change and validation. Local generation does not require GitHub actions.
