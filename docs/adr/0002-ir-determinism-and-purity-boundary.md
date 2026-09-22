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

The **Build Digest** is the digest of the logical T1 projection of a fully resolved, compiled build graph.
The full executable graph may also carry deterministic T2 execution metadata that Plan identity binds but Build
identity excludes. NKD's define-once / publish-many model and its zero-binary trust both depend on Build identity
being a **pure function of resolved content inputs only**. If a host, clock, random, builder observation, or
generated-identifier value leaks into it, the identity becomes host-dependent and the trust model collapses. The
SRD states this directly: output-affecting clock, randomness, host, and automatically generated identifier values
are prohibited from the deterministic definition.

The subtlety is that some values *look* like a clock but are pinned inputs — the source-date derived from a
pinned commit — while others are genuine host observations that must be recorded somewhere and must never touch
the identity. This ADR draws the boundary explicitly and makes it enforceable.

## Decision

### 1. Three-tier field partition

Every field lives in exactly one tier:

- **T1 — inside the Build Digest** (the hashed, compiled graph): resolved logical sources (stable catalog
  source key + commit + tree/content digest + source-date), per-component builder pins (`oci-hermetic` toolchain
  digest; `nix` container-image digest + flake lock), prebuilt content digests, the **builder type**, the pinned
  normalization / build-flag environment allowlist, concrete target platform, and exact AICR activation and
  profile/generator pins and effective bundle-rendering settings where present. A network URL, registry host,
  mirror path, credential reference, or other transport locator is never a T1 field.
- **T2 — deterministic metadata, *not* in the Build Digest**: the Build Digest itself, the expected
  per-artifact digests, the **builder-profile identity**, deterministic execution requirements and their
  Plan bindings, the conformance claim, the ValidationVector reference, and the accelerated-runtime reference.
  Output names, supported-platform admission lists and packaging
  are also excluded from build identity. They remain bound in the owning catalog/Definition/Plan
  records. Deployment classification and topology are not admitted NKD inputs. Authority-bearing T2 records are signed **detached**, never embedded in the bytes the identity covers.
- **T3 — detached, non-identity record** (may carry host/clock/builder facts; **may be independently signed**):
  the signed build-observations attestation (observed host, actual build times, **observed engine class**), and
  lifecycle times (variant expiry, status effective-time). Operator-supplied lifecycle times are **inputs**,
  never wall-clock reads. Observed wall-clock build times appear **only** in the non-reproducible observations
  attestation, never in an identity or an enforcement path.

### 2. Identity is over the resolved logical graph; pin-derived time is an input, host time is not

- The Build Digest is the digest of the **resolved/compiled logical T1 projection** of one Definition output
  under `nkd.build`, after exact producer and pin selection. There is no authored Recipe or intermediate group.
  Two named outputs with the same selected producers, target and pinned logical graph share one Build Digest.
- A value **derived from a pin**, such as the source-date taken from the pinned commit's committer date, is a
  **deterministic input** and belongs in T1.
- The **host wall clock, hostname, home directory, host timezone, and kernel** are observations. They are
  excluded from T1 by construction and may appear only in T3.

### 3. Logical input identity is separate from relocatable transport

Every resolved input has two different representations:

- **Logical input** — the stable input key and immutable content/revision facts used by T1 and the
  resolved-input-manifest; and
- **Transport binding** — one or more fetch locations, credential references, and mirror locations keyed by
  that logical input. Transport bindings are execution configuration, not build semantics.

Changing source mirrors or registry transport MUST preserve the selected logical input. Before an input
crosses into an offline build node, execution verifies its complete pinned logical identity: the stable input key
and every immutable content and revision fact recorded in the resolved-input-manifest. Matching fetched bytes or
source-tree content alone is insufficient; a different pinned revision with identical tree bytes fails closed.
The full executable graph and Plan retain transport settings and their exact byte identities. Excluding locators
from `buildDigest` does not authorize unchecked relocation. A general air-gap lifecycle remains outside the
current scope.

The separation is structural: `ir.LogicalGraph` and `ir.LogicalNode` cannot represent source URLs, artifact
locators, flake references, execution requirements or execution bindings. The complete `ir.Graph` is validated
before projection. Clearing strings just before hashing is insufficient, because a new field could otherwise
leak into identity unnoticed.

Generated targets have no display ID inside the graph. Output names bind uses in the Definition and Plan;
they do not change the logical graph. Equal digests never merge independently required uses: execution,
restore and terminal evidence must match the exact output/target as well as its Build Digest.

AICR renderer settings affect produced bytes and therefore belong in T1. Authored omission and an explicit
Helm default compile to the same complete activation. Lists are copied and sorted before compilation;
executable graph validation rejects missing effective defaults, duplicate entries and noncanonical order.
The graph does not infer these settings from an external cluster declaration.

