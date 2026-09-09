# ADR-04 — Signing & verification architecture

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-04 in the consolidated NKD ADR set.
- **Relates to:** ADR-03 (trust-failure codes), ADR-05 (trust objects), ADR-07 (builder-profile signing), ADR-06
  (publication signing), ADR-16 / air-gap (offline verify).

- **Scope:** Sections 1–4 govern qualified detached-document trust; §5 governs v1 OCI signing and public consumer scopes.

## Context

NKD supports two explicit trust contracts. Qualified reproduction authenticates detached publisher expectations
and independently produced bytes. The v1 pipeline-attested lane publishes artifact bytes with keyless signatures
and production provenance from a named hosted workflow. Both require external consumer trust policy; neither
allows a catalog or signed object to choose its own trusted identity. Offline equivalence belongs to the
qualified contract. The mechanisms below have separate claims and admission requirements.

## Decision

### 1. Detached keyless signing behind a testable seam

- **Default mechanism.** **Detached keyless** signing (cosign + Fulcio + Rekor) is the **default**. The
  signature and certificate are **sidecars** — never embedded in the bytes the identity covers.
- **Testable seam.** Signing and verification go through a **Signer/Verifier seam** with three test tiers: a
  **fake** (pure-logic unit tests, no binary), a **local-key round-trip** (fast CI, no OIDC/network), and a
  **gated keyless/Rekor lane** (integration, requires an OIDC identity).
- **Executable identity, not a version string.** A supported cosign **version range** is tested for
  compatibility, but neither a version string nor ambient `PATH` resolution establishes executable identity.
  Every stock qualified signing or verification resolves a configured executable target once, requires an
  externally configured exact `sha256` byte pin, streams it into a sealed anonymous capability, and on Linux
  executes only that inherited descriptor. The executable must be a static ELF; a filesystem-selected
  interpreter or shared-library closure fails `engine-capability`.
- **Environment scrubbing.** Loader-injection variables are removed for both adapters. Verification
  additionally removes cosign/Sigstore/TUF environment overrides — insecure flags, trust roots, log keys, and
  verification time — so stock verification authority is configured only by validated argv.
- **Failure ordering.** A missing or malformed executable pin fails `unpinned-input` at adapter construction,
  before document or trust-policy I/O. On Linux a byte mismatch is detected when the executable capability is
  captured, before signature material reaches cosign; document authentication may already have snapshotted its
  own exact input bytes by then.
- **Platform boundary.** Darwin deliberately fails `engine-capability` for stock authority execution: `/dev/fd`
  is noexec and macOS exposes no `fexecve`/`execveat` equivalent. A renameable named snapshot is not an
  acceptable fallback.
- **Identity handling.** **Assert on exit codes, not stdout text.** Thread the **OIDC issuer + identity at both
  sign and verify**. **Fail closed** if keyless is requested with no resolvable identity, and never default a
  specific issuer or identity.
- **Stock signing protocol.** Stock signing holds a defensive copy of the exact subject and streams it to cosign
  on stdin. cosign receives only anonymous inherited writable descriptors for signature, certificate, and
  optional bundle output. After a successful child exit, NKD seals and captures those exact descriptor contents
  and returns an opaque receipt bound to the subject digest. The production signer requires an explicit expected
  OIDC identity and issuer, passes the issuer as a qualified argument, extracts the issued certificate claims,
  and refuses a mismatch before returning the receipt. Cosign/Sigstore/TUF and loader environment overrides are
  scrubbed for signing as well as verifying.
- **Publication binding.** Publication rebinds that receipt immediately before writing any public document or
  sidecar; cosign never receives a named subject or a public output pathname. The complete signed set is staged
  and synced under a cross-process lock, then published with a durable journal plus no-replace links, so
  cancellation and process crash can roll back or resume one exact generation without accepting a mixed set.
- **Injected runners.** Custom injected runners use a separate private-path diagnostic protocol and are
  explicitly part of the embedding caller's TCB.
- **Keyed / KMS mode** is an optional operator fallback, not the default.

### 2. Trust store and resolution precedence

Trust must originate from **outside** the verified object. Resolution precedence:
**CLI flags > operator trust store > catalog-advisory.**

- A **catalog-declared** signer identity is **advisory only**. It can seed a trust listing and be promoted by an
  explicit operator action, but is **never auto-trusted** — otherwise a tampered catalog would self-authenticate.
