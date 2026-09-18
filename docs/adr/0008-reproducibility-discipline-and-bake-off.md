# ADR-08 — Reproducibility discipline & two-machine bake-off

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-08 in the consolidated NKD ADR set.
- **Relates to:** ADR-07 (reproduction identity & builder profile), ADR-10 (pluggable builder types), ADR-05
  (trust objects / evidence bundle), ADR-04 (signing).

- **Scope:** This is the qualified reproduction discipline. V1 production scope is explicit in §6.

## Context

Reproducibility is NKD’s load-bearing claim, so it must be **demonstrated, not asserted**. The SRD calls for a
two-environment bit-identity gate. This ADR settles *whose* gate that is and *what it proves*, defines the
protocol, specifies how empirical evidence graduates a builder-profile attribute from advisory to fail-closed
(the hook ADR-07 depends on), and confirms the discipline is builder-agnostic — it covers `nix` as well as
`oci-hermetic`.

## Decision

### 1. Role: publisher qualification, plus operator verification as the second environment

The standalone **two-machine bake-off is a publisher qualification-time gate**: before qualified expectations are published, the
publisher proves it reproduces bit-identically across two deliberately-different environments. The **operator’s
gate is a single verify** against the published expected digests, and their independent rebuild supplies the
second environment in aggregate. The “two-environment reproducibility” guarantee is therefore
**publisher-qualified plus operator-verified**, not a mandatory per-operator bake-off. This keeps operator cost
low without weakening the guarantee.

### 2. Protocol: emit / compare, isolated, deterministic

- **Emit / compare split.** Each environment independently produces an expected-digests record; a separate
  compare step asserts bit-identity and, on divergence, **names the first divergent component** with per-block
  and per-path granularity from the evidence bundle.
- **Environment isolation.** Per-build environment settings are scoped to that build. Nothing mutates the process
  environment globally during a bake-off, so parallel builds cannot interfere and the varied host facts are the
  only intended difference.
- **Deterministic.** The compare is a pure digest comparison. Identical inputs across two environments yield an
  identical result, and any difference is fatal and diagnosable.


### 3. Evidence-gated graduation of profile attributes

A **host-environment** attribute (kernel, libc, microarchitecture) graduates from *advisory* to *fail-closed*
(ADR-07 §2) only when a bake-off **demonstrates** it changes output:

- The bake-off runs across two deliberately-different environments; on divergence the divergent block or path is
  identified from the evidence bundle.
- The **coarsest controllable attribute** responsible is graduated to fail-closed, **citing the bake-off report**
  as the demonstration; a follow-up spike may narrow it to a finer attribute.
- Graduation **always requires a cited report** — never a guess. Attribution is intrinsically hard, because
  varying several host facts at once obscures which one caused a divergence; the coarse-then-narrow rule is the
  pragmatic answer.

Builder-identity attributes are *not* subject to this. They are fail-closed by definition (ADR-07 §2) and need no
demonstration.

### 4. Builder-agnostic

The bake-off compares **artifact digests** and is therefore indifferent to builder type. A `nix` component is
treated identically: compare artifact digests across two environments under the **same pinned Nix version and
privilege set**. Varying the Nix version is a *separate* concern — it only re-confirms that the Nix version is
output-affecting, which is already pre-classified fail-closed — and is not part of proving reproduction.

### 5. Input-tree byte-identity and normalization discipline

Reproducibility across environments requires the *inputs* to be byte-identical, not just the sources:

- **Pre-realized inputs must be byte-stable.** Vendored dependency trees (for `oci-hermetic`) and the locked
  flake closure (for `nix`) must hash identically across environments. The bake-off run validates this, and a
  spike de-risks it.
- **Normalization inputs are pinned.** The source-date value derived from the pinned commit, locale and timezone
  normalization, and the build-flag discipline that strips non-reproducible metadata are all pinned as part of
  the build identity. They apply uniformly to every build and cannot be overridden by a catalog.

### 6. Pipeline-attested scope

The v1 lane makes no byte-identity claim and does not require a two-machine bake-off. In particular, wrapped
builds use the ADR-10 production contract without this emit/compare gate. The absence of a bake-off can never
be presented as a qualified result or as evidence for graduating profile attributes.

## Consequences

**Positive**
- The reproducibility claim is empirical and diagnosable; a divergence names the first divergent component and
  narrows to a block or path.
- Operators bear a single verify, not a two-machine bake-off, and the cross-environment proof still holds.
- The gate is builder-agnostic — new builder types need no new bake-off machinery.

**Negative / costs**
- Attribution of a host-caused divergence to a specific attribute is imperfect; graduation may be coarse until a
  narrowing spike runs.
- Publisher qualification requires provisioning two deliberately-different environments.
- Byte-stable pre-realized input trees (vendored tree, locked closure) are fiddly and must be proven per builder.

**Neutral**
- The bake-off consumes the same expected-digests and evidence-bundle artifacts as normal verify; it adds a
  compare, not a new trust surface.

## Alternatives considered

- **Mandatory per-operator two-machine bake-off.** Rejected — expensive and redundant, since the operator’s own
  verify already supplies an independent second environment.
- **Trust a builder’s reproducibility (e.g. Nix) without a bake-off.** Rejected — the guarantee is
  verification-based; a builder raises the odds but does not remove the need to verify.
- **Graduate attributes to fail-closed by judgement.** Rejected for host-environment attributes — graduation must
  cite a demonstration to avoid over- or under-tightening the reproducible set.

## De-risking spikes

1. **Two distinct-machine bit-identity** — provision two deliberately-different environments and prove the hard
   lanes (CGO, `nix`) reproduce bit-identically; feed any divergence into §3 graduation.
2. **Byte-stable pre-realized inputs** — prove vendored trees and locked flake closures hash identically across
   environments.

## Follow-ups

- **ADR-07:** consumes §3 to graduate host-environment profile attributes.
- **ADR-10:** the `nix` builder’s offline-closure model is validated by spike 2.

## Acceptance evidence

Current tests cover deterministic comparison, diagnosable per-component divergence, profile binding, and
qualified versus diagnostic output. They do not establish the defining two-independent-machine claim.

Acceptance requires a recorded publisher qualification across two deliberately different machines for every
release builder class, including identical authenticated input-closure receipts and artifact bytes. Any proposed
profile-field graduation must cite that run and a controlled variation demonstrating causality.
