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

- **Scope:** Sections 1–5 govern qualified status; §6 governs the deferred v1 tag/revocation lifecycle.

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

A status declaration is **published as a signed OCI referrer attached to the publication artifact** in the
operator’s registry. A consumer holding a release digest **discovers** declarations by **querying that digest’s
referrers** — no central service, no side-channel to poll. For **air-gap** there is no registry to query, so the
same declarations are **mirrorable out-of-band** as a bundle. NKD **produces**, **publishes** (as a referrer),
and **consumes** (checks referrers at resolve/verify) — it hosts nothing.

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

Release-grade freshness is selected only by a policy-owned **time authority**, never by a request scalar. The
status policy pins a canonical `timeAuthority` identity. That authority snapshots its source once through a
context-aware `Now` operation and issues an opaque `TrustedTime` capability binding `{timeAuthority, epoch}`
under a distinct canonical family. Checkpoint verification requires the observation's authority to equal the one
pinned by policy, and binds the authority, epoch, and observation digest into the resulting checkpoint receipt.
Release Authorization (clearance) authenticates the same tuple. A raw `asOfEpoch`, including a correctly
formatted historical value, may drive diagnostic status evaluation but cannot create a release status binding.

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
- A declaration whose `effectiveEpoch` is later than the trusted observation epoch is not yet enforced. Strict
  lifecycle verification requires a `TrustedTime` from the policy-pinned authority; omitting it or supplying a
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

Current contract evidence in `pkg/nkd/status/*_test.go` and `pkg/nkd/release/*_test.go` covers authenticated exact checkpoint/declaration bytes, signer policy,
freshness, lower-sequence replay, same-sequence equivocation, predecessor and complete-declaration binding,
withdrawal, caller-selected old-epoch bypass, time-authority substitution, release rollback, persistent
high-water CAS. The removed mirror-generation tests no longer establish current implementation evidence. This is module evidence, not registry
interoperability or trusted-time operations.

End-to-end acceptance still requires publishing two successive checkpoints as OCI referrers, replaying the first
through the registry discovery adapter, proving a consumer that observed the second fails closed,
rotating the authorized signer set without resetting the authority sequence, and exercising the chosen time
authority's epoch mapping, skew, outage, and rollback policy. A fresh installation must also prove that a pinned
minimum prevents a valid old genesis checkpoint from becoming its trust root.
