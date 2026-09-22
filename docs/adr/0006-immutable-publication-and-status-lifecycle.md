# ADR-06 — Immutable publication & status lifecycle

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-06 in the consolidated NKD ADR set.
- **Relates to:** ADR-05 (publication objects), ADR-04 (signing), ADR-16 (digest-first selection), ADR-15
  (air-gap / mirror).
- **Satisfies:** SEC-16 (immutable publication), UC-11 (supersede / deprecate / withdraw).

- **Scope:** Sections 1–5 specify the qualified status protocol; its publication and trusted-time integration
  remain deferred. Signed-document diagnostics and checkpoint/high-water validation are implemented.
  Section 6 governs the deferred v1 tag/revocation lifecycle.

## Context

The SRD requires that a published build or release be **immutable** — a correction is a **new signed identity,
never a mutation** — and that a publisher be able to **supersede, deprecate, or withdraw** a release **without
modifying** the original publication or its evidence. This must hold with **no NVIDIA-hosted service** and only
standard interfaces.

An earlier design posited a publisher-maintained “append-only ledger.” Content-addressing already makes each
publication immutable, so NKD needs no bespoke publication ledger. A registry referrer listing, however, is
neither complete nor monotonic: a registry or mirror can omit a withdrawal or replay an older, correctly signed
view, and transparency-log inclusion proves that an object existed rather than that a consumer saw the latest
status. The status path therefore needs its own authenticated freshness and anti-rollback mechanism.

## Decision

### 1. Publication identity via content-addressing (no maintained ledger)

This is a deferred qualified publication contract. The unused publication-identity constructor is removed;
its canonical family remains reserved and cannot mint identities. Current pipeline publication uses ADR-20 §6.

`publicationIdentity = Digest("nkd.publication", <exact canonical ValidationVector / ReleaseSet>)`, **distinct** from
`buildDigest`, which covers only the compiled graph's logical projection — two profiles or manifests over one graph would
otherwise collide. Immutability needs no ledger: a publication *is* its digest, so any byte change is a
**different publication**. Registry listing is a discovery aid; the signed objects and their transparency
inclusion are the auditable record. Freshness comes from §4's status checkpoints, never from assuming the listing
is complete. The consumer-side status high-water mark is narrowly scoped security state, not publication
history.

### 2. Status-declaration schema

```
StatusDeclaration {
  schemaVersion
  target        # publicationIdentity — a non-digest target fails closed
  action        # superseded | deprecated | withdrawn
  replacedBy    # required iff superseded — an immutable identity
  reason
  effectiveEpoch  # publisher-signed lifecycle epoch; never inferred while hashing the publication
}
```

Its own identity is its content digest. It **references its target only by immutable identity** and **never opens
or rewrites** the target’s publication or evidence.

### 3. Storage & discovery: OCI referrers, mirrorable out-of-band

The deferred discovery protocol publishes a status declaration as a signed OCI referrer attached to the publication artifact in the
operator’s registry. A consumer holding a release digest **discovers** declarations by **querying that digest’s
referrers** — no central service, no side-channel to poll. For **air-gap** there is no registry to query, so the
same declarations must be mirrorable out-of-band as a bundle. Registry discovery, referrer publication and
mirror-bundle integration are not implemented. Current commands authenticate explicitly supplied documents.

### 4. Signed status checkpoints: freshness and anti-rollback

A status lookup is authoritative only when accompanied by a signed checkpoint for the target:

```
StatusCheckpoint {
  schemaVersion
  target                 # exact publicationIdentity
  authority              # stable status-authority identity/role across key rotation
  sequence               # unsigned integer, monotonic per target + authority
  previousCheckpoint     # required after the genesis checkpoint
  declarationDigests[]   # sorted complete current set; empty is meaningful
  issuedEpoch
  validUntilEpoch
}
```

The checkpoint and every referenced declaration are canonical, digest-addressed, and signed by a key currently
authorized for the pinned status authority. `declarationDigests` is a **complete snapshot**, not a best-effort
list. Publishing a new declaration or retracting/correcting status produces a new checkpoint; prior objects
remain immutable. Corrections and signer-key rotations advance the same authority sequence rather than rewriting
history or resetting freshness. Checkpoints are OCI referrers alongside declarations and are carried identically
in the out-of-band bundle.

A consumer keeps a monotonic high-water mark `{target, authority, sequence, checkpointDigest}` in its trust
state. It rejects a lower sequence, a different checkpoint at the same sequence (equivocation), a broken
predecessor link, an incomplete checkpoint, or a checkpoint outside its validity interval at the trusted
observation epoch. A stateless or newly provisioned consumer must receive a minimum accepted checkpoint digest
and sequence in its trust policy or mirror bundle; without one it cannot make an anti-rollback claim and must
fail closed in strict lifecycle mode.

The deferred release protocol requires freshness from a policy-owned time authority, never a request scalar.
The policy must pin that authority, and the verifier must bind its authenticated epoch and observation identity
to the accepted checkpoint and clearance. The unused time-authority capability and release-checkpoint adapter
are removed; their canonical time family remains reserved and nonminting. Current checkpoint diagnostics use an
explicit `asOfEpoch` and do not grant release authority. No production trusted-time adapter is implemented.

The implemented diagnostic operation is owned by `status.AcceptDiagnosticCheckpoint`. Status owns the private
high-water schema, protected reads, kernel-released locking, byte-CAS and directory fsync. It authenticates the
complete checkpoint and commits state derived from the verified receipt before interpreting withdrawal, so
`CodeWithdrawn` cannot leave the previous minimum available for replay. The CLI supplies document locations,
policy and the explicit diagnostic epoch, then presents the decision; it does not construct accepted state.

