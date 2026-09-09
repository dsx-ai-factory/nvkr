# NKD — Epic Backlog to Satisfy the PLC-L1 SRD

> [!CAUTION]
> **Historical backlog.** Completion markers, APIs, flags, and support assertions below are planning records,
> not current implementation or release claims. The proposed ADR baseline and
> `docs/adr/reconciliation.md` govern the reviewed architecture and current support boundary.

**Source of truth:** [`docs/nkd-srd-doc.md`](nkd-srd-doc.md) (16 use cases, 21 feature requirements FR-01–FR-21,
17 security requirements SEC-01–SEC-18 [SEC-12 absent], plus §1.3 constraints / §1.2 assumptions / §1.4 dependencies).
**Baseline:** the current `nkdctl` POC ([`docs/poc.md`](poc.md), per-requirement status in [`docs/checklist.md`](checklist.md)).
**Design detail:** the upfront schemas/contracts/spikes for Epic E1 live in [`docs/design-plan.md`](design-plan.md).

This backlog is organized as **4 epics** — a design epic plus three sequenced implementation streams:

| Epic | Covers | Kind |
|---|---|---|
| **E1 — Design & Contracts** | the shared schemas, interfaces, invariants, ADRs, and spikes everything builds on | Design |
| **E2 — Build & Reproducibility** | foundation substrate, recipe/composition/catalog, hermetic + no-compile build across CI engines, builder profile + bake-off | Implementation |
| **E3 — Trust, Verify & Evidence** | attestation/signing/trust, verification engine, variant lane, SBOM/provenance/vulnerability, offline/air-gap | Implementation |
| **E4 — Release, Delivery & CLI** | release lifecycle + compatibility + gates + inventory, AICR, pinning/publication, packages/images/disk delivery, CLI + SDK | Implementation |

The four epics collapse an earlier 15-area decomposition; each old area is now a labelled **sub-area** inside an epic.
**Story keys are preserved** (for example `E5-S1`, `E12-S3`) as stable identifiers — they match the cross-references in
`docs/design-plan.md` and do **not** imply the old epic numbering.

---

## 1. Executive summary

- **4 epics: 1 design + 3 implementation.** E1 has 8 design stories (one per domain, 54 decisions total; full detail in
  `docs/design-plan.md`); E2–E4 carry ~110 implementation stories, each with testable acceptance criteria.
- **Coverage is complete:** every FR (21/21), SEC (17/17), and UC (16/16) has exactly one owning implementation epic; E1
  underpins all of them by defining the contracts they implement.
- **Leverage, don't restart.** The POC already ships a working `resolve → IR → hermetic build → attest → verify → package
  → SBOM` path plus composition, the no-compile lane, builder profile, the variant lane, AICR shelling, and a boot-verified
  qcow2. Most stories are **harden/extend** (turn plumbing into executed+tested capability), not greenfield.
- **The single biggest unlock is executing cosign for real** (story **E5-S1** in E3). Signing/verify is plumbing-only today;
  E5-S1 gates the real trust loop and several dependents (E4-S3, E6-S1, E7-S5, E9-S1, E11-S5, E12-S4/S5).
- **Boundary discipline is load-bearing:** nkdctl *produces* verifiable, digest-selectable artifacts and release sets; it
  does not operate the fleet, run the conformance suite, or gate infrastructure mutation (see the E4 → Release Lifecycle notes).

### Epic sizes

| Epic | Title | Sub-areas | Stories | Phase |
|---|---|---|---|---|
| E1 | Design & Contracts | 8 domains (A–H) | 8 (54 decisions) | 0 |
| E2 | Build & Reproducibility | 5 | 40 | 1 |
| E3 | Trust, Verify & Evidence | 5 | 35 | 2 |
| E4 | Release, Delivery & CLI | 5 | 35 | 3 |

---

## 2. Traceability matrix (proof of full coverage)

Each requirement maps to exactly one owning implementation epic (E2–E4); E1 (Design) defines the contracts they implement.

### 2.1 Feature requirements

| FR | Title | Owner |
|---|---|---|
| FR-01 | Asymmetric Match | **E2** |
| FR-02 | Layered Composition | **E2** |
| FR-03 | Deterministic Recipe | **E2** |
| FR-04 | Fail-Closed Inputs | **E2** |
| FR-05 | CI Portability | **E2** |
| FR-06 | Hermetic Build | **E2** |
| FR-07 | No-Compile Lane | **E2** |
| FR-08 | Attestation Signing | **E3** |
| FR-09 | Identity Verification | **E3** |
| FR-10 | Bake-off Gate | **E2** |
| FR-11 | Variant Handling | **E3** |
| FR-12 | Builder Profile Binding | **E2** |
| FR-13 | Canonical Boundary | **E2** |
| FR-14 | Deterministic Assembly | **E4** |
| FR-15 | Deterministic SBOM | **E3** |
| FR-16 | Accelerated Runtime | **E4** |
| FR-17 | Constraint-Driven Pinning | **E4** |
| FR-18 | Atomic Promotion | **E4** |
| FR-19 | Offline Verification | **E3** |
| FR-20 | Coded Errors | **E2** |
| FR-21 | Interface Standardization | **E2** |

### 2.2 Security requirements (SEC-12 absent)

| SEC | Title | Owner |
|---|---|---|
| SEC-01 | Integrity Protection | **E3** |
| SEC-02 | Digest Authentication | **E3** |
| SEC-03 | Zero-Binary Trust | **E3** |
| SEC-04 | Keyless Signing | **E3** |
| SEC-05 | Verification Logic | **E3** |
| SEC-06 | Verification Enforcement | **E3** |
| SEC-07 | Verifier Isolation | **E3** |
| SEC-08 | Independent Gates | **E4** |
| SEC-09 | Secret Handling | **E2** |
| SEC-10 | Dependency Security | **E2** |
| SEC-11 | Build Isolation | **E2** |
| SEC-13 | Registry Integrity | **E4** |
| SEC-14 | Airgap Verification | **E3** |
| SEC-15 | ValidatedVariant | **E3** |
| SEC-16 | Immutable Publication | **E4** |
| SEC-17 | NKE Verification | **E4** |
| SEC-18 | Vulnerability Reporting | **E3** |

### 2.3 Use cases

| UC | Persona | Description | Advanced by |
|---|---|---|---|
| UC-01 | Publisher | Define & publish an immutable release | E2, E3, E4 |
| UC-02 | Operator | Build & establish trust before promotion | E2, E3 |
| UC-03 | Operator | Acquire without compiling | E2 |
| UC-04 | Operator | Deliver the substrate | E4 |
| UC-05 | Operator | Publish an updated definition | E4 |
| UC-06 | GPU-tenant Operator | Materialize the accelerated runtime | E4 |
| UC-07 | Auditor | Audit artifact provenance | E3 |
| UC-08 | Fleet operator / SRE | Consume complete compatible sets | E4 |
| UC-09 | Air-gapped operator | Verify & operate offline | E3 |
| UC-10 | Operator | Validate before promotion | E4 |
| UC-11 | Publisher | Supersede or withdraw a release | E4 |
| UC-12 | Operator | Respond to a failed gate | E3, E4 |
| UC-13 | Operator | Manage a sanctioned variant | E3 |
| UC-14 | Operator / NKE | Select a compatible release | E2, E4 |
| UC-15 | Fleet operator / SRE | Recover or roll back | E4 |
| UC-16 | NKE Eng / Publisher | Define the NKE software inventory | E4, E3 |

---

## 3. Sequencing

The epics run in order — each depends on the contracts and substrate of the prior ones:

```
E1 Design & Contracts   (deps: —)      freeze schemas, invariants, exit codes, the signer seam; run the spikes
      │
      ▼
E2 Build & Reproducibility (deps: E1)  foundation + recipe/IR + hermetic & no-compile build + profile/bake-off
      │
      ▼
E3 Trust, Verify & Evidence (deps: E1,E2)  executed signing + verify + variant + SBOM/provenance/vuln + offline
      │
      ▼
