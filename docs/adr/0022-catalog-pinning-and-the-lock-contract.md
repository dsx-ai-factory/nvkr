# ADR-22 — Catalog pinning & the lock contract (declare vs. resolve)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-22 in the consolidated NKD ADR set (added to give the pin/lock model a decided home).
- **Relates to:** ADR-02 (what enters `recipeDigest`), ADR-10 (toolchain / builder pins), ADR-11 (the catalog
  the lock feeds resolution), ADR-12 (the criteria/composition shape a curated set takes), ADR-13 (the aggregate
  SBOM dependency graph a curated set induces), ADR-15 (mirrorable pinned inputs), ADR-16 (digest-first
  selection), ADR-07 (the profile boundary the lock must NOT cross), ADR-21 (bump → regen), ADR-10 (a wrapped
  build's `embeds` are derived from a curated set), ADR-01 (canonical codec).
- **Satisfies:** FR-01/FR-02 (digest-pinned, fail-closed inputs). Supports the `recipeDigest` determinism of
  ADR-02.

## Context

NKD aggregates a dozen-plus upstream components that each move independently as releases and CVE patches land.
If every pin lived in a file compiled into the `nkdctl` binary, keeping current would mean cutting a tool
release constantly and the repository would churn forever. Multiple ADRs already *assume* a lockfile exists
(ADR-02 "source-date derived from a lock", ADR-10 flake locks, ADR-15 locked closures), but the pin/lock
contract itself — the declare-vs-resolve split, what feeds `recipeDigest`, what a bump is, and where the
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
is attempted on the URL. In the other direction a **component has exactly one source** — one source closure per
hermetic build. A component needing bytes from two repositories is two components, or one source that vendors
the other.

### 2. The lock feeds `recipeDigest`; a bump is a recipe change

Resolution moves to **load time**: `registry.yaml` + `pins.lock.yaml` → a fully-pinned catalog → recipe/IR →
`recipeDigest`. `recipeDigest` depends on the **fully-resolved pins**, so provenance is unchanged and a **pin
bump is a recipe change** — exactly like a source-commit bump, and repinning a prebuilt artifact is likewise a
recipe change (ADR-16 selects by digest regardless).

**Templated version stamps** keep the hand-authored file from churning: values like `KUBE_GIT_VERSION` /
`KUBE_GIT_COMMIT` are **templates rendered from the pin** at load time (`{{.Tag}}`, `{{.Commit}}`, …), and the
recipe's `k8sVersion` axis is filled from a source via `versionFrom:`. `recipeDigest` is computed from the
**rendered** values, so it changes **exactly** when a pin changes — no more, no less. Any template token other
than the sanctioned set fails closed at load.

### 3. Fail closed on any unpinned or under-resolved input

An **unpinned source ref**, or a **toolchain pinned by tag instead of digest**, **fails resolution** — it never
builds (a core SRD invariant). The lock must be **complete** — every declared source resolved, every required
`artifacts:` digest present for the target arch — or resolution fails closed with a distinct coded error. The
validator accepts either digest form for a toolchain (manifest-list `golang@sha256:…` or a per-arch image ID)
and every per-arch entry, but fails closed on a tag. Completeness is an exact key-set contract: orphan source
pins, artifacts for undeclared components/architectures, and duplicate semantic entries are rejected unless the
schema explicitly declares them as retained compatibility data. Stale data cannot silently survive a bump.

A **derived member** (§7) whose lock pin disagrees with the version its umbrella declares **at the umbrella's
pinned tree** is likewise an **under-resolved input**: resolution fails closed with a distinct coded error rather
than preferring either value. Re-derivation, not the recorded `derivedFrom` string, is what makes the curated set
a checked property.

Authority-bearing catalog reads are context-aware and resource-bounded. The on-disk provider holds a real root
and opens every component descriptor-relative with `O_NOFOLLOW`; it accepts regular files only, limits one file
to 4 MiB and one deep load to 32 MiB, and limits recursive overlay/mixin discovery to 1,024 entries and 16
directory levels. Cancellation is checked before any path I/O and between bounded chunks. Legacy providers that
lack the deep context interface remain usable only through the synchronous diagnostic compatibility API;
`client/v1` rejects them before a read.

### 4. `catalog pin` / `bump` machine-manage the lock; `--check` is the dry-run

- `catalog pin <source> --tag vX.Y.Z` — resolve that tag → `{tag, commit, treeHash, sourceDateEpoch}` → write
  the lock entry.
- `catalog bump [--pins|--artifacts|--all]` — for each source, find the newest tag matching its `constraint`
  (e.g. `1.32.x`), resolve it, and update the lock; `--artifacts` resolves per-arch prebuilt digests
  (release `.sha256` sidecars / registry manifest inspect) into the `artifacts:` section. Bounded, intentional
  churn.
- `catalog bump --check` — **dry-run**: print the lockfile diff, write nothing (the ADR-21 doctrine's
  `--check` twin, and the "show me what a bump would change" command).
- **Derived pins (§7).** `pin`/`bump` **refuse** an independent bump of a member marked `pinnedBy` with a coded
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
| A build recipe / new component | `registry.yaml` | occasional | catalog release (data) |
| Resolver / IR / engine logic | Go code | rare | `nkdctl` release (tool) |

`derivedFrom` (§7) is **pin provenance carried in the lock as data**: it records which umbrella pin produced a
member entry, so a reviewer reads the curation out of an ordinary lockfile diff. It is not a signature, not a
receipt, and not an authority on its own — the authority is the re-derivation check in §3.

### 6. Catalog data pins DEFINITION inputs only — never the builder profile

Catalog data pins **definition** inputs, all of which enter `recipeDigest`: resolved sources and prebuilt
artifacts live in `pins.lock.yaml`; the directly digest-pinned **component build toolchain** lives in
`registry.yaml`. The signed builder profile does **not** override either. It owns environment identity instead:
the `nkd-runner`, its bootstrap toolchain, host-environment facts, and builder capabilities (ADR-07), all excluded
from `recipeDigest` by construction.

This boundary is load-bearing: changing a component toolchain is a reviewed catalog/recipe change and moves
`recipeDigest`; changing the runner/bootstrap uses `profile repin --runner|--bootstrap-toolchain` and does not.
Putting runner/bootstrap identity in the lock would corrupt recipe identity; putting a source/component-toolchain
pin in the profile would make the definition overridable by its environment.

### 7. Derived pin sets: one source may curate other sources' pins

Upstreams increasingly ship a **curated set**: one repository states which versions of several other components
belong together (a Helm umbrella chart's lock file, a platform manifest, a release matrix). Re-typing those
versions into NKD's registry loses the curation — nothing then detects that a member drifted away from the set
the upstream tested. So a source MAY declare a **pin set**:

```yaml
# registry.yaml (hand-authored)
sources:
  nke-platform:                                  # the umbrella
    repo: https://github.com/NVIDIA/nke
    tagPrefix: platform/
    constraint: 1.4.x
    pinSet:
      file: charts/nke-platform/Chart.lock       # read from the PINNED tree only
      members:
        cwe-controller: { source: cwe,          path: dependencies[name=cwe].version }
        spire-agent:    { artifact: spire-agent, path: dependencies[name=spire].version }
  cwe:
    repo: https://github.com/NVIDIA/cwe
    tagPrefix: controller/
    pinnedBy: nke-platform                       # refuses an independent bump
```

```yaml
# pins.lock.yaml (machine-managed)
sources:
  nke-platform: { tag: platform/v1.4.2,   commit: 9f2c…, treeHash: 41ab… }
  cwe:          { tag: controller/v0.9.3, commit: 7d10…, treeHash: c3e8…,
                  derivedFrom: nke-platform@9f2c… }
```

Pinning the umbrella is one deterministic operation:

1. resolve the umbrella's own tag to `{commit, treeHash}` exactly as any other source;
2. **read the member-version file from the pinned tree only** — no templating, no dependency resolution, no
   dependency update, and no network. For a Helm umbrella this is the **lock file**, never the chart's declared
   version *ranges*: a range is a constraint, and resolving it would re-decide what the upstream already decided;
3. map each extracted member version to that member source's tag through its `tagPrefix`, resolve the tag to
   `{commit, treeHash}`, and write the member's ordinary lock entry stamped `derivedFrom: <umbrella>@<commit>`.
   A member in the **acquired** lane takes its content digests through the existing artifact-pin path instead —
   the derivation supplies the version, not a new digest mechanism.

The extractor is **data in the registry** (a file path plus a value path), so a new curated set is a reviewed
registry edit, not new resolver code. It must be **pure**: no expression evaluation, no shell, no fetch, one
bounded read inside the pinned tree, and a missing file or unmatched value path fails closed.

Three rules keep the set honest. Members marked **`pinnedBy` refuse an independent bump** — the only legal way to
move them is to bump the umbrella, which regenerates the whole set at once (§4). **Lock validation re-derives**
every member version from the umbrella's pinned tree and fails closed on any disagreement (§3); that check, not
the `derivedFrom` string, is what turns "curated" into a **checked property**. And only **direct** members are
derived: if a member is itself a pin set, it must be **declared explicitly** as its own umbrella, so the graph
stays one level deep at each declaration and no implicit transitive closure is walked.

The curation is visible downstream as well as in the lock. A composition consuming a curated set binds the
**umbrella's commit** (`curatedBy`), so the resolved graph records *which* curated set it came from and a set
change moves `recipeDigest` through the ordinary §2 path. The aggregate SBOM's dependency graph points from the
umbrella to its members rather than presenting them as unrelated peers. Both are stated here as consequences of
the pin model; their normative shape belongs to ADR-11 and ADR-13 (Follow-ups).

## Consequences

**Positive**
- Staying current is a **data** operation (a catalog release), not a tool release — the repo does not churn.
- `recipeDigest` changes exactly when a pin changes; provenance is preserved with resolution at load time.
- Shared-source components bump atomically; the hand-authored file stays quiet via templated stamps.
- The definition/environment boundary is explicit, so catalog edits and `profile repin` cannot overlap or
  override one another.
- A monorepo's modules pin independently without a source-per-repository fiction, and identical commits still
  share one checkout.
- An upstream's curated set stays curated: drift between a member and the set the upstream tested is a
  fail-closed validation error rather than an unnoticed lockfile edit.

**Negative / costs**
- Two files to understand (declare vs. resolve) and a load-time resolution step.
- `catalog bump` needs network + git to resolve tags/commits and prebuilt digests.
- A shared source means a bump moves several components at once (atomicity is intended, but coarse).
- A pin set is a second, coarser bump unit: an umbrella bump can move many members in one reviewed change, and a
  member cannot be patched alone without first changing the declaration.
- Each curated set costs a reviewed extractor declaration, and an upstream that reshapes its manifest breaks
  derivation loudly (correctly, but it is maintenance).

**Neutral**
- The reviewable lock file is strict, deterministically emitted YAML input; its raw bytes are not a semantic
  trust-object identity. After strict parsing, its normalized resolved values enter ADR-01's canonical recipe
  graph. ADR-15 may hash the exact lock file as a transported blob, which must not be confused with
  `recipeDigest` or a canonical `LockSnapshot` object.

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
  ambiguous inside one hermetic build and in `recipeDigest`. Declare two sources against the same repository
  instead (§1); the checkout node de-duplicates by commit anyway.
- **A repository-level pin shared by every source naming that repository.** Rejected — it forces unrelated
  modules of a monorepo to move together and re-introduces the churn `tagPrefix` exists to avoid.
- **Resolve the chart's declared version *ranges* at pin time.** Rejected — NKD would then re-decide what the
  upstream already decided in its lock file, and two pins of the same tree could differ. The pin set reads the
  upstream's resolved lock file from the pinned tree only (§7).
- **Hand-author each member pin and diff it against the upstream set by review.** Rejected — an unchecked
  convention. Re-derivation at validation time makes it a property the tooling enforces.

## Follow-ups

- **ADR-02:** the fully-resolved pins are the `recipeDigest` inputs; source-date comes from the lock.
- **ADR-10:** toolchain and prebuilt-artifact digests the lock carries.
- **ADR-15:** the lock and its pinned inputs are mirrorable for air-gap.
- **ADR-21:** `catalog bump` → regen; the `--check` dry-run; the definition/environment boundary vs.
  `profile repin`.
- **ADR-11:** a composition that consumes a curated set binds the umbrella's commit as `curatedBy`; the merge
  algebra and conflict rules for a curated member are ADR-11's to state normatively.
- **ADR-12 §3:** a curated set is a composition, not a recipe-selection axis — the SKU takes the **umbrella's
  shape**, and members are composition members bound by digest.
- **ADR-13:** the aggregate SBOM's dependency graph points from the umbrella to its members; the CycloneDX
  representation of that edge is ADR-13's to fix.
- **ADR-10:** the `embeds` of a wrapped build are **derived from the same pin set** when the upstream publishes
  one, rather than hand-authored (ADR-10).

## Acceptance evidence

Current contract evidence in `pkg/nkd/catalog/load_test.go`, `provider_context_test.go`, `pin_context_test.go`,
`prebuilt_test.go`, `lock_contract_test.go`, and `client/v1/catalog_io*_test.go` covers strict schema/unknown
fields, exact source/artifact key-set completeness, orphan and architecture rejection, deep-context admission,
pre-canceled/no-I/O behavior, no-follow regular files, byte/count/depth limits, deterministic listings and
canonical bytes, registry+lock snapshot CAS, one-winner concurrent writes, atomic replacement, and preservation
of the prior lock on conflict. Operational acceptance still
requires every documented bump mode against real upstreams, dry-run/write byte equality, process kill points on
supported filesystems, and publication of a separately versioned catalog-data release.

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
