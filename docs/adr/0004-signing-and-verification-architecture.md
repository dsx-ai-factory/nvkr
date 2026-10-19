# ADR-04 — Signing & verification architecture

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-04 in the consolidated NVKR ADR set.
- **Relates to:** ADR-03 (trust-failure codes), ADR-05 (trust objects), ADR-07 (builder-profile signing), ADR-06
  (publication signing), ADR-16 / air-gap (offline verify).

- **Scope:** Sections 1–4 govern qualified detached-document trust; §5 governs v1 OCI signing and public consumer scopes.

## Context

NVKR supports two explicit trust contracts. Qualified reproduction authenticates detached publisher expectations
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
  optional bundle output. After a successful child exit, NVKR seals and captures those exact descriptor contents
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

The local M3 rehearsal uses a separate `LocalCosign` detached-document adapter with explicit
passwordless demo keys. It holds the pinned static Linux executable, private/public key material and
signature descriptors, bounds execution and output, removes ambient signing configuration, and verifies
each newly signed document under the independently supplied public key before issuing its local receipt.
Local signing explicitly disables public transparency submission; verification is offline under that key.
The receipt binds exact document and public-key bytes and cannot be used as a keyless `SignedDocument`,
hosted workflow identity or qualification verdict. The local qualification owner must independently
enforce signer roles, complete artifact/input membership and all gates before invoking it. This is an
explicit local rehearsal adapter, never a fallback when hosted signing fails. Reconciliation records
real-key interoperability and the remaining end-to-end work separately.

Local rehearsal key admission currently supports one P-256 PKIX `PUBLIC KEY` PEM block per role,
matching the local cosign key-generation path. Parsing rejects other curves, malformed points,
PEM headers, private-key labels, extra blocks and non-whitespace surrounding material. Canonical
public-key encoding removes PEM presentation differences before checking that builder, smoke and
qualifier keys are distinct. Role assignment remains part of the independently selected policy identity.

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

The v1 registry signer uses the catalog-pinned cosign 3.1.3 executable. Artifact signatures, SLSA production
provenance and CycloneDX SBOMs all use standard Sigstore v0.3 DSSE bundles published as OCI referrers.
`sign` and `attest` explicitly select bundle format; SBOM creation uses `attest-blob` with the same format.
The signature predicate is `https://sigstore.dev/cosign/sign/v1`; SLSA and CycloneDX retain their distinct
standard predicate types. One evidence class cannot satisfy another.

Verification downloads only the required signature/provenance bundles using cosign's predicate-filtered
`download attestation`, then verifies their exact bytes with `verify-blob-attestation`, the expected digest,
identity and issuer. It reads observed identity/issuer from the embedded certificate in those same verified
bytes, never from cosign's presentation output or statement annotations. Legacy reports and public-key
bundles cannot satisfy this keyless policy. Registry order is normalized; bounded candidates are checked
against the externally selected policy, and success requires an authenticated bundle for each required type.
The adapter bounds download output, candidate count and verification duration, uses private temporary bundle
files, checks them for mutation and removes them on success or failure. It does not download artifact payloads
or use a local-image snapshot. Missing evidence fails without a partial successful record or legacy fallback.
Previously published legacy signature/provenance attachments must be reissued as bundles.

The adapter is keyless only; the keyed/KMS fallback in §1 applies solely to detached qualified documents.
Catalog tool sources and locks own the acquisition reference and exact executable digest; Environment selects
the expected identity and issuer. Ambient PATH or a version string is not a pin. Production retains normal
transparency checks. Local-key tests deliberately substitute fixture trust and do not establish hosted acceptance.

The pipeline signs and attests each deliverable manifest in its own repository, then the release index.
The same Run-level production predicate is subject-bound separately to each manifest. Hosted verification
requires all deliverable signatures and attestations as well as the root checks before recording success.
Independent cosign consumption authenticates that artifact; release membership and required SBOM coverage
still require the signed release index (ADR-20). No extra payload publication is introduced.

