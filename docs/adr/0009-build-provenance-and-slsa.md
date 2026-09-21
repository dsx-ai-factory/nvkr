# ADR-09 — Build provenance & SLSA

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-09 in the consolidated NKD ADR set.
- **Relates to:** ADR-02 (tier boundary), ADR-05 (evidence bundle / ValidationVector), ADR-07 (observed engine
  class), ADR-13 (external evidence for prebuilt), ADR-01 (canonical codec / serialization profile).

- **Scope:** Sections 1–4 describe reproduction/acquisition evidence; §5 defines the distinct production predicate.

## Context

NKD emits machine-readable SLSA provenance per artifact, and that provenance folds into the trust/evidence model.
Determinism is in tension with it: a build attestation naturally records *when* and *where* a build ran, but a
per-build value cannot belong to the **reproducible** evidence a verifier recomputes and compares. This ADR
resolves that tension.

SLSA Provenance v1 makes `runDetails.metadata.startedOn` / `finishedOn` **optional**; the required fields are
`buildDefinition.buildType`, `buildDefinition.externalParameters`, and `runDetails.builder.id`. Omitting build
times from the deterministic provenance is therefore spec-valid.

## Decision

### 1. Deterministic SLSA provenance, per built artifact

Emit one **in-toto v1 statement** carrying an `https://slsa.dev/provenance/v1` predicate per **built** artifact:

- `subject` = the artifact digest;
- `runDetails.builder.id` = the **builder-profile identity** (`profileDigest`) — the reproducible "who built it,"
  not a host;
- `buildDefinition.buildType` = a stable NKD build-type URI;
- `buildDefinition.resolvedDependencies` = the pinned sources + toolchains (for `nix`, the flake-lock closure);
- **build-time fields omitted** for determinism.

Serialize through the canonical codec (ADR-01). The statement's digest is a **stable expected evidence digest** in
the ValidationVector (ADR-05), so verify recomputes and compares it.

The NKD reproduction build-type URIs use `/v2` (`oci-hermetic`, `nix`, `prebuilt-acquire`) and bind
`externalParameters.buildDigest`. The surrounding standard in-toto Statement/v1 and SLSA provenance/v1 formats
remain unchanged. The owning provenance index uses `nkd.dev/provenance-index/v2`; legacy Recipe fields and
build-type versions cannot be relabeled as this graph identity.

### 2. Reproducible provenance and deferred signed observations

"Signed" and "part of the reproducibility identity" are **independent** properties. The qualified-lane design
separates deterministic provenance from per-attempt observations:

- **Deterministic SLSA provenance** (§1) — reproducible, digest-addressed, a covered expected-evidence digest.
- **Signed build-observations attestation (deferred)** — actual build times, observed host facts, and observed
  `engineClass` (ADR-07), authenticated separately from reproducible evidence. A future publisher must bind
  those observations to the exact attempt and artifact; they cannot enter reproducible identity or expected
  evidence digests.

No supported operation produces, signs, or consumes a separate build-observations attestation today. The unused
descriptive schema is removed until such an operation exists. Current pipeline-attested production provenance
records invocation times under §5; it does not establish observed host facts or qualified execution authority.
ADR-02's T3 still permits a detached observation record to be independently signed without affecting
reproducible identity.

### 3. `nix` provenance is builder-agnostic

A `nix`-built component emits the **same** SLSA statement: `subject` = artifact digest, `resolvedDependencies` =
the flake-lock closure, `builder.id` = `profileDigest`. There is no separate Nix provenance format; this is
consistent with builder-agnostic verify.

### 4. `prebuilt` provenance

A no-compile component emits **no from-source build claim**. NKD may emit a profile-free `prebuilt-acquire/v2`
statement binding the exact acquired bytes, build, and resolved transport inputs; that statement proves
acquisition, not upstream construction. The artifact also records a **verified reference** to the upstream's
external provenance where one exists, else an explicit `evidence: none` — never silently absent. The
external-reference schema lives in ADR-13.

### 5. Production provenance for pipeline-attested artifacts

Production provenance is a separate SLSA v1 predicate. It records how one Run produced bytes; it is excluded from
reproducible identity and cannot replace §1's reproduction predicate.

