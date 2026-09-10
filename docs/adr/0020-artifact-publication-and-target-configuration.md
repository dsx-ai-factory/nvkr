# ADR-20 — Artifact publication & target-registry configuration

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-20 in the consolidated NKD ADR set (added to close the publish-targets gap).
- **Relates to:** ADR-17 (external-interface seams), ADR-18 (pipeline emission), ADR-06 (publication / referrers),
  ADR-16 (promotion), ADR-04 (signing).
- **Satisfies:** FR-21 (standard, replaceable interfaces), SEC-03 (zero-binary), SEC-09 (secret handling),
  SEC-13 (registry-by-digest).

- **Scope:** Sections 3–4 describe qualified publication. Section 6 defines v1 publication; §7 is deferred registration.

## Context

The emitted pipeline produces verified artifacts — OCI images, helm-OCI bundles, `.deb` and nix packages, disk
images, and the trust objects — that must be published to **operator-controlled** registries and repositories.
Operators need to configure *where* those artifacts go without coupling the artifacts to any one environment.
This ADR fixes the target-configuration surface and the emitted publish phase.

## Decision

### 1. Targets are operator/transport configuration — never part of the recipe identity

Two operators who build the same recipe and publish the same verified artifacts to *different* registries produce
**identical artifact digests**; only the location differs. Target registries and repositories are therefore
**environment configuration**, declared **outside the catalog and recipe** and **excluded from `recipeDigest`**,
because they are transport rather than a build input. This preserves define-once / publish-many and
cross-environment byte-identity.

### 2. Target configuration surface

Environment (`nkd.dev/environment/v1`) owns runtime settings, destinations, credential references and signing
utility pins. Definition owns selected components and byte-changing packaging intent. The compiler derives
routine envelopes and routes from each named output, rejecting ambiguous, missing or unsupported routing.
Changing destination or signing tools can change Environment, prepared plan and evidence while preserving logical
Recipe identity. Output-affecting tools remain bound to Recipe/composition identity.

Each acquired tool declares `format: raw` or `format: tar-gzip`. Both bind exact transport and executable
SHA-256 digests. Raw tools require those digests to be identical; archive tools select one safe executable
member. Unknown formats fail before acquisition. This supports official static executable release assets
without introducing a repackaging step or inferring transport format from filenames.


Environment may also map a canonical source URL to an HTTPS fetch mirror and a GitHub secret reference
through `sourceAccess`. This changes transport and Environment identity, not the canonical source, pinned
commit/tree, or Recipe identity. The current adapter binds the credential only to the matching GitHub fetch
execution step using transient, repository-scoped Git configuration. Secret values never enter a remote URL,
serialized plan or transported source tree. Local and GitLab execution reject this configuration until those
adapters support equivalent credential isolation.

OCI is the current v1 publication transport. Executable, APT-repository and Helm outputs can share a recursive
OCI closure; an APT tree inside it is not an enabled direct APT-server publisher. Package repository and object
store targets remain separately capability-gated adapters. Credential values never enter serialized inputs.
Transport settings stay replaceable by environment owners.

### 3. The emitted publish phase

The generated pipeline gains a **publish phase after the unconditional terminal verify**:

- **only verified artifacts publish** — publish is gated on verify;
- for an atomic release, publish and promote are gated on the **completeness check** (ADR-16), so a partial set is
  not publishable as a release;
- pushes go through the **ADR-17 external-interface seams** (`OCIRegistry` / `PackageRepo` / `ObjectStore`), so
  the default implementations are operator-swappable.

### 4. Content-addressed, referrers-attached

Artifacts publish **by digest**. The authenticated Authorization (clearance) carries an exhaustive canonical
publication manifest for every cleared subject and every required ValidationVector, SBOM, provenance, and status
referrer. Each attachment binds both its NKD semantic identity and its raw OCI blob digest and media type.
Publication must match that manifest exactly: zero, missing, extra, type-confused, or substituted referrers fail
before a push. Consumers select ReleaseSets by **immutable digest**, and a mutable tag cannot bypass
digest-plus-signature verification (SEC-13).

The clearance is the signed **root/commit object over that manifest**, not a member of its own referrer closure;
including its own blob or detached signature would be circular. It is published separately by its
`nkd.clearance` identity with exact detached `RoleClearance` signature material, and it is the discovery and
commit point a downstream consumer authenticates before trusting the manifest. A status checkpoint targets the
ADR-06 `publicationIdentity`, so it is discoverable from this Publication Root; it MUST NOT be attached to
arbitrary component OCI subjects. Package and object releases carry their ValidationVector and evidence closure
through the object-store side of the same root protocol rather than silently discarding OCI-shaped referrer
inputs.

