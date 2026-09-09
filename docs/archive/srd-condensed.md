# NKD — Condensed SRD (v1)

> [!CAUTION]
> **Historical requirements summary.** Its normative wording describes a target and does not override the
> proposed ADR baseline or the current support/evidence record in `docs/adr/reconciliation.md`.

**Recorded status:** formerly a normative brief. It merges **NKD v1** (`nkd-v1.md`, the favored source) with the reconciled
requirements in `docs/srd.md`. This is the executive/normative summary (3–4 pages); `docs/srd.md` remains the
full ~230-SHALL specification with traceability. Where the two differ, `nkd-v1.md` governs. Keywords MUST /
SHALL / SHOULD / MAY per RFC 2119.

---

## 1. Purpose & scope

**NKD (NVIDIA Kubernetes Definition)** is two things: (a) an immutable, versioned **definition** of the
Kubernetes + operating-system + accelerated-runtime inputs NVIDIA validates for NKE, and (b) a reproducible,
CI-portable **pipeline** plus signed **builder profiles** and **validation vectors** that deterministically
materialize that definition into deployable artifacts.

NVIDIA runs the pipeline first — once per supported builder profile — builds and validates canonical reference
artifacts, and publishes the definition, pipeline, profiles, expected artifact identities, SBOMs, provenance, a
Kubernetes conformance claim, and validation evidence — but **does not distribute the built artifacts**. The
operator imports the released pipeline into its own CI, selects a supported profile, runs it on the exact
pinned inputs, verifies byte-identity against the published digests, then adds its own signatures/registry
metadata (outside the canonical boundary) and promotes. **NKE consumes the artifacts; it does not build them.**

- **Primary customer:** the AI-Factory operator running self-service Kubernetes for internal or external
  tenants. Tenants consume through NKE and do not build NKD artifacts.
- **NKD is not a Kubernetes distribution.** It is a recipe + reproducible pipeline, not a set of hosted
  binaries. Rejected substrates (Nix/Guix/Bazel/apko/mkosi/Packer) and copyleft/BUSL core dependencies are out;
  the core stays Apache-2.0-clean (BuildKit/containerd/podman are acceptable).

## 2. Ownership boundary

> **NVIDIA owns** the definition, reproducible pipeline, supported builder profiles, canonical reference builds,
> and validation vectors. **The operator owns** its pipeline execution, reproduced or explicitly-approved
> variant artifacts, promotion policy, and runtime environment.

Withholding the reference bytes is a **delivery/hosting decision, not a liability transfer.** NVIDIA retains
recipe correctness, the compatibility matrix, lifecycle/advisories, security fixes, the conformance
claim/suite, CVE policy, incident triage, and ValidatedVariant approval authority. It avoids only: artifact
hosting/egress at scale, a per-byte CVE-rebuild SLA, and a per-operator packaging/format matrix. There is **no
privileged internal path** — NVIDIA's own first operator (DS9) uses the same released recipe, engine, profile,
vector, and bootstrap as any external operator.

## 3. Design principles (invariants)

1. **Recipe before artifacts** — every promoted artifact set traces to exactly one immutable NKD recipe.
2. **One build implementation** — NVIDIA and operators run the same versioned engine + one declared profile;
   CI workflows are thin adapters, never independent build logic.
3. **Reference-first reproducibility** — NVIDIA builds first per profile and publishes profile-specific
   canonical digests; an operator build is *Reproduced* only when profile + canonical payloads match.
4. **Operator-controlled supply chain** — operator artifacts are built, stored, signed, and promoted under
   operator policy.
5. **Fail closed** — missing provenance, unpinned/mismatched input, tag-not-digest toolchain, incompatible
   version, or incomplete validation blocks the build/promotion; never a silent default.
6. **Immutable normal path** — normal change produces a new image and replaces nodes; running nodes are not
   patched in place.
7. **Explicit drift** — emergency mutation is visible, expiring, and reconciled into a later recipe.
8. **Identical contract → identical canonical output** — the tuple (recipe · engine · input manifest · signed
   profile) pins reproducibility; byte identity holds only within it. Operator signatures/registry metadata are
   a separate envelope around the comparable payload.
9. **Evidence travels with the release** — identities, provenance, SBOMs, compatibility, conformance, and
   validation results ship for NVIDIA's *withheld* reference artifacts.
10. **Controlled fallback** — a digest mismatch fails closed for *Reproduced*; only a gated, signed,
    time-bounded exception can become a *ValidatedVariant*.

## 4. Architecture & core model