- Under signature-required mode: no resolvable identity → `trust-policy-missing`; a verified signer matching none
  of the trusted set → `untrusted-signer` (ADR-03).

### 3. Verifier isolation (layered)

- **Default: a measure-only verify path** that re-hashes independently-produced artifacts and **never invokes
  the build engine**, enforced by a dependency test asserting the verify path does not import the builder.
- **High-assurance: a separately-linked verifier binary** containing no build-engine code, used for publisher
  qualification and the P0 isolation sign-off. It is the only layer that gives **link-level** isolation; a
  shared defect inside one binary could otherwise bias both artifact and proof.
- Both layers run the same signature, trust, and digest checks. Isolation weakens no check.

### 4. Offline bundle + mirrored trusted root

- Air-gap preserves the **same** semantics — a real Rekor **inclusion proof**, never "ignore the transparency
  log".
- The publisher ships a **self-contained Sigstore bundle** (certificate chain + signature + Rekor inclusion proof
  + signed checkpoint) **and** a **pinned, mirrored trusted-root snapshot** (Fulcio / Rekor / CT keys).
  Validating the certificate offline needs the trusted root, so mirroring the bundle alone is insufficient.
- The expected trusted-root digest is an operator trust-policy input — an explicit CLI pin or the operator store
  — bound into the immutable signer-role policy. A verified document, bundle, or caller-supplied material
  pathname cannot select its own root. Missing, malformed, path-only, or mismatched root authority fails
  `unpinned-input` before cosign runs. The accepted receipt records the exact root digest for audit.
- The authenticator supplies the exact document bytes directly to cosign on stdin. Signature, certificate,
  bundle, and root bytes are captured once into sealed anonymous files and exposed only as inherited descriptor
  paths. Sigstore v0.3 is self-contained and rejects detached signature/certificate flags, so that protocol
  gives cosign only the held bundle and root paths, extracts claims from the exact bundle-embedded leaf, and
  requires the held detached certificate to match that leaf. Rename or restore of any source or private snapshot
  pathname therefore cannot separate the bytes verified from the bytes decoded or authorized.
- Offline verify checks the inclusion proof against the mirrored root; a corrupt proof → `signature-invalid`.

### 5. Pipeline-attested OCI signing and consumer verification

The v1 registry signer uses cosign-native `sign` and `attest` on each pushed digest, with cosign 2.x tag-based
signature/attestation storage. It is keyless only; the keyed/KMS fallback in §1 applies solely to detached
qualified documents. Environment owns the exact lowercase SHA-256 pin of the cosign executable, its acquisition
reference, expected identity, and issuer. Ambient PATH or a version string is not a pin.

The signer re-derives the subject from held artifact bytes and requires equality with the Run's observed subject.
The subsequent verifier re-resolves the registry reference, recomputes the served manifest digest, and verifies
both signature and subject-bound production provenance (ADR-09). A mismatch fails the Run and returns no
successful signing or verification record. Registry credentials use a private restrictive-mode `DOCKER_CONFIG`,
never argv. Dry-run executes no signer and contacts no registry for signing; it records intended argv and
`dryRun: true`, retaining `static-unqualified` evidence.

Build identity is a fixed committed workflow path and default-branch ref under an explicit issuer. The separate
recipe/evidence qualification workflow has its own identity; build provenance and qualification evidence cannot
substitute for one another. Consumer instructions publish both identities and retain stock `cosign verify` and
`cosign verify-attestation --type slsaprovenance1` as supported independent verification paths. An NKD reader is
a convenience, never a mandatory trust service. Event and credential admission are owned by ADR-18.

A Verification Policy explicitly selects lane and acceptance scope:

| Lane / scope | Required acceptance |
|---|---|
| `pipeline-attested` / `artifact-publication` | Exact selected closure, accepted build identity and issuer, signature, and subject-bound production provenance |
| `pipeline-attested` / `release` | Artifact-publication checks plus ADR-16 qualification identity, verdicts, membership, and ADR-06 revocation checks |
| `qualified` / `release` | Qualified authority, evidence, freshness, status, and publication contracts in ADR-05/06/16/20 |

Missing scope is an error. Unsupported scopes fail admission before dependent I/O, without falling back to a
weaker scope. The returned opaque capability binds the exact reference and policy, including scope; closing it
invalidates shallow copies. Public descriptions and inspection never supply verification authority.

