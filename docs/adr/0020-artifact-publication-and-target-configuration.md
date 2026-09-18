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

- **Scope:** Sections 3–4 specify deferred qualified publication. Diagnostic transport construction is
  implemented as stated in §4. Section 6 defines v1 publication; §7 is deferred registration.

## Context

The emitted pipeline produces verified artifacts — OCI images, helm-OCI bundles, `.deb` and nix packages, disk
images, and the trust objects — that must be published to **operator-controlled** registries and repositories.
Operators need to configure *where* those artifacts go without coupling the artifacts to any one environment.
This ADR fixes the target-configuration surface and the emitted publish phase.

## Decision

### 1. Targets are operator/transport configuration — never part of the build identity

Publishing the same verified artifact bytes to *different* registries preserves their **artifact digests**;
only the location differs. This makes no claim that two executions reproduce identical bytes. Target registries
and repositories are **environment configuration**, declared **outside producer instructions** and **excluded from `buildDigest`**,
because they are transport rather than a build input. This preserves define-once / publish-many and
cross-environment byte-identity.

### 2. Target configuration surface

Environment (`nkd.dev/environment/v1`) owns runtime settings, one optional OCI publication namespace,
credential references and signing identity/issuer policy. `publication.repositoryPrefix` is an explicitly
qualified, untagged repository prefix; credentials, anonymous access and plain HTTP are transport settings.
When publication is configured, the release index and metadata publish under
`<repositoryPrefix>/releases`. Each deliverable and its SBOM attestation bundles publish in
`<repositoryPrefix>/<output-name>`. The signed
release metadata binds canonical sibling repository names and manifest digests; no complete closure is copied
into the product repositories.
Omitting publication produces a build-only Run. Destinations and a separate output-to-destination map are
not authored. The generated internal routes still bind exact package inputs and targets.

The publisher uploads each publication root by its content digest, without creating or advancing `latest`,
a fixed version tag, or a development tag. It returns the digest-pinned reference recorded in the Run.
Explicit low-level tagged publication remains a discovery operation; automatic version/channel aliases and
promotion policy are not implemented by this lifecycle configuration.

Definition owns selected components, byte-changing packaging intent, and catalog tool selectors (`sbomTool`,
`cosignTool`, and output `aicr.generator`). Catalog source declarations own release URL templates, executable
members, formats, platform and version constraints. The generated source lock owns the resolved tool URL,
archive digest and executable digest (ADR-22). Environment contains no executable pins. A dry-run signing
Environment does not acquire the selected cosign executable. Output-affecting tools remain bound to Build
identity; evidence tools affect catalog/plan/evidence identity without changing the logical source-build graph.


Environment may also map a canonical source URL to an HTTPS fetch mirror and a GitHub secret reference
through `sourceAccess`. This changes transport and Environment identity, not the canonical source, pinned
commit/tree, or build identity. The current adapter binds the credential only to the matching GitHub fetch
execution step using transient, repository-scoped Git configuration. Secret values never enter a remote URL,
serialized plan or transported source tree. Local and GitLab execution reject this configuration until those
adapters support equivalent credential isolation.

OCI is the current v1 publication transport. Executable, APT-repository and Helm outputs can share a recursive
OCI closure; an APT tree inside it is not an enabled direct APT-server publisher. Package repository and object
store publishers remain deferred and fail capability admission. Credential values never enter serialized inputs.
Transport settings stay replaceable by environment owners.

The disk target is a future OCI evidence and control root over an exact QCOW2 payload. An optional S3 object and
CDN URL are projections of those same payload bytes, not alternate Build identities. The signed root must bind
the payload digest, exact object route, and authenticated upload/read-back result before a consumer may accept
the projection. Object-store credentials remain Environment references and cannot enter the disk, evidence, or
route metadata. The first disk authoring contract adds no object-store schema, OCI disk wrapper, signing route,
publisher, or consumer adapter; all remain capability-gated. An FDE payload containing an unlock credential
cannot pass the existing no-secret publication rule.