```
criteria ─▶ distribution recipe ─▶ pipeline engine (hermetic) ─▶ canonical artifacts (RAW OS image + k8s pkgs)
                                                                          │
                                     reproduction verifier ◀── compare ── validation vector (NVIDIA, per profile)
                                                                          │
                                                                    NKE consumes (does not build)
```

- **AICR analogy / dependency.** NKD applies AICR's separation at the distribution layer: recipe →
  materializer → validate. **AICR is the foundational declarative layer for the accelerated-runtime profile**
  (validated GPU/network operator pairings), referenced by identity + digest in the recipe.
- **CI adapters** (GitHub Actions, GitLab, Jenkins, …) are execution hosts that call the *same* engine and
  contain no artifact-construction logic. An orchestrator name is **not** a builder profile.
- **Criteria resolution** is asymmetric (recipe-`any` is a wildcard; a specific field requires the query to
  supply it; ties break by specificity, then fail closed). Active axes: `role × k8sVersion × os × arch` (arch
  multi-arch, native per-arch, model B). Recipes may be **composed** from layered overlays/mixins (AICR-style
  `base ← overlay` inheritance + deep-merge), which flatten to the same IR/`recipeDigest`.

## 5. Functional requirements (condensed)

**Recipe & inputs**
- **R01** The distribution recipe MUST be immutable and carry, by value or digest: a unique identity/version;
  compatible NKE versions; Kubernetes/core/accelerated component versions + source-or-package digests; the AICR
  profile identity+digest; canonical base-image/OS/arch/kernel/driver inputs; required k8s+NKE config;
  engine/toolchain identities; ≥1 supported builder-profile identity + its validation-vector ref;
  canonicalization rules; expected validation suites + thresholds; the Kubernetes conformance claim; upgrade
  edges; lifecycle/support dates; NVIDIA signature. A correction produces a **new identity**, never a mutation.
- **R02** Every input capable of changing canonical output MUST be pinned and recorded; a canonical-input
  resolver MUST fail closed on missing/mismatched content. Secrets MUST be referenced, never embedded in
  artifacts/manifests/logs.
- **R03** `recipeDigest = sha256(deterministic IR)` — no host/clock/random/UUID value enters the IR; the same
  (catalog, query, arch) yields a byte-identical IR and digest.

**Pipeline & builder profile**
- **R04** The engine MUST be hermetic and authoritative; CI adapters MUST invoke it unchanged. Builds run in
  digest-pinned toolchain containers with `--network=none` (fetch/vendor in a separate network step). One IR
  lowers to every engine adapter identically (topology/transport only, never semantics), ending in an
  **unconditional** verify.
- **R05** Byte identity is guaranteed only within a complete, signed **supported builder profile**. Its signed
  **digest is the identity** (a label like `gha-ubuntu-24.04-amd64` is not). The engine MUST run **fail-closed
  host preflight before resolving inputs** and stop on a hard-fail field divergence. **NKD v1 declares exactly
  one profile**; breadth is demand-driven.
- **R06** Every recipe MUST have ≥1 **operator-controlled** profile (self-hosted/pinned runner). A profile
  field MUST NOT be **hard-fail** unless a divergence in it has been demonstrated to change canonical output;
  others are advisory (recorded, non-blocking). NVIDIA MUST publish a **re-baseline SLA** + **grace window** so
  a provider runner-image rotation cannot indefinitely halt every operator on a profile.

**Byte-identity boundary (§ 8.3.1)**
- **R07** The recipe MUST declare, per artifact, the logical-content boundary and the delivery-byte boundary.
  **RAW is the only canonical disk artifact** — a SHA-256 over the complete logical LBA stream. **QCOW2/VMDK/VHD
  are derived delivery envelopes**, verified by the decoded logical-disk digest against the reproduced RAW,
  **never** by container-file digest; conversion is a declared, pinned, deterministic post-reproduction step and
  a conversion mismatch MUST NOT enter variant review. OCI images, `.deb`/`.rpm`/tar, and ISO are canonical by
  their normalized unsigned bytes. Operator envelope (registry, tags, keys, signatures, timestamps) is outside
  the compared payload. FDE is an overlay; byte-identity is asserted pre-encryption only.

**Release set, vector & fallback**
- **R08** A build MUST emit one **atomic** release set (canonical OS image(s) + matching k8s package bundle +
  release manifest + per-artifact/aggregate SBOMs + provenance + compatibility/validation results + bootstrap
  inputs). Partial sets are not promotable.
- **R09** NVIDIA MUST publish, per profile, a signed **validation vector** (delivery + logical-content digests,
  manifest, SBOMs, provenance, conformance results, comparison rules) sufficient to **diagnose a mismatch
  without the bytes** — at minimum a per-artifact aggregate digest, **block-range digests over the RAW LBA
  stream** at a declared block size, and a **per-path** filesystem manifest.
