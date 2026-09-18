# ADR-05 — Trust object model & digest-authentication chain

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-05 in the consolidated NKD ADR set.
- **Relates to:** ADR-01 (canonical codec), ADR-04 (signing), ADR-07 (builder-profile scope), ADR-09 (provenance),
  ADR-13 (SBOM), ADR-16 (release gates / Inventory).
- **Satisfies:** SEC-01, SEC-02, FR-22 (verification schema as a versioned public contract), FR-23 (validation
  evidence bound by digest).

## Context

NKD publishes a small family of **digest-addressable, integrity-protected** trust objects that bind a build to
its expected identities and evidence. The SRD requires each object to be independently digest-addressable
(SEC-01), and requires an expected artifact digest to be trusted **only** when the signed ValidationVector
authenticates it (SEC-02). The SRD marks the ValidationVector, ReleaseSet, and Inventory *illustrative /
non-normative*, so NKD owns their canonical schemas and versions them to reconcile with the eventual downstream
(NKE) contract. This ADR fixes those schemas, unifies the “expected-digests manifest” with the ValidationVector,
and defines the digest-authentication chain.

## Decision

The retained implementation supports document authoring/authentication, qualified reproduction diagnostics and
required membership checks. Qualified release authorization and its receipt assembly are deferred: their
unused implementation has been removed. The chain described below remains the proposed acceptance contract,
not a claim that a maintained command or SDK can clear or publish a qualified release.

### 1. The ValidationVector *is* the expected-digests manifest

**One** signed object binds one resolved build graph and one builder profile to expected artifacts and evidence. There
is no separate “manifest” beside the vector. Its `schemaVersion` is the **versioned public contract** FR-22 calls
for.

```
ValidationVector {
  schemaVersion
  buildDigest
  builderProfile { id, digest, hostControl }     # binds the from-source subset only (ADR-07)
  components [ { name, outputType, toolchain, inputDigest,
                 builderType, engineClass, sha256 } ]
  publisherEvidence {
    sbomIndexDigest, provenanceIndexDigest, evidenceDigest
  }
}
```

The `builderProfile` binding covers only the from-source subset; an all-prebuilt vector carries none (ADR-07).
The exact `components` set is part of vector identity. A component subset is therefore published only as
a distinct subset-scoped vector with its own subset-scoped evidence aggregates, and reproducing a subset can
never satisfy comparison against a full vector. A direct output's selected producer set is complete; no deployment metadata
filters it into a subset. The vector is serialized and digested through the canonical codec
(ADR-01) and stays operator-replaceable via `schemaVersion`.

### 2. ReleaseSet + Inventory, required set derived from the Inventory

```
ReleaseSet {
  schemaVersion, version
  members [ { component, arch, version,
              buildDigest, vectorDigest, resolvedInputDigest,
              profileDigest, artifactDigests[] } ]
  inventoryRef            # inventory digest, bound into the ReleaseSet digest
}

Inventory {
  schemaVersion, version
  components [ { name, versionConstraint,
                 requiredArtifacts, artifactKinds,
                 externalSBOMRef, externalSBOMDigest,
                 externalProvenanceRef, externalProvenanceDigest,
                 evidenceGaps } ]
  compatibility { kubernetes, nke, platform }
}
```

The **required set is derived from the Inventory**, not hand-listed in the ReleaseSet — one source of truth for
completeness (ADR-16) and compatibility. `inventoryRef` binds into the ReleaseSet digest, so rebinding a
different Inventory yields a new set identity.

The active schemas are `nkd.dev/validation-vector/v3`, `nkd.dev/inventory/v4`, and
`nkd.dev/release-set/v4`. Their `buildDigest` fields bind the generated `nkd.build` identity. Removed
`recipeDigest`, deployment-role fields, and older majors are rejected, never renamed inside authenticated bytes. Frozen digest
family tags remain unchanged for these existing trust-object families (ADR-01).

### 3. Digest-authentication chain (SEC-02)

An expected digest is trusted **only if it is carried in the bytes that passed signature verification**. Verify
computes over the **exact signed ValidationVector bytes** and **refuses any side-loaded expected-digests
reference**; an unauthenticated digest is never a trusted reference. The signature/trust gate runs **before** the
digest comparison (ADR-04, ADR-03).

### 4. Publisher evidence and later observations are different trust objects

The **publisher-evidence index** owns the per-artifact rows
`{ name, artifactDigest, sbomDigest, provenanceDigest, evidenceSchema }` and their aggregate evidence digest.
Those rows derive only from qualified SBOM and provenance capabilities that rechecked the exact artifact and
evidence bytes. The ValidationVector references the three publisher aggregates shown above. Attaching digest
strings never makes a vector publishable.

The retained verification diagnostic recomputes those publisher aggregates against the authenticated vector.
The unused digest setter and separate external-evidence release-receipt projections are removed. Acquisition
still verifies exact external bytes, schema and subject before embedding their verified SBOM properties; it
does not issue a release authorization.

