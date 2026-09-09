# ADR-02 — IR determinism & the purity boundary

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-02 in the consolidated NKD ADR set.
- **Relates to:** ADR-01 (canonical codec), ADR-07 (reproduction identity & builder profile), ADR-05 (trust
  objects), ADR-09 (build record / provenance).

## Context

The **Recipe Digest** is the digest of the fully-resolved, compiled build graph. NKD's define-once /
publish-many model and its zero-binary trust both depend on that identity being a **pure function of resolved
inputs only**. If a host, clock, random, builder, or generated-identifier value leaks into it, the identity
becomes host-dependent and the trust model collapses. The SRD states this directly: output-affecting clock,
randomness, host, and automatically generated identifier values are prohibited from the deterministic definition.

The subtlety is that some values *look* like a clock but are pinned inputs — the source-date derived from a
pinned commit — while others are genuine host observations that must be recorded somewhere and must never touch
the identity. This ADR draws the boundary explicitly and makes it enforceable.

## Decision

### 1. Three-tier field partition

Every field lives in exactly one tier:

- **T1 — inside the Recipe Digest** (the hashed, compiled graph): resolved logical sources (stable catalog
  source key + commit + tree/content digest + source-date), per-component builder pins (`oci-hermetic` toolchain
  digest; `nix` container-image digest + flake lock), prebuilt content digests, the **builder type**, the pinned
  normalization / build-flag environment allowlist, and platform/architecture. A network URL, registry host,
  mirror path, credential reference, or other transport locator is never a T1 field.
- **T2 — signed metadata, deterministic, *not* in the Recipe Digest**: the Recipe Digest itself, the expected
  per-artifact digests, the **builder-profile identity**, the conformance claim, the ValidationVector reference,
  and the accelerated-runtime reference. These are signed **detached** — never embedded in the bytes the
  identity covers.
- **T3 — detached, non-identity record** (may carry host/clock/builder facts; **may be independently signed**):
  the signed build-observations attestation (observed host, actual build times, **observed engine class**), and
  lifecycle times (variant expiry, status effective-time). Operator-supplied lifecycle times are **inputs**,
  never wall-clock reads. Observed wall-clock build times appear **only** in the non-reproducible observations
  attestation, never in an identity or an enforcement path.

### 2. Identity is over the resolved graph; pin-derived time is an input, host time is not

- The Recipe Digest is the digest of the **resolved/compiled** graph — *after* composition and pin resolution,
  not of the authored recipe. Two authored recipes that resolve to the same pinned graph share one identity.
- A value **derived from a pin**, such as the source-date taken from the pinned commit's committer date, is a
  **deterministic input** and belongs in T1.
- The **host wall clock, hostname, home directory, host timezone, and kernel** are observations. They are
  excluded from T1 by construction and may appear only in T3.

### 3. Logical input identity is separate from relocatable transport

Every resolved input has two different representations:

- **`LogicalInput`** — the stable input key and immutable content/revision facts used by T1 and the
  resolved-input manifest; and
- **`TransportBinding`** — one or more fetch locations, credential references, and mirror locations keyed by
  that logical input. Transport bindings are execution configuration, not recipe semantics.

Online and air-gap execution may replace a `TransportBinding` but MUST NOT change its `LogicalInput`. Before an
input crosses into an offline build node, the resolver verifies that bytes fetched through the binding reproduce
the logical tree/content identity. A binding that resolves to different bytes fails closed. The complete binding
map is itself canonical, digest-addressed, and authenticated as part of the mirror/execution manifest (ADR-15),
so excluding locators from `recipeDigest` does not make relocation unaudited.

This separation is structural: the type passed to Recipe Digest computation contains `LogicalInput` values only.
A sanitizer that deletes URL fields immediately before hashing is not sufficient, because a newly added locator
could otherwise leak into identity unnoticed.

### 4. Enforceable purity guard

An automated two-part guard enforces the boundary; documentation alone does not:

- **Static check** — forbids nondeterministic sources (wall-clock reads, randomness, UUID generation, hostname,
  host-environment-into-identity) in the identity-producing code paths. Any use is allowlisted only with an
  explicit justifying note.
- **Empirical invariance test** — mutates host, clock, and builder facts and asserts the Recipe Digest **and**
  the T2 signed-metadata bytes are byte-stable, while mutating any **pinned** input **does** change the identity.

## Consequences

**Positive**
- The Recipe Digest is provably host-independent; the same definition yields the same identity everywhere.
- Host/builder provenance is preserved in T3 without polluting the identity or the signed metadata.
- The guard turns an easily-violated invariant into a CI-enforced one.

**Negative / costs**
- The three-tier placement must be maintained as new fields are added, and the guard's allowlist needs review.
- The pin-derived-time versus host-time distinction is a real trap that reviewers must understand.

**Neutral**
- Detached signing (T2 never embedded) is shared with ADR-04/ADR-03/ADR-05; this ADR only fixes *which tier*
  each field is in.

## Alternatives considered

- **Two tiers (covered vs detached), folding the builder-profile identity into the Recipe Digest.** Rejected —
  makes the Recipe Digest host/profile-dependent and defeats define-once / publish-many.
- **Put builder/host provenance inside the signed metadata (T2).** Rejected — perturbs the signed bytes on every
  host and breaks cross-builder equality of the manifest.
- **Rely on code review to keep the identity pure.** Rejected — the leak is invisible until a digest silently
  changes across hosts; it must be machine-enforced.

## Follow-ups

- **ADR-07:** `profileDigest` is T2; the observed engine class is T3; builder pins are T1.
- **ADR-09:** the build record is the T3 home for host/clock/builder observations.
- **ADR-01:** the identity digest uses the canonical codec and domain tag defined there.
- **ADR-15:** defines the exhaustive authenticated `TransportBinding`/mirror manifest and proves relocation
  preserves every `LogicalInput` and the Recipe Digest.

## Acceptance evidence

Current package-level evidence includes:

- the explicit `ir.LogicalGraph` / `ir.LogicalNode` identity DTOs, which cannot represent source, artifact, or
  flake transport locators;
- `pkg/nkd/ir/marshal_test.go::TestLogicalIdentityDTOClassifiesEveryExecutionField`, whose reflection coverage
  makes every new execution field declare its T1/content-reference/transport/composition class;
- `pkg/nkd/ir/validate_test.go::TestGraphDigestRemainsTransportIndependentAfterFullValidation`, which proves
  independently relocated full execution graphs project to identical logical bytes and `recipeDigest`; and
- `pkg/nkd/ir/marshal_test.go::TestDigestHostInvariance` plus the static guard in
  `internal/architecture/purity_test.go` for host purity.

Remaining end-to-end acceptance evidence requires:

1. negative tests showing one-byte/tree/revision substitution through an authenticated mirror fails before build;
   and
2. an online→air-gap activation round trip that proves the logical-input manifest and `recipeDigest` are
   unchanged while every transport locator changes.