- **R10** Digest mismatch fails closed for *Reproduced*. A release set MAY become a **ValidatedVariant** only
  when the cause is a pre-classified representation/host-nondeterminism difference (`NONDETERMINISTIC_METADATA`
  and peers — never content/input/toolchain/unknown drift) **and** logical content, SBOM/vuln/policy, and the
  full conformance/NKE/bootstrap/upgrade/rollback gates pass. Approval is two-tier: **pre-classified** causes
  (enumerated in the vector as machine-checkable predicates) auto-approve on the operator's own infra; **novel**
  causes need a signed NVIDIA exception. A ValidatedVariant gets a **distinct, expiring identity**, MUST NOT
  reuse the reference identity, and MUST NOT claim byte reproduction.
- **R11** Reproduction and runtime **conformance are two independent gates**; both are required for ordinary
  promotion. The **reproduction verifier MUST be isolated** from the build stages so a defect cannot manufacture
  both an artifact and its proof. An operator-built cluster is not claimed conformant until the operator runs
  the released suite against its own instantiation.

**Consumption, extension & operations**
- **R12** Before mutating infrastructure, NKE MUST verify recipe/pipeline/profile/input/vector/artifact
  identities, an exact vector match **or** a current signed ValidatedVariant, operator signatures/provenance,
  release-set completeness, and compatibility — selecting release sets by **immutable identity/digest, never a
  mutable tag**. A registry compromise MUST NOT let a mutable tag bypass digest+signature verification.
- **R13** NKD MUST declare a **bounded operator extension surface** (versioned overlay schema) sufficient for
  ordinary environment integration — CA bundle, proxy, internal DNS/NTP, monitoring/EDR agent, site auth —
  without forcing fork status. Every operator change classifies as: *published tuple* (own signed recipe/vector,
  reproducible) · *declared overlay* (outside canonical payload, reproducible + supported) · *variant* (changed
  canonical input / unsupported profile / out-of-schema overlay — no byte claim) · *fork* (changed engine
  semantics — unsupported).
- **R14** The normal path is **immutable** (new image → replace nodes, bounded interruption + rollback).
  **EmergencyDrift** is a time-bounded, authorized, evidence-carrying mutation reconciled into a later recipe; a
  missed deadline or out-of-scope change becomes **UnsupportedDrift**. Every node MUST report the recipe +
  release-set identities it runs; unrecognized mutation marks it drifted.
- **R15** Airgap/offline operation MUST preserve the **same verification semantics** as connected operation
  (mirrored vector + offline reproduction verify); the transport is open. A mandatory offline verify path
  exists.

## 6. Non-functional & constraints

- **Determinism:** sorted/normalized serialization, no timestamps/UUIDs/host paths/random in any canonical
  payload or the IR; reproducibility flags pinned in the IR (`CGO_ENABLED`, `-trimpath`, `-buildvcs=false`,
  `-ldflags=-buildid= -s -w`, `GOTOOLCHAIN=local`, `SOURCE_DATE_EPOCH` from the pinned commit).
- **Build from source, hermetically** is the default and strongest lane (referencing prebuilt artifacts is
  de-facto redistribution). A parallel, **opt-in no-compile lane** MAY acquire a pinned, content-addressed
  prebuilt artifact and verify it against a signable digest (fail-closed) — same discipline, weaker claim (no
  from-source guarantee), profile-independent.
- **Interfaces:** only standard ones — any OCI registry, any apt/deb repo, any CI engine, standard PXE/HTTP
  boot, standard sigstore (cosign keyless, **detached** signatures). NVIDIA-internal systems are non-normative
  background, never dependencies.
- **Security:** recipes/pipeline/vectors/release-sets integrity-protected + digest-addressable; expected digests
  authenticated by the signed vector; no credentials in artifacts/SBOMs/provenance/logs; vuln results identify
  the scanned artifact + policy version.
- **Multi-arch** amd64 + arm64, native per-arch (no cross-compilation).
- **Errors** are coded and map to distinct process exit codes; all fail-closed paths name the offending input.

## 7. Out of scope (v1)

Distributing/hosting the reference bytes; more than one builder profile; QCOW2/VMDK/VHD as *byte-reproduced*
artifacts; arbitrary OS/arch/Kubernetes/NKE outside the published compatibility matrix; claiming conformance
before the operator validates its own instantiation; guaranteed zero workload disruption on every node
replacement; managing the operator's registry/secret store/build infra/promotion; operator modifications
outside the declared extension surface. Downloadable reference artifacts MAY be added later as a convenience,
never replacing the definition + pipeline as the source of truth.