SBOMs use a distinct standard CycloneDX predicate. Before publication the pinned signer creates a Sigstore
v0.3 attestation bundle using `attest-blob --hash` for the exact preparation-bound subject, then verifies it
with `verify-blob-attestation` under the expected identity, issuer and normal transparency checks. The bundle
must contain the exact canonical SBOM and one matching statement subject. Registry discovery uses an OCI
referrer manifest beside the associated deliverable; source evidence retains its original source subject.
Hosted pull-back verification independently checks every bundle. Keyless creation/verification remains
Linux-only and raw preparation layouts cannot satisfy authenticated release consumption.

In GitHub `per-output-v1`, the preparation producer inspects the complete payload. The separate SBOM job
receives only its bounded metadata and canonical documents, with exact subject, membership, Plan and Run
bindings; it does not independently inspect payload bytes. The publisher combines the resulting bundles
with the original preparation only after verifying the bundles, unchanged subjects and metadata, and exact
decoded document equality. It then checks the complete final payload before publication. Metadata alone
grants no payload-integrity capability. The full-layout attestation path retains its existing complete-byte
checks. This separation removes the unchanged payload's SBOM round trip while preserving OIDC isolation.

SBOM bundle creation permits at most three fresh keyless invocations within one three-minute deadline,
with cancellation-aware one- and two-second delays, only when Rekor v1 reports its explicit HTTP 409
`createLogEntryConflict` failure. Each attempt retains the same frozen predicate and exact subject and uses
a separate private bundle output. A conflict is never accepted as evidence: only a successful invocation
followed by the unchanged predicate, identity, issuer and transparency checks can return a bundle. Other
failures, output overflow and verification failures are not retried. This compensates for the conflict-recovery
gap in the pinned Cosign dependency, addressed upstream in
[sigstore-go PR 647](https://github.com/sigstore/sigstore-go/pull/647). Fresh signatures can leave unused
transparency entries. This policy does not add retries to OCI release signing or qualification attestation.

The signer re-derives each subject and requires equality with the Run's publication binding. In GitHub
`per-output-v1`, the publisher exports bounded metadata bound to the exact root it pushed. The signer
re-derives the root and sibling subjects from that metadata, checks the exact output/Plan/Run bindings and
cryptographically verifies required SBOM bundles before signing. It does not download or independently
check payload bytes; complete-byte checks remain in preparation, publication and independent remote
verification. Legacy signing retains its full-layout inspection. No caller-supplied digest or successful
job-status description substitutes for these checks.
The subsequent verifier re-resolves the registry reference, recomputes the served manifest digest, and verifies
both signature and subject-bound production provenance (ADR-09). A mismatch fails the Run and returns no
successful signing or verification record. Registry credentials use a private restrictive-mode `DOCKER_CONFIG`,
never argv. Dry-run executes no signer and contacts no registry for signing; it records intended argv and
`dryRun: true`, retaining `static-unqualified` evidence.

The production build signer's identity is a fixed committed workflow path and admitted branch ref under an explicit issuer.
Signed development uses the separate `nvkr-dev.yml` branch identity and reserved disposable namespace
specified by ADR-18/20. It performs the same signature, provenance and SBOM checks under that development
trust policy. A production consumer pinned to `nvkr-run.yml` does not accept those certificates; selecting
development signing grants no production or qualified acceptance. Registry cleanup does not erase public
transparency records or relax the existing disclosure/identity-service prerequisites.
Signer identity authenticates who produced the artifact; Build Digest separately identifies the selected logical graph. The separate
build/evidence qualification workflow has its own identity; build provenance and qualification evidence cannot
substitute for one another. Consumer instructions publish both identities and use stock
`cosign verify-attestation` with the explicit signature or SLSA predicate type. Downloaded bundles are also
independently verifiable with `verify-blob-attestation` and an independently selected digest. An NVKR reader is
a convenience, never a mandatory trust service. Event and credential admission are owned by ADR-18.

A Verification Policy explicitly selects lane and acceptance scope:

| Lane / scope | Required acceptance |
|---|---|
| `pipeline-attested` / `authenticity` | Exact metadata/evidence closure, root and every required named output's signature and subject-bound production provenance, and required SBOM bundles under the accepted build identity and issuer; artifact payload bytes unchecked |
| `pipeline-attested` / `artifact-publication` | Exact selected payload/evidence closure, accepted build signer identity and issuer, signature, and subject-bound production provenance |
| `pipeline-attested` / `release` | Artifact-publication checks plus ADR-16 qualification identity, verdicts, membership, and ADR-06 revocation checks |
| `qualified` / `release` | Qualified authority, evidence, freshness, status, and publication contracts in ADR-05/06/16/20 |

The `authenticity` scope fetches bounded manifests, configurations and evidence, including native child
metadata for aggregate images, without retrieving artifact payload layers. Root authentication alone is
insufficient: every required named output must satisfy the signature/provenance checks, and every required
SBOM bundle must pass cryptographic verification and its exact subject/stage/membership checks. Missing or
rejected evidence returns no successful result. Metadata navigation remains unauthenticated.

Lifecycle issues a separate opaque `AuthenticatedRelease` capability for this scope. Its description states
`payloadIntegrityVerified: false`; it cannot authorize payload export, satisfy full artifact-publication
verification, or issue an exact-input receipt. The scope authenticates the producer's recorded evidence, not
artifact availability, consumer-side disk inspection, qualification or revocation. Stock execution retains
the Linux and pinned-verifier admission requirements.

For disk outputs, `VerificationPolicy.minimumDiskEvidence` is part of the policy digest. Its omitted default is
`build-provenance-v1` for pipeline-attested scopes; explicit `strict` requires inspected evidence. Qualified
policy defaults to `strict` and rejects the producer-only minimum. A strictly inspected disk also satisfies
the weaker minimum. Authenticity applies the same minimum to signed disk evidence without reading or
reinspecting the disk. Unknown values fail closed, and no producer-signed disk mode can weaken consumer policy.

Missing scope is an error. Unsupported scopes fail admission before dependent I/O, without falling back to a
weaker scope. The returned opaque capability binds the exact reference and policy, including scope and the
minimum disk-evidence requirement; closing it
invalidates shallow copies. Public descriptions and inspection never supply verification authority.

Live keyless acceptance requires the protected hosted workflow, public-transparency disclosure approval before
the first signed NVKE bytes, bounded service retry/availability policy, and an independent cosign-only consumer
exercise. Unit tests and local dry-run records establish none of those operational facts.

The specified, disabled file-delivery extension in ADR-20 preserves the original build signature,
subject-bound SLSA provenance and SBOM bundles byte-for-byte. Signing must retain the exact authenticated
bundle bytes through an opaque closeable proof owner and provide supplied-bundle verification without
registry discovery. The current verification/signing JSON records are descriptive and cannot serve as
portable proofs. Capture must occur during the same verification that issues the source capability;
re-downloading an unbound candidate or serializing a verified boolean is insufficient.

The derived canonical delivery root uses detached keyless document signing with the admitted pinned signer,
normal transparency checks and a portable Sigstore bundle. Reusing detached-signing primitives does not
grant a new authority: the pipeline-attested delivery role must be explicitly admitted and cannot borrow
qualified-document or build-provenance authority. The signature binds relocation/completion, never a new
build or runtime verdict. Exact root bytes, proof bytes, subject and invocation are bound before publication.

Source verification and delivery verification take independently supplied Verification Policies. Both use
the pipeline-attested artifact-publication boundary for the complete operation: the delivery signature alone
cannot satisfy source provenance, evidence membership or payload checks. A policy may explicitly permit
one principal in both roles; that is not independent signer separation. The consumer never obtains trusted
identities from the root. Publisher-policy digests describe the issuing operation; a stricter consumer
binds its own effective policies to its resulting receipt. Disk evidence minimums remain enforced.
The root/locator/commit ordering in ADR-20 prevents circular signing. An indeterminate final upload cannot
be reported as producer success, but cannot invalidate a completion independently authenticated later.
This contract grants no implemented S3, offline, Secure Boot, full Release or qualified acceptance.

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

Current contract evidence lives in `pkg/nvkr/internal/heldfile`, `pkg/nvkr/sign/signer_test.go`,
`pkg/nvkr/sign/signer_linux_test.go`, `pkg/nvkr/sign/qualified_test.go`, `pkg/nvkr/trust/document_test.go`,
`cmd/nvkr/qualified_lifecycle_test.go`, and `cmd/nvkr/verify_test.go`. It covers exact-byte signing, private
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