That non-circular root/discovery/signature protocol is mandatory for a qualified publication claim. The delivery
module implements its transport representation as an OCI image index with artifact type
`application/vnd.nkd.publication-root.v1`: every child is an exact OCI manifest or nested index, and the canonical
root config binds each child's name, kind, artifact type, OCI descriptor digest, exact payload or root blob
digest, semantic digest, size, and optional platform. The registry tag is only a discovery hint; the publisher
returns, and the consumer requires, `repository@sha256:<root-index-digest>`. Pull materializes and verifies the
complete recursive closure before the destination becomes visible.

Loose executables use `application/vnd.nkd.binary-bundle.v1`. The Binary Bundle's canonical config binds OS,
architecture, purpose, optional ReleaseSet and Inventory identities, and every file's name, absolute install
suggestion, mode, ordinary SHA-256, and size. Its one deterministic
`application/vnd.nkd.binary-bundle.layer.v1.tar+gzip` layer has an OCI title, so a general-purpose client such as
ORAS can download it and ordinary `tar` can unpack it. Debian packages may also be projected into a deterministic
Acquire-By-Hash repository; that APT Projection is not a new release artifact, and an unsigned `Release` file is
development-only.

The public CLI constructor emits only a visibly `diagnostic` root and requires `--allow-diagnostic` before push.
It cannot set publication identity, publication-manifest digest, or clearance digest. The package-level
`delivery.BuildQualifiedPublicationRoot` is the only qualified publisher constructor: it consumes the opaque
authenticated `RoleClearance` capability, rehydrates the exact `Authorization`, matches its ReleaseSet,
Inventory, publication manifest, artifacts, referrers, optional derived Binary Bundles, and detached proof
material, then emits an opaque `QualifiedRoot`. `Authorization` alone and the legacy transport entry point remain
fail-closed, because neither carries those proof bytes. Remote CLI inspection verifies structural and content
closure but does not authenticate a claimed qualified root; production consumption still requires the operator's
trust policy and clearance authentication.

### 5. Credentials by reference, never embedded

The target config names a registry or repository plus a **credential reference** resolved from the CI engine's
secret mechanism, which is transport. **No secret** enters any artifact, manifest, SBOM, log, or the config
itself (SEC-09); signatures remain detached (SEC-03).

### 6. Pipeline-attested publication

NVIDIA's hosted workflow publishes signed artifact bytes to its configured OCI registry. ADR-04 owns keyless
signature/attestation storage and ADR-16 owns evidence tiers and full release acceptance. The compiler generates
the root and exact lifecycle binding from named outputs; normal authors do not wire binary → root → route.

Payload identities survive publication unchanged. The outer OCI root separately binds content, compositions and
their exact realization (ADR-19). Consumer verification validates the complete closure before exposing accepted
views. Plain `release inspect` is descriptive. No tag-based cosign signature substitutes for qualified clearance.

Helm outputs accept either an AICR-generated tree or one observed source archive component. The latter carries
a single `helm/` directory, extracted through the bounded transport adapter and repacked deterministically.
Lifecycle observation checks the exact archive against its producer result and independently reproduces the
publication payload. Optional Definition Helm settings select a chart and another named OCI output. The
compiler adds that image package as an explicit dependency. The rendered-controller adapter checks the native
image closure, embeds its manifest digest in the unique single-container Deployment, clears the default
repository, and refreshes bundle checksums. The template requires a repository at deployment time, preserving
artifact bytes across registry mirrors; a values override cannot replace its embedded digest. The adapter
requires a plain rendered `templates/controller.yaml` and an image values mapping, rejecting ambiguity or
pre-existing templates. Other chart shapes require a separate adapter. This does not establish external chart
dependency availability or successful deployment.

Push, sign and pull-back verification are separate effects: later failure may leave bytes or partial evidence in
the registry. No atomic remote rollback or cross-run resume is claimed. A complete local Run does not establish
the deferred qualification/manifest acceptance in ADR-16. Retention and recovery require deployment ownership.

### 7. Operator release registration — deferred

An optional signed `ReleaseRegistration` index may let an operator map a segment to an already-cleared,
published release. It carries references, not artifacts or validation authority, and remains post-v1 with no
implementation. Registration is internal discovery machinery, not another normal authored product object.