A producer's execution requirements and deterministic class binding have one executable representation: the
full build graph embedded in the Run Plan. Scheduler records reference that exact instruction by output and
node identity; they do not copy its execution settings. Utility operations retain their own instructions. The
logical build projection cannot represent producer execution requirements or bindings. Changing resources, eligible host platforms, typed capability
policy, or executor timeout changes catalog and Plan identities without changing what the selected producers build. The
request is not capability proof: compilation deterministically binds an Environment execution class into Plan
identity, while authenticated runtime observations remain a separate execution-admission record (ADR-18).

The compiler accepts admitted Definition and Environment values and returns a complete Plan. It owns graph
derivation, required output slots, packaging, routing and evidence edges together. The Plan stores independent
copies of the authored inputs and validates their original typed digests on rehydration. No copied compiler-input
document or separate execution-settings digest stands between those inputs and the resulting Plan. Lifecycle
preparation freezes attempt state from that Plan without completing or repairing compiler output.

### 4. Enforceable purity guard

An automated two-part guard enforces the boundary; documentation alone does not:

- **Static check** — forbids nondeterministic sources (wall-clock reads, randomness, UUID generation, hostname,
  host-environment-into-identity) in the identity-producing code paths. Any use is allowlisted only with an
  explicit justifying note.
- **Empirical invariance and sensitivity tests**: `TestDigestHostInvariance` mutates host- and clock-related
  process environment values and asserts that the Build Digest is byte-stable. Separate tests assert that
  mutating a **pinned content input** changes Build identity, while mutating an authored execution request changes
  catalog and Plan identities and full executable graph bytes without changing Build identity.

## Consequences

**Positive**
- The Build Digest is provably host-independent; the same definition yields the same identity everywhere.
- Host/builder provenance is preserved in T3 without polluting the identity or the signed metadata.
- The guard turns an easily-violated invariant into a CI-enforced one.

**Negative / costs**
- The three-tier placement must be maintained as new fields are added, and the guard's allowlist needs review.
- The pin-derived-time versus host-time distinction is a real trap that reviewers must understand.

**Neutral**
- Detached signing (T2 never embedded) is shared with ADR-04/ADR-03/ADR-05; this ADR only fixes *which tier*
  each field is in.

## Alternatives considered

- **Two tiers (covered vs detached), folding the builder-profile identity into the Build Digest.** Rejected —
  makes the Build Digest host/profile-dependent and defeats define-once / publish-many.
- **Put builder/host provenance inside the signed metadata (T2).** Rejected — perturbs the signed bytes on every
  host and breaks cross-builder equality of the manifest.
- **Rely on code review to keep the identity pure.** Rejected — the leak is invisible until a digest silently
  changes across hosts; it must be machine-enforced.

## Follow-ups

- **ADR-07:** the builder-profile digest is T2; the observed engine class is T3; builder pins are T1.
  The AICR activation's separately owned `profileDigest` identifies a pinned content input and belongs to T1.
- **ADR-09:** the build record is the T3 home for host/clock/builder observations.
- **ADR-01:** the identity digest uses the canonical codec and domain tag defined there.
- **ADR-15:** general air-gap lifecycle remains out of scope; any future adapter must preserve logical
  content identity while authenticating every relocated transport binding.

## Acceptance evidence

Current package-level evidence includes:

- the explicit `ir.LogicalGraph` / `ir.LogicalNode` identity DTOs, which cannot represent source, artifact, or
  flake transport locators;
- `pkg/nkd/ir/marshal_test.go::TestBuildIdentityFieldRulesCoverEveryGraphField`, whose reflection coverage
  makes every full-graph field declare whether Build identity includes its logical value or content reference,
  or excludes it as transport or execution metadata;
- `pkg/nkd/ir/validate_test.go::TestGraphDigestRemainsTransportIndependentAfterFullValidation`, which proves
  independently relocated full execution graphs project to identical logical bytes and `buildDigest`; and
- `pkg/nkd/ir/marshal_test.go::TestDigestHostInvariance` plus the static guard in
  `internal/architecture/purity_test.go` for host purity.

Compiler tests additionally cover direct authored input binding, independent ownership after caller mutation,
exact producer references, forbidden copied execution settings, and unchanged Build identity when execution
requirements change. These are contract checks, not provider capability evidence.

The focused IR migration tests also cover concrete targets, exact producer membership, AICR inputs, strict
legacy-field rejection and the distinct `nkd.build` family. Repository checks are recorded in reconciliation.
Remaining external acceptance requires:

1. negative tests showing one-byte/tree/revision substitution through an authenticated mirror fails before build;
   and
2. any future air-gap adapter to record an activation round trip proving `buildDigest` remains unchanged
   while every transport locator changes. No current air-gap implementation is claimed.