### 3. The emitted publish phase

The generated pipeline gains a **publish phase after the unconditional terminal verify**:

- **only verified artifacts publish** — publish is gated on verify;
- for an atomic release, publish and promote are gated on the **completeness check** (ADR-16), so a partial set is
  not publishable as a release;
- maintained OCI pushes use the delivery module's registry adapter with Environment-owned destinations and
  credentials. Qualified publication, direct package repositories and object stores have no production adapter.

### 4. Content-addressed, referrers-attached

The qualified clearance and commit protocol below is deferred. Its unused same-process implementation has
been removed; the transport representation remains implemented for diagnostic and pipeline publication.

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
`application/vnd.nkd.publication-root.v3`: every child is an exact OCI manifest or nested index, and the canonical
root config binds each child's name, kind, artifact type, OCI descriptor digest, exact payload or root blob
digest, semantic digest, size, and optional platform. The registry tag is only a discovery hint; the publisher
returns, and the consumer requires, `repository@sha256:<root-index-digest>`. Pull materializes and verifies the
complete recursive closure before the destination becomes visible.

The config uses `nkd.dev/publication-root/v3` and `application/vnd.nkd.publication-root.config.v3+json`.
Its output bindings carry Build Digest and exact named output membership; deployment cluster metadata is
removed. Readers reject earlier metadata and removed fields. ValidationVector v3 and ReleaseSet/Inventory v4
remain implemented. The retired clearance and publication-manifest families remain reserved and nonminting;
retained transport media identifiers do not imply a current clearance issuer or verifier.

Loose executables use `application/vnd.nkd.binary-bundle.v1`. The Binary Bundle's canonical config binds OS,
architecture, purpose, optional ReleaseSet and Inventory identities, and every file's name, absolute install
suggestion, mode, ordinary SHA-256, and size. Its one deterministic
`application/vnd.nkd.binary-bundle.layer.v1.tar+gzip` layer has an OCI title, so a general-purpose client such as
ORAS can download it and ordinary `tar` can unpack it. Debian packages may also be projected into a deterministic
Acquire-By-Hash repository; that APT Projection is not a new release artifact, and an unsigned `Release` file is
development-only.

The public CLI constructor emits only a visibly `diagnostic` root and requires `--allow-diagnostic` before push.
It cannot set publication identity, publication-manifest digest, or clearance digest. Qualified publishing is
not implemented. The unused qualified constructor, legacy filesystem publication adapters, clearance authentication and
release-only receipt implementation are removed. A future qualified publisher must consume
authenticated clearance, match its exact ReleaseSet, Inventory, publication manifest, payloads, referrers and
detached proof bytes, and implement the complete root commit protocol before it may emit a qualified root.
Remote CLI inspection verifies structural and content closure but does not authenticate a claimed qualified
root. Qualified consumption requires an implemented clearance verifier and the operator's trust policy;
no current diagnostic operation grants that authority.

### 5. Credentials by reference, never embedded

The target config names a registry or repository plus a **credential reference** resolved from the CI engine's
secret mechanism, which is transport. **No secret** enters any artifact, manifest, SBOM, log, or the config
itself (SEC-09); signatures remain detached (SEC-03).

Publication verification admits a declared credential before registry resolution or pull. A missing username
or secret returns `NKD-E-TRUST-POLICY-MISSING`, without consulting ambient Docker credentials or writing a
verification record. Resolution and closure retrieval use the same admitted credential values.

### 6. Pipeline-attested publication

NVIDIA's hosted workflow publishes signed artifact bytes to its configured OCI registry. ADR-04 owns keyless
signature/attestation storage and ADR-16 owns evidence tiers and full release acceptance. The compiler generates
one release index from named outputs; normal authors do not wire payload → root → route.