The configured time source is part of local trust policy. It may be an authenticated wall-clock service, a
hardware-backed clock, or a separately authenticated monotonic policy epoch, but the deployment must document
its epoch mapping, maximum allowed skew, rollback recovery, and offline behavior. It must not be a CLI flag,
request timestamp, or callback selected by the release candidate. An offline mirror carries the checkpoint chain,
its signatures, and the trust-policy minimum, and the offline verifier must also hold the pinned time-authority
adapter. Deliberate recovery to an older sequence or time source requires a separately
authenticated local trust-policy update; neither a status signer nor a release request can authorize its own
rollback.

### 5. Consumption trust (withdraw-DoS defense)

Trust anchors to a **signature**, not to file presence:

- A declaration is honored **only when signed by the pinned status-signer identity**, resolved via the trust
  store and possibly distinct from the release signer. An **unsigned or wrong-signer declaration is ignored**, so
  a dropped fake file does nothing.
- Only declarations named by the accepted current checkpoint participate in enforcement. A signed `withdrawn`
  from the pinned identity → **fail closed** (`withdrawn`); `deprecated` → warn + proceed; `superseded` → warn +
  name `replacedBy`.
- A declaration whose `effectiveEpoch` is later than the trusted observation epoch is not yet enforced. The deferred strict
  lifecycle verifier must authenticate time from the policy-pinned authority; omitting it or supplying a
  caller-selected diagnostic epoch cannot turn an expired or pre-withdrawal checkpoint into a release-valid one.

### 6. Pipeline-attested release tags and revocation — deferred

The v1 Release uses the manifest and qualification contract in ADR-16, with an immutable tag derived from the
Definition digest and repository commit, plus one mutable channel tag. Tags aid discovery; consumers pin the
resolved digest. A rerun of the same commit returns the existing manifest digest and `already released` without
publishing another manifest. Every other existing release tag is immutable. Concurrent creation, collision
validation, partial-write reconciliation, and channel ordering require an implemented registry commit protocol;
content addressing alone does not provide it.

V1 revocation requires all three actions: a signed `nkd.dev/revocation/v1` marker naming subject/manifest digest,
reason and time, attested by the qualification identity; removal of the channel tag from that digest; and consumer
rejection of the revoked digest even when its original signature still verifies. Keep the immutable tag and
transparency evidence. This is separate from qualified withdrawal: it has no trusted-time authority, high-water
mark, or freshness window. These v1 lifecycle operations remain unimplemented.

## Consequences

**Positive**
- Immutability is intrinsic to content-addressing; there is no ledger to maintain, race, or corrupt.
- Status is discoverable through a standard OCI mechanism and mirrorable for air-gap.
- Signature-anchored consumption defeats withdraw-DoS; checkpoints additionally detect stale or replayed status
  once a trust minimum or high-water mark exists.

**Negative / costs**
- Referrer discovery depends on the operator’s registry supporting the referrers API; out-of-band supply is the
  fallback.
- The status-signer identity must be provisioned and pinned — an operator and trust decision.
- Consumers must persist a small high-water mark or receive a trust-policy minimum. Availability of both a fresh
  checkpoint and the pinned time-authority adapter is a deliberate fail-closed dependency in strict lifecycle
  mode.

**Neutral**
- Reuses the ADR-01 codec, the ADR-04 signing seam, and the ADR-05 publication objects; this ADR adds a
  lifecycle, not new mechanisms.

## Alternatives considered

- **Publisher-maintained append-only publication ledger.** Rejected — publication immutability already follows
  from content addressing. The smaller signed status-checkpoint chain is retained because registry listing plus
  transparency inclusion cannot prove freshness.
- **A separate signed status feed / well-known location.** Rejected as the default — heavier and needs a
  discovery convention; referrers are registry-native. (Out-of-band remains the air-gap path.)
- **Honor any `withdrawn` declaration by file presence.** Rejected — trivial denial-of-service on a good release.

## Follow-ups

- **ADR-03:** allocate distinct stale-checkpoint, rollback, and equivocation codes before the checkpoint
  interface is published; do not collapse them into `withdrawn`.
- **ADR-16:** resolve and verify select by immutable digest and consult referrers for status before accepting a
  release.
- **ADR-15:** the out-of-band status bundle includes the checkpoint chain, trust-policy minimum, and rollback
  behavior.

## Acceptance evidence

Current contract evidence in `pkg/nkd/status/*_test.go` and `cmd/nkdctl/qualified_lifecycle_test.go` covers exact
authenticated checkpoint/declaration bytes, signer policy, validity at an explicit diagnostic epoch,
lower-sequence replay, same-sequence equivocation, predecessor and complete-declaration binding, withdrawal,
and durable high-water locking, crash recovery and CAS. It does not establish trusted-time release authority,
registry discovery, mirror interoperability or a qualified clearance path.

End-to-end acceptance still requires publishing two successive checkpoints as OCI referrers, replaying the first
through the registry discovery adapter, proving a consumer that observed the second fails closed,
rotating the authorized signer set without resetting the authority sequence, and exercising the chosen time
authority's epoch mapping, skew, outage, and rollback policy. A fresh installation must also prove that a pinned
minimum prevents a valid old genesis checkpoint from becoming its trust root.