A later operator comparison produces a separate **ReproductionObservation** carrying the reference-vector digest,
produced-report digest, comparison policy, verdict, and diagnosable mismatches. An observation is not publisher
evidence and is never included in, or hashed back into, the reference vector it evaluates. Runtime conformance,
compatibility, and vulnerability results are likewise release-gate receipts under ADR-13/16, not mutable fields
added to an already published vector. Every claim therefore has exactly one owner and no vector self-compares.

Every retained trust object — resolved build graph, ValidationVector, ReleaseSet, Inventory,
publisher-evidence index, and ReproductionObservation — is independently digest-addressable via the ADR-01 codec,
satisfying SEC-01.

### 5. Generate membership from frozen intent

Normal callers author outputs through a Definition (ADR-17). Preparation freezes component, platform,
resolved build identity, exact named output use, and required output slots before execution. Observed bytes fill those slots only after
the owning executor verifies their exact membership. Missing, unexpected, conflicting, or multiply realized
members fail completeness; build failure cannot shrink the required set.
Equal Build Digests do not merge independently required uses: producer, restore, terminal observation and
evidence joins must satisfy each named output binding separately. A digest-only map cannot supply completeness.

The qualified Inventory's exact artifact digests are unavailable before execution. Generate that compatibility
record only after authenticated binding of the frozen requirements, then derive its ReleaseSet. A static plan
or pipeline-attested Run cannot mint qualified Inventory or clearance. Published reproduction expectations stay
independent of the later operator observations compared against them.

### 6. Coverage profiles for external qualification — deferred

A future versioned ValidationVector coverage profile enumerates scenario classes, the exhaustiveness rule over
the promoted/provisioned set, and how conformance/qualification `vectorDigests` map to coverage. A pinned
`suitePolicyDigest` governs which coverage a passing verdict may assert. An external consumer may demand, for
example, GPU-runtime health and CNI conformance. Raw test results remain opaque; NKD checks the declared coverage
and suite policy. Coverage is schema, not additional authority. No coverage-profile implementation is claimed.
Deployment qualification and cluster topology remain outside the active artifact model (ADR-19); no
cluster-result field is retained in the ValidationVector. External observations remain outside the published
vector and require a separately specified subject and policy before any future acceptance.

## Consequences

**Positive**
- One signed object per build+profile, one publisher aggregate per vector — no manifest/vector drift.
- The Inventory is the single source of required-set and compatibility data.
- The SEC-02 chain makes a side-loaded or swapped digest inert.
- FR-22 and FR-23 are satisfied without folding a later verifier observation into its own reference.

**Negative / costs**
- These schemas are non-normative in the SRD; they must carry `schemaVersion` and stay operator-replaceable to
  absorb the eventual NKE contract without a breaking rewrite.
- Deriving the required set from the Inventory couples release completeness to the Inventory’s correctness.

**Neutral**
- The objects reuse the ADR-01 codec and the ADR-04 signing seam; this ADR adds shapes, not new mechanisms.

## Alternatives considered

- **Separate “manifest” and “ValidationVector” objects.** Rejected — two shapes for one binding; drift risk.
- **Hand-list required members in the ReleaseSet.** Rejected — duplicates the Inventory and drifts.
- **Trust a side-loaded expected-digests file.** Rejected — breaks SEC-02; only signed-carried digests are trusted.
- **Duplicate evidence rows in both the publisher index and the vector.** Rejected — double authorship;
  reference the qualified aggregates by digest instead.
- **Put an operator reproduction verdict into the reference vector.** Rejected — it is circular and lets a
  self-comparison masquerade as publisher evidence. Emit a separate authenticated observation.

## Follow-ups

- **ADR-16:** the completeness gate derives the required member/artifact set directly from the authenticated
  Inventory; runtime conformance and operator reproduction arrive as independent typed receipts.
- **ADR-13 / ADR-09:** produce the qualified per-artifact SBOM and provenance rows the publisher index aggregates.
- **ADR-07:** defines the `builderProfile` binding scope (from-source subset only).

## Acceptance evidence

Current contract evidence in `pkg/nkd/attest/*_test.go`, `pkg/nkd/trust/document_test.go`,
`pkg/nkd/release/*_test.go`, and `client/v1/*_test.go` covers exact authenticated ValidationVector/ReleaseSet
bytes, profile-free versus source-profile binding, publisher-evidence derivation, mutation/substitution
rejection, required-set completeness, and the distinction between a reference vector and a later qualified
observation.

End-to-end acceptance requires detached signing and reloading the complete publisher-evidence → ValidationVector
→ ReleaseSet → Authorization (clearance) chain across process boundaries, then proving that a side-loaded digest,
a swapped evidence row, a self-comparison observation, or a different Inventory cannot satisfy any link.