E4 Release, Delivery & CLI (deps: E1,E2,E3)  release sets + gates + AICR + pinning + delivery + CLI/SDK
```

Within the streams, some E4 sub-areas (delivery, AICR) can proceed in parallel with E3 once E2 lands. The **critical path**
is `E1 sprint-0 contracts → E2 foundation+build → E5-S1 executed cosign (E3) → E6 verify (E3) → E4 release`. Start E1's
sprint-0 freeze set (design-plan §2) and the reproducibility spikes (design-plan §6) first; they gate everything.

---

## 4. Cross-cutting decisions (kept from the design review)

These single-owner and boundary decisions were reconciled during design and still hold under the 4-epic grouping — full
rationale in `docs/design-plan.md` §3–§7:

- **One executed cosign seam** (`E5-S1`, E3) is the single Signer/Verifier owner; profile signing, verify, variant
  exceptions, offline verify, and publication signing all depend on it.
- **One canonical codec + digest-addressing** (design DD-A1) and **one IR-purity boundary** (DD-A2/C4) are frozen in E1 and
  implemented in E2's Foundation sub-area; no schema, identity, or exit code is decided twice.
- **The FR-13 canonical-disk primitive** (`LogicalDiskDigest` / envelope decode-verify) is owned in E2 (Reproducibility) and
  consumed by E4 (Delivery), not re-implemented.
- **E4 boundary discipline:** the release-lifecycle stories *produce* evidence and *re-selectable* verified sets — nkdctl
  consumes a handed-off conformance result and emits a clearance/verification result; it does not run the conformance suite,
  perform infrastructure rollback, or own cluster instantiation.

---

## 5. The four epics


---

### E1 — Design & Contracts

`kind: design` · `phase: 0` · `deps: — (foundation; everything depends on it)` · `detail: docs/design-plan.md`

**Goal.** Settle, before any critical-path code, the shared schemas, interfaces, invariants, and spikes that the implementation epics build on: the canonical serialization + digest-addressing codec, the IR-purity boundary, the frozen coded-error/exit-code table, the trust-contract types (Manifest / Report / VerifyAgainst seam / ValidationVector / ReleaseSet), the executed-cosign Signer/Verifier seam, the reproduction-identity tuple, and the composition merge algebra — plus the technical spikes that de-risk them.

**Outcome.** The trust cluster becomes a DAG rather than a cyclic knot, nothing is designed twice, and E2–E4 bind to stable contracts. This epic is the engineering expression of the `docs/design-plan.md` **sprint-0 freeze set**.

**Sprint-0 freeze set (ratify first — design-plan §2):** the canonical codec (DD-A1), the IR-purity partition + guard (DD-A2/C4), schemaVersion/migration (DD-A3), the banded exit-code table (DD-A7/G1/B3/D4), the reproduction tuple (DD-C1), the cosign Signer/Verifier interface (DD-B1/B6), the trust-store precedence (DD-B2), the release object-family skeleton (DD-H4/A4), the SDK facade + ctx/cleanup + output contract (DD-G2/G3/G4), package topology (DD-G6), and the composition merge algebra + sibling-ambiguity predicate (DD-E1/E2).

**Parallel spikes (design-plan §6):** two-machine bake-off (DD-C6), vendored-tree reproducibility (DD-H2), qemu-img decode determinism (DD-D3), ext4 residual eliminate-vs-bound (DD-D5), cancel-cleanup (DD-G3).

**Design stories (8 — one per domain).** Each story settles a cluster of related design decisions; the decisions total 54, with the full recommendation, schema, options, and open questions for each in `docs/design-plan.md` §8. Sprint-0 decisions (freeze before any critical-path code — design-plan §2) are flagged per story.

- **E1-A — Trust Contracts & Data Model**  `[design · 7 decisions · 5 sprint-0]`
  - *Scope:* The canonical codec + digest-addressing, the IR-purity boundary, the exit-code registry, and the trust-object schemas (Manifest, ValidationVector, ReleaseSet, Inventory, publication ledger).
  - *Decisions:* DD-A1, DD-A2, DD-A3, DD-A4, DD-A5, DD-A6, DD-A7
  - *Sprint-0 (freeze first):* DD-A1, DD-A2, DD-A3, DD-A4, DD-A7
- **E1-B — Signing & Verification Architecture**  `[design · 7 decisions · 4 sprint-0]`
  - *Scope:* The executed cosign Signer/Verifier seam, trust-store precedence, the offline bundle format, and verifier isolation.
  - *Decisions:* DD-B1, DD-B2, DD-B3, DD-B4, DD-B5, DD-B6, DD-B7
  - *Sprint-0 (freeze first):* DD-B1, DD-B2, DD-B3, DD-B6
- **E1-C — Reproducibility & Builder-Profile Model**  `[design · 7 decisions · 3 sprint-0]`
  - *Scope:* The reproduction-identity tuple, builder-profile signing, the hard-fail field policy, the IR-purity guard, and the two-machine bake-off protocol.
  - *Decisions:* DD-C1, DD-C2, DD-C3, DD-C4, DD-C5, DD-C6, DD-C7
  - *Sprint-0 (freeze first):* DD-C1, DD-C4, DD-C6
- **E1-D — Canonical Disk & Delivery Model**  `[design · 7 decisions · 1 sprint-0]`
  - *Scope:* The RAW-canonical disk manifest, the qemu-img envelope decode-verify seam, the pre-encryption/FDE boundary, role SKUs, and ISO/provisioning shapes.
  - *Decisions:* DD-D1, DD-D2, DD-D3, DD-D4, DD-D5, DD-D6, DD-D7
  - *Sprint-0 (freeze first):* DD-D4
- **E1-E — Composition & Resolution Semantics**  `[design · 6 decisions · 2 sprint-0]`
  - *Scope:* The normative merge algebra + sibling-ambiguity predicate, golden-digest fixtures, the scheme allowlist, the extension-surface classifier, and AICR axis matching.
  - *Decisions:* DD-E1, DD-E2, DD-E3, DD-E4, DD-E5, DD-E6
  - *Sprint-0 (freeze first):* DD-E1, DD-E2
- **E1-F — Supply-Chain Evidence & Serialization**  `[design · 7 decisions]`
  - *Scope:* The per-artifact SBOM identity, the SLSA provenance predicate, the evidence-serialization profile, external-evidence references, and vuln-report determinism.
  - *Decisions:* DD-F1, DD-F2, DD-F3, DD-F4, DD-F5, DD-F6, DD-F7
- **E1-G — CLI / SDK & Error Contract**  `[design · 6 decisions · 3 sprint-0]`
  - *Scope:* The coded-error table, the client/v1 SDK facade, context + subprocess cleanup, the output contract, the selector grammar, and package topology.
  - *Decisions:* DD-G1, DD-G2, DD-G3, DD-G4, DD-G5, DD-G6
  - *Sprint-0 (freeze first):* DD-G1, DD-G2, DD-G3
- **E1-H — Air-gap & Release-Lifecycle Flows**  `[design · 7 decisions · 1 sprint-0]`
  - *Scope:* The pinned-input/mirror model, vendored-tree reproducibility, toolchain mirrorability, the conformance-handoff contract, digest-first selection, and status declarations.
  - *Decisions:* DD-H1, DD-H2, DD-H3, DD-H4, DD-H5, DD-H6, DD-H7
  - *Sprint-0 (freeze first):* DD-H4


---

### E2 — Build & Reproducibility

`kind: implementation` · `phase: 1` · `deps: E1` · `stories: 40` · `owns: FR-01, FR-02, FR-03, FR-04, FR-05, FR-06, FR-07, FR-10, FR-12, FR-13, FR-20, FR-21, SEC-09, SEC-10, SEC-11`

**Goal.** Turn a criteria query into a single deterministic, digest-identified IR and build or acquire every component reproducibly and hermetically — the foundation substrate (contracts implemented), the recipe/composition/catalog front end, the from-source and no-compile lanes lowered identically to every CI engine, and the signed-builder-profile reproducibility boundary with a two-machine bake-off gate.

**Outcome.** An operator can resolve one recipe and reproduce every Kubernetes-substrate component byte-for-byte in their own CI on whichever engine they run — the trustworthy build half that everything downstream is measured against.

**Use cases advanced.** UC-01, UC-02, UC-03, UC-14

**Sub-areas:** Foundation · Recipe, Composition & Catalog · Hermetic From-Source Build & CI Portability · No-Compile (Prebuilt) Acquisition Lane · Reproducibility Boundary & Builder Profile

#### Foundation — canonical codec, coded errors, SDK skeleton, trust-contract types, context/IO  ·  stories E0-S*  ·  `poc: mixed`

Freeze, in one place and before any critical-path code, the cross-cutting substrate every other Epic binds to: the single canonical serialization + digest-addressing codec, the IR-purity boundary and its automated guard, the frozen coded-error/exit-code contract, the shared trust-contract types (Manifest/Report/VerifyAgainst seam/EvidenceBundle + the ValidationVector/ReleaseSet skeleton), and the client/v1 SDK skeleton with ctx-first, cancellable I/O.

- **E0-S1 — Canonical serialization codec + DigestAddressable seam (pkg/nkd/canonical)**  `[new · M · FR-03, DD-A1]`
  - *Intent:* Add one canonical codec (RFC-8785-JCS-style, domain-tagged digests) that every NEW trust object serializes and hashes through, ending the sigs.k8s.io/yaml vs json.MarshalIndent split and giving attest.Manifest the Digest() it lacks.
  - *Acceptance criteria:*
    - canonical.Bytes(v) emits UTF-8, lexicographically-sorted-key, insignificant-whitespace-free output; canonical.Digest(domain,v) = sha256:hex(sha256(domain\x00Bytes(v))) with a per-kind domain tag so a vector digest can never be mistaken for a manifest digest.
    - Every new trust type implements Marshal()/Digest() through it and attest.Manifest.Digest() is added; a golden-bytes + golden-digest test pins each type.
    - ir.Graph/recipeDigest stays on sigs.k8s.io/yaml (grandfathered, documented as the single codec exception); a test asserts the shipped f08265d2 is unchanged.
- **E0-S2 — IR-purity three-tier field partition + automated guard**  `[new · M · FR-03, DD-A2, DD-C4]`
  - *Intent:* Ratify and enforce which fields may enter recipeDigest-covered bytes (T1) vs signed metadata (T2) vs the detached record (T3), so host/clock/profile/engine identity never leaks into the IR.
  - *Acceptance criteria:*
    - A written invariant + a field-placement table land in the docs/checklist: T1 = resolved catalog inputs only; T2 = deterministic signed metadata (recipeDigest/profileDigest/expected sha256/vectorRef); T3 = detached build-record (host/clock/builder, every time value an operator-supplied input).
    - An AST lint over ir/recipe/compose/catalog fails on time.Now / math-rand / crypto-rand-for-ids / os.Hostname / uuid, allowlisted only by an explicit justifying comment.
    - A differential test mutating host TZ/HOME/hostname/kernel/wall-clock asserts recipeDigest AND the signed-manifest bytes are byte-stable, while mutating any pinned catalog input DOES change recipeDigest.
- **E0-S3 — schemaVersion + additive-only migration discipline**  `[new · S · DD-A3]`
  - *Intent:* Version every trust object and fix the rule for adding fields later so an evolution never perturbs already-published/covered bytes nor churns recipeDigest.
  - *Acceptance criteria:*
    - Every trust object carries schemaVersion "nkd.dev/<kind>/vN" as the canonical first field inside the hashed bytes.
    - A new field is ,omitempty and serializes to nothing when unset — proven by a zero-value golden byte-identical to the pre-field version; a breaking change is a new major + new digest domain (no silent reinterpretation).
    - Per-object golden-bytes tests pin both the zero-value and a fully-populated instance so a field reorder/format drift fails CI.
- **E0-S4 — Frozen banded coded-error / exit-code table + Retryable predicate**  `[harden · M · FR-20, DD-A7]`
  - *Intent:* Make FR-20 a proven, collision-free contract: reconcile every new code the epics name into ONE banded table (resolving the flagged slot-19/24 and 30-39 collisions) and assert observed process exit codes. Relocated from the former E14-S1.
  - *Acceptance criteria:*
    - The reconciled banded table lands (semantic 0-3; resolve/crypto 10-19; build/verify/trust/disk 20-29; retryable 30-31; release/lifecycle 40-51) with every Code once, statuses pairwise-distinct, none in the POSIX 128+signal range, snapshot-tested (docs/design-plan.md §3).
    - Retryable(err) is true iff Code in {Unavailable,Timeout}; network 5xx/dial/EOF classify as Unavailable, ctx deadline/cancel as Timeout.
    - A CLI table test spawns the binary for every fail-closed path and asserts the observed exit status; an audit test asserts every I/O/subprocess/serialization-boundary error is a *errors.Error (never bare fmt.Errorf).
- **E0-S5 — Shared trust-contract types + ValidationVector/ReleaseSet skeleton**  `[new · L · FR-08, FR-09, FR-18, DD-H4, DD-A4]`
  - *Intent:* Define, once, the trust types the whole cluster references — Manifest, pipeline.Report, the VerifyAgainst seam, EvidenceBundle — and land the pkg/nkd/release object-family SKELETON with frozen field lists + digest coverage, so E5/E6/E7/E12 bind to stable shapes instead of forming cycles.
  - *Acceptance criteria:*
    - pkg/nkd/release ships ValidationVector/ReleaseSet/Inventory/Clearance/Selection type skeletons with schemaVersion (v1alpha1), deterministic Marshal()+Digest() (sorted, no host/clock/random), a 100-marshal determinism test, and LogicalContentDigest/ContentManifestDigest reusing assemble.LogicalDiskDigest/BlockManifestOf.
    - attest.Manifest.ValidationVectorRef is defined as the vector content digest (string, back-compat) so a swapped vector is detectable with zero manifest-shape change.
    - The VerifyAgainst seam + pipeline.Report contracts are stable and consumed by both attest (produce) and verify (consume) as a ONE-WAY dependency — a test asserts no attest<->verify import cycle.
- **E0-S6 — client/v1 SDK facade skeleton (functional options, ctx-first)**  `[new · L · FR-20, DD-G2]`
  - *Intent:* Fulfil the thin-CLI-over-SDK invariant: move orchestration out of package main into a reusable pkg/nkd/client (v1) facade exposing every capability, built with documented functional options and ctx-first methods. Relocated from the former E14-S2.
  - *Acceptance criteria:*
    - client.New(opts ...Option) yields documented defaults with zero options; options carry seams/config only (WithDataProvider/WithDocker/WithHTTPClient/WithLogger/WithConcurrency/WithTimeout/WithSigner).
    - One ctx-first method per capability (Resolve/Bundle/Build/Fetch/Package/Attest/Verify/SBOM/BakeOff + Catalog sub-service) returns a typed result + coded *errors.Error; query/criteria go in Request structs so new axes are additive.
    - A golden-CLI test capturing stdout/stderr/exit for every command is committed BEFORE the relocation; an external client_test (importing only pkg/nkd/client, never package main) resolves a recipe and computes a stable recipeDigest.
- **E0-S7 — context threading + subprocess cleanup-on-cancel + bounded HTTP**  `[extend · L · FR-20, DD-G3]`
  - *Intent:* Bound and make cancellable every long/blocking op, and guarantee a cancel leaves no orphaned containers or stale output. Relocated from the former E14-S3/S4.
  - *Acceptance criteria:*
    - ctx is the first arg on every Client method and Runner.Run; every exec.Command in pipeline/local, pull, vendor, catalog/pin, sign becomes exec.CommandContext with WaitDelay + SIGTERM->SIGKILL reaping.
    - Each docker run gets --rm --init and a deterministic --name nkd-<runNonce>-<node> (nonce NOT in the IR); the scheduler keeps a cleanup registry and docker rm -f each on ctx.Done/exit; build/pull write to <out>.tmp and os.Rename only on success.
    - main.go installs signal.NotifyContext + a global --timeout; a cancel test (fake slow node) leaves zero orphaned nkd-* containers and zero .tmp dirs, returns the CodeTimeout exit, and writes no manifest; a single bounded, body-capped, context-aware http.Client is injected via WithHTTPClient.
- **E0-S8 — stdout=results / stderr=structured-logs output contract**  `[extend · M · FR-20, DD-G4]`
  - *Intent:* Separate diagnostics from results once and for all so result streams stay machine-consumable and logs never contaminate stdout. Consolidated from the former E14-S9.
  - *Acceptance criteria:*
    - A log/slog logger via WithLogger (default warn->stderr); all diagnostic fmt.Fprintf(os.Stderr,...) migrate to it; result streams (IR/SBOM/manifest/report) go to stdout or --out files ONLY.
    - Global --log-level + NKDCTL_LOG env + NO_COLOR are honored; report-producing commands accept --output json emitting a single deterministic document (sorted keys, no clock).
    - A capture test on resolve --output json asserts stdout is one valid parseable document with zero log lines, all logs on stderr, and NO_COLOR yields no escape bytes.
- **E0-S9 — Package topology + single-owner rule**  `[new · S · DD-G6]`
  - *Intent:* Name and own the new package surface up front so later Epics do not author overlapping types and package main never re-accretes orchestration.
  - *Acceptance criteria:*
    - Create pkg/nkd/{canonical,client}; RESERVE + document pkg/nkd/{provenance,vuln,release,status,mirror,trust} owned by their Epic (E8/E8/E12/E11/E9/E5).
    - A test asserts command Actions contain zero orchestration (no catalog.Embedded()/recipe.Resolve() in main) — only client calls; client_test is an external package.
    - Documented rules land: every new package returns *errors.Error, marshals deterministically (no clock/host/random), and host/builder/profile facts live only in the detached build-record/manifest, never the IR.

*Definition of done —*
- One canonical codec + DigestAddressable seam is used by every new trust object; recipeDigest stays f08265d2 (grandfathered), proven by test.
- The IR-purity invariant is enforced by an AST lint + a host-mutation differential test; no host/clock/random reaches recipeDigest or the signed-manifest bytes.
- The banded coded-error/exit-code table is frozen + snapshot-tested; a CLI table test asserts observed exit codes; Retryable() distinguishes transient failures.
- The shared trust contracts (Manifest.Digest/Report/VerifyAgainst/EvidenceBundle) and the pkg/nkd/release object-family skeleton exist with deterministic digests and a one-way attest->verify dependency.
- A pkg/nkd/client (v1) facade skeleton (functional options, ctx-first) exists; commands.go Actions are thin adapters; an external client_test embeds it without importing package main; I/O is context-bounded with cleanup-on-cancel; the stdout/stderr contract holds.

#### Recipe, Composition & Catalog  ·  stories E1-S*  ·  `poc: mostly-done-harden`

Harden nkdctl's resolution + composition + catalog front-end so any layered catalog flattens to exactly one deterministic, digest-identified IR and every ambiguity or unpinned/non-standard input fails closed with a distinct coded error.

- **E1-S1 — Fail closed on ambiguous sibling overlays in layered composition**  `[new · M · FR-01, FR-02]`
  - *Intent:* Extend FR-01's 'ties fail closed' guarantee into the layered path: today compose.findMatchingOverlays returns every maximal leaf and Compose merges equal-specificity, non-ancestor sibling leaves by name order, silently resolving a genuine conflict. Detect the conflict and fail closed like the flat resolver's CodeAmbiguous.
  - *Acceptance criteria:*
    - compose.Compose detects two or more maximal leaf overlays at equal Specificity() that are not in each other's spec.base chain and that both set a conflicting value on the same ComponentRef key (Enabled, or the same Overrides leaf path with differing values), and returns a coded error (CodeAmbiguous) naming the conflicting leaves (sorted) and the component/field.
    - Sibling leaves that touch DISJOINT components or set IDENTICAL values still compose cleanly (the existing orthogonal-leaf behavior in compose_test.go:101 is preserved for non-conflicting refs).
    - A single unambiguous most-specific leaf, and ancestor-chain merges (base←mid←leaf), remain unaffected — existing compose_test.go cases pass unchanged.
    - recipe.resolveLayered surfaces the error with the same exit code the flat CodeAmbiguous path yields; a new resolve_test.go case with a conflicting-sibling layered fixture asserts the exit code and deterministic message.
- **E1-S2 — Normative merge/conflict-resolution spec + golden-digest composition fixtures**  `[harden · M · FR-02, FR-03]`
  - *Intent:* FR-02 requires composition flatten 'according to documented merge and conflict-resolution rules.' The rules live only in code comments and CLAUDE.md. Write the normative spec and lock the flattened output with golden recipeDigest fixtures so any merge-semantics drift is caught.
  - *Acceptance criteria:*
    - docs/composition.md specifies, as normative rules: maximal-leaf selection, base←overlay chain child-wins ordering, deep-merge override semantics (scalar/slice replace, map merge), enabled:false disable semantics, mixin constraint no-compose (name collision fails closed), and the S1 sibling-ambiguity rule — cross-referenced to compose.go functions.
    - A golden test pins the composed recipeDigest for the embedded layered default (recipes/overlays + mixins) for control-plane and worker, amd64 and arm64, matching the documented values (e.g. control-plane amd64 f08265d2…) and failing on any unintended change.
    - A golden test pins the composed recipeDigest for examples/layered-catalog across its documented criteria combinations (operator/tenant × cp/worker), asserting distinct, stable digests.
    - docs/checklist.md §4 rows for the layered composition requirements are updated to cite the new spec + golden tests as evidence.
- **E1-S3 — Prove overlay overrides cannot unpin sources or downgrade toolchains**  `[harden · S · FR-04, FR-02]`
  - *Intent:* ComposeComponents already re-runs validateComponent on override-merged components, but the only negative test is a strict-decode typo. Add the security-relevant negative coverage so an operator-authored overlay override provably cannot reintroduce an unpinned commit or a tag-not-digest toolchain — closing a fail-closed hole on the composition seam.
  - *Acceptance criteria:*
    - A test asserts an overlay override that sets a component's toolchain to a tag (e.g. golang:1.24) fails closed via ComposeComponents with CodeTagToolchain.
    - A test asserts an override that blanks or shortens the pinned commit / treeHash fails closed with CodeUnpinnedSource.
    - A test asserts an override that flips fromSource=false without an exclusionReason (or otherwise violates the from-source XOR prebuilt XOR exclusion rule) fails closed with the existing coded error, while a benign override (versionStamp/env) still composes.
    - Each negative case is exercised end-to-end through recipe.resolveLayered so the mapped process exit code is asserted, not just the package error.
- **E1-S4 — Composition-order determinism guard + nondeterministic-source lint**  `[harden · M · FR-03]`
  - *Intent:* FR-03 forbids any host/clock/random/UUID input to the IR. Add an active guard that shuffling all discovery/iteration orders never perturbs recipeDigest across the layered path, plus a source-level test that fails if the digest-producing packages import or call nondeterministic APIs.
  - *Acceptance criteria:*
    - A test resolves+composes+compiles the layered default with overlay/mixin discovery order, ReadDir results, and Go map iteration deliberately shuffled across N runs and asserts a byte-identical recipeDigest every run.
    - A guard test scans ir, recipe, and compose package source (go/parser or import list) and fails if any imports time, math/rand, crypto/rand-for-ids, os.Hostname, or a uuid package, or calls time.Now — with an explicit allowlist comment for any justified use.
    - A perturbation test proves recipeDigest IS sensitive: changing one overlay component value or one pin field yields a different digest (immutable-definition => new identity).
    - Tests run under make test with no Docker/network dependency.
- **E1-S5 — Consolidated fail-closed input matrix across flat, layered, and prebuilt lanes**  `[harden · M · FR-04]`
  - *Intent:* Fail-closed input handling exists but is scattered. Build one table-driven negative-fixture suite that proves every unpinned/tag/unsupported input across all three resolution lanes fails closed with the correct DISTINCT exit code, so FR-04's guarantee is regression-locked and legible.
  - *Acceptance criteria:*
    - A table-driven test enumerates: unpinned/short commit, missing treeHash, tag-not-digest toolchain, per-arch prebuilt missing digest, declaring both from-source and prebuilt, reserved-axis non-empty, and unknown/absent outputType — each asserting the specific coded error (CodeUnpinnedSource/CodeTagToolchain/CodeReservedAxis/CodeInvalidCatalog) and its distinct mapped exit code.
    - The same negative inputs are proven to fail in BOTH a flat catalog and a layered catalog fixture (overlay/mixin/base variants), demonstrating no lane bypasses the gate.
    - A test asserts fail-closed occurs at resolution BEFORE any build/acquire node is emitted (no ir.Node produced for a failing recipe).
    - docs/checklist.md maps each row to its FR-04 evidence; exit-code assignments are documented as a stable set.
- **E1-S6 — First-class layered `catalog validate`, standard-interface enforcement, and --data isolation**  `[extend · M · FR-21, FR-04]`
  - *Intent:* FR-21 requires standard interfaces only plus an embedded default fully replaceable by --data. Make `catalog validate` a complete authoring gate for layered catalogs, enforce standard reference schemes, and assert --data replaces the embedded default with zero leakage.
  - *Acceptance criteria:*
    - nkdctl catalog validate on a layered catalog (base + overlays + mixins) performs schema validation AND a composition dry-run for representative criteria, reporting the first fault with a coded error and non-zero exit; a broken overlay (unknown base, dangling mixin, cycle, unknown component ref) is reported by name.
    - Component source/prebuilt references are validated to use only standard interfaces (git/https/oci schemes); a non-standard or scheme-less ref fails closed with a coded error.
    - A test proves --data <dir> fully replaces the embedded default: an operator catalog with an os value/component absent from the embedded default resolves with no code change, and the embedded default's components/overlays are provably NOT visible under --data (no provider leakage).
    - examples/layered-catalog and examples/operator-catalog both pass the enhanced catalog validate in CI-runnable (no-Docker) tests.
- **E1-S7 — Digest-selectable resolved-recipe selection record**  `[extend · M · FR-01, FR-21, FR-03]`
  - *Intent:* Advance UC-14 (select a compatible release from published info): emit, for a resolved query, a deterministic machine-readable selection record binding the concrete active criteria (role×k8sVersion×os×arch[, clusterType]) to the single winning recipe id and its recipeDigest, so a downstream consumer/NKE can pin exactly one recipe by digest through a standard interface.
  - *Acceptance criteria:*
    - nkdctl recipe (or a documented subcommand/flag) emits a deterministic JSON selection record: {criteria (resolved concrete axes), recipeId/coordinate, recipeDigest}, with no clock/host/random fields, byte-identical across runs.
    - The record proves exactly-one selection: a query resolving to one recipe yields one record; an ambiguous or no-match query yields the corresponding coded error and non-zero exit, never a record.
    - Per-arch resolution yields distinct records with distinct recipeDigests for the same criteria differing only in arch (multi-arch, native-per-arch model B).
    - A test round-trips the record and confirms the recipeDigest equals the IR digest produced by the compile path (single source of truth).

*Definition of done —*
- Every layered catalog flattens to exactly one deterministic IR: sibling-overlay conflicts fail closed with CodeAmbiguous, and the composition merge/conflict rules are documented in docs/composition.md and locked by golden recipeDigest fixtures.
- recipeDigest is proven order-invariant (shuffle guard) and free of host/clock/random inputs (source lint), while remaining sensitive to any real definition change.
- All unpinned/tag/unsupported inputs fail closed with distinct, documented exit codes across the flat, layered, and prebuilt lanes — including overlay overrides that attempt to unpin a source or downgrade a toolchain — with no build/acquire node emitted for a failing recipe.
- catalog validate is a complete fail-closed authoring gate for layered catalogs (schema + composition dry-run + standard-interface scheme check), and --data full replacement with zero embedded-default leakage is asserted by test.
- A deterministic digest-selectable recipe selection record is emitted for a resolved query, advancing UC-14, with recipeDigest proven equal to the compiled IR digest.
- docs/checklist.md §4 rows for FR-01/FR-02/FR-03/FR-04/FR-21 are refreshed to cite the new tests/docs as evidence; make test stays green with no Docker/network dependency.

#### Hermetic From-Source Build & CI Portability  ·  stories E2-S*  ·  `poc: mixed`

Turn the working one-IR-many-adapters build plumbing into a hardened, provably hermetic and least-privileged from-source build lane: every CI engine lowers the byte-identical container invocation the local runner executes, builds run isolated with no secret and no network leak, and every input/host/builder identity is recorded without perturbing recipeDigest.

- **E2-S1 — Prove adapter build invocation is byte-identical to the local runner**  `[harden · M · FR-05, FR-06]`
  - *Intent:* Upgrade the one-IR-many-adapters guarantee from 'carries the pins' to a structural equivalence proof: the build command each engine emits must be the exact argv the local runner executes, so 'byte-identical across engines' is enforced, not asserted by inspection.
  - *Acceptance criteria:*
    - A test extracts the concrete build invocation from each emitted engine document (github/gitlab/tekton/argo) and asserts it is character-for-character equal to the `docker run` argv local.go build() constructs for the same node (toolchain digest, --platform, --network=none, sorted -e env, workdir, command).
    - The equivalence test iterates every build AND pull node in the default layered recipe plus the operator-catalog example, so no component-specific divergence can slip through.
    - A deliberately introduced build-semantic difference in any single adapter (e.g. an extra -e or a dropped --network=none) fails the test, and the failure names the diverging engine + node.
    - buildLine/pullLines remain the single source of the invocation for all adapters (no per-engine build string is hand-built), verified by the test importing the same helper.
- **E2-S2 — Schema/lint-validate every emitted CI document in make check**  `[extend · M · FR-05]`
  - *Intent:* Guarantee each adapter emits a document the target engine will actually accept, not merely a plausible string, and freeze the topology/transport mapping with golden fixtures so an adapter change is reviewable as a diff.
  - *Acceptance criteria:*
    - make check renders each engine for a fixed recipe and validates it: GitHub via actionlint, GitLab against its CI JSON schema (or `glab ci lint` when available, else vendored schema), Tekton/Argo against their CRD OpenAPI schemas — validators shelled as subprocesses (Apache-clean), skipped-with-notice when a validator binary is absent so `make test` stays Docker/-network-free.
    - Golden fixtures for all five engines are committed; a divergence updates the golden via an explicit `-update` flag and shows as a reviewable diff.
    - Each golden asserts the required unconditional-verify idiom per engine (github `if: always()`, gitlab `when: always`, tekton `finally:`, argo finished-state depends) is still present.
    - CI runs the validation job; a schema-invalid emission fails the build with a distinct coded error, not a panic.
- **E2-S3 — Least-privilege container hardening lowered uniformly across all engines**  `[harden · M · SEC-11, FR-06]`
  - *Intent:* Make SEC-11 isolation a property of the IR contract, not an ambient default: builds and pulls run with dropped capabilities, no privilege escalation, non-root, and a read-only rootfs with an explicit writable output mount — applied by the local runner AND lowered identically by all four emitters.
  - *Acceptance criteria:*
    - local.go build()/pull() and emit_common.dockerRun both add `--cap-drop=ALL --security-opt=no-new-privileges --read-only --user <nonroot> --pids-limit` (writable output via the existing per-build WriteDir tmpfs/volume mount only), driven from a single shared helper so local and emitted invocations stay byte-identical (re-verified by E2-S1).
    - A catalog that requests `--privileged`, a host bind mount outside the source/output paths, or host namespaces fails resolution/compile with a distinct coded error (fail closed), covered by a test.
    - All 15 Tier-1 from-source components still build successfully under the hardened flags (recorded in poc.md); any component needing a relaxation declares it explicitly in the catalog rather than the runner relaxing globally.
    - A test asserts every build and pull node's rendered invocation (local + 4 engines) carries the full hardening flag set and that none carries a network mode other than none (build) / default (fetch+vendor+pull).
- **E2-S4 — Emit a deterministic build record capturing input, host, and builder identity**  `[new · L · SEC-11]`
  - *Intent:* Close the SEC-11 'record input, host, and builder identity' requirement (and the documented SLSA-provenance gap) by emitting a provenance record OUTSIDE the deterministic expected-digests manifest, so host/clock facts are captured for audit without ever perturbing recipeDigest.
  - *Acceptance criteria:*
    - `nkdctl build`/`attest` write a separate build-record artifact (SLSA-style predicate) capturing resolved inputs (recipeDigest, per-source repo+commit+treeHash, per-component toolchain digest, prebuilt content digests), the builder-profile identity+digest, and observed host facts from profile.ObserveHost (kernel, arch, runtime).
    - The build record is emitted to its own path (e.g. `--provenance-out`) and is byte-for-byte excluded from the expected-digests manifest and from recipeDigest — proven by a test that a host/clock change alters the build record but leaves manifest bytes and recipeDigest unchanged.
    - The input-facts subset of the record is deterministic (sorted, no clock/random) so two builders on the same profile emit identical input facts; only the host-observation and timestamp fields differ.
    - verify can cross-check the build record's recipeDigest/profileDigest against the manifest and fails closed with a distinct coded error on mismatch.
- **E2-S5 — Enforce and test secret exclusion from env, logs, and all artifacts**  `[harden · M · SEC-09]`
  - *Intent:* Make SEC-09 an enforced invariant instead of an implicit consequence: reject secret-shaped build env at load/compile, guarantee the runner passes only the allowlist (never host env / docker config), and scan every produced artifact for embedded secrets.
  - *Acceptance criteria:*
    - compile.go/load.go reject any catalog-supplied build/version-stamp env whose key matches a secret denylist (*TOKEN*, *SECRET*, *KEY* except allowlisted repro keys, *PASSWORD*, *CREDENTIAL*, AWS_*/GH_*) with a distinct coded fail-closed error; a test covers a rejected entry.
    - A test asserts local.go build/pull never inject os.Environ(), --env-file, or a ~/.docker/config.json / credential mount — only the sorted -e allowlist reaches the container.
    - A scanner test renders the manifest, build record, SBOM, and all four CI YAMLs for a recipe and asserts none contains a secret-shaped literal, and that any signing identity (cosign) appears only as a reference (OIDC identity / key ref), never an embedded private key.
    - Emitted CI documents reference secrets via the engine's native secret mechanism (e.g. ${{ secrets.* }} / masked variables) only where genuinely needed, never inlined — asserted by the scanner.
- **E2-S6 — Pin and integrity-verify the vendored dependency set**  `[extend · L · SEC-10]`
  - *Intent:* Close the SEC-10 hole where `go mod vendor` pulls modules under network AFTER the source tree hash is verified, leaving the vendored set neither integrity-checked nor covered by recipeDigest.
  - *Acceptance criteria:*
    - The vendor step runs with go.sum verification enforced (GOFLAGS mod discipline, GONOSUMCHECK not set, pinned GOPROXY/GOSUMDB or an operator-pinned proxy) and fails closed with a distinct coded error on any checksum mismatch or missing module.
    - After vendoring, the runner computes a digest over the materialized module set (or the go.sum) and the catalog pins it; a drift between the pinned vendor digest and the freshly vendored tree fails the hermetic build closed before compile.
    - The vendor pin is part of recipeDigest (a dependency bump is a recipe change, exactly like a source-commit bump), proven by a test that changing a pinned module digest changes recipeDigest.
    - A test with a tampered go.sum / substituted module content demonstrates fail-closed with the coded error and names the offending module.
- **E2-S7 — Negative hermeticity proof: builds cannot reach the network**  `[new · S · FR-06, SEC-11]`
  - *Intent:* Prove --network=none is actually enforced (not merely declared) and that vendoring is the only network-permitted node, closing the FR-06/SEC-11 assurance gap with an executable adversarial test.
  - *Acceptance criteria:*
    - A test runs a build node whose command attempts an outbound connection (sentinel curl/dial) under the normal runner path and asserts it fails, demonstrating --network=none isolation is real; the test is gated behind a Docker-available build tag so `make test` stays offline.
    - A structural test asserts exactly the fetch and vendor and pull nodes carry NetworkDefault and every build node carries NetworkNone, and that the vendor node is topologically ordered before all builds that consume its source.
    - The negative test also confirms the hardened container (E2-S3) blocks privilege escalation (a build attempting to gain new privileges fails).
    - Results and the hermeticity assurance argument are recorded in poc.md.

*Definition of done —*
- Each of the five engines (local/github/gitlab/tekton/argo) emits a schema-valid document whose build/pull invocation is proven byte-identical to the local runner's argv, including the least-privilege hardening flags.
- Every build and pull container runs least-privileged (cap-drop=ALL, no-new-privileges, non-root, read-only rootfs + explicit writable output) and hermetically (--network=none), with a negative test proving network and privilege-escalation are actually blocked.
- A deterministic build record captures resolved inputs + builder-profile + observed host facts, emitted outside the expected-digests manifest and provably excluded from recipeDigest.
- Secret-shaped env is rejected fail-closed at load/compile, the runner passes only the allowlist, and a scanner proves no secret is embedded in any manifest/build-record/SBOM/CI-YAML.
- The vendored dependency set is integrity-verified and pinned into recipeDigest, failing closed on any checksum/drift mismatch.
- All new fail-closed paths return distinct coded exit codes and are covered by tests; make check (fmt/vet/lint/test) stays green and offline.

---

#### No-Compile (Prebuilt) Acquisition Lane  ·  stories E3-S*  ·  `poc: mostly-done-harden`

Turn nkdctl's working prebuilt-acquisition plumbing into a fully hardened, tested, policy-governed lane: a catalog may declare a component acquired (not built) from an operator-approved, content-addressed source, and nkdctl fetches it by pinned per-arch digest, fails closed on any mismatch, records its identity plus verified external evidence in the same detached expected-digests manifest, and lowers the acquire+verify to all four CI adapters identically.

- **E3-S1 — Execute and test the attest->verify round-trip for a prebuilt-only recipe**  `[harden · M · FR-07]`
  - *Intent:* Prove the no-compile lane end-to-end through attest and verify (not just fetch): emit a detached expected-digests manifest for a prebuilt-only recipe, then re-verify it offline and fail closed on tamper — turning today's plumbing into an executed, tested capability.
  - *Acceptance criteria:*
    - `nkdctl attest` over a prebuilt-only recipe emits an expected-digests manifest whose per-component entry carries the acquired content digest (arch-scoped) and NO builderProfile digest field (asserted absent), because acquisition is host-independent.
    - `nkdctl verify --offline` over the same recipe re-confirms every prebuilt identity with no network — binaries by re-hashing the cached artifact, images by `docker image inspect` presence-by-digest — and exits 0.
    - Mutating any one manifest digest makes verify exit 22 (CodeVerifyFailed) naming that component as the first divergent node.
    - A test in `make test` (no Docker) drives the full emit->verify round-trip against an httptest server for binaries and an injected image-presence checker for images, covering the pass, tamper, and offline paths.
- **E3-S2 — Prove the XOR lane invariant and profile-independence on a mixed recipe**  `[harden · S · FR-07]`
  - *Intent:* Verify at recipe scale that a single recipe mixing from-source and prebuilt components compiles to one deterministic IR with both build and pull nodes under one terminal verify, and that only the from-source nodes carry a builder-profile digest.
  - *Acceptance criteria:*
    - A recipe with >=1 fromSource and >=1 prebuilt component compiles to one `ir.Graph` containing both `KindBuild` and `KindPull` producer nodes plus exactly one terminal `KindVerify` node depending on every producer (buildComponents + pullComponents).
    - recipeDigest (sha256 of the deterministic IR) is byte-stable across repeated compiles of the mixed recipe.
    - The emitted manifest attaches the builder-profile digest to from-source components only; prebuilt components carry none (profile-independence asserted at recipe scale).
    - A component declaring both `fromSource` and `prebuilt`, or `prebuilt` plus an `exclusionReason`, fails `Load`/resolve with CodeInvalidCatalog (extend the XOR check beyond the unit-level TestPrebuiltFailClosed to the full load+resolve path).
- **E3-S3 — Assert byte-parity of pull acquisition across all four CI adapters and the local runner**  `[harden · S · FR-07]`
  - *Intent:* Guarantee one-IR-many-adapters for the no-compile lane: the acquire+verify command text the four emitters lower must be identical to what the local runner executes, and adapters must never carry acquisition or verification semantics of their own.
  - *Acceptance criteria:*
    - A golden test asserts `pullLines(n)` for a binary node equals the exact command sequence `Local.pullBinary` runs (curl -fsSL <url> -o <name>; sha256sum -c of the hex pin) and for an image equals `docker pull <ref>@<digest>`.
    - All four emitters (github/gitlab/tekton/argo) embed those exact lines per pull node and place the terminal verify depending on every pull job unconditionally (github `if: always()`, gitlab `when: always`, tekton `finally:`, argo finished-state `depends`).
    - The command text extracted from the four emissions is identical (differs only in topology/transport wrappers), asserted by a normalized-line comparison.
    - An emitter given a pull node with an empty Digest fails closed with CodeUnpinnedSource rather than emitting an unverifiable acquire step.
- **E3-S4 — Enforce an operator-approved prebuilt-source allowlist, fail closed**  `[new · M · FR-07]`
  - *Intent:* Satisfy UC-03's 'operator-approved source' and SRD §1.1 'policy-approved prebuilt artifacts': a catalog may declare which hosts/registries prebuilt refs may come from, and any ref outside that policy fails closed with a distinct coded error.
  - *Acceptance criteria:*
    - The registry may declare `prebuiltSources:` — an allowlist of permitted host/registry prefixes; when declared it is enforced at load and resolve, and a prebuilt url or image whose host is not on the allowlist fails closed with a distinct coded error (e.g. CodePolicyViolation), naming the component and the offending host.
    - When no allowlist is declared the lane's behavior is unchanged (backward-compatible), and this default-permit is documented as an explicit, legible choice.
    - The resolved, approved source policy is recorded as provenance so a change to the allowlist is a catalog change (visible in the lock/IR diff), not a silent relaxation.
    - Tests cover an in-allowlist ref validating, an out-of-allowlist ref failing closed with the coded error, and the embedded from-source default catalog being unaffected.
- **E3-S5 — Support digest-verified tarball prebuilt deliveries with deterministic extraction**  `[extend · L · FR-07]`
  - *Intent:* Broaden the lane beyond single binaries and images to archive (.tar.gz/.tgz) artifacts — how crictl, cni-plugins, containerd, and helm ship prebuilt — verifying the archive digest then extracting declared members deterministically into a reproducible .deb.
  - *Acceptance criteria:*
    - A prebuilt component may deliver a `.tar.gz`/`.tgz`: the pull node verifies the archive's sha256 against the per-arch pin (fail closed, CodeVerifyFailed) BEFORE any extraction.
    - A catalog field (e.g. `prebuilt.extract: [bin/crictl]`) selects member path(s); a requested member absent from the archive fails closed with a coded error.
    - Extraction is deterministic — sorted entries, normalized mode, mtime pinned to SOURCE_DATE_EPOCH, no host paths — so the produced host-package (.deb) digest is byte-identical across two runs.
    - Tests over an httptest-served fixture archive cover digest-verified extraction, reproducible package bytes across runs, a wrong archive digest failing closed (exit 22), and a missing member failing closed.
- **E3-S6 — Record and verify external evidence references for prebuilt artifacts**  `[new · L · FR-07]`
  - *Intent:* Close UC-03's 'verify their identities AND supporting evidence' half: a prebuilt component may reference the upstream artifact's SBOM, provenance, and signature, and nkdctl records those as verified, digest-pinned references in the manifest and re-confirms them on verify.
  - *Acceptance criteria:*
    - A prebuilt component may declare `evidence:` references (upstream SBOM and provenance as url+pinned-digest, and/or a signature identity); each reference is fetched and fails closed if its bytes do not match the pinned digest.
    - `attest` records the verified evidence references per prebuilt component in the expected-digests manifest; a component with no evidence is marked `evidence: none` explicitly (legible, never silently absent).
    - `verify --offline` re-confirms each evidence reference is present and digest-matches against a mirror, and fails closed with a coded error naming the component when one is missing or divergent.
    - Signature-identity (cosign) verification is wired through the seam owned by the signing epic; where cosign is not yet executed it is a fail-closed, documented TODO — no evidence reference is ever silently treated as verified.
- **E3-S7 — Thread context, timeouts, and bounded retry through all no-compile I/O**  `[harden · M · FR-07]`
  - *Intent:* Harden the lane's network robustness deterministically: every HTTP and docker invocation in fetch and pin-artifacts must honor a caller context deadline and retry transient failures within a capped budget, replacing the timeout-less http.Get in pin-artifacts.
  - *Acceptance criteria:*
    - All no-compile HTTP I/O (pull download, pin-artifacts sidecar/artifact GET, httpGetBody) uses an http.Client with a context deadline threaded from the command; the timeout-less `http.Get` in pin-artifacts is removed.
    - Transient failures (5xx, connection reset, timeout) retry with bounded, capped exponential backoff; a permanent failure returns the existing coded error (CodeFetchFailed) after the cap.
    - `docker pull`, `docker manifest inspect`, and `docker image inspect` run under a context timeout and are cancelled on parent cancellation.
    - Tests cover: an httptest server that 503s N-1 times then 200s succeeds within budget; one that always 503s fails closed with CodeFetchFailed after the cap; a cancelled context aborts promptly without leaving a partial file.
- **E3-S8 — Harden catalog pin-artifacts: sidecar cross-check and stale-pin drift gate**  `[harden · M · FR-07]`
  - *Intent:* Make the machine-managed artifact-pin story trustworthy and drift-aware: cross-check a .sha256 sidecar against a fresh hash of the artifact, and provide a check mode that fails CI when a prebuilt pin is stale or missing an arch.
  - *Acceptance criteria:*
    - `resolveBinaryDigest` cross-checks the `.sha256` sidecar against a fresh download-and-hash of the artifact and fails closed (coded error) on divergence, so a MITM cannot win by serving a matching poisoned pair (or sidecar-trust is opt-in via a flag defaulting to verify-both).
    - `catalog pin-artifacts --check` exits non-zero, writing nothing, when any requested prebuilt component lacks a pin for a requested arch or its lock `tag` differs from the component's resolved source `tag` (stale-pin drift gate for CI).
    - The lock is written deterministically (sorted components and arches, stable formatting) so a no-op re-pin produces an empty diff (preserving the existing `already pinned` fast path).
    - Tests cover sidecar/artifact divergence failing closed, a stale-tag lock being flagged by `--check`, and `--check` exiting non-zero on a missing per-arch pin.

*Definition of done —*
- A prebuilt-only recipe passes an executed, tested attest->verify--offline round-trip in `make test`, with tamper failing closed at exit 22 and the manifest carrying no builder-profile digest.
- A mixed from-source + prebuilt recipe compiles to one deterministic IR under a single terminal verify, with the XOR invariant enforced at load/resolve and profile-independence of prebuilt nodes proven.
- All four CI adapters lower every pull node to command text byte-identical to the local runner's acquire+verify, asserted by a golden test; an unpinned pull node fails closed.
- The lane supports binary, tarball (deterministic extraction), and image deliveries, each digest-verified per arch and fail-closed on mismatch, with an enforced operator-approved-source allowlist and recorded/verified external-evidence references.
- All no-compile HTTP/docker I/O honors context deadlines with bounded retry, and `catalog pin-artifacts` cross-checks sidecars and exposes a `--check` drift gate.
- `make check` is green; docs/checklist.md FR-07 evidence is refreshed to reflect executed (not plumbing-only) verify and the new allowlist/evidence/tarball capabilities.

#### Reproducibility Boundary & Builder Profile  ·  stories E4-S*  ·  `poc: mixed`

Turn nkdctl's reproducibility contract from plumbing into an executed, tested, honestly-bounded guarantee: every byte-identity claim is scoped to a signed, digest-identified builder profile whose fail-closed preflight runs before any input resolves, the two-environment bake-off becomes a CI-enforced gate that also crosses two distinct machines, and disk claims attach unambiguously to the RAW-canonical logical stream with qcow2/vmdk/vhd verified by their decoded logical-disk digest (pre-encryption only).

- **E4-S1 — Gate profile preflight strictly before any input/source resolution**  `[harden · M · FR-12]`
  - *Intent:* FR-12 requires the fail-closed host preflight to run BEFORE build inputs resolve, but runLocalBuild currently resolves the recipe (and the local runner then fetches sources/toolchains) after nothing has checked the host. Reorder so a hard-fail host divergence stops the run before any source ref, toolchain image, or lock entry is touched, and prove the ordering with a test.
  - *Acceptance criteria:*
    - In runLocalBuild (and the fetch/attest paths that build from source), preflightGate runs before resolveRecipe/source resolution; a hard-fail divergence returns CodePreflightFailed with no source fetch or toolchain pull attempted.
    - A test injects a profile whose target.arch hard-fail field cannot match the host and asserts the build aborts before any pipeline.Local.Run/source-root read occurs (e.g. via a fake runner that records whether it was invoked).
    - --skip-preflight still records advisory divergences into the evidence bundle but does not fetch inputs when a hard-fail would otherwise abort is explicitly documented and covered by a test.
    - The all-prebuilt (no from-source component) path continues to skip the builder-profile preflight (recipeHasSource guard preserved) with a test.
- **E4-S2 — Recipe-declared supported-profile set with ≥1 operator-controlled enforced**  `[extend · L · FR-12]`
  - *Intent:* Profiles are currently arch-derived embedded defaults; FR-12 requires each recipe to permit at least one operator-controlled profile and to bind reproduction to a member of that declared set. Add a catalog-level supported-profile declaration, validate it fail-closed, and make profile selection resolve against it.
  - *Acceptance criteria:*
    - Catalog schema gains a per-recipe/registry `profiles:` list of {identity, digest, hostControlClass}; catalog validate fails closed (CodeInvalidCatalog) when a recipe with a from-source component declares zero operator-controlled profiles.
    - resolveProfile selects from the recipe's declared set (matching --profile or the default) and fails closed with a distinct coded error when the requested profile is not a declared member.
    - The embedded default recipes/ registry declares its operator-controlled profile explicitly; a golden test asserts validation passes and that removing the operator-controlled entry fails closed.
    - Selection is deterministic (declared set sorted by digest) and the selected profile digest is unchanged across repeated resolves.
- **E4-S3 — Sign and verify the builder profile; authenticate profileDigest in the trust chain**  `[new · L · FR-12]`
  - *Intent:* FR-12 says 'signed builder profile' but the profile is only digest-identified — cosign is plumbing-only. Produce a detached signature over the canonical profile document and make verify authenticate the profile the manifest is bound to, so an unsigned or tampered profile cannot back a Reproduced verdict.
  - *Acceptance criteria:*
    - profile Marshal() canonical bytes are the signing payload; a `nkdctl profile sign` (or attest --sign extension) emits a detached signature + certificate over those bytes using the existing sign.SignBlobArgs path (never embedded in the profile).
    - verify --require-signature fails closed with a distinct coded error when the profile bound by manifest.ProfileDigest lacks a valid detached signature or its recomputed digest ≠ the manifest's profileDigest.
    - A test drives the cosign invocation through a mockable subprocess seam and asserts: tampered profile bytes → digest mismatch → fail closed; matching signed profile → pass; no NVIDIA-hardcoded issuer/identity.
    - The signed-profile requirement is skipped (not spuriously failed) for all-prebuilt manifests that carry no profileDigest.
- **E4-S4 — Bind the full reproduction tuple (recipe × engine × input-manifest × profile)**  `[harden · M · FR-12, FR-10]`
  - *Intent:* VerifyAgainst today only compares recipeDigest + profileDigest, but FR-12 asserts byte-identity holds only within the same recipe/engine/input-manifest/profile tuple. Record engine identity and the resolved input-manifest (lock) digest on the manifest and make cross-tuple comparisons fail closed as 'different build', not 'variant'.
  - *Acceptance criteria:*
    - attest.Manifest gains engineId and inputManifestDigest (sha256 of the resolved pins.lock/effective inputs) fields, populated deterministically (no host/clock/random) and serialized in the sorted, deterministic manifest.
    - VerifyAgainst treats an engine or input-manifest mismatch as a fatal, non-variant error with a distinct coded exit — parallel to the existing recipeDigest/profile mismatch handling — naming which tuple field diverged.
    - recipeDigest is unaffected (the new fields live on the manifest, not the IR); a test asserts recipeDigest is byte-stable before/after this change.
    - A test builds the same recipe under two engine ids and asserts VerifyAgainst refuses to call it Reproduced.
- **E4-S5 — Two-machine bake-off protocol: split emit/compare, isolate env, wire CI**  `[harden · L · FR-10]`
  - *Intent:* cmdBakeOff proves same-machine varied-env identity only, mutates process env globally, and is a manual command. Restructure it into a portable emit-then-compare protocol that two physically distinct runners execute, so the FR-10 two-environment gate is real, not within-machine.
  - *Acceptance criteria:*
    - bake-off gains an emit mode that writes a signed expected-digests manifest per environment and a compare mode that reuses attest.VerifyAgainst to assert bit-identity across two independently-produced manifests, failing closed (distinct coded exit) and naming the first divergent component.
    - Environment isolation no longer leaks: each run's TZ/HOME/build-path are scoped to that run (no persistent os.Setenv), verified by a test asserting process env is unchanged after bake-off returns.
    - All four CI emitters (github/gitlab/tekton/argo) can lower the two-runner bake-off to two distinct runner labels/images that each emit a manifest and a final compare job — one IR, many adapters, no build-semantics divergence between the two environments.
    - docs/poc.md and checklist NKD-FR-BLD-070/REP-005 are updated to state exactly which axes were crossed (two distinct machines) and the compare job's exit code on mismatch.
- **E4-S6 — Bake-off as a CI-enforced go-test gate over deterministic fixtures**  `[extend · M · FR-10]`
  - *Intent:* The bake-off is only a manual CLI invocation, so nothing in `make test`/CI guards it. Add a deterministic, Docker-free test gate over the manifest-compare path so a regression that breaks bit-identity fails the standard local gate.
  - *Acceptance criteria:*
    - A go test drives attest.VerifyAgainst over fixture manifests representing environment A and B: identical inputs → Reproduced; a single perturbed component digest → fail closed with the distinct CodeVerifyFailed and the named first-divergent component.
    - The test asserts the compared manifests are byte-deterministic (re-marshal round-trips equal) so the gate itself is reproducible and adds no Docker/network dependency to `make test`.
    - A regression that reintroduces an unpinned/host value into buildEnv (e.g. drops SOURCE_DATE_EPOCH or GOFLAGS) is caught by a companion test asserting the reproEnvKeys guard rejects a catalog attempting to override those keys.
    - make check runs the new tests green in CI (no from-source Docker path required).
- **E4-S7 — Derived-envelope decode-verify: qcow2/vmdk/vhd → RAW logical-disk digest**  `[new · L · FR-13]`
  - *Intent:* FR-13's load-bearing half is missing: a qcow2 is only ever produced, never decoded and checked against the RAW canonical digest. Implement the decode-verify path so a derived envelope reproduces iff its decoded logical LBA stream matches the published RAW logical-disk digest, and wire it into cmdDisk and verify.
  - *Acceptance criteria:*
    - A new assemble function decodes an envelope to its logical stream by shelling qemu-img convert (Apache-clean subprocess seam, fail-closed CodeUnsupported if the tool is absent) and returns LogicalDiskDigest of the decoded stream — never the container-file digest.
    - cmdDisk emits a DiskManifestRef (logicalDigest + blockSize + blockCount from BlockManifestOf) into the evidence/manifest for every disk artifact it produces.
    - verify accepts an envelope + expected logical digest, decodes it, and fails closed (distinct coded error) naming the first divergent block via BlockManifestOf when the decoded stream differs; a matching qcow2 and its source RAW verify to the identical digest in a test.
    - A test asserts two byte-different qcow2 containers (different writer padding/UUID) that decode to the same RAW stream both verify Reproduced, proving the assertion attaches to the decoded stream, not the container.
- **E4-S8 — Pre-encryption boundary marker + fail-closed on encrypted/FDE envelopes**  `[extend · M · FR-13]`
  - *Intent:* FR-13 scopes byte-identity to the pre-encryption image (FDE is an overlay). Make that boundary explicit and enforceable so an encrypted envelope can never be silently hashed as ciphertext and mistaken for a reproduction.
  - *Acceptance criteria:*
    - The DiskManifestRef records that logicalDigest is computed over the pre-encryption logical stream (an explicit boundary marker field).
    - The decode-verify path detects an encrypted/LUKS-wrapped or qcow2-`encrypt`ed envelope and fails closed with a distinct coded error rather than hashing ciphertext, with a message pointing to the pre-encryption boundary.
    - A test feeds an encrypted envelope and asserts the fail-closed coded exit; docs (poc.md/checklist NKD-FR-BLD-063) state FDE is an overlay and byte-identity is asserted on the pre-encryption image only.
    - The plain-RAW and derived-envelope paths remain unchanged for unencrypted inputs (regression test).
- **E4-S9 — Surface the pinned reproducibility-flag discipline into the published manifest**  `[harden · M · FR-12, FR-10]`
  - *Intent:* The repro flags (SOURCE_DATE_EPOCH policy, GOFLAGS -trimpath -buildvcs=false, -ldflags=-buildid=, TZ/LC_ALL) are in the IR and thus in recipeDigest, but a verifier holding only the published manifest cannot see WHICH flag diverged. Record the flag set (or its per-component digest) on the manifest so a mismatch is diagnosable without NVIDIA reference bytes.
  - *Acceptance criteria:*
    - The manifest/evidence bundle records the pinned reproducibility-env set per component (or a stable digest of it) sourced from buildEnv, serialized deterministically (sorted, no host/clock/random).
    - The evidence bundle produced by verify on a divergence names the component AND the reproducibility-env delta (or its digest mismatch) so the cause is diagnosable from published bytes alone — asserted by a test that perturbs one env key and checks the evidence output.
    - A test proves the recorded env set is byte-identical to what buildEnv pins and that reproEnvKeys still fails closed on a catalog override attempt (SOURCE_DATE_EPOCH/GOFLAGS cannot be shadowed).
    - recipeDigest is unchanged by adding this manifest-side field (round-trip stability test).

*Definition of done —*
- Profile preflight is proven (by test) to fail closed before any input/source resolution, and each from-source recipe declares ≥1 operator-controlled profile enforced by catalog validate.
- A builder profile is signed and verify --require-signature fails closed on an unsigned/tampered/mismatched profile; the Reproduced verdict is bound to (recipe × engine × input-manifest × signed-profile) with recipeDigest unchanged.
- The two-environment bake-off runs as a CI-enforced gate (go-test path green in make check) and a documented two-distinct-machine emit/compare protocol, failing closed with a distinct coded exit naming the first divergent node.
- A qcow2/vmdk/vhd envelope is verified by its decoded RAW logical-disk digest (not the container bytes), fails closed on an encrypted/FDE envelope, and records a pre-encryption DiskManifestRef.
- A byte divergence is diagnosable from the published manifest/evidence bundle alone — reproducibility-flag discipline surfaced, no NVIDIA reference bytes required — with all new fail-closed paths returning distinct coded exit codes and covered by tests.


---

### E3 — Trust, Verify & Evidence

`kind: implementation` · `phase: 2` · `deps: E1, E2` · `stories: 35` · `owns: FR-08, FR-09, FR-11, FR-15, FR-19, SEC-01, SEC-02, SEC-03, SEC-04, SEC-05, SEC-06, SEC-07, SEC-14, SEC-15, SEC-18`

**Goal.** Make the zero-binary trust model real and enforceable — executed detached keyless signing + a trust store, a fail-closed isolated verifier that names the first divergent node, the sanctioned expiring ValidatedVariant lane, deterministic CycloneDX SBOM + SLSA provenance + vulnerability evidence, and equivalent-trust offline / air-gapped operation.

**Outcome.** A publisher ships only a signed, detached expected-digests manifest; any operator or auditor independently verifies byte-identity, signatures, provenance, and SBOM — online or air-gapped — and a sanctioned representation-only variance is governed, not faked.

**Use cases advanced.** UC-01, UC-02, UC-07, UC-09, UC-12, UC-13, UC-16

**Sub-areas:** Attestation, Detached Signing & Trust · Verification Engine · ValidatedVariant Lane · Supply-Chain Evidence · Offline / Air-Gapped Operation

#### Attestation, Detached Signing & Trust  ·  stories E5-S*  ·  `poc: mixed`

Turn nkdctl's cosign plumbing into real, executed, tested detached keyless signing and verification of the expected-digests manifest, and add the missing trust primitives (operator-configurable OIDC issuer/identity, a persisted trust store, and digest-addressable/integrity-protected release objects) so an operator can publish only a signed detached manifest and any auditor can verify it — offline included — with fail-closed coded errors on every trust boundary.

- **E5-S1 — Execute cosign for real behind a testable Signer/Verifier seam with an end-to-end roundtrip test**  `[harden · L · FR-08, SEC-04, SEC-03]`
  - *Intent:* Convert the plumbing-only cosign path into an executed, tested capability. Introduce a Signer/Verifier interface in pkg/nkd/sign (exec-backed real impl + injectable fake) so cmdAttest/cmdVerify depend on the seam, and prove a full sign→verify→tamper-fails cycle actually runs cosign, not just builds args.
  - *Acceptance criteria:*
    - pkg/nkd/sign exposes a Signer and Verifier interface with an exec-cosign implementation and an injectable fake; cmdAttest and cmdVerify call the interface (not sign.Run directly), so tests can substitute the fake with no cosign binary present.
    - A hermetic roundtrip test signs a manifest with a locally generated cosign key-pair (cosign generate-key-pair + --key, no Fulcio/OIDC network) and then verifies it, asserting exit 0; the test is skipped with a clear message (not failed) when cosign is not on PATH.
    - A negative test mutates one byte of the signed manifest and asserts verify-blob exits non-zero and nkdctl surfaces e.CodeVerifyFailed naming the signature failure before any build runs.
    - An integration test (gated behind an env-provided OIDC token / CI) exercises the keyless Fulcio path end-to-end and asserts a Rekor entry is produced; absence of the token skips, never fails.
    - docs/checklist.md rows NKD-FR-INT-026/036 are updated from 'partial (untested)' to 'done' with the executed-test evidence path recorded.
- **E5-S2 — Operator-configurable OIDC issuer + identity at sign time**  `[extend · M · INT-027, SEC-04, FR-08]`
  - *Intent:* Close INT-027: today attest has no way to select the signing OIDC issuer/identity, only --sign. Add operator-configurable keyless signing inputs threaded into cosign sign-blob, with fail-closed behavior when keyless is requested but no OIDC identity is resolvable.
  - *Acceptance criteria:*
    - nkdctl attest accepts --oidc-issuer, --identity, and --identity-token (and honors standard COSIGN/SIGSTORE OIDC env vars) and threads them into sign.SignOptions → cosign sign-blob args; no NVIDIA-specific issuer or identity is ever defaulted or hardcoded.
    - Requesting --sign keyless with neither an ambient OIDC token nor an explicit --identity-token/--oidc-issuer fails closed with a distinct coded error (e.CodeUnsupported or e.CodeVerifyFailed) rather than silently producing an unusable or interactive signature.
    - A test asserts the constructed sign-blob args include the operator-supplied issuer/identity flags in a stable order and that arg construction is byte-stable across runs (deterministic, no map iteration).
    - The produced detached signature+certificate verify against the same issuer/identity via the S1 roundtrip test, proving the sign-time and verify-time identity plumbing agree.
- **E5-S3 — `nkdctl trust` subcommand + persisted, digest-addressable trust store**  `[new · L · INT-046, SEC-04, SEC-02]`
  - *Intent:* Add the missing trust-management surface (INT-046): a persisted set of pinned (OIDC issuer, certificate identity) entries that verify resolves from when CLI flags are omitted, so an operator manages their trusted signers once and fails closed on anything not pinned.
  - *Acceptance criteria:*
    - nkdctl trust add|list|remove|show manages a persisted trust store (deterministic canonical YAML/JSON, sorted entries, each entry = {issuer, identity, optional note}); the store file location is operator-configurable and never contains host/clock/random fields.
    - verify resolves the expected signer issuer/identity from the trust store when --identity/--issuer are not passed; if the signing certificate's issuer/identity is not present in the store (and none supplied on the CLI), verify fails closed with a distinct coded error naming the untrusted signer — never trusting an unpinned identity.
    - A catalog/recipe MAY declare a trust identity that seeds trust show (catalog-sourced trust), but it is advisory: verify still requires the operator's store/flag to accept it, and the resolution precedence (CLI flag > operator store > catalog advisory) is documented and tested.
    - Tests cover add/list/remove roundtrip, deterministic store serialization, and the fail-closed 'signer not in trust store' path with the correct exit code.
- **E5-S4 — Enforce SEC-03 zero-binary: detached-only, signature never perturbs covered bytes**  `[harden · M · SEC-03, FR-08]`
  - *Intent:* Make the zero-binary invariant an enforced, tested guarantee rather than a convention: signing must only ever produce sidecar files and must never embed a signature into any bytes the reproducibility contract covers (the manifest, OCI images, .deb).
  - *Acceptance criteria:*
    - A test reads the manifest bytes before and after `attest --sign`/`--bundle` and asserts they are byte-identical (signature/cert/bundle are written only to sidecar paths out.sig/out.pem/out.bundle), proving the signature never mutates the manifest the digests are computed over.
    - attest/package fail closed with a distinct coded error if asked to embed a signature into an artifact whose bytes are part of recipeDigest/expected-digests (e.g. an OCI image or .deb target) — the detached-only path is the only path.
    - The signed manifest carries no embedded signature, certificate, or Rekor payload field (asserted by parsing the manifest JSON and checking those keys are absent); the trust material lives exclusively in detached sidecars.
    - docs/checklist.md / poc.md record that zero-binary detachment is enforced by test, not just documented.
- **E5-S5 — SEC-02 digest-authentication chain: an unauthenticated digest is never a trusted reference**  `[extend · M · SEC-02, SEC-04]`
  - *Intent:* Bind the signature check to the digest check so that when trust is required, every expected digest nkdctl treats as authoritative is one carried by the signed manifest — closing the gap where verify's signature verification and its digest comparison are independent.
  - *Acceptance criteria:*
    - When a signature/bundle is verified (or --require-signature is set), verify enforces that the expected digests it compares against come from the same bytes that passed cosign verify-blob — i.e. the digest set is authenticated by the signed vector — and refuses to trust any expected digest not covered by that signed manifest.
    - verify fails closed with a distinct coded error if asked to use an external/side-loaded expected-digests reference (or a digest) that is not authenticated by the signed manifest currently being verified.
    - A test proves that a manifest whose recipeDigest/component digests were altered after signing fails verify at the signature stage (before build), and that a valid signature over the exact manifest is required for any digest in it to be treated as a trusted reference.
    - The precedence is documented: with --require-signature, a byte-identity match against an unsigned manifest is NOT a pass — the signature gate runs first and independently produces a fail-closed exit code.
- **E5-S6 — Integrity-protect and independently digest-address recipes, pipeline bundles, validation vectors, and release sets**  `[new · L · SEC-01]`
  - *Intent:* Satisfy SEC-01 by making each trust-relevant object canonically digest-addressable and by upgrading the validation vector from a bare string ref to a first-class integrity-protected object, plus an optional ReleaseSet binder that ties the four digests together under one signable object.
  - *Acceptance criteria:*
    - nkdctl computes and records a canonical sha256 for each of: the recipe/IR (already recipeDigest), the emitted pipeline bundle, the validation vector, and the release set — each serialized deterministically (sorted, no host/clock/random) so the digest is reproducible and independently recomputable.
    - attest.Manifest's ValidationVectorRef is upgraded (back-compatibly) to carry {ref, digest}; the digest binds the referenced validation vector so a swapped vector is detected, and a mismatch fails closed with a distinct code.
    - A ReleaseSet object binds {recipeDigest, manifestDigest, validationVectorDigest, pipelineBundleDigest} deterministically and is itself sign-able via the S1 Signer seam; a test asserts the ReleaseSet digest is stable across runs and changes iff any bound digest changes.
    - Tests verify each object is independently addressable (recomputing its digest from bytes reproduces the recorded value) and that tampering with any one bound object is detected via its recorded digest.
- **E5-S7 — Executed Rekor transparency-log inclusion + offline inclusion-proof verification**  `[harden · M · SEC-04, SEC-03]`
  - *Intent:* Prove the transparency-log half of SEC-04 actually works: produce a mirrorable bundle carrying a Rekor inclusion proof and verify it offline, and test the fail-closed airgap guards end-to-end rather than only asserting arg strings.
  - *Acceptance criteria:*
    - An integration test (gated on cosign + Rekor/OIDC availability) produces a --bundle with a real Rekor inclusion proof at sign time and verifies it with --bundle --offline (no network), asserting exit 0.
    - A test proves --offline without a --bundle fails closed with the documented coded error, and that --insecure-ignore-tlog is the only way to skip the tlog and is never implied by --offline (regression-locks the existing guard with an executed path, not just arg inspection).
    - A negative test corrupts the inclusion proof in the bundle and asserts offline verify-blob exits non-zero and nkdctl reports e.CodeVerifyFailed.
    - docs/checklist.md NKD-FR-REP-023/024 evidence is updated to reference the executed inclusion-proof test rather than arg-construction only.

*Definition of done —*
- A full detached keyless sign→verify→tamper-fails cycle actually executes cosign (behind the Signer/Verifier seam) and is covered by tests, with the local-key roundtrip green in normal CI and the keyless/Rekor path green when the OIDC gate is present.
- OIDC issuer+identity are operator-configurable at both sign and verify time with no hardcoded NVIDIA identity, and keyless-without-resolvable-identity fails closed with a distinct coded error.
- `nkdctl trust` manages a deterministic persisted trust store; verify resolves and pins the signer from it and fails closed (distinct exit code) on any unpinned/untrusted signer.
- Zero-binary is enforced by test: signing never mutates covered bytes, only sidecars are produced, and the SEC-02 chain guarantees no unauthenticated digest is ever a trusted reference.
- Recipes, pipeline bundles, validation vectors, and release sets are each independently digest-addressable and integrity-protected, with a signable ReleaseSet binder and recomputable digests.
- docs/checklist.md rows for FR-08/SEC-01..04 and NKD-FR-INT-026/027/036/046 + REP-023/024 are updated from partial to done with executed-test evidence.

#### Verification Engine  ·  stories E6-S*  ·  `poc: mixed`

Turn nkdctl's verify path from mostly-plumbing into an executed, tested, fail-closed reproduction verifier that cosign-verifies the signed manifest against a pinned signer, independently recomputes recipeDigest, names the first divergent node with a distinct coded exit, and runs as the isolated, unconditional terminal node of every emitted pipeline so no single defect can forge both an artifact and its proof.

- **E6-S1 — Execute cosign verify-blob for real and prove it fail-closed with a roundtrip test**  `[harden · M · SEC-05]`
  - *Intent:* Convert the sign package from plumbing-only into an executed, tested capability so the signature gate in SEC-05 is actually enforced rather than merely shelled. Today sign.Run builds and can invoke cosign but no test ever runs a real sign→verify, so a broken/absent signature check would go unnoticed.
  - *Acceptance criteria:*
    - A test (build-tag or skip-if-absent guarded) generates an ephemeral cosign keypair or uses cosign sign-blob, signs a real expected-digests manifest, and nkdctl verify --signature/--bundle succeeds (exit 0) end-to-end.
    - Mutating one byte of the manifest after signing makes verify exit non-zero with CodeVerifyFailed wrapping 'cosign verify-blob (manifest signature)'.
    - When cosign is absent from PATH, a signature-bearing verify returns CodeUnsupported (not a silent skip and not a crash).
    - The mirrored-bundle --offline path is exercised in the roundtrip (sign-blob --bundle → verify-blob --bundle --offline) and passes with no network, confirming the airgap inclusion-proof check runs.
- **E6-S2 — Pin and enforce the expected signer identity/issuer as a fail-closed trust policy**  `[extend · M · SEC-05, FR-09]`
  - *Intent:* SEC-05 requires verifying the manifest 'against the pinned issuer/identity', but --identity/--issuer are optional free-form flags today, so a signature from any Fulcio identity would pass. Bind the expected keyless identity+issuer into the catalog/trust policy and refuse verification when they are unset or mismatched.
  - *Acceptance criteria:*
    - A trust policy (catalog-level or a --trust-policy/embedded default) carries the expected certificate-identity (SAN) and OIDC issuer; verify threads them into VerifyBlobArgs and fails closed with CodeVerifyFailed if a provided signature/bundle is validated but identity/issuer are absent or unmatched.
    - --require-signature implies the pinned identity+issuer MUST be present; verify refuses (distinct coded exit) if a signature is required but no identity/issuer is pinned, rather than accepting any signer.
    - A test asserts that a valid signature from a WRONG identity or issuer is rejected fail-closed, and a matching one passes.
    - The insecure escape hatch (--insecure-ignore-tlog) still requires explicit opt-in and is logged; it never becomes the default meaning of offline or of a missing identity.
- **E6-S3 — Independently recompute recipeDigest and deterministically name the first divergent node with a distinct coded exit**  `[harden · M · FR-09, SEC-05]`
  - *Intent:* Harden the FR-09 core so the operator's LOCALLY recomputed recipeDigest (from their resolved recipe/IR, not the build report's self-report) is diffed against the signed manifest as the first check, and the single first divergence — recipeDigest before any component, components in stable sorted order — is the named failure. Guarantee distinct, documented exit codes per failure class for the CI gate.
  - *Acceptance criteria:*
    - verify recomputes recipeDigest via recipe.Compile(resolveRecipe).Digest() and cross-checks it against both the manifest's RecipeDigest and the rebuilt report's digest; a mismatch is reported as the FIRST divergent node before any component is compared.
    - A golden test with multiple simultaneously-diverging components asserts verify names exactly one node (the lexicographically first) and that the naming is stable across runs (no map iteration nondeterminism).
    - recipeDigest mismatch, builder-profile mismatch, component-digest mismatch, missing/unattested component, and signature failure each return a distinct, documented coded exit (e.errors code → exit), verified by a table test.
    - No host/clock/random value enters the recomputed IR used for the diff (asserted by re-running verify twice and getting byte-identical recipeDigest + evidence).
- **E6-S4 — Add an isolated measure-only verify path that consumes independently-produced artifacts and never invokes the build engine**  `[new · L · SEC-07]`
  - *Intent:* SEC-07 requires the reproduction verifier be isolated from build stages so one defect cannot manufacture both an artifact and its proof. Locally, cmdVerify today calls runLocalBuild AND verifies in one process. Add a measure-only mode (e.g. `nkdctl verify --artifacts <dir>`) that hashes pre-built artifacts and diffs against the signed manifest with zero calls into the build/runner code.
  - *Acceptance criteria:*
    - A `verify --artifacts <dir>` (or `verify-artifacts`) mode reads independently-produced artifacts, computes each sha256 the same way the build report does, builds a got manifest, and runs VerifyAgainst — without ever calling runLocalBuild or the pipeline runner.
    - A unit/architecture test proves the measure-only path does not import or invoke build-engine execution (e.g. a seam/interface the test can assert is never called, or a build-tag that excludes the runner and still compiles the verifier).
    - The measure-only path fails closed on a missing artifact, an extra artifact not in the manifest, or a digest mismatch, each with the correct coded exit and named node.
    - Signature verification (S1/S2) and recipeDigest recompute (S3) still run in the measure-only path, so isolation does not weaken any check.
- **E6-S5 — Harden emitter-level verifier isolation: distinct context, read-only artifacts, no build toolchain in the verify node**  `[harden · M · SEC-07, SEC-06]`
  - *Intent:* In the emitted pipelines the verify node must run in a context structurally isolated from the build stages so a compromised build stage cannot also produce the proof. Ensure across all four engines the terminal verify runs in a separate job/pod/step with its own image, no build toolchain mounted, and read-only access to producer outputs.
  - *Acceptance criteria:*
    - All four emitters (github/gitlab/tekton/argo) place the verify node in a distinct execution unit (separate job/task/template) that does not mount the build toolchain container or writable build outputs.
    - Golden-YAML tests assert the verify unit references only the nkdctl verifier image + the (read-only) artifacts/manifest, never the per-component toolchain digests used by build stages.
    - The verify unit re-runs acquisition/hashing independently (or consumes signed producer outputs) rather than trusting a build stage's self-reported digest, and this is asserted per engine.
    - A negative test confirms an emitter cannot produce a graph where a build node and the verify node share the same writable workdir mount.
- **E6-S6 — Guarantee verify is the unique, unconditional terminal node on every engine with a fail-emission validator**  `[harden · S · SEC-06]`
  - *Intent:* SEC-06 is largely done and tested (TestEmittersEmitUnconditionalVerify), but the guarantee is per-emitter marker-string assertions, not a structural invariant. Add a compile-time/graph-level validator so any emitted pipeline that lacks exactly one unconditional terminal verify fails emission fail-closed, and lock the behavior with golden tests.
  - *Acceptance criteria:*
    - compile.go / the emitter path validates that the IR graph ends in exactly one KindVerify node that depends (transitively) on every producer node and has no successors; emission fails closed with a coded error if absent, duplicated, or non-terminal.
    - Each engine's unconditional idiom is asserted by golden output (github if: always(), gitlab when: always, tekton finally:, argo finished-state depends) plus a test that a build-node failure still schedules verify.
    - A property/table test feeds a deliberately malformed graph (missing verify, or verify not depending on a producer) and asserts emission is rejected on all four engines.
    - Documentation/checklist row NKD-FR-REP-020 is updated to reference the structural validator, not just marker strings.
- **E6-S7 — Emit a deterministic, machine-readable coded-failure verdict on every verify outcome for the CI gate**  `[harden · M · FR-09, SEC-05, SEC-07]`
  - *Intent:* UC-12 needs a coded, auditable failure that stops promotion. The evidence bundle exists (attest.EvidenceBundle, --evidence-out) but is only written opportunistically and ignores write errors. Make verify always emit a deterministic verdict artifact (Reproduced | ValidatedVariant | Rejected) with per-component causes + the named first divergent node + the exit code, suitable for a gate to consume.
  - *Acceptance criteria:*
    - Every verify outcome (success, fail-closed mismatch, rejected variant, signature failure) can emit a deterministic evidence/verdict document; --evidence-out write failures are surfaced (not silently swallowed as today).
    - The verdict document is byte-identical across repeated runs of the same inputs (no timestamps/host/random) and records verdict, first divergent node, per-component cause codes, and the process exit code.
    - A --format json (or the evidence bundle) exposes the exit-code taxonomy so a CI step can branch on the coded result and stop promotion; a test asserts a divergent build yields the documented non-zero code AND a Rejected verdict.
    - The verdict output cross-references the recipeDigest and (when bound) the builder-profile digest so an auditor can distinguish Reproduced from a profile-bounded variant.

*Definition of done —*
- A real cosign sign→verify roundtrip runs in tests: valid signature passes, tampered manifest fails closed (CodeVerifyFailed), missing cosign returns CodeUnsupported, and the --offline mirrored-bundle inclusion proof verifies with no network.
- verify enforces a pinned signer identity+issuer and refuses (distinct coded exit) an unsigned, wrong-signer, or unpinned-under-require-signature manifest.
- verify independently recomputes recipeDigest and names exactly one deterministic first divergent node (recipeDigest before components), with a distinct documented exit code per failure class covered by a table test.
- A measure-only isolated verify path hashes independently-produced artifacts and diffs against the signed manifest without invoking the build engine, asserted by an architecture test.
- All four emitters place exactly one unconditional terminal verify in an isolated context; a structural validator fails emission when it is absent/duplicated/non-terminal, locked by golden per-engine tests.
- Every verify outcome emits a deterministic, byte-identical machine-readable verdict (Reproduced|ValidatedVariant|Rejected) carrying the first divergent node, per-component causes, and exit code for a CI gate to stop promotion.

#### ValidatedVariant Lane  ·  stories E7-S*  ·  `poc: partial`

Turn the POC's variant plumbing into a fully enforced, two-tier ValidatedVariant lane: a digest mismatch is accepted only for a pre-classified (or signed-exception) representation-only cause that passes SBOM/vulnerability/integrity/policy gates, yielding a distinct, bounded-expiry identity that provably cannot be presented as a byte-for-byte reproduction.

- **E7-S1 — Enforce a bounded, RFC3339 variant expiry (parse + reject unbounded/expired)**  `[harden · S · FR-11, SEC-15]`
  - *Intent:* Make the 'expiring' property of a ValidatedVariant real: today `--variant-expiry` is an optional free string that is hashed into the identity but never validated or enforced. Add deterministic expiry parsing/enforcement with an injectable clock so no time.Now() ever leaks into the identity/IR.
  - *Acceptance criteria:*
    - Add `variant.ParseExpiry(string) (time.Time, error)` (strict RFC3339, UTC-normalized) and `variant.Expired(expiry string, now time.Time) (bool, error)`; the identity digest is computed over the normalized RFC3339 form so equivalent timestamps yield one identity.
    - `verify --allow-variant` fails closed with CodeVariantRejected when `--variant-expiry` is empty, malformed, or not strictly in the future (unbounded/perpetual variants are refused).
    - `now` is a parameter (never time.Now() inside identity/marshal code); a table test proves the identity is byte-stable across clocks and only the explicit expiry string changes it.
    - Tests cover: empty => rejected, non-RFC3339 => rejected, past expiry => rejected, valid future => VariantOutcome carries the normalized expiry, and the coded exit code is distinct.
- **E7-S2 — Consumer path: verify a pre-existing ValidatedVariant identity + evidence (UC-13)**  `[new · M · FR-11, SEC-15]`
  - *Intent:* The POC only PRODUCES a variant (verify --allow-variant). UC-13's consumer half — receive a variant evidence bundle + identity and verify it — has no path. Add a `verify --variant-evidence <bundle>` (or `variant verify`) mode that revalidates a variant handed over from another party without rebuilding.
  - *Acceptance criteria:*
    - Given an EvidenceBundle (verdict=validated-variant) + the signed expected-digests manifest, the command recomputes `variant.Identity` from (recipeDigest, profileDigest, normalized expiry, approvals) and fails closed if it does not equal the bundle's VariantIdentity.
    - It fails closed (distinct coded error) if the variant is expired as of `now`, if the verdict is not validated-variant, or if any approval references a preClassId not present in the manifest's PreClassified set.
    - It fails closed if the VariantIdentity equals or is presented as the manifest's RecipeDigest / any reproduced identity (over-claim guard delegated to E7-S3's predicate).
    - On success it prints the distinct identity, expiry, and per-component approved causes; exit 0. A table test drives each failing branch to its own exit code.
- **E7-S3 — Structurally forbid a variant identity from being presented as a reproduction**  `[harden · S · FR-11]`
  - *Intent:* Enforce FR-11/UC-13's 'must not claim byte-for-byte reproduction' invariant in code, not just by naming convention. A `variant:sha256:` identity must be unusable anywhere a Reproduced/canonical identity is expected.
  - *Acceptance criteria:*
    - Add `variant.IsVariantIdentity(s string) bool` (prefix + shape check) and a guard so a Reproduced verdict can never be emitted for, and Manifest.Parse rejects, a RecipeDigest that is a variant identity (CodeVerifyFailed).
    - EvidenceBundle invariants enforced + tested: VerdictValidatedVariant MUST carry a non-empty VariantIdentity and VariantExpiry; VerdictReproduced MUST NOT carry either; a bundle violating this fails to marshal/validate.
    - A test asserts VerifyAgainst / the Reproduced path returns false-closed when handed a got-manifest whose component or recipe identity is a variant identity.
    - Docs/checklist note updated: the distinct-identity rule is now enforced, not advisory.
- **E7-S4 — SEC-15 precondition gate: require SBOM/vulnerability/integrity/policy pass before granting a variant**  `[extend · M · SEC-15]`
  - *Intent:* SEC-15 requires a ValidatedVariant to PASS SBOM/vulnerability/integrity/policy checks in addition to being a representation cause. Today VerifyVariant only classifies divergences. Add a fail-closed precondition gate that must pass before any variant (pre-classified or signed) is granted.
  - *Acceptance criteria:*
    - Add a `VariantPreconditions` check that requires: a deterministic CycloneDX SBOM present for each divergent component (reuse pkg/nkd/sbom), the manifest integrity verified (the existing cosign manifest-signature gate must have passed — not skipped via insecure-ignore-tlog), and a referenced vulnerability/policy attestation (`--variant-policy-evidence <ref>`) present and digest-matched.
    - VerifyVariant refuses (CodeVariantRejected, distinct message per missing gate) if any precondition is absent or fails; the granted-variant path is unreachable without all gates green.
    - The precondition results are recorded in the EvidenceBundle (per-gate pass/fail rows) so the decision is auditable and diagnosable.
    - Tests: missing SBOM => rejected; unsigned/insecure manifest => rejected; missing policy/vuln evidence => rejected; all-present + representation cause => granted.
- **E7-S5 — Signed, expiring exception for a NOVEL eligible representation cause**  `[new · L · FR-11, SEC-15]`
  - *Intent:* Implement the second tier of FR-11/SEC-15 that today simply fails closed: a novel (not pre-classified) but eligible representation cause may be approved via a signed, expiring exception document. This is the genuinely-missing capability and depends on cosign actually executing.
  - *Acceptance criteria:*
    - Define a signed exception artifact (cause code [must be Eligible], component scope, machine-checkable predicate, bound recipeDigest+profileDigest, and its own expiry) supplied via `--variant-exception <file>` with `--exception-bundle`/`--exception-signature`, cosign-verified against the operator's pinned issuer/identity (reuse pkg/nkd/sign, executed for real — offline-verifiable with a mirrored bundle).
    - When a divergence is not pre-classified, VerifyVariant consults verified exceptions; a matching eligible exception auto-approves it and the resulting Identity records the exception id (distinct from a preClassId); the variant expiry is min(exception.expiry, --variant-expiry).
    - An exception carrying an INELIGIBLE cause (content/input/toolchain/unknown), an unverifiable signature, a recipe/profile-digest mismatch, or an expired exception is refused with CodeVariantRejected (fail closed).
    - Tests cover: valid signed exception => granted with exception-id in evidence; ineligible-cause exception => rejected; bad signature => rejected; profile-mismatch exception => rejected; offline verification via mirrored bundle succeeds.
- **E7-S6 — Discriminate ineligible causes (CONTENT vs INPUT vs TOOLCHAIN vs UNKNOWN)**  `[extend · M · FR-11, SEC-15]`
  - *Intent:* variant.Classify currently labels every non-approved divergence CONTENT_MISMATCH, erasing the diagnosability SEC-15's taxonomy exists for. Discriminate the real ineligible cause so an operator knows whether to repin an input, file a toolchain defect, or investigate content drift.
  - *Acceptance criteria:*
    - Classify uses available evidence to assign INPUT_MISMATCH when a pinned input digest recorded in the got-manifest differs from the want-manifest's, TOOLCHAIN_DEFECT when the profile/toolchain digest diverged, CONTENT_MISMATCH when inputs+toolchain match but bytes differ, and UNKNOWN when evidence is insufficient.
    - All four remain Eligible()==false and continue to fail closed; only the recorded cause changes — no ineligible cause can ever be auto-approved.
    - The discriminated cause is written to each ComponentEvidence row in the EvidenceBundle for the failing/rejected verdict.
    - Table tests drive one divergence per cause and assert the code + the fail-closed exit; a defense-in-depth test confirms an ineligible pre-classified entry still never approves.
- **E7-S7 — Validate pre-classified predicates at load + widen the predicate context**  `[harden · S · FR-11]`
  - *Intent:* A malformed predicate today silently never matches (a governance foot-gun), and the context is only {component,outputType}, which is too coarse to make many representation causes machine-checkable. Validate predicates when the manifest loads and expand the evaluable context.
  - *Acceptance criteria:*
    - Manifest.Parse / catalog validation rejects (fail closed, coded) any PreClassified whose predicate is not a well-formed conjunction of `key (==|!=) value` terms over the documented context key set, or references an unknown key — no silent never-match.
    - The predicate context is widened to include the diverged metadata field and (for disk artifacts) block/byte granularity keys, and the documented grammar is captured in a package doc comment + docs/checklist.md.
    - Every eligible representation cause the shipped catalogs use is expressible and covered by a passing table test; an unknown-key or unbalanced predicate produces a distinct load-time error.
    - evalPredicate retains its fail-closed default (unparseable => false) as defense in depth even though load-time validation now catches these earlier.

*Definition of done —*
- A digest mismatch is accepted only when it is a representation-only cause (pre-classified OR signed-exception), all SEC-15 preconditions (SBOM/vuln/integrity/policy) pass, and a strictly-future bounded expiry is supplied — every other mismatch fails closed with a distinct coded exit code.
- Every granted variant carries a distinct `variant:sha256:` identity that is deterministically recomputable, is provably never emitted as or accepted for a Reproduced/canonical identity, and expires (unbounded is refused).
- A consumer can verify a handed-over variant (identity recompute + not-expired + not-reproduction + approvals traceable) without rebuilding, and gets a coded failure per broken check.
- Ineligible divergences are discriminated into CONTENT/INPUT/TOOLCHAIN/UNKNOWN and recorded in a deterministic evidence bundle for diagnosability.
- cosign is executed and tested end-to-end for the signed exception and the integrity precondition (offline-verifiable via a mirrored bundle), not merely arg-built.
- docs/checklist.md NKD-FR-REP-035..037 rows and the FR-11/SEC-15 entries are refreshed to reflect enforced (not advisory) status, with test evidence named.

#### Supply-Chain Evidence — SBOM, Provenance, Vulnerability  ·  stories E8-S*  ·  `poc: mixed`

Turn nkdctl's single deterministic CycloneDX document into a complete per-artifact supply-chain evidence set — component-level SBOMs bound to artifact identity, deterministic SLSA build provenance, aggregate release evidence linking every artifact to its SBOM/provenance digest, and artifact-scoped vulnerability results mapped to affected recipe versions and remediation paths — all with declared schema versions and a documented deterministic serialization profile.

- **E8-S1 — Per-artifact component-level SBOM bound to artifact identity + aggregate index**  `[harden · M · FR-15]`
  - *Intent:* FR-15 and UC-16 require a deterministic CycloneDX SBOM per BUILT ARTIFACT bound to that artifact's identity, plus an aggregate that links them. Today pkg/nkd/sbom emits a single document keyed on recipeDigest with each component as a components[] entry; split that into one component-level SBOM per materialized artifact (metadata.component = the artifact, its sha256 as bom-ref/identity) and an aggregate index that references each per-artifact SBOM by digest.
  - *Acceptance criteria:*
    - sbom.FromRecipe (or a new FromArtifact) emits one CycloneDX SBOM per built artifact whose metadata.component.hashes carries the artifact's SHA-256 and whose bom-ref/serialNumber is derived from that artifact digest, not just the recipeDigest
    - An aggregate SBOM index document lists every per-artifact SBOM by {artifact name, artifact sha256, sbom sha256}, sorted, and still carries nkd:recipeDigest binding it to the IR
    - `nkdctl sbom` writes the per-artifact SBOMs and the aggregate index deterministically; a golden test asserts byte-identity across two runs for both
    - When no build has run (resolve-only), each component-level SBOM is still emitted from source provenance with no artifact hash, and the aggregate is still deterministic
    - NKD-FR-REP-025/026/027 remain green (existing sbom tests still pass or are updated in place)
- **E8-S2 — Declared schema versions + canonical deterministic serialization profile for evidence**  `[harden · S · FR-15]`
  - *Intent:* SRD §1.3 requires each NKD release to declare its supported schema versions and a deterministic serialization profile. Add a single source of truth enumerating the CycloneDX specVersion, SLSA provenance predicate version, and evidence-bundle schema, and make the SBOM serialNumber a canonical, deterministic RFC-4122 UUIDv5 (namespaced on recipeDigest+artifact identity) instead of the current pseudo-URN slice — closing NKD-NFR-025.
  - *Acceptance criteria:*
    - A schemaProfile object (package-level) enumerates supported {cyclonedx, slsaProvenance, evidenceBundle, vulnReport} schema versions and the serialization rules (JSON, sorted keys, 2-space indent, no timestamps/random), surfaced via a `nkdctl` command or field in the aggregate/release evidence
    - SBOM serialNumber is a deterministic RFC-4122 UUIDv5 computed from a fixed namespace + (recipeDigest, artifact identity); a test asserts it is a valid v5 UUID and is stable across runs
    - A test validates each emitted CycloneDX document against the CycloneDX 1.5 (and 1.6 if declared) JSON schema and fails on any schema violation
    - The serialization profile is documented in docs/ and referenced from the checklist; NKD-NFR-025 moves to done
- **E8-S3 — Verified external SBOM/provenance references for prebuilt (externally supplied) artifacts**  `[extend · S · FR-15]`
  - *Intent:* FR-15 and §1.3 say that for externally supplied artifacts NKD records VERIFIED references to their SBOM and provenance. Today sbom.componentFor treats every !FromSource component as a Tier-4 exclusion, so a prebuilt (fetch-lane) component is mis-recorded and carries no external references. Handle the prebuilt XOR from-source XOR exclusion trichotomy correctly and record fail-closed-verified external references.
  - *Acceptance criteria:*
    - componentFor distinguishes prebuilt (Prebuilt != nil) from an exclusion: a prebuilt component emits a CycloneDX component with type, its pinned content sha256 as a hash, and externalReferences of type bom/attestation pointing at the declared external SBOM/provenance plus nkd:externalSbomDigest / nkd:externalProvenanceDigest
    - If a catalog declares an external SBOM/provenance reference with an expected digest, nkdctl verifies the fetched reference against that digest and fails closed with a distinct coded error on mismatch; an undeclared reference is recorded as unverified, never claimed verified
    - Exclusions remain distinct (nkd:fromSource=false + exclusionReason) and are never conflated with prebuilt
    - A test over examples/operator-catalog (acme-prebuilt-node) asserts the prebuilt SBOM component carries the content digest + external references and is deterministic
- **E8-S4 — Emit deterministic SLSA build provenance (in-toto) per built artifact**  `[new · L · FR-15]`
  - *Intent:* §1.3 requires machine-readable build provenance conforming to the applicable SLSA provenance spec; today none is emitted. Build an in-toto Statement with predicateType slsa.dev/provenance/v1 directly from the IR: subjects = per-artifact digests, builder.id = the signed builder-profile digest, buildType = the hermetic-from-source lane, invocation/resolvedDependencies = pinned sources + toolchain digests. Must be deterministic (no clock/host/random; buildStartedOn/finishedOn omitted or fixed per the serialization profile).
  - *Acceptance criteria:*
    - A new provenance emitter produces one in-toto Statement per artifact with predicateType slsa.dev/provenance/v1, subject[].digest.sha256 = the artifact digest, builder.id = profileDigest, and resolvedDependencies listing each pinned source commit/treeHash and toolchain digest
    - `nkdctl attest --provenance` (or an equivalent verb) writes the provenance deterministically; a golden test asserts byte-identity across two runs and validates the document against the SLSA provenance v1 schema
    - Emission fails closed with a distinct coded error if any subject artifact lacks a resolved sha256, if the toolchain is tag-not-digest, or if no builder profile is bound (a prebuilt-only manifest carries no builder-profile provenance, consistent with its host-independent acquisition)
    - No host path, clock, UUID, or random value appears in the provenance bytes (asserted by a test that diffs two runs under varied HOME/TZ/PATH)
- **E8-S5 — Aggregate release evidence linking artifact identity ↔ SBOM digest ↔ provenance digest**  `[extend · M · FR-15]`
  - *Intent:* §NKE-software-inventory and the SRD ValidationVector evidence shape (sbomDigest, provenanceDigest) require aggregate release evidence that links each artifact identity to its component SBOM and provenance records. Extend attest.EvidenceBundle / the expected-digests manifest with per-artifact evidence rows and make verify fail closed when a referenced SBOM/provenance digest does not match the recomputed one.
  - *Acceptance criteria:*
    - EvidenceBundle (or a ReleaseEvidence sibling) gains per-artifact rows carrying {artifact name, artifact sha256, sbomDigest, provenanceDigest} and a top-level aggregate evidence digest computed over the sorted set, all deterministically serialized
    - attest folds the per-artifact SBOM and SLSA-provenance digests into the manifest/evidence bundle so an operator can retrieve each by immutable identity
    - `nkdctl verify` recomputes the SBOM and provenance for a built artifact and fails closed, naming the first divergent node, if its digest differs from the referenced one
    - Round-trip + determinism tests pass and no host/clock/random enters the aggregate evidence
- **E8-S6 — Vulnerability scan report identifying scanned artifact + policy/DB version, consumable by stock scanners**  `[new · L · SEC-18]`
  - *Intent:* SEC-18 requires vulnerability results that identify the scanned artifact and the policy version. Add a `nkdctl vuln scan` that shells an Apache-2.0 scanner (grype/trivy) over the per-artifact CycloneDX SBOM (or OCI image), captures the scanner DB/policy version as an explicit pinned input (never host clock), and emits a deterministic report keyed on the artifact digest + policyVersion + recipeDigest. Also closes NKD-NFR-026 (stock-scanner consumability).
  - *Acceptance criteria:*
    - `nkdctl vuln scan` consumes a per-artifact SBOM/image and emits a deterministic report whose header names the scanned artifact sha256, the scanner name + pinned DB/policy version, and the recipeDigest; findings are sorted and carry no scan-time timestamp
    - The scanner is invoked as a subprocess (Apache-clean, not linked); a missing scanner fails closed with a distinct coded exit code (or print-only with an explicit --require-scanner gate), mirroring the cosign/aicr shelling pattern
    - Given a pinned DB snapshot / fixture scanner output, the report is byte-identical across runs (a fixture-based unit test asserts this without network)
    - A CI/opt-in test ingests an nkdctl-emitted CycloneDX SBOM into grype and trivy sbom mode and asserts clean parse + expected component count, moving NKD-NFR-026 to done
- **E8-S7 — Advisory + remediation object mapping advisory → affected recipe versions → replacement recipe**  `[new · M · SEC-18]`
  - *Intent:* SEC-18 requires that advisories map to affected recipe versions and remediation paths. Implement the SRD cveRemediation shape: a deterministic advisory object linking an advisory/policy id to the affected recipeDigests, the replacement recipeDigest, the replacement validation-vector reference, and the remediation path — so a scan finding traces to which recipe versions are affected and how to remediate.
  - *Acceptance criteria:*
    - An advisory object carries {advisory/CVE-or-policy id, policyVersion, affectedRecipeDigests[], replacementRecipeDigest, replacementValidationVectorRef, remediationPath}, deterministically serialized (sorted, no clock/random)
    - `nkdctl vuln advisory` (or vuln report --advisories) joins scan findings from E8-S6 to the affected recipe versions and surfaces the remediation recipe
    - Fail closed with a distinct coded error when an advisory references an unknown/unresolvable recipe digest or a remediation recipe whose recipeDigest cannot be recomputed
    - A test asserts an advisory correctly maps a seeded finding to affectedRecipeDigests and a replacementRecipeDigest and round-trips deterministically

*Definition of done —*
- `nkdctl sbom` emits a deterministic, schema-declared CycloneDX SBOM per built artifact bound to that artifact's digest plus an aggregate index; byte-identical across runs, validates against the CycloneDX JSON schema, and ingests cleanly into grype and trivy (NKD-NFR-025/026 done).
- Prebuilt/externally-supplied components carry verified external SBOM/provenance references (fail-closed on declared-digest mismatch) and stay distinct from Tier-4 exclusions.
- `nkdctl attest --provenance` emits deterministic SLSA provenance v1 in-toto statements per artifact (builder.id = profile digest, materials = pinned sources+toolchains), and aggregate release evidence links each artifact identity to its sbomDigest and provenanceDigest with verify failing closed on any referenced-digest divergence.
- `nkdctl vuln` produces a deterministic report naming the scanned artifact + pinned policy/DB version + recipe identity, and an advisory object mapping the advisory to affected recipe digests and a replacement recipe; missing scanner or unknown recipe fails closed with distinct coded exit codes.
- Every new evidence artifact is free of host/clock/random values and is covered by determinism + fail-closed + schema-validation tests; the checklist rows for FR-15/SEC-18 and NKD-FR-REP-025..027 / NFR-025/026 are refreshed with evidence.

#### Offline / Air-Gapped Operation  ·  stories E9-S*  ·  `poc: mixed`

Let an air-gapped operator build, verify, and deliver an NKD substrate with zero network egress while preserving identical trust semantics online and offline: a mirrored, digest-pinned validation vector plus an offline-verified Rekor inclusion proof (never tlog-ignore), backed by a complete pre-staged mirror of every pinned input so the hermetic build never reaches the network.

- **E9-S1 — Execute the offline cosign verify path end-to-end (real signature + Rekor inclusion proof), under a gated integration test**  `[harden · L · FR-19, SEC-14]`
  - *Intent:* Turn the cosign plumbing into an actually-executed, tested capability so 'offline verify' is a proven behavior, not arg construction. Verify the mirrored Rekor inclusion proof with real cosign under blocked egress and prove tamper fails closed.
  - *Acceptance criteria:*
    - A gated integration test (build tag `integration` / `make test-airgap`) runs `attest --bundle` then `verify --bundle --offline` invoking real cosign against a mirrored bundle (sig+cert+Rekor inclusion proof), asserting exit 0 with no outbound network — using a local sigstore scaffold or a checked-in fixture bundle so no public tlog is contacted.
    - The verify step runs with egress blocked (dead HTTP_PROXY/HTTPS_PROXY sink or network namespace) and the test asserts zero outbound connection attempts are made by the offline path.
    - Tampering either the manifest bytes or the bundle causes verify to fail closed with CodeVerifyFailed / exit 22 and names the failing check (signature vs inclusion-proof vs digest).
    - poc.md and docs/checklist.md NKD-FR-REP-022/023/024 + NFR-070 are updated to state cosign is executed under the integration gate, removing the 'cosign not executed in the POC' caveat.
- **E9-S2 — Mirror and digest-verify the validation vector offline (mirrored vector, not a fetched URL)**  `[extend · M · FR-19, SEC-14]`
  - *Intent:* FR-19/SEC-14 require the mirrored validation vector to be part of offline semantics. Make attest record it as a content-addressed pin and make verify resolve+verify it from a local mirror, failing closed on a remote-only ref under --offline.
  - *Acceptance criteria:*
    - attest records the validation vector as a content-addressed ref (`sha256:...` + optional locator) on the manifest/evidence bundle; a bare-URL-only vector is rejected under an `--offline`/airgap attest with a distinct coded error.
    - `verify --offline` resolves the vector from a local mirror directory (`--validation-vector-dir`), verifies its bytes hash to the pinned digest, and fails closed (distinct exit code) on mismatch or absence — never fetching it.
    - The deterministic evidence bundle records the mirrored vector's digest and that it was resolved from the local mirror (no host/clock/URL-fetch state leaks into the serialized bytes).
    - Unit tests cover: matching mirrored vector passes; mismatched/absent vector under --offline fails closed; output is byte-deterministic.
- **E9-S3 — `nkdctl inputs` — deterministic enumeration of every pinned input by immutable identity**  `[new · M · FR-19, SEC-14]`
  - *Intent:* Close the NFR-071 gap: there is no single command that lists all pinned inputs. Emit sources, toolchains, package snapshots, and base images by immutable identity so an operator can mirror exactly them, and fail closed on any input not mirrorable by immutable identity.
  - *Acceptance criteria:*
    - New subcommand emits a deterministic, sorted, machine-readable manifest of all pinned inputs derived from the IR + catalog: git sources (repo@commit@treeHash), toolchain images (registry digest per arch), package-snapshot mirror+date, and any base images — with no host/clock/random values.
    - It fails closed (coded error naming the offending input) when an input is not addressable by immutable identity — specifically the kubernetes/runc LOCAL toolchain image IDs — unless `--allow-local` is explicitly passed.
    - Output is byte-identical across repeated runs on the same recipe and per-arch (distinct manifest per platform), covered by a determinism test.
    - Works on both the embedded default catalog and a `--data` operator catalog with no code change.
- **E9-S4 — Pre-stage inputs into a content-addressed mirror and relocate the IR so the hermetic build has zero egress (including vendor)**  `[extend · L · FR-19, SEC-14]`
  - *Intent:* Close BLD-066: build the mirror store and rewrite the IR to consume it, eliminating the last network step (the `--network=default` vendor of etcd/coredns/helm) so the entire build runs `--network=none` from a local mirror.
  - *Acceptance criteria:*
    - `nkdctl mirror <dir>` fetches each enumerated input once into a content-addressed local store (git tar/bundle per commit+treeHash, toolchain images via `docker save`/`skopeo copy @digest`, needed package-snapshot subset, base images), verifying each against its pin and failing closed on mismatch.
    - A `build --airgap <dir>` resolves sources AND toolchains from the mirror, with non-vendored sources' deps pre-materialized into the mirrored source tree so the vendor step no longer runs under `--network=default` — every container runs `--network=none`.
    - An airgap build with all egress blocked (dead proxy / netns) succeeds and produces the same recipeDigest and byte-identical component outputs as the online build.
    - A missing/mismatched mirror input fails closed with a coded error naming it; there is no silent network fallback (a network attempt in airgap mode is itself an error).
- **E9-S5 — `bundle --airgap` — mirrorable disconnected-execution pipeline across all four CI adapters**  `[new · L · FR-19, SEC-14]`
  - *Intent:* Close INT-022: emit a self-contained bundle (relocated IR + inputs manifest + mirrorable verification bundle) whose CI pipeline resolves every pinned input from the mirror, preserving one-IR-many-adapters (topology/transport only).
  - *Acceptance criteria:*
    - `bundle --engine <e> --airgap` emits the pipeline plus the S3 inputs manifest, with build/fetch steps referencing the mirror (path/registry) instead of upstream; a test asserts no `github.com` / `dl.k8s.io` / `registry.k8s.io` / `snapshot.debian.org` egress URL for a pinned input remains in the airgap YAML.
    - All four emitters (github/gitlab/tekton/argo) accept `--airgap` and lower the SAME relocated IR — a test asserts identical build semantics (same `docker run --network=none` command + terminal verify) across engines, differing only in topology/transport.
    - The airgap bundle is byte-deterministic across runs and self-describing: a top-level manifest lists every file with its digest.
    - The emitted verify step is the mirrored-bundle offline verify (`verify --bundle --offline`), not `--insecure-ignore-tlog`, and remains the unconditional final node in every engine.
- **E9-S6 — Make qualified toolchains mirrorable — pin kubernetes/runc local image IDs to registry digests or fail closed under airgap**  `[harden · M · FR-19, SEC-14]`
  - *Intent:* Close the NFR-071 toolchain gap: local per-arch image IDs (toolchainImagesByArch) cannot be mirrored by immutable identity. Provide a machine-managed pin to a registry digest, or make airgap enumeration fail closed rather than leak a non-mirrorable input.
  - *Acceptance criteria:*
    - A `catalog pin-toolchains` (or `catalog pin` extension) resolves and records a mirrorable `registry/...@sha256:` digest for every toolchain, including the currently-local kubernetes/runc images, into the lock — machine-managed, deterministic.
    - The IR validator still accepts every per-arch digest form; an airgap build resolves the toolchain from the mirror by its registry digest (`docker load`/`skopeo`), never rebuilding it locally.
    - Under `--airgap`, `nkdctl inputs` reports every toolchain of the default catalog as mirrorable-by-digest OR fails closed naming the offending source (no silent local-ID passthrough).
    - A test over the default catalog asserts all 8 sources' toolchains resolve to registry digests (or the documented fail-closed case).
- **E9-S7 — UC-09 end-to-end air-gapped CUJ with a negative egress gate**  `[new · M · FR-19, SEC-14]`
  - *Intent:* Prove the whole loop (enumerate -> mirror -> airgap build -> attest --bundle -> verify --bundle --offline -> sbom) runs with all egress blocked and equivalent trust, and that any need for the network fails closed rather than silently reaching it.
  - *Acceptance criteria:*
    - A scripted CUJ (scripts/airgap.sh or a make target) runs the full enumerate->mirror->build->attest->verify->sbom loop with egress blocked throughout and asserts success plus byte-identity (recipeDigest + component digests) to the connected run.
    - A negative test asserts that a missing mirror input, or `--offline` without a mirrored bundle/vector, fails closed with the expected distinct exit code and makes no network call.
    - poc.md and docs/checklist.md record UC-09 as demonstrated with the exact commands and the egress-block method; INT-022's 'deferred' and the REP-022..024 'cosign not executed' caveats are removed or narrowed to match reality.
    - The CUJ is runnable by an operator with no NVIDIA-internal access (embedded or `--data` catalog only).

*Definition of done —*
- `verify --bundle --offline` executes real cosign against a mirrored Rekor inclusion proof under blocked egress in a gated integration test, and fails closed (exit 22) on a tampered manifest or bundle — cosign is executed, not just arg-built.
- The validation vector is resolved and digest-verified from a local content-addressed mirror under `--offline`; a remote-only vector fails closed.
- `nkdctl inputs` enumerates every pinned input by immutable identity and `nkdctl mirror` pre-stages them; `build/bundle --airgap` runs `--network=none` with zero egress (vendor included) producing byte-identical outputs and the same recipeDigest as the online run.
- All four CI emitters lower an `--airgap` IR carrying no upstream URL for any pinned input (mirror-relocated) and byte-deterministic, with the mirrored-bundle offline verify as the unconditional final node.
- A scripted UC-09 CUJ proves build->verify->deliver with all egress blocked and equivalent trust, and a negative test proves any network need fails closed; poc.md/checklist reflect the executed status with caveats removed or narrowed.


---

### E4 — Release, Delivery & CLI

`kind: implementation` · `phase: 3` · `deps: E1, E2, E3` · `stories: 35` · `owns: FR-14, FR-16, FR-17, FR-18, SEC-08, SEC-13, SEC-16, SEC-17`

**Goal.** Publish and deliver — atomic, digest-selectable release sets with compatibility + two independent promotion gates + rollback + the pinned NKE inventory, the shelled accelerated-runtime (AICR) integration, constraint-driven pinning + immutable publication + supersede/withdraw, the reproducible packages/images/bootable-disk delivery surface, and the CLI + SDK capstone.

**Outcome.** A complete, compatible, digest-verified release set is promoted all-or-nothing, delivered as installable packages / images / bootable node artifacts, kept current by constraint-driven pins, and consumed by any control-plane engine (NKE or an alternative) through a clean, agnostic contract.

**Use cases advanced.** UC-01, UC-04, UC-05, UC-06, UC-08, UC-10, UC-11, UC-12, UC-14, UC-15, UC-16

**Sub-areas:** Accelerated-Runtime (AICR) Integration · Constraint-Driven Pinning, Publication & Release Maintenance · Release Lifecycle · Substrate Delivery · CLI Capstone & External Interface Seams

#### Accelerated-Runtime (AICR) Integration  ·  stories E10-S*  ·  `poc: mixed`

Turn nkdctl's AICR plumbing into an executed, tested, fail-closed capability: an accelerator-enabled recipe references a digest-pinned AICR profile by identity+digest, nkdctl shells the deterministic 3-step render (aicr recipe → aicr evidence digest [verified vs pin] → aicr bundle) without ever linking AICR, and the resulting accelerated-runtime bundle is bound to the immutable release and covered by verify — while core Kubernetes recipes remain wholly AICR-free when accelerator capabilities are inactive.

- **E10-S1 — Asymmetric-match AICR axis values against the resolved profile**  `[harden · M · FR-16]`
  - *Intent:* Close the correctness/fail-closed gap where AICR axes are gated on presence only. Make the requested accelerator/service/intent/platform values participate in resolution against the winning recipe's aicrProfile so a mismatch fails closed and multiple AICR recipes can be disambiguated.
  - *Acceptance criteria:*
    - recipe.Resolve compares each requested AICR axis value (accelerator/service/intent/platform) to the corresponding aicrProfile field using the existing asymmetric MatchField semantics; a request whose value differs from the profile (e.g. --accelerator a100 against an h100 profile) returns a distinct coded error (e.g. CodeNoMatch/CodeReservedAxis), never a silently-wrong resolution
    - When two recipes each declare an aicrProfile, the requested AICR axis values select the correct one; if both remain equally applicable resolution returns CodeAmbiguous naming both recipe IDs (mirrors the active-axis tie-break path)
    - A request omitting an AICR axis the profile declares is still permitted (profile field is authoritative/provenance) but the resolved profile carried forward is unchanged; behavior is covered by a table test enumerating match/mismatch/omitted per axis
    - Existing TestAICRAxisGating and the acme-accel-node demonstrator still pass (control-plane/h100/eks/ubuntu/training resolves; a mismatched accelerator now fails closed with a stable exit code)
- **E10-S2 — Fail-closed enum-domain validation of AICR profile criteria at catalog load**  `[harden · S · FR-16]`
  - *Intent:* Reject an aicrProfile whose accelerator/service/intent/os value is outside the known AICR domain at load time, so a typo fails closed early with a coded error instead of producing a wrong recipe or a late aicr failure.
  - *Acceptance criteria:*
    - catalog load adds domain maps (accelerator, service, intent, and os-for-AICR) mirroring criteria.roleDomain/archDomain, and validateRecipes returns CodeInvalidCatalog for any aicrProfile value outside its domain, naming the offending field and value
    - platform remains optional and, when set, is validated against its documented domain (kubeflow|dynamo|slurm); an empty platform stays valid
    - The domain lists live in one place and are referenced by both load validation and the criteria AICR-axis handling (no divergent copies); a test asserts a representative in-domain profile loads and each out-of-domain field is individually rejected
    - catalog validate on examples/operator-catalog stays green; a fuzz/typo fixture (accelerator: h1OO) is rejected with the coded error and a deterministic message
- **E10-S3 — Pin the shelled aicr binary and harden evidence-digest parsing**  `[harden · M · FR-16]`
  - *Intent:* Bring the aicr subprocess under the same pin-everything, fail-closed, diagnosable discipline as the toolchains: verify the aicr binary identity, parse its canonical digest strictly, and surface stderr in coded errors.
  - *Acceptance criteria:*
    - The CLIBundler records and (when the profile/catalog pins one) verifies the aicr binary version/digest before rendering; an unpinned-or-mismatched binary fails closed with a distinct coded error, and the resolved aicr identity (version/digest) is captured into the Result and manifest AICRRef
    - aicr evidence digest output is parsed strictly (exact canonical-digest field/line, not the first [0-9a-f]{64} anywhere in stdout); a fixture with extra hex noise before the real digest no longer mis-parses
    - On any aicr sub-step failure the coded error wraps captured stderr (via ExitError.Stderr) so the message is diagnosable, and the digest-drift case still returns CodeVerifyFailed naming pinned vs computed
    - Unit tests cover strict-parse success, strict-parse rejection of noisy output, binary-mismatch fail-closed, and stderr propagation using a scripted fake aicr
- **E10-S4 — Fold the accelerated-runtime bundle into the trust model and verify**  `[extend · L · FR-16]`
  - *Intent:* Extend zero-binaries/verify to the AICR bundle: capture the rendered bundle's own deterministic content digest into expected-digests and make nkdctl verify recompute it, so a drifted or fabricated bundle fails closed like every other node.
  - *Acceptance criteria:*
    - attest --aicr-bundle computes a deterministic bundle digest (canonicalized over checksums.txt / sorted file digests, no host paths or timestamps) and folds it into the signed expected-digests manifest as a first-class node (AICRRef.BundleDigest), in addition to the existing profile identity+digest
    - nkdctl verify, given a manifest carrying an AICR bundle node, recomputes the bundle digest (re-render via the pinned profile or re-hash a supplied bundle) and fails closed naming the AICR bundle as the first divergent node on mismatch; an offline verify path is supported
    - The bundle digest is stable across two renders on the same profile (determinism test) and is independent of the recipeDigest (the IR digest is provably unchanged by adding an AICR bundle)
    - A tampered-bundle fixture makes verify exit with the standard verify-failed code; the untampered demonstrator verifies clean
- **E10-S5 — Context, timeout, and cancellation for all aicr shell-outs**  `[harden · S · FR-16]`
  - *Intent:* Remove the hard-coded context.Background() on the AICR subprocess calls so aicr recipe/evidence/bundle honor a caller deadline and cancellation and fail closed with a coded error rather than hanging.
  - *Acceptance criteria:*
    - cmdAICR and attest --aicr-bundle thread a real context.Context (from the CLI) into aicr.Bundle; a --timeout flag (with a sane default) bounds the whole 3-step render
    - Each of the three exec.CommandContext calls observes cancellation; a cancelled/timed-out render returns a distinct coded error (e.g. CodeBuildFailed/CodeTimeout) naming the step, not a raw context error
    - A test injects a hanging fake aicr and asserts the render aborts within the deadline with the coded error and no leaked process
    - No behavior change on the happy path (Preview/--print still needs no subprocess; the demonstrator still renders)
- **E10-S6 — Wire and guard the Bundler SDK-injection seam (never import AICR)**  `[extend · M · FR-16]`
  - *Intent:* Make the Bundler interface reachable as a real injection point via the SDK facade so an operator's own program can supply an in-process AICR adapter, and add an enforced guard that neither pkg/nkd/aicr nor any core package imports github.com/NVIDIA/aicr.
  - *Acceptance criteria:*
    - The SDK facade exposes an option to inject a custom aicr.Bundler; when injected, aicr bundle / attest --aicr-bundle use it instead of the default CLIBundler, and an example/test injects a fake in-process bundler end to end (no aicr binary present)
    - A build/lint guard test asserts github.com/NVIDIA/aicr appears in no import set of the compiled nkdctl binary (pkg/nkd/aicr and all core packages), enforcing the Apache-clean 'never linked' invariant
    - The default binary behavior is unchanged when no Bundler is injected (CLIBundler shells aicr; fail-closed CodeUnsupported when absent)
    - Docs/godoc state the two adapters and the injection path; the seam is exercised by a test that is not a pure interface satisfaction check (it drives a full render via the injected bundler)
- **E10-S7 — Bind the rendered bundle to the immutable release for UC-06**  `[extend · L · FR-16]`
  - *Intent:* Deliver UC-06's 'bind the resulting configuration to the immutable release': attach the accelerated-runtime bundle and its digest to the release so a downstream lifecycle system can digest-select it, without nkdctl running any cluster lifecycle.
  - *Acceptance criteria:*
    - attest emits the AICR bundle as a digest-selectable member of the release set (bundle content digest + profile identity+digest recorded together in the signed manifest), consumable by a downstream lifecycle system by digest
    - nkdctl can publish/attach the bundle to an OCI target (referrer/artifact) when a repo is provided, or leave a reference-only manifest entry when not — both paths are deterministic and fail closed on push errors
    - A round-trip test proves: resolve accelerator recipe → render bundle → attest → the manifest lets a consumer select exactly that bundle by digest, and a substituted bundle is rejected by the recorded digest
    - No cluster/fleet lifecycle is performed by nkdctl; the output is compatible release-set + evidence only (documented in the story's tests/comments)
- **E10-S8 — Hermetic aicr stub and end-to-end executed CI proof**  `[harden · M · FR-16]`
  - *Intent:* Convert the one-off 'proven live with AICR v0.18.0' claim into a repeatable, hermetic test that exercises the full recipe→evidence→bundle→attest→verify path including the digest-drift fail-closed branch, without depending on a real aicr install.
  - *Acceptance criteria:*
    - A committed fake aicr binary/stub emits a deterministic recipe + a controllable evidence digest + a fixed bundle, letting a test drive nkdctl aicr bundle and attest --aicr-bundle to completion in CI (no network, no real AICR)
    - The test asserts: verified-digest happy path renders and records the AICRRef; a stub-induced digest drift fails closed with CodeVerifyFailed; a missing binary fails closed with CodeUnsupported
    - The verify-side recomputation (E10-S4) and release binding (E10-S7) are covered by the same harness so the accelerated-runtime slice has an executed regression, and docs/checklist.md NKD-FR-RC-013a evidence is updated from 'proven live (one-off)' to 'covered by hermetic CI test'
    - make test stays Docker-free and deterministic; the harness runs in the standard test lane

*Definition of done —*
- Requesting an AICR axis value that does not match the resolved profile fails closed with a distinct coded exit code, and multiple AICR recipes disambiguate by accelerator/service/intent/platform (S1).
- An out-of-domain accelerator/service/intent/os in an aicrProfile is rejected at catalog load with a coded error (S2); the shelled aicr binary is version/digest-checked and its evidence digest strictly parsed (S3).
- The rendered accelerated-runtime bundle's content digest is folded into the signed expected-digests manifest and recomputed by nkdctl verify, failing closed on drift with an offline path (S4); the bundle is bound to the immutable release as a digest-selectable member (S7).
- All aicr shell-outs honor context/timeout/cancellation and fail closed (S5); the Bundler seam is injectable via the SDK facade and an enforced guard proves github.com/NVIDIA/aicr is never linked into the binary (S6).
- A hermetic aicr stub drives the full recipe→evidence→bundle→attest→verify path (happy path + digest-drift + missing-binary) as a Docker-free CI regression, and docs/checklist.md NKD-FR-RC-013a evidence is updated accordingly (S8).
- Core Kubernetes recipes with accelerator capabilities inactive resolve and build with zero AICR involvement, proven by an explicit test.

#### Constraint-Driven Pinning, Publication & Release Maintenance  ·  stories E11-S*  ·  `poc: mixed`

Harden the constraint-driven source/artifact pinning machinery into a bounded, idempotent, fail-closed release operation, and add the missing immutability layer: content-addressed immutable publication with an append-only ledger and a new signed status-declaration primitive that supersedes, deprecates, or withdraws a release without ever mutating the original publication or its evidence.

- **E11-S1 — Harden constraint-driven pin/bump resolution: timeouts, dry-run delta, fail-closed re-Load**  `[harden · M · FR-17]`
  - *Intent:* Turn the pin/bump/pin-artifacts resolve path into a bounded, idempotent, fail-closed operation so a hung remote or a bad bump can never wedge or corrupt the machine-managed lock.
  - *Acceptance criteria:*
    - ResolvePin/ListTags/resolveImageDigest/pin-artifacts git+docker+https calls take a context.Context with a bounded, flag-configurable timeout; a canceled/timed-out context returns e.New(CodeFetchFailed, ...) with nonzero exit rather than blocking — covered by a test injecting an already-canceled context.
    - `catalog pin`/`bump`/`pin-artifacts` gain `--dry-run` that prints the exact lock delta (source: old→new tag+commit+epoch; artifact: per-arch old→new digest) and writes nothing; a test asserts the on-disk lock is byte-identical before and after a --dry-run.
    - After computing the candidate lock in memory, pin/bump re-runs catalog.Load against it and refuses to write (coded error, nonzero exit) if the resulting catalog fails validation or recipeDigest cannot be recomputed — a bump can never emit an unloadable lock.
    - WriteLock output is proven byte-stable: marshaling the same PinLock twice (with the in-memory map reordered) yields identical bytes including the do-not-edit header, asserted in a test.
- **E11-S2 — Full-set reproducibility gate and recorded pin delta for scheduled bumps**  `[extend · M · FR-17]`
  - *Intent:* A constraint-driven bump must prove reproducibility across the whole affected component set (not a single smoke component) and record exactly what changed before the pin change can land, on any of the four CI engines.
  - *Acceptance criteria:*
    - emitUpdateWorkflow attests+verifies the full set of components touched by the bump (default: every source's components; overridable via `--only`), so a bump that breaks byte-identity for ANY component fails the PR gate; a test asserts the emitted YAML verifies more than the lone `kubectl` node and ends in an unconditional verify.
    - The workflow writes a machine-readable pin delta (source: old→new tag+commit and the constraint that selected each) into the PR body/commit, derived from the `--dry-run` delta — no hidden bumps.
    - `catalog update-ci --engine github|gitlab|tekton|argo` reuses the existing emitters (one-lock-many-adapters) so each engine lowers to the same bump→build→verify sequence with the engine's always-run guard (`if: always()`/`when: always`/`finally:`/finished-state depends); a test asserts each engine's bump job carries the verify step.
    - The gate fails closed (nonzero, coded) when verify reports any divergence; a test drives a simulated post-bump digest mismatch through the emitted verify step semantics.
- **E11-S3 — Artifact-pin idempotency, upstream-mutation detection, and pinned-input retrievability audit**  `[harden · M · FR-17]`
  - *Intent:* Make the no-compile lane's machine-managed pins tamper-evident and re-verifiable, satisfying 'pinned inputs remain retrievable by immutable identity for the release lifetime.'
  - *Acceptance criteria:*
    - `catalog pin-artifacts` cross-checks the upstream `.sha256` sidecar against a full download-and-hash when both are obtainable and fails closed (CodeFetchFailed) on disagreement instead of trusting the sidecar blindly; a test with a stubbed sidecar/body mismatch asserts the failure.
    - Re-running `pin-artifacts` on unchanged upstreams is a no-op (zero lock diff); if an already-pinned URL/image now resolves to a DIFFERENT digest, the command fails closed naming the artifact — an upstream mutation is never silently re-pinned — unless an explicit `--repin` is passed.
    - A new `catalog verify-pins` re-resolves every source commit/tree and every artifact digest against the lock and reports, with a distinct coded exit, any input that no longer retrieves or no longer matches — the retrievability/immutability audit for a published catalog.
    - All three paths return distinct documented coded errors and are unit-tested with fakes so no live network is required in `make test`.
- **E11-S4 — Immutable publication identity + append-only publication ledger (correction = new identity)**  `[new · L · SEC-16]`
  - *Intent:* Enforce SEC-16: a published manifest is content-addressed and immutable; any correction yields a NEW identity and never overwrites a prior publication — and demonstrate UC-05 (a bump publishes a new immutable release without changing the selection interface).
  - *Acceptance criteria:*
    - The published manifest carries a canonical publication identity = sha256 of its deterministic Marshal (distinct from recipeDigest, which covers only the IR); a test asserts two byte-identical manifests share the identity and any single field change yields a different one.
    - `nkdctl attest publish --index <path>` appends {publicationIdentity, recipeId, recipeDigest, createdEpoch (operator-supplied input, never time.Now)} to an append-only, deterministically-marshaled ledger; re-publishing the SAME identity is a no-op, and attempting to overwrite an existing identity's bytes in place is refused with a new coded error (e.g. CodeImmutableViolation) — asserted by test.
    - UC-05 demonstrated: a simulated `catalog bump` that changes a pin produces a new recipeDigest → new publication identity; the prior ledger entry stays byte-unchanged AND the criteria selection interface (role×k8sVersion×os×arch) still resolves the recipe unchanged — a before/after test asserts distinct identities with stable selection.
    - The ledger and identity work fully offline (no network, no signing dependency) so immutability holds even before a signature is attached.
- **E11-S5 — Execute and test the detached signature over the publication identity**  `[harden · M · SEC-16]`
  - *Intent:* Make 'a correction is a new SIGNED identity' real by actually executing (not just constructing) the detached keyless signature over the publication-identity bytes and round-tripping it in test.
  - *Acceptance criteria:*
    - `attest --sign`/`--bundle` invokes sign.Run over the canonical publication-identity bytes and writes the detached signature/cert/bundle; a guarded integration test (cosign-on-PATH or the faked signer seam) asserts a signature is produced and that `verify` accepts it and fails closed when the manifest bytes are altered post-signing.
    - The signature is asserted DETACHED (never embedded in any artifact) and computed over the publication identity, so re-signing unchanged bytes is reproducible and any byte change invalidates the prior signature.
    - A faked Signer seam (mirroring the AICR Bundler injection pattern) lets `make test` exercise the sign→verify round-trip with no network or real cosign; a unit test covers the seam and the fail-closed path when the signer is required but absent.
- **E11-S6 — Signed status-declaration object + `nkdctl status declare` (supersede/deprecate/withdraw)**  `[new · L · SEC-16]`
  - *Intent:* Add the entirely-new UC-11 primitive: a deterministic, detached-signable declaration that changes the STATUS of a published identity without ever touching the original publication or its evidence.
  - *Acceptance criteria:*
    - New `pkg/nkd/status` defines StatusDeclaration{schemaVersion, target (immutable publication identity/digest), action ∈ {superseded, deprecated, withdrawn}, replacedBy (required for superseded, an immutable identity), reason, effectiveEpoch (operator-supplied input, never clock/random)}; it marshals deterministically (sorted keys) and its own identity is its content digest.
    - `nkdctl status declare --target <digest> --action ... [--replaced-by <digest>] --reason ... [--effective <epoch>] [--sign]` emits the declaration (and a detached signature with `--sign`); a test asserts `superseded` without `--replaced-by`, or any non-digest target, fails closed with a coded error, and that emitted bytes are byte-identical across repeated runs.
    - The declaration references its target ONLY by immutable identity and the command never opens or rewrites the target manifest, ledger entry, or evidence bundle — a test asserts all target files are byte-unchanged after `status declare`.
    - `nkdctl status show <dir>` lists declarations in deterministic order with signed status; `status verify` checks a declaration's signature and identity offline and fails closed on tamper.
- **E11-S7 — verify/resolve consume status declarations (withdrawn fails closed; superseded/deprecated surface replacement)**  `[new · M · SEC-16]`
  - *Intent:* Close the UC-11 loop so a signed status declaration actually governs consumption — honoring immutability (original untouched) and the fail-closed, offline discipline.
  - *Acceptance criteria:*
    - `nkdctl verify --status <dir|declaration>` (and recipe/resolve selection) consults matching signed declarations: a WITHDRAWN target fails closed with a distinct coded error (e.g. CodeWithdrawn) and nonzero exit; DEPRECATED prints a warning but proceeds; SUPERSEDED warns and names the replacement identity — tests cover all three actions.
    - Status consumption is offline-capable (declarations + detached signatures verified with no network, mirroring the `--offline` bundle path) and fails closed if a declaration's signature is required but missing/invalid.
    - An unsigned/unverifiable declaration is ignored for enforcement unless `--require-status-signature` is set, in which case it fails closed — no silent trust of an unsigned withdraw (guards against an attacker-supplied withdraw DoS).
    - Consumption never modifies the original manifest or evidence; a test asserts selection of a non-withdrawn identity is unaffected by unrelated declarations, and that a future-dated effectiveEpoch is not yet enforced without importing a clock into the enforcement path.

*Definition of done —*
- `catalog pin|bump|pin-artifacts` run under bounded context timeouts, offer `--dry-run` deltas, re-Load before writing, and produce byte-stable locks; the scheduled bump gate verifies the full affected component set on the selected CI engine, records the pin delta, and fails closed on any divergence (tests green, no live network in `make test`).
- Artifact pins are idempotent and tamper-evident: sidecar/body cross-check, upstream-mutation detection with explicit `--repin`, and a `catalog verify-pins` retrievability audit, each with a distinct coded exit.
- A published manifest carries a content-addressed publication identity backed by an append-only ledger that refuses in-place mutation (correction = new identity); the UC-05 before/after test proves a bump yields a new identity with a stable selection interface and an unchanged prior ledger entry.
- The detached keyless signature is actually executed over the publication identity and round-trips through verify (real cosign or the faked seam), failing closed on post-sign byte changes.
- `pkg/nkd/status` plus `status declare|show|verify` emit deterministic, detached-signable supersede/deprecate/withdraw declarations that reference targets only by immutable identity and never modify the original publication or evidence.
- `verify`/resolve consume signed status declarations offline — withdrawn fails closed (new coded error wired into errors.go with a distinct exit code), superseded/deprecated surface the replacement — with tests for all three actions and the require-signature path.

#### Release Lifecycle — Sets, Compatibility, Gates, Rollback, Inventory  ·  stories E12-S*  ·  `poc: mixed`

Deliver the release-lifecycle layer that sits above per-component reproduction: a digest-addressable, atomic release set bound to first-class ValidationVectors and a pinned NKE software inventory, gated by two independent mandatory checks (byte reproduction × runtime conformance), selectable and rollback-able by immutable digest, and consumable by a downstream lifecycle system through a signed pre-mutation clearance — without nkdctl ever running a cluster or mutating infrastructure.

- **E12-S1 — First-class ValidationVector / compatibility-record object**  `[extend · M · FR-18, SEC-08]`
  - *Intent:* Promote the current attest.Manifest.ValidationVectorRef *string* into a first-class, deterministically-serialized, digest-addressable release.ValidationVector (new pkg/nkd/release) that binds one recipeDigest + one builderProfile digest to the expected artifact identities and evidence digests, mirroring SRD worked examples 5.5/5.7.
  - *Acceptance criteria:*
    - release.ValidationVector carries recipeDigest, builderProfile{id,digest,hostControl}, artifacts[]{id,role,format,logicalContentDigest,contentManifestDigest,sha256} and evidence{sbomDigest,provenanceDigest,e2eTestDigest,clusterValidationDigest}; Marshal() is deterministic (sorted keys, artifacts sorted by id, no host/clock/random) and Digest()=sha256 of that serialization is stable across 100 marshals in a test.
    - release.VectorFromReport(report, profile, evidence) builds a vector from a local pipeline.Report + builder profile + evidence refs with zero host/clock/random fields; two builds of the same recipe under the same profile emit byte-equal vectors and equal Digest() (test).
    - A vector with an empty/unpinned recipeDigest or builderProfile.digest fails construction with a distinct coded error (CodeVectorUnpinned) and exit code, fail closed (test).
    - attest.Manifest.ValidationVectorRef is populated from vector.Digest() as an immutable self-reference, and a test detects a mismatch between the referenced vector digest and the recomputed vector digest.
- **E12-S2 — Atomic release set + completeness gate (partial sets not promotable)**  `[new · L · FR-18]`
  - *Intent:* Add release.ReleaseSet aggregating the per-role/per-arch recipe manifests, their ValidationVectors, evidence bundles, and SBOM/provenance refs into one digest-addressable set, with an all-or-nothing completeness predicate so a partial set can never be promoted.
  - *Acceptance criteria:*
    - release.ReleaseSet{version, members[]{role,arch,recipeDigest,vectorDigest,evidenceDigest,artifactDigests}, inventoryRef, requiredMembers[]} has a deterministic Digest(); `nkdctl release assemble --out release-set.json` writes it and prints the set digest.
    - ReleaseSet.CheckComplete() returns CodeReleaseIncomplete (distinct exit code) and NAMES every missing required member/artifact/evidence when any declared-required item is absent or its gate verdict is not pass; a set missing one required arch's disk artifact is rejected (test).
    - `nkdctl release promote` fails closed and produces no promotable output unless CheckComplete() passes AND every member manifest verifies (recipeDigest + profile); a test asserts a partial set exits non-zero and yields nothing promotable.
    - The set is immutable: re-assembling with any changed member produces a different set digest (a correction is a new identity, never a mutation) — asserted by a test.
- **E12-S3 — Two independent mandatory promotion gates (reproduction × runtime conformance)**  `[harden · M · SEC-08]`
  - *Intent:* Enforce SEC-08 — reproduction and runtime conformance are two independent gates, neither substitutes for the other. nkdctl already runs the reproduction gate and records a conformance CLAIM; add ingestion of an operator-supplied, bound conformance/NKE-compat RESULT and require BOTH verdicts for promotion, handing off cluster execution honestly.
  - *Acceptance criteria:*
    - release.Gate records two independent per-member verdicts: reproduction{reproduced|validated-variant|rejected} (from existing verify/evidence) and conformance{passed|failed|absent} ingested from `--conformance-result <path>` (recorded by digest); a member with reproduction=reproduced but conformance=absent is NOT promotable (CodeGateUnmet), and the reverse also fails — both asserted by tests.
    - nkdctl does NOT run the cluster: the conformance result is an INPUT only; a missing result makes the gate `absent` and fails closed, never inferred from the reproduction verdict — a test proves a passing reproduction cannot satisfy the conformance gate.
    - The conformance result must bind to the member's recipeDigest + validationVector digest; a result bound to a different recipe/vector is rejected with CodeGateUnmet (prevents cross-binding a stale/foreign result) — test.
    - docs/checklist.md NKD-FR-REP-038/039 updated from partial to reflect the executed two-gate enforcement (claim + result ingest + verdict), with cluster execution documented as out-of-scope-by-design handoff.
- **E12-S4 — Registry integrity — select by immutable digest; tag→digest resolve + digest+signature verify**  `[new · M · SEC-13]`
  - *Intent:* Enforce SEC-13 — a compromised operator registry MUST NOT let a mutable tag bypass digest+signature verification. Make release-set/member selection digest-first: any tag is resolved to a digest, then digest+signature verified before use; a bare tag that cannot be pinned-and-verified fails closed.
  - *Acceptance criteria:*
    - release.Resolve(ref) accepts name@sha256:… (used directly) or a mutable name:tag (resolved to its current digest, then treated as the immutable ref); the recorded/effective selection is the resolved digest, not the tag string — a test where a tag's digest changes proves selection is by digest.
    - `nkdctl release verify/promote/rollback` on a tag-only ref that fails cosign identity/issuer verification exits non-zero with a distinct code (CodeUnverifiedRef); a tampered or absent signature fails closed (test).
    - After resolution the verified digest is compared to any operator-supplied expected digest; a registry that served a different digest for the tag fails closed naming served-vs-expected — test.
    - Offline path preserved: with --bundle/--offline, digest resolution uses a local mirror + the mirrored bundle's Rekor inclusion proof (reusing sign.VerifyBlobArgs), no network, same fail-closed semantics.
- **E12-S5 — Downstream-consumer verification gate (NKE-facing pre-mutation clearance)**  `[new · L · SEC-17]`
  - *Intent:* Satisfy SEC-17 — a downstream consumer MUST verify identities, signatures, provenance, completeness, and compatibility before mutating infrastructure. nkdctl runs no lifecycle, so it produces one verify verb that checks all five properties fail-closed and emits a machine-readable clearance the consumer gates on.
  - *Acceptance criteria:*
    - `nkdctl release verify --release-set <ref> [--inventory <ref>] --identity --issuer` performs, all fail-closed: (1) signature+identity/issuer, (2) per-member provenance present+verified, (3) per-member reproduction verdict, (4) completeness (CheckComplete), (5) compatibility vs the pinned inventory; the first failing property is named with its distinct coded error and non-zero exit.
    - On full pass it emits a deterministic release.Clearance{releaseSetDigest, verifiedProperties[], inventoryDigest, verdict:cleared} with no host/clock/random; nkdctl performs NO infra mutation (asserted: verify is read-only, writes only the clearance file).
    - A set that reproduces byte-for-byte but fails compatibility (a member's k8sVersion/NKE range not in the inventory) is NOT cleared (CodeCompatMismatch) — a test proves reproduction alone never clears the consumer gate.
    - Missing/unverifiable provenance for any member blocks clearance (CodeProvenanceMissing) — a test proves an SBOM-only member (no SLSA provenance) fails closed.
- **E12-S6 — Recover / roll back to a previously verified release set**  `[new · M · SEC-13, SEC-17]`
  - *Intent:* Advance UC-15 — restore a previously verified release set after a failed upgrade/remediation with no mutable or unverified artifacts, re-running the full clearance path before the prior set becomes the active selection.
  - *Acceptance criteria:*
    - `nkdctl release rollback --to <releaseSetDigest>` re-runs the S5 clearance path against the target set before emitting it; a target that no longer verifies (revoked signature, missing artifact) is refused with the corresponding coded error — rollback never restores an unverified set (test).
    - The rollback target is an immutable set digest only; a tag/mutable ref is rejected (CodeUnverifiedRef) via S4 resolution — a mutable rollback target fails closed (test).
    - Rollback emits a deterministic release.Selection{activeReleaseSetDigest, supersedes} with no clock/host; re-running rollback to the same digest is idempotent (byte-equal Selection) — asserted by a test.
- **E12-S7 — Versioned NKE software inventory as a pinned input bound to a release**  `[new · M · FR-18, SEC-17]`
  - *Intent:* Advance UC-16 — consume an authoritative, versioned, machine-readable NKE software inventory as a pinned input, bind it into the release-set digest, and derive completeness (S2) and compatibility (S5) from it.
  - *Acceptance criteria:*
    - release.Inventory{schemaVersion, version, components[]{name,role,versionConstraint,requiredArtifacts[],externalSBOMRef,externalProvenanceRef}, compatibility{kubernetes[],nke[],platform[]}} loads from `--inventory <ref>` and is pinned by digest; a tag/unpinned inventory ref fails closed (CodeInventoryUnpinned) — test.
    - ReleaseSet.inventoryRef binds the inventory digest into the set Digest() (rebinding a different inventory changes the set digest) — asserted by a test.
    - CheckComplete() derives its required-artifact list FROM the inventory: a set missing an inventory-required component/artifact fails closed naming it; an inventory-declared external artifact records its verified externalSBOMRef/externalProvenanceRef instead of requiring a from-source build — both asserted.
    - Compatibility (S5) evaluates a member's k8sVersion/NKE version against the inventory's compatibility ranges; an out-of-range member yields CodeCompatMismatch — test.

*Definition of done —*
- New pkg/nkd/release package with ValidationVector, ReleaseSet, Inventory, Gate, Clearance, and Selection types — all deterministically serialized (sorted, no host/clock/random) and digest-addressable, with determinism/byte-equality tests green.
- `nkdctl release assemble|verify|promote|rollback` subcommands wired over the SDK facade; partial-set promotion, unmet gate, mutable/unverified ref, compatibility mismatch, and unpinned inventory each fail closed with a distinct coded exit code and a test.
- Two independent gates enforced: a member is promotable only when its reproduction verdict AND an ingested, bound conformance result both pass; nkdctl runs neither the cluster nor any infra mutation (handoff documented).
- Release sets/members are selected, promoted, and rolled back only by immutable digest (tags resolved-then-verified); rollback restores only a re-verified set and is idempotent.
- The consumer clearance attestation is emitted only when identity+signature, provenance, reproduction, completeness, and compatibility all pass; new error codes registered with distinct exit codes and docs/checklist.md updated for FR-18/SEC-08/SEC-13/SEC-17 + REP-038/039 with evidence.

#### Substrate Delivery — Packages, Images, Bootable Disk  ·  stories E13-S*  ·  `poc: mixed`

Turn every verified from-source binary into deterministic, digest-selectable delivery artifacts — .deb host packages, control-plane OCI images, and RAW-canonical node disk images (with qcow2/vmdk/vhd/ISO envelopes verified back to the RAW logical-disk digest) — so an operator can install the substrate post-provision on any base OS, and a downstream fleet can consume a complete, digest-verified artifact collection.

- **E13-S1 — Promote disk assembly into a tested Go path and record the canonical RAW logical-disk digest**  `[harden · M · FR-14, FR-13]`
  - *Intent:* Disk assembly is shell-only (scripts/_assemble-disk.sh via runScript) with no coded errors, no unit tests, and assemble.LogicalDiskDigest/BlockManifestOf are never invoked by the CLI. Wrap the assembly in pkg/nkd/assemble so nkdctl disk computes, prints, and writes a deterministic sidecar of the canonical RAW logical-disk digest + block manifest, and returns distinct coded errors on failure — making the RAW boundary a tool output, not a script comment.
  - *Acceptance criteria:*
    - nkdctl disk computes assemble.LogicalDiskDigest over [0,virtual_size) of the produced RAW and a BlockManifestOf at a fixed block size, prints both and writes a deterministic sidecar (no timestamps/host paths in it); two runs on the same inputs yield identical digest and sidecar bytes.
    - A Go test asserts BlockManifestOf.Aggregate equals LogicalDiskDigest for a fixture RAW and that the block series length matches ceil(size/blockSize).
    - Disk-assembly failure returns a distinct coded exit code (CodeBuildFailed for tool failure, CodeUnsupported for a bad format/role) rather than a bare shell non-zero; a test forces a failure and asserts the code.
    - Existing assemble tests (Deb/OCI/Tarball/LogicalDisk) remain green; the deterministic invariants (fixed GPT/partition GUIDs, ext4 UUID/hash_seed, SOURCE_DATE_EPOCH) are documented and referenced from Go, not only the script.
- **E13-S2 — Derived-envelope format matrix (raw|qcow2|vmdk|vhd) verified by decoded logical-disk digest**  `[extend · L · FR-14, FR-13]`
  - *Intent:* --format accepts only qcow2 and never proves the envelope decodes back to the canonical RAW. Implement each envelope as a deterministic, pinned conversion of the single canonical RAW, then decode it (qemu-img) and assert the decoded logical-disk digest equals the RAW's canonical digest — fail closed naming the divergence. The digest recorded/attested for a disk artifact is always the RAW logical-disk digest, never the container-file digest.
  - *Acceptance criteria:*
    - nkdctl disk --format {raw,qcow2,vmdk,vhd} all produce an artifact from one canonical RAW; an unknown format still fails closed with a coded error (CodeUnsupported), preserving current fail-closed behavior on the extended set.
    - After producing an envelope the tool decodes it and asserts decoded-logical-disk-digest == canonical RAW digest; a mutated/re-encoded envelope fails closed with CodeVerifyFailed naming the first divergent block, covered by a test.
    - A test asserts qcow2 and vmdk built from the same RAW record the SAME canonical digest (container-file digests may differ, canonical digest does not), enforcing 'RAW is canonical, envelopes are derived'.
    - attest/verify for a disk artifact consume the RAW logical-disk digest (wired through, not container bytes); a test round-trips assemble→attest→verify on a fixture disk and fails closed on tamper.
- **E13-S3 — First-class deterministic ISO output (xorriso/mksquashfs, GPL-shelled)**  `[new · L · FR-14, FR-13]`
  - *Intent:* ISO is named canonical alongside RAW in the SRD but is entirely absent. Add a deterministic ISO builder that shells xorriso/mksquashfs inside the pinned rootfs toolchain (GPL tools as subprocesses only — Apache-clean core preserved), producing a byte-reproducible installer/live ISO with a recorded canonical digest, selectable via the same --format switch.
  - *Acceptance criteria:*
    - nkdctl disk --format iso produces a bootable ISO; two independent assemblies are byte-identical (fixed volume-id, sorted directory order, SOURCE_DATE_EPOCH + frozen-clock timestamps), asserted by a two-assembly compare in a docker/tool-tagged test.
    - The ISO's recorded canonical digest is stable across runs and changes when SOURCE_DATE_EPOCH changes (no clock leakage), asserted by test.
    - xorriso/mksquashfs are invoked only as subprocesses in the pinned toolchain (no copyleft entering nkdctl's linked deps); the checklist entry NKD-FR-BLD-060/INT-020 flips from 'ISO absent' to implemented with evidence.
- **E13-S4 — Role-parameterized node SKUs — distinct control-plane and worker disk artifacts**  `[extend · M · FR-14, FR-13]`
  - *Intent:* _assemble-disk.sh bakes every binary into one disk; the two SKUs exist only at recipe resolution. Parameterize disk assembly by --role so each SKU stages exactly the components the existing criteria resolver returns, yielding two distinct, individually-reproducible node artifacts with distinct canonical digests — reusing resolution, adding no build semantics.
  - *Acceptance criteria:*
    - nkdctl disk --role control-plane and --role worker each stage exactly the resolver's component set for that role (control-plane includes kube-apiserver/etcd/scheduler/controller-manager; worker excludes them), asserted by inspecting the staged manifest.
    - Each SKU is byte-reproducible across two assemblies, and a test asserts the control-plane and worker canonical RAW digests are distinct.
    - Role selection routes through the existing criteria/recipe resolver (no duplicated component lists); an unsupported/empty role fails closed with a coded error (CodeUnsupported/CodeNoMatch).
- **E13-S5 — Operator-configurable provisioning artifacts (PXE / iPXE / HTTP-boot)**  `[new · L · FR-14, FR-13]`
  - *Intent:* Only direct qcow2/OVMF boot exists; there is no operator-configurable provisioning path. Emit a deterministic PXE/iPXE/HTTP-boot bundle (kernel + initrd + boot script + a digest manifest) derived from the canonical RAW/rootfs, so any standard PXE/HTTP-boot environment can provision the node. nkdctl produces the artifacts and evidence; it does not run the boot/DHCP server (fleet lifecycle stays downstream).
  - *Acceptance criteria:*
    - nkdctl disk --provision {pxe,ipxe,http} emits kernel+initrd+boot script + a manifest listing every artifact by sha256; the base URL is an operator flag (no hardcoded server), so nothing fixed is baked into the substrate.
    - The emitted bundle is deterministic (identical across two runs); a test asserts stability and that the boot script references artifacts strictly by digest.
    - Only standard PXE/iPXE/HTTP interfaces are used; a test asserts the manifest is complete (every emitted file digested) and that a missing/renamed artifact fails closed.
- **E13-S6 — FDE as an off-by-default overlay with byte-identity asserted pre-encryption only**  `[new · M · FR-14, FR-13]`
  - *Intent:* FDE is fully deferred and the pre-encryption boundary is honored merely by omission. Add an explicit, off-by-default LUKS overlay applied only AFTER the canonical (pre-encryption) RAW is produced and its logical-disk digest recorded; the reproducibility/verify contract attaches to the pre-encryption image, and the tool refuses — with a coded error — any attempt to assert byte-identity on the encrypted output.
  - *Acceptance criteria:*
    - FDE is off by default; --fde luks produces an encrypted overlay only after the pre-encryption canonical RAW digest is recorded/attested; a test asserts default-off and that the pre-encryption digest is emitted before encryption.
    - verify/attest scope the byte-identity assertion to the pre-encryption digest; asking the tool to assert reproducibility on encrypted bytes fails closed with a distinct coded error (explicit exclusion, not silent by-omission), covered by a test.
    - The exclusion is legible in output/checklist (NKD-FR-BLD-064/065, NKD-FR-REP-028/029, NKD-CON-013) — flipped from 'by construction only' to an asserted, tested boundary.
- **E13-S7 — Unified deterministic-assembly gate across .deb / OCI / disk with epoch-sensitivity and bounded ext4 residual**  `[harden · M · FR-14]`
  - *Intent:* FR-14 requires .deb, OCI, and disk all byte-reproducible, but package/OCI are unit-tested while disk determinism is only a manual two-assembly shell compare with an uncharacterized ~969-block ext4 inode-table residual. Add a single determinism gate (Go test + make target) that re-runs each assembler twice, asserts byte-identity and SOURCE_DATE_EPOCH-sensitivity per class, and either eliminates or reduces the full-rootfs ext4 residual to a documented, test-asserted deterministic bound.
  - *Acceptance criteria:*
    - One test/target runs each assembler twice and asserts byte-identical output for .deb, OCI layout, tarball, and RAW disk; and asserts the digest changes when SOURCE_DATE_EPOCH changes for each class (no accidental clock/host leakage).
    - The ext4 full-rootfs inode-table residual is eliminated or normalized to a documented, deterministic, machine-checkable bound (region masked/pinned), so disk determinism is a green assertion rather than narrative — NKD-FR-REP-008/NKD-NFR-009 updated with evidence.
    - The pure portions run in the make check path; the docker/tool-dependent disk portion is gated behind a build tag/target and fails closed on any drift.
- **E13-S8 — Executed packages-first / thin-base / BYO-OS install proof**  `[harden · M · FR-14, FR-13]`
  - *Intent:* Packages-first (.deb + control-plane OCI, thin base, installed post-provision) is marked done but apt-repo install verification was never performed and 'installed post-provision' is only shell smoke. Add an executed proof that the produced .debs install cleanly into the thin mmdebstrap base and the OCI images load, with the base OS remaining operator-substitutable — flipping NKD-FR-BLD-040/075/076/077 evidence from narrative to tested.
  - *Acceptance criteria:*
    - A CUJ/test installs the produced .debs into the thin base (dpkg -i in the pinned toolchain) and asserts binaries land at the expected paths and run --version; documents the apt/deb-repo consumability caveat as closed.
    - Control-plane OCI images load from the OCI layout (skopeo/ctr) and pass assemble.VerifyLayout; a test asserts the substrate is delivered as separate packages/images, not baked into the base rootfs (BYO-OS decoupling holds).
    - checklist entries for packages-first/thin-base/decoupled delivery cite the executed evidence run, not construction-only reasoning.

*Definition of done —*
- nkdctl disk records and attests the canonical RAW logical-disk digest + block manifest for every disk artifact, with coded fail-closed errors — not a shell-only path.
- --format {raw,qcow2,vmdk,vhd,iso} all produce deterministic artifacts, each derived envelope verified to decode back to the canonical RAW digest (fail closed on divergence); ISO is first-class.
- Distinct, individually-reproducible control-plane and worker node SKUs are producible by role, plus deterministic PXE/iPXE/HTTP provisioning bundles referenced by digest.
- FDE is an explicit off-by-default overlay with byte-identity asserted (and encrypted-bytes assertions refused) strictly pre-encryption.
- A unified determinism gate asserts byte-identity + SOURCE_DATE_EPOCH-sensitivity for .deb/OCI/tarball/RAW disk, with the ext4 residual eliminated or bounded and machine-checked.
- Packages-first / thin-base / BYO-OS delivery is proven by an executed install/load run, and every touched checklist requirement (NKD-FR-BLD-040/060/061/062/063/064/065/075/077, REP-008/028/029, INT-020/035) is updated with real evidence.

#### CLI Capstone & External Interface Seams  ·  stories E14-S*  ·  `poc: mixed`

Complete the operator-facing CLI surface on top of the E0 foundation: the missing verbs (query, diff, trust; catalog author, catalog publish), uniform --data on every catalog-consuming command, the standard/replaceable external-interface seams (OCI referrers / apt-deb / object store), and application of the E0 stdout/stderr + --output json contract across all subcommands.

- **E14-S1 — Complete the subcommand surface: query, diff, trust**  `[new · L · UC-12, FR-21]`
  - *Intent:* Add the three missing operator-facing verbs required by the surface, each fail-closed and machine-consumable: selector-path value extraction, a minimal reproducible recipe diff for the CVE fast-path/handoff, and a persisted trust store for verify's OIDC identities.
  - *Acceptance criteria:*
    - `nkdctl query <selector>` extracts a single value or list from the resolved IR by a documented minimal selector grammar (e.g. .recipeDigest, .components[*].toolchain), prints machine-consumable output, and fails closed with a coded error on an unknown path (INT-016).
    - `nkdctl diff --from <irA> --to <irB>` (or two queries) deterministically computes the minimal reproducible recipe diff — the set of changed pinned fields (commit/treeHash/toolchain/artifact digest) — and exits non-zero when they differ; a test asserts a single-commit bump yields a one-line diff (INT-039).
    - `nkdctl trust {add,list,remove}` persists OIDC issuer+identity pairs to a documented trust-store file, and verify consumes the store when no explicit --issuer/--identity is given, failing closed (coded) when a required signature check has no trusted identity (INT-046).
    - All three commands carry --data where they resolve a catalog, return coded errors mapped to exit codes, and are registered in main.go / exposed on the SDK facade.
- **E14-S2 — Add catalog author and catalog publish subcommands**  `[new · L · FR-21]`
  - *Intent:* Complete the catalog lifecycle verbs so an operator can declaratively add a component and publish a signed, publisher-agnostic catalog+manifest bundle — without touching the embedded default and without code changes.
  - *Acceptance criteria:*
    - `nkdctl catalog author add|edit` writes/updates a component entry in a --data catalog's registry.yaml (source ref, output type, digest-pinned toolchain), then runs catalog.Validate and fails closed with a coded error if the result is unpinned / tag-toolchain / invalid — proving a component is added with no code change (INT-044).
    - `nkdctl catalog publish` emits a publisher-agnostic bundle (catalog + expected-digests manifest) to a target dir or OCI ref and shells the sign seam to produce a detached signature; the embedded default catalog stays from-source and untouched (INT-045).
    - A test round-trips author->load->resolve (the authored component resolves through recipe.Resolve) and asserts publish produces a deterministic bundle whose manifest recipeDigest equals a direct attest run's.
- **E14-S3 — --data on every subcommand; publisher-agnostic uniformity**  `[harden · S · FR-21]`
  - *Intent:* Eliminate the provider-selection inconsistency so every catalog-consuming command replaces the embedded default the same way: add --data to image, unify catalog pin/bump/pin-artifacts/update-ci off --dir, and route all commands through the single providerFor helper.
  - *Acceptance criteria:*
    - `image` gains --data via providerFor, and catalog pin/bump/pin-artifacts/update-ci accept --data (keeping --dir as a documented deprecated alias) so provider selection is uniform across the whole command tree.
    - A test enumerates app.Commands recursively and asserts every command that loads a catalog exposes a --data flag and obtains its provider only via the single providerFor helper (no direct catalog.Embedded() in command Actions).
    - `--data <dir>` on any such command fully replaces the embedded default with no code path referencing NVIDIA/NKD identity, and a bad --data path fails closed with CodeInvalidCatalog.
- **E14-S4 — Standard, replaceable external-interface seams (OCI referrers / apt-deb / object store)**  `[extend · L · FR-21]`
  - *Intent:* Introduce operator-swappable Go interfaces for the three external service classes with permissive default implementations, so nkdctl integrates only via standard interfaces and an operator can inject an equivalent without importing package main — including attaching manifest/SBOM/signature as OCI referrers.
  - *Acceptance criteria:*
    - New interfaces OCIRegistry (Push + PutReferrer), PackageRepo (Publish .deb), and ObjectStore (Put/Get) are defined with permissive default implementations (subprocess/stdlib) and swap seams exposed as Client options (WithOCIRegistry/WithPackageRepo/WithObjectStore).
    - The default OCIRegistry impl pushes a deterministic OCI image and attaches the expected-digests manifest + SBOM + signature as OCI referrers to the image digest (INT-034/048); no interface type references any NVIDIA-internal package.
    - A test injects a fake OCIRegistry via option and asserts the facade calls it rather than the default, and that the default impl against a local registry produces a standard referrers index.
- **E14-S5 — Structured logs to stderr; machine-consumable result streams**  `[extend · M · FR-20-adjacent]`
  - *Intent:* Separate diagnostics from results cleanly: route all logging through a leveled structured logger on stderr while keeping the IR/SBOM/manifest/report result streams machine-consumable on stdout, honoring NO_COLOR and a log-level control.
  - *Acceptance criteria:*
    - A leveled structured logger (log/slog) writing to stderr is injected via WithLogger (default: warn-level text to stderr); all diagnostic fmt.Fprintf(os.Stderr,...) migrate to it and result streams (IR, SBOM, manifest, report) remain on stdout/files only.
    - A global --log-level flag plus NKDCTL_LOG env var set the level, NO_COLOR disables any ANSI, and report-producing commands accept --output json for machine-consumable structured output.
    - A test captures stdout and stderr for a resolve run and asserts stdout is a single valid parseable result (IR/JSON) with zero log lines, all logs go only to stderr, and NO_COLOR yields no escape sequences.

*Definition of done —*
- query, diff, and trust are registered, --data-uniform, fail closed with coded errors, and exposed on the SDK facade (INT-016/039/046).
- catalog author adds a component declaratively (validate fail-closed) and catalog publish emits a signed publisher-agnostic bundle; the embedded default stays from-source and untouched (INT-044/045).
- --data (or a documented alias) is present on every catalog-consuming command via the single providerFor helper (INT-012).
- Standard external-interface seams (OCIRegistry referrers / PackageRepo / ObjectStore) exist with permissive default impls swappable via Client options (INT-034/048).
- The E0 stdout=results/stderr=logs + --output json contract is applied uniformly across the new verbs.


---

## 6. Suggested next steps

1. **Ratify the 4-epic split** and the E1 → E2 → E3 → E4 order.
2. **Start E1's sprint-0 freeze set** (design-plan §2) and the reproducibility spikes (§6) — they gate the critical path.
3. **On the critical path, prioritize `E5-S1` (execute cosign)** in E3 as soon as E2's foundation + build land; it unblocks
   the real trust loop.
4. **Lock the non-normative schemas early** — ValidationVector / ReleaseSet / NKE inventory (E1 design + E4 release lifecycle)
   are marked "not decided" in the SRD; agree a `schemaVersion` before many stories reference them.
5. **File the backlog** — the FR/SEC/UC IDs are ready to attach as labels; each epic maps to a tracker epic and its sub-areas
   to sub-epics/labels.

*Generated from `docs/nkd-srd-doc.md` against the POC; the 54 design decisions live in `docs/design-plan.md`. Story keys are
stable across the consolidation. Refresh `docs/checklist.md` as stories land.*