The signed metadata references each deliverable manifest once in its own sibling repository: CLI binary bundle,
unchanged native CWE image, and Helm artifact. The OCI index directly references only its metadata manifest. Helm has an empty-config artifact manifest and its final bound archive layer; its former
nested publication root is removed. The native image is pullable from its own output repository by its manifest
digest, without the release index. Local OCI-layout index files are handoff scaffolding, not registry wrappers.

One canonical release metadata config, carried by one manifest because an OCI index has no config, owns
output names, sibling repository locators, descriptor identities, output-to-Build-Digest/component mappings, content subject and
required evidence membership. There is no separately published lifecycle-binding object. Output identity is
the deliverable manifest digest (APT retains its typed repository identity); input layout-index hashes are not
Artifact identities. The qualified representation in §4 remains a separate protocol, not a compatibility path
for former pipeline publications. The current consumer rejects pipeline roots without this metadata.

The delivery module owns the one canonical release-content projection and its digest for both producer and
consumer. It contains sorted exact output names, kinds, artifact digests, Build Digests and components; repository
references are excluded. This ownership consolidation preserves existing canonical content bytes. The compiler
still joins independently frozen output requirements to observed payloads before publication. The consumer does
not reconstruct compiler records or derive both expectations and observations from release metadata.

Consumer integrity checking opens an opaque delivery publication with an explicit lifetime. It snapshots the
index and blobs into bounded anonymous immutable files, verifies every reachable descriptor and evidence slot,
and checks format-specific payload membership against that exact closure. Private extraction workspaces are
removed before the capability is returned. Later source-path replacement cannot change the checked output
projection, and defensive copies cannot mutate the held view; closing releases the file capabilities and rejects
further projections. Signature and policy verification remain in the lifecycle consumer. This capability proves
integrity and membership only, never signer authority by itself. Path-based diagnostic operations retain their
independent revalidation at each call.

Each required SBOM is embedded once as a CycloneDX predicate in a signed in-toto statement inside a standard
Sigstore v0.3 bundle. An empty-config OCI referrer manifest carries that bundle in the owning output repository;
no raw SBOM copy is published. Its OCI subject is the associated deliverable manifest, while the statement
subject remains the exact inventoried binary, native image manifest, or source/final archive. Shared evidence
belongs to the first sorted output membership's repository. Membership binds the exact subject, stage,
canonical document digest, bundle blob and manifest digests, and typed document identity. The CLI inventories its exact binary; CWE inventories
the native manifest and records the scanned OCI-export input; Helm has distinct source-archive and final-bound-
package inventories. Identical evidence may have one published document with sorted output memberships only
after each output's exact producer/evidence handoff is validated. Equal Build Digests cannot satisfy missing
output uses. Per-build indexes stay internal. Missing, substituted, duplicate or unbound evidence fails
construction and consumption. A protected pre-publication stage creates and verifies the bundles before
assembling the final release metadata; production publication and authenticated consumption reject raw
preparation evidence. Finalization regenerates the canonical documents from handoffs and rejects disagreement.

Signatures, SLSA provenance and SBOMs use the same standard Sigstore v0.3 bundle format as OCI referrers
(ADR-04); their predicate types remain distinct. One root signature covers evidence and output repository/digest bindings through metadata; one SLSA attestation targets that
same root. Signature/attestation attachments stay outside the signed index to avoid circular identity. Each deliverable manifest also receives its own signature and subject-bound production attestation in its
own repository for independent consumption. Signing processes deliverables before the root; hosted verification
requires every child signature and attestation before recording success. The Release consumer still authenticates
membership and SBOM coverage through the root. Standalone verification does not establish that release-wide claim. Run completion is a separate descriptive
record, not signed merely because its referenced bytes are signed. Consumers resolve only sibling repositories within the chosen release namespace, fetch every pinned manifest
and verify its full payload/evidence closure before exposing accepted views. Metadata cannot redirect credentials
to another host or namespace. The publisher validates local bytes first, uploads and checks deliverables in their
repositories, then publishes the release index last; hosted verification pulls the full distributed closure. Plain `release inspect` is descriptive and exposes deliverables, not
SBOMs as extra products. No tag-based cosign signature substitutes for qualified clearance. The full graph and
wrapper rationale are in [publication architecture](../publication-architecture.md).

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