Use a strict versioned, sorted, duplicate-free index with a new `nkd.release-registration` family and a distinct
registration signer role. Segment keys preserve the layers: recipe selection `{category, os, arch, k8sVersion}`;
composition `{role, clusterType, cni}`; and digest-pinned AICR activation. Accelerated runtime is a category plus
AICR profile/inputs, not a flat GPU artifact axis. A role subset has no independent Recipe identity.

Each entry binds Recipe Digest, signed ValidationVector digest, pinned publication root and clearance digest;
a cluster entry also binds the exact cluster identity. Optional qualification must match **both** its exact
cluster and release subject; a bare recipe entry cannot carry cluster qualification. Names, mutable osProfile
strings, tags and paths cannot establish identity. The repository locator of a root is transport; only the
root digest participates in logical registration identity, so a byte-identical mirror preserves it. Human
labels are nonidentity annotations.

NKD mints/signs over already authenticated cleared contents. A capability-gated adapter only transports that
record to an operator-owned endpoint and returns an authenticated acceptance receipt; it cannot mint a passing
boolean, mutate NKD authority, or register an uncleared composition. Vendor/provider/count vocabulary stays in
the downstream Projection. Admission fails before output until an authenticated endpoint adapter exists.

The record uses canonical byte-CAS/no-replace persistence and OCI referrer discovery on publication identity,
or an authenticated air-gap copy. There is no hosted NKD feed or new registered/available release state.
Qualified status checkpoints still govern supersession, deprecation and withdrawal. Production acceptance needs
register → segment resolve → downstream consumption plus wrong-signature, mismatched qualification, mutable
reference, withdrawn target, mirror-location invariance and forged-adapter-receipt negatives.

## Consequences

**Positive**
- Operators fully control destinations; the same verified artifacts publish anywhere without changing their
  identity.
- Qualified publication is gated on verification and completeness. V1 uploads bytes before signing and
  pull-back verification; visibility alone cannot establish acceptance.
- Transports are standard and swappable, and content addressing with referrers keeps consumers on immutable
  digests.

**Negative / costs**
- The target config is an environment surface that must stay out of the recipe identity — a boundary reviewers
  must hold.
- Credential handling depends on the CI engine's secret mechanism, which is per-engine plumbing.

**Neutral**
- Publication identity and discovery are ADR-06; this ADR adds the *targets* and the *publish phase*, not new
  trust mechanisms.

## Alternatives considered

- **Declare targets in the catalog or recipe.** Rejected — perturbs `recipeDigest` and couples a recipe to one
  environment; targets are transport, not definition.
- **NKD hosts a registry or release-stream service.** Rejected — deployment owners supply storage and discovery. The v1 NVIDIA workflow publishes bytes; qualified publication remains operator-owned.
- **Qualified publication before authorization.** Rejected — only verified, cleared contents may be published
  as a qualified release. V1's distinct ordering is stated in §6.

## Follow-ups

- **ADR-17:** the `OCIRegistry` / `PackageRepo` / `ObjectStore` seams the publish phase uses.
- **ADR-16:** the completeness/promotion gate the release publish is bound to.

## Acceptance evidence

The container-image delivery adapter has contract tests for exact OCI-export archive import, native image
manifest/config/layer closure, decompressed layer identities, platform selection, safe archive members and
unchanged image identities inside a publication root. Public OCI outputs lower to one built OCI-export
component, checked against its producer bytes before publication; consumer projection checks the native image
and its platform. The CWE producer is generated and its upstream Dockerfile was exercised locally; acquired-image
layout conversion remains unavailable, and no combined image/chart hosted Run is yet recorded. Source Helm
archive selection and exact producer observation have focused contract tests. This adapter does not mint qualified execution or
release authority.

Current contract evidence in `pkg/nkd/transport/*_test.go`, `pkg/nkd/release/*_test.go`, and
`pkg/nkd/delivery/*_test.go` covers target exclusion from recipe identity, authorization-bound subjects,
exhaustive referrer matching, semantic-versus-blob digest separation, no implicit I/O, deterministic binary and
APT layouts, recursive OCI closure verification, extraction, and rejection of missing, extra, or substituted
bytes. The dated local NKE record in reconciliation establishes development registry interoperability for
earlier inputs, not authenticated release publication or production-registry acceptance.

End-to-end acceptance requires publishing one cleared mixed-artifact ReleaseSet to operator-selected targets,
discovering every referrer by immutable digest, authenticating the downstream clearance, and proving that
different destinations preserve every semantic and blob identity while omitted credentials never enter logs or
payload bytes.
