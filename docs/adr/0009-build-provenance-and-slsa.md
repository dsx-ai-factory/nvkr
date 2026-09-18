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
remain unchanged. The owning provenance index and build-observations records use
`nkd.dev/provenance-index/v2` and `nkd.dev/build-observations/v2`; legacy Recipe fields and build-type versions
cannot be relabeled as this graph identity.

### 2. Two attestations: reproducible provenance vs signed observations

"Signed" and "part of the reproducibility identity" are **independent** properties. NKD emits **two** attestations,
both signable:

- **Deterministic SLSA provenance** (§1) — reproducible, digest-addressed, a covered expected-evidence digest.
- **Signed build-observations attestation** — the **actual build times, the observed host facts, and the observed
  `engineClass`** (ADR-07). It is **signed** (authenticated) but **non-reproducible** and **not part of any
  identity or of the expected evidence digests**. It attaches per build to the artifact as a referrer, for audit
  and diagnosis.

This refines ADR-02's T3: a detached, non-identity record **may be independently signed**. Build times are kept
without perturbing the reproducible provenance.

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
| `buildType` | Versioned NKD URI identifying `hermetic-build`, `wrapped-build`, or `acquired`; any wrapped/network-enabled build makes the complete plan `wrapped-build`, regardless of build order |
| `builder.id` | Committed workflow path and ref; a local Run uses a distinct local builder URI |
| Invocation metadata | Exact Run identity with start and finish times |
| `externalParameters` | Definition, Environment, catalog/selection and prepared plan identities |
| `internalParameters` | Generator version, signing mode, and pipeline-helper digest |
| `resolvedDependencies` | Source commits/trees, toolchains, plan, and declared embedded components |

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
- Provenance stays reproducible and verifiable (times omitted) while per-build times and host facts remain
  attested in the signed observations — nothing is lost.
- One provenance format across builder types.
- Prebuilt components are honestly marked: a verified reference or `evidence: none`.

**Negative / costs**
- Two attestations per build to produce and store.
- The build-type URI and the omit-times profile must be documented as part of the serialization profile (ADR-01).

**Neutral**
- Reuses the canonical codec, the signing seam, and the evidence bundle; adds shapes, not mechanisms.

## Alternatives considered

- **Include build times in the SLSA statement.** Rejected for the *reproducible* provenance — it makes the
  statement non-reproducible. The times live in the signed observations attestation instead.
- **Drop build times entirely.** Rejected — the audit value of when and where is worth keeping, and costs nothing
  once it lives in a non-identity signed record.
- **A bespoke, non-SLSA provenance format.** Rejected — downstream tooling expects SLSA/in-toto.

## Follow-ups

- **ADR-02:** T3 wording updated to "not part of the identity, may be independently signed."
- **ADR-05:** the deterministic provenance digest is an expected evidence digest in the ValidationVector; the
  signed observations attestation is attached per-build, not a covered digest.
- **ADR-13:** external-provenance references for prebuilt artifacts.

## Acceptance evidence

Current contract evidence in `pkg/nkd/provenance/provenance_test.go` covers the supported in-toto Statement/v1 +
SLSA provenance/v1 profile, exact single-artifact subjects, build/input/builder binding, pinned dependencies,
stable ordering, and rejection of mutable or type-confused inputs. It does not prove producer signing or
cross-process observation provenance.

End-to-end acceptance requires a qualified producer to sign and publish deterministic provenance plus the
separate non-identity observation, then an independent verifier to rehash the artifact and reject subject,
builder, dependency, policy, or signer substitution.