| Field | Production contract |
|---|---|
| `buildType` | NKD `/v2` URI identifying `hermetic-build`, `wrapped-build`, or `acquired`; any wrapped/network-enabled build makes the complete plan `wrapped-build`, regardless of build order |
| `builder.id` | Committed workflow path and ref; a local Run uses a distinct local builder URI |
| Invocation metadata | Exact Run identity with start and finish times |
| `externalParameters` | Exactly `definitionDigest`, `environmentDigest`, `catalogDigest`, `planDigest`, and the descriptive `runPlanReference` |
| `internalParameters` | Exactly generator version and signing mode; an authenticated helper-execution identity remains a distinct qualified-lane requirement |
| `resolvedDependencies` | Source commits/trees, toolchains, plan, and declared embedded components |

The plan-level `buildType` is deliberately the weakest applicable production lane: one wrapped or
network-enabled producer keeps the complete plan `wrapped-build`. Stronger evidence for an exact component may
remain independently inspectable, but it cannot promote the aggregate production predicate. Likewise, a list of
matching resolved dependencies records what the producer consumed; it does not assert that the produced subject
was byte-reproduced.

The Definition and Environment parameters identify the admitted authored inputs directly. Run plan v4 carries
those exact values and revalidates their identities before execution or attestation. The retired compile-spec
and execution-settings objects are not provenance dependencies. Their replacement changes the NKD build-type
URI major; a `/v1` production predicate cannot be interpreted with `/v2` parameter meanings. Reproduction
build types retain their separate semantics and validators.

Cosign wraps the predicate in an in-toto statement whose subjects are the independently re-derived published
digests. Signing mode remains inside the predicate so dry-run evidence cannot lose its meaning when copied.
Production and reproduction validators reject each other's shapes; coercion is forbidden. Hosted finalization
reconstructs expected production semantics from the admitted plan and checks dependencies, build metadata,
workflow identity, and exact subject rather than accepting any well-formed predicate.

Acquired provenance is NKD's acquisition record (upstream reference, lock pin, fetch time); it makes no upstream
build or signature-verification claim. Without declared digest-pinned upstream evidence it remains
`evidence: none` under ADR-13. Live producer signing and consumer acceptance remain separate operational tests.

## Consequences

**Positive**
- Provenance stays reproducible and verifiable with times omitted. Production provenance records invocation
  times; independently authenticated host observations remain a future capability.
- One provenance format across builder types.
- Prebuilt components are honestly marked: a verified reference or `evidence: none`.

**Negative / costs**
- A future qualified observations publisher would produce and store a separate attestation.
- The build-type URI and the omit-times profile must be documented as part of the serialization profile (ADR-01).

**Neutral**
- Reuses the canonical codec, the signing seam, and the evidence bundle.

## Alternatives considered

- **Include build times in the SLSA statement.** Rejected for the *reproducible* provenance — it makes the
  statement non-reproducible. Production provenance keeps invocation times separately; qualified observations
  remain deferred.
- **Drop build times entirely.** Rejected — production provenance preserves their audit value without adding
  them to reproducible evidence.
- **A bespoke, non-SLSA provenance format.** Rejected — downstream tooling expects SLSA/in-toto.

## Follow-ups

- **ADR-02:** T3 wording updated to "not part of the identity, may be independently signed."
- **ADR-05:** the deterministic provenance digest is an expected evidence digest in the ValidationVector; a
  future signed observations attestation must be attached per build, not included as a covered digest.
- **ADR-13:** external-provenance references for prebuilt artifacts.

## Acceptance evidence

Current contract evidence in `pkg/nkd/provenance/provenance_test.go` covers the supported in-toto Statement/v1 +
SLSA provenance/v1 profile, exact single-artifact subjects, build/input/builder binding, pinned dependencies,
stable ordering, and rejection of mutable or type-confused inputs. It does not prove producer signing or
cross-process observation provenance.

`pkg/nkd/provenance/production_test.go` pins exact `/v2` production predicate bytes, verifies the original
Definition/Environment parameter set, and rejects retired build types and compiler-handoff parameters. The
compiler/signing tests bind the same exact values from a completed Plan. These tests do not establish a new
hosted signing or independent-consumer result.

End-to-end acceptance requires a qualified producer to sign and publish deterministic provenance plus the
separate non-identity observation, then an independent verifier to rehash the artifact and reject subject,
builder, dependency, policy, or signer substitution.