A future design must use a strict versioned index, a separately allocated digest family, and explicit signer
policy. Each entry must reference exact named outputs, target platforms, authenticated publication contents and
clearance. Names, tags, paths and repository locations cannot establish identity; a byte-identical mirror
preserves the referenced identity. Deployment roles, categories, cluster context and qualification are outside
the current artifact model and must not be added to build or release identity merely to support discovery.

No endpoint adapter, registration schema, or registration authority is implemented. Any later implementation
requires its own admission, exact-byte persistence, authenticated endpoint response and operational evidence;
it cannot derive availability or qualification from a plan or caller-provided success flag.

## Consequences

**Positive**
- Operators fully control destinations; the same verified artifacts publish anywhere without changing their
  identity.
- Qualified publication is gated on verification and completeness. V1 uploads bytes before signing and
  pull-back verification; visibility alone cannot establish acceptance.
- Transports are standard and swappable, and content addressing with referrers keeps consumers on immutable
  digests.

**Negative / costs**
- The target config is an environment surface that must stay out of the build identity — a boundary reviewers
  must hold.
- Credential handling depends on the CI engine's secret mechanism, which is per-engine plumbing.

**Neutral**
- Publication identity and discovery are ADR-06; this ADR adds the *targets* and the *publish phase*, not new
  trust mechanisms.

## Alternatives considered

- **Declare publication destinations in producer instructions.** Rejected — perturbs `buildDigest` and couples a build to one
  environment; targets are transport, not definition.
- **NKD hosts a registry or release-stream service.** Rejected — deployment owners supply storage and discovery. The v1 NVIDIA workflow publishes bytes; qualified publication remains operator-owned.
- **Qualified publication before authorization.** Rejected — only verified, cleared contents may be published
  as a qualified release. V1's distinct ordering is stated in §6.

## Follow-ups

- **ADR-17:** ownership and capability admission for production publication adapters.
- **ADR-16:** the completeness/promotion gate the release publish is bound to.

## Acceptance evidence

The container-image delivery adapter has contract tests for exact OCI-export archive import, native image
manifest/config/layer closure, decompressed layer identities, platform selection, safe archive members and
unchanged image identities inside a publication root. Public OCI outputs lower to one built OCI-export
component, checked against its producer bytes before publication; consumer projection checks the native image
and its platform. The [combined hosted Run](../evidence/2026-09-10-combined-run.md) exercises CLI, CWE and
source Helm production, image-bound chart packaging, GHCR publication, real keyless signing and complete
finalization. This historical layout had three duplicated publications. Independent consumers authenticated all three; downloaded payload checks
exercise the native image, recovered CLI and rendered chart binding. Acquired-image layout conversion remains
unavailable. Source Helm archive selection and exact producer observation also have focused contract tests.
This adapter does not mint qualified execution or release authority.

Current contract evidence in `pkg/nkd/factory/*_test.go` and `pkg/nkd/delivery/*_test.go` covers target exclusion
from build identity, independently required output membership, exact evidence closure, semantic-versus-blob
digest separation, no implicit I/O, deterministic binary and
APT layouts, recursive OCI closure verification, extraction, and rejection of missing, extra, or substituted
bytes. The dated local NKE record in reconciliation establishes development registry interoperability for
earlier inputs, not authenticated release publication or production-registry acceptance.

The single-entry layout and stage-bound evidence require a new authorized hosted publication run; the earlier
NVIDIA run does not establish their external behavior.

Qualified end-to-end acceptance requires publishing one cleared mixed-artifact ReleaseSet to operator-selected targets,
discovering every referrer by immutable digest, authenticating the downstream clearance, and proving that
different destinations preserve every semantic and blob identity while omitted credentials never enter logs or
payload bytes.