Live keyless acceptance requires the protected hosted workflow, public-transparency disclosure approval before
the first signed NKE bytes, bounded service retry/availability policy, and an independent cosign-only consumer
exercise. Unit tests and local dry-run records establish none of those operational facts.

## Consequences

**Positive**
- Qualified detached trust preserves the zero-binary contract online and offline; v1 separately publishes
  signed artifact bytes under §5.
- Trust anchors outside the verified object close the catalog self-trust hole.
- The seam makes trust logic testable without a live signing service.

**Negative / costs**
- Keyless behavior (certificate identity, tlog inclusion) is exercised only when the gated OIDC lane is present;
  fast CI relies on the fake and local-key tiers.
- Operators must distribute the exact verifier-executable digest as trust configuration and rotate it explicitly
  with cosign upgrades. A supported version range remains compatibility guidance, not verification authority.
- Operators must also distribute and rotate the exact trusted-root digest independently of the signed object.
- The stock qualified subprocess signer and verifier currently require Linux. Darwin and platforms without an
  exact executable-capability primitive fail closed; supporting them requires a separately linked implementation
  or another non-pathname capability mechanism.
- A separately-linked verifier adds packaging surface.
- The offline bundle format and trusted-root refresh are an infra dependency and a spike.

**Neutral**
- cosign / Fulcio / Rekor are standard, replaceable interfaces — shelled, never linked — consistent with the
  Apache-clean core.

## Alternatives considered

- **Link the sigstore libraries.** Rejected — pulls heavy deps and risks the Apache-clean core; shell cosign
  instead.
- **Embed the signature in the artifact/manifest.** Rejected — mutates the bytes the digest covers; violates
  zero-binary.
- **Let a catalog-declared identity alone establish trust.** Rejected — a tampered catalog self-authenticates.
- **Offline via `--insecure-ignore-tlog`.** Rejected — drops the transparency guarantee; verify a mirrored
  inclusion proof instead.

## De-risking spikes

1. **Offline bundle & trusted root** — fix the bundle media type and the mirrored trusted-root input; test the
   offline verify with egress blocked (local sigstore scaffold or checked-in fixtures).
2. **Keyless in hermetic CI** — a network-free fake-keyless harness, or an ephemeral local sigstore, so keyless
   behavior is exercised without a public tlog.

## Follow-ups

- **ADR-03:** the trust-failure classes (`signature-invalid` / `untrusted-signer` / `trust-policy-missing`) and
  their ordering (the signature/trust gate runs before the digest comparison).
- **ADR-05 / ADR-06 / ADR-07:** the manifest, publication identity, and builder profile are all signed through
  this seam.

## Acceptance evidence

Current contract evidence lives in `pkg/nkd/internal/heldfile`, `pkg/nkd/sign/signer_test.go`,
`pkg/nkd/sign/signer_linux_test.go`, `pkg/nkd/sign/qualified_test.go`, `pkg/nkd/trust/document_test.go`,
`cmd/nkdctl/qualified_lifecycle_test.go`, and `cmd/nkdctl/verify_test.go`. It covers exact-byte signing, private
snapshots of diagnostic subject/output bytes, anonymous held production signing outputs, strict role/schema
dispatch, offline inclusion-proof arguments, actual certificate identity/issuer extraction, mutation and
insecure-option rejection, and a stock measure-only verifier that refuses to rebuild. It also covers rejection
of ambient or unpinned verifier execution, wrong binary and trusted-root pins, sealed Linux descriptor
execution, stdin subject signing and verification, inherited material and output descriptors, loader plus
cosign/Sigstore/TUF verification-authority environment scrubbing, no publication before successful signing,
exact identity/issuer enforcement, ambient signing-environment rejection, cancellation rollback, concurrent
no-replace publication, subprocess-crash recovery from a partial signed set, and adversarial source/executable
rename-and-restore. The qualified signer returns an opaque receipt bound to the exact signed subject; retained
legacy signer/verifier surfaces are explicitly unqualified.

This evidence does not exercise a public keyless service, trusted-root rotation, or a separately linked verifier
binary. The stock signing subprocess is an operational producer tool, not consumer trust authority; its output
remains untrusted until this independently pinned verification path authenticates it.

End-to-end acceptance requires the fake, local-key, and gated keyless tiers named above; an egress-blocked
offline round trip using a rotated mirrored root; and a dependency/binary inspection proving the high-assurance
verifier contains no build-engine code.
