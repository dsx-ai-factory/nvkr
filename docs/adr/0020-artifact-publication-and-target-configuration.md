# ADR-20 — Artifact publication & target-registry configuration

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-20 in the consolidated NVKR ADR set (added to close the publish-targets gap).
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

Environment (`nvkr.dev/environment/v1`) owns runtime settings, one optional OCI publication namespace,
credential references and signing identity/issuer policy. `publication.repositoryPrefix` is an explicitly
qualified, untagged repository prefix; credentials, anonymous access and plain HTTP are transport settings.
When publication is configured, the release index and metadata publish under
`<repositoryPrefix>/releases`. Each deliverable and its SBOM attestation bundles publish in
`<repositoryPrefix>/<output-name>`. The signed
release metadata binds canonical sibling repository names and manifest digests; no complete closure is copied
into the product repositories.
Omitting publication produces a build-only Run. Destinations and a separate output-to-destination map are
not authored. The generated internal routes still bind exact package inputs and targets.
Each derived route owns exactly one publisher and its publication reference. When signing is configured,
its sign and verification operations bind that same route's package, reference and signature artifacts.
Another publisher or signer cannot acquire route authority merely by naming an existing target.

The publisher uploads each publication root by its content digest, without creating or advancing `latest`,
a fixed version tag, or a development tag. It returns the digest-pinned reference recorded in the Run.
Explicit low-level tagged publication remains a discovery operation; automatic version/channel aliases and
promotion policy are not implemented by this lifecycle configuration.

A Helm chart deliverable has a bounded discovery exception: after its manifest is durably published at its digest,
the publisher additionally advances two chart-scoped tags — the chart's own `<version>` (matching what real
`helm push` itself would tag) and a `<version>-<digest>` tag that is a deterministic, collision-free function of
the chart's exact published content. Both remain non-authoritative discovery hints under the same tag rule
stated above and in §6; no consumer, evidence binding, or release acceptance path treats either tag as
identity. These two automatic aliases are scoped to `helm-oci` outputs.

A second explicit exception is `publication.imageTags`, mapping an exact aggregate OCI output name to one
literal non-authoritative tag in that output's sibling repository. Admission rejects unknown, native-only or
non-image outputs before registry I/O. After digest publication, the publisher reads back and verifies the
entire release closure, including every sibling payload and required SBOM, before the first image alias update.
It then verifies each alias against the exact index bytes, media type and digest. Existing exact aliases are
checked without rewriting them. Failure can leave an alias advanced; no rollback, compare-and-swap or
multi-tag transaction is promised. Tags never replace immutable references, signed membership or acceptance.

Definition owns selected components, byte-changing packaging intent, and catalog tool selectors (`sbomTool`,
`cosignTool`). Catalog source declarations own release URL templates, executable
members, formats, platform and version constraints. The generated source lock owns the resolved tool URL,
archive digest and executable digest (ADR-22). Environment contains no executable pins. A dry-run signing
Environment does not acquire the selected cosign executable. Output-affecting tools remain bound to Build
identity; evidence tools affect catalog/plan/evidence identity without changing the logical source-build graph.


Environment may also map a canonical source URL to an HTTPS fetch mirror and a GitHub secret reference
through `sourceAccess`. This changes transport and Environment identity, not the canonical source, pinned
commit/tree, or build identity. The current adapter binds credentials to the matching GitHub fetch execution step and, for private
pin-set sources, the bootstrap regeneration and Run finalization steps that re-read pinned Git trees.
Production jobs require dispatch on the default branch or an exact `githubDispatchBranches` entry (ADR-18).
The separate development mode admits manual branch dispatch with the same fixed source-read steps.
Signed development uses the explicit isolated publication contract below; arbitrary job setup and variables
remain rejected. Branch code
participates in source retrieval and must be reviewed before using that credentialed mode. Git configuration
is transient and repository-scoped. Secret values never enter a remote URL,
serialized plan or transported source tree. Local and GitLab execution reject this configuration until those
adapters support equivalent credential isolation.

OCI is the current v1 publication transport. Executable, APT-repository and Helm outputs can share a recursive
OCI closure; an APT tree inside it is not an enabled direct APT-server publisher. Package repository and object
store publishers remain deferred and fail capability admission. Credential values never enter serialized inputs.
Transport settings stay replaceable by environment owners.

The pipeline-attested disk transport uses a distinct OCI manifest over the unchanged QCOW2 payload. Its one
`application/vnd.qemu.qcow2` layer binds the exact payload digest and size. The checked config declares the
guest platform; the layer media type and manifest annotations declare the QCOW2 format and encryption.
Production consumption requires the explicit encryption declaration to match the release binding: `none`, or
`luks2` only with signed `build-provenance-v1` evidence. Unknown, missing or substituted declarations fail closed;
the diagnostic reader keeps a missing encryption declaration unknown. Manifest identity and payload identity are separate.
An importing descriptor walk retains its verified disk owner through the same phase's payload copy instead
of reopening and hashing that inode again for selection. A standalone producer wrapper is selected and
verified in one payload pass. Every copy/export still rehashes its held bytes; separate hosted publication
and evidence boundaries authenticate their own closures and share one exact-output membership comparison.
The evidence-attestation handoff consumes the private preparation under §6, transferring unchanged blobs
without another payload copy. It still revalidates the complete source and final closures.
The disk publisher adapts the held QCOW2 directly to the registry image interface, without copying its bytes
into another local OCI layout. Exact bounded manifest/config bytes remain detached from mutable paths.
Every upload body independently reads and hashes the held inode, checks its exact size and unchanged
metadata, and remembers local integrity failures across transport retries. After the registry write, an
unconditional full rehash covers deduplication paths that skipped the body; immutable-reference readback
remains mandatory before release-root publication. Closing the owner cancels future reads, joins active
file reads and revokes body adapters. It issues no live producer receipt or stronger evidence claim.
Other native output kinds retain their existing private publication layout.
Remote acquisition may restart only a one-layer disk body interrupted by a typed HTTP/2 peer
`PROTOCOL_ERROR` reset. At most three attempts share the caller's deadline (or a thirty-minute default
when none exists), with one- and two-second cancellable waits. Each failed private layout must be removed
before another attempt starts, and the same immutable root and complete closure are reauthenticated.
Close, filesystem, integrity and other protocol errors do not authorize recovery. Destination commit and
post-commit inspection remain outside retry; no publication or signing operation is repeated.
The wrapper platform describes the guest artifact; inspection tooling, scanner pins and resource admission
select the native host profile from the frozen root producer's execution binding, including cross-guest TCG builds.
Strict inspection packaging consumes the original live Run's exact-subject reconciliation receipt.
The explicit narrower candidate `build-provenance-v1` mode instead consumes the original live held disk and authenticated
transitive material trace. Both paths rehash the exported QCOW2, use the same atomic package/evidence
transaction, preserve every producer evidence slot and bind raw payload separately from OCI identity.
The disk binding carries its descriptive evidence mode in signed release metadata. Document validators require
matching source-bound documents and reject invented inspection claims. Consumer acceptance is independently
bound by `VerificationPolicy.minimumDiskEvidence` in the policy digest: pipeline-attested policy defaults to
`build-provenance-v1`, while explicit `strict` requires inspected evidence. Qualified policy defaults to strict
and rejects producer-only acceptance. Neither path accepts a
restored pathname or decoded description as live producer authority. The compiler describes the wrapper's output-repository and evidence edges.
Receipt-gated packaging and consumer APIs are implemented. The explicit native ARC grouped executor consumes
the live inspection-and-packaging operation in strict mode; producer-provenance mode skips inspection and consumes
the original producer/material owners instead. Producer provenance is the M2 and M3 MVP disk acceptance scope;
full inspection and material reconciliation are deferred hardening (ADR-13 §6). Run outcomes and remaining operational evidence are tracked
in [reconciliation: Native ARC disk route](reconciliation.md#native-arc-disk-route). The ordinary local scheduler
still rejects disk publication.
Local admission rejects that unsupported publication graph before runtime, source or workspace work;
compiler-only and dry-run inspection remain available. Transport construction alone does not authenticate
composition or authorize release.

For artifact-publication consumption, the verified lifecycle owner may export
the complete held transport closure after matching its authenticated aggregate
invocation to the expected originating Run/attempt. Export requires that same owner's
opaque exact-input receipt binding the Definition, Environment and Plan expectation.
Export uses the existing descriptor-relative, fsynced no-replace transaction and never reopens producer
paths. Subsequent binary/chart/image extraction preserves their existing owning
checks; archive transport checksums remain separate from native OCI subjects.
This export grants no runtime verdict or stronger release/evidence lane. Contract
tests exercise origin mismatch, closure substitution, closed capabilities and
pre-start cancellation; externally exercised end-to-end consumption remains unrecorded.

An optional future S3 object and CDN URL must project those same payload bytes, not alternate Build identities.
The signed root must bind the payload digest, exact object route and authenticated upload/read-back result
before a consumer may accept that projection. Object-store schema, credentials, publication and consumption
remain deferred. Qualified disk reproduction and strict FDE publication remain capability-gated. An FDE payload
containing a private unlock credential cannot pass the existing no-secret publication rule. ADR-10's
public `changeit` bootstrap value has no confidentiality requirement. Its native ARC publication route requires
explicit `build-provenance-v1`, retains the exact producer encryption declaration through wrapper and signed
metadata, and makes no inspected-content, secure-deployment or confidentiality claim. The consumer's existing
minimum evidence policy still rejects producer-only evidence when strict is required; no separate confidentiality
policy is implied by accepting a disk. The separate raw diagnostic route remains build-only.

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
referrer. Each attachment binds both its NVKR semantic identity and its raw OCI blob digest and media type.
Publication must match that manifest exactly: zero, missing, extra, type-confused, or substituted referrers fail
before a push. Consumers select ReleaseSets by **immutable digest**, and a mutable tag cannot bypass
digest-plus-signature verification (SEC-13).

The clearance is the signed **root/commit object over that manifest**, not a member of its own referrer closure;
including its own blob or detached signature would be circular. It is published separately by its
`nvkr.clearance` identity with exact detached `RoleClearance` signature material, and it is the discovery and
commit point a downstream consumer authenticates before trusting the manifest. A status checkpoint targets the
ADR-06 `publicationIdentity`, so it is discoverable from this Publication Root; it MUST NOT be attached to
arbitrary component OCI subjects. Package and object releases carry their ValidationVector and evidence closure
through the object-store side of the same root protocol rather than silently discarding OCI-shaped referrer
inputs.

That non-circular root/discovery/signature protocol is mandatory for a qualified publication claim. The delivery
module implements its transport representation as an OCI image index with artifact type
`application/vnd.nvkr.publication-root.v4`: every child is an exact OCI manifest or nested index, and the canonical
root config binds each child's name, kind, artifact type, OCI descriptor digest, exact payload or root blob
digest, semantic digest, size, and optional platform. The registry tag is only a discovery hint; the publisher
returns, and the consumer requires, `repository@sha256:<root-index-digest>`. Full-layout pull materializes and verifies the
complete recursive closure before the destination becomes visible.

The config uses `nvkr.dev/publication-root/v4` and `application/vnd.nvkr.publication-root.config.v4+json`.
Its release metadata binds the exact Definition, Environment, compiled Plan and release-content identities.
Its output bindings carry Build Digest and exact named output membership; deployment cluster metadata is
removed. Distinct named single-platform native image outputs may reference the same exact manifest in
separate sibling repositories. Each keeps its own output, Build Subject and evidence bindings. Shared
physical descriptors require identical native media type, size, blob and semantic digests, and platform;
conflicting aliases, repeated logical names and duplicate direct index descriptors remain errors. Inspection
checks every logical output and evidence binding while accounting for the distinct physical descriptor
closure. This does not merge producer identities or grant publication or qualification authority.
Readers reject earlier metadata and removed fields. ValidationVector v3 and ReleaseSet/Inventory v4
remain implemented. The retired clearance and publication-manifest families remain reserved and nonminting;
retained transport media identifiers do not imply a current clearance issuer or verifier.

One metadata acquisition reuses its registry puller across the root and admitted sibling repositories.
Authentication negotiation is reused within each repository for that operation; the puller and its
authentication state are not retained for another acquisition. Every requested manifest, config and evidence blob retains
its exact descriptor, size, digest and complete metadata inspection checks. Session reuse neither caches
verified authority nor substitutes a previous logical member's checks. The caller's deadline and the
metadata acquisition bound still cover the complete operation.

Loose executables use `application/vnd.nvkr.binary-bundle.v1`. The Binary Bundle's canonical config binds OS,
architecture, purpose, optional ReleaseSet and Inventory identities, and every file's name, absolute install
suggestion, mode, ordinary SHA-256, and size. Its one deterministic
`application/vnd.nvkr.binary-bundle.layer.v1.tar+gzip` layer has an OCI title, so a general-purpose client such as
ORAS can download it and ordinary `tar` can unpack it. Debian packages may also be projected into a deterministic
Acquire-By-Hash repository; that APT Projection is not a new release artifact, and an unsigned `Release` file is
development-only.

Diagnostic `release pull --output <name> --out <directory>` obtains one named output directly from the pinned
registry release root. It checks the complete bounded release metadata/evidence closure before format admission,
then downloads only the selected output's complete payload closure. Delivery checks exact descriptors and
format-specific payload membership before exposing the new destination. The supported projections are:

| Output format | Projection directly beneath the destination |
|---|---|
| `executable` | Exact regular bundle files by recorded name, SHA-256, size and mode; absolute installation suggestions are not followed |
| `disk-image` | `<output-name>.qcow2` containing the exact plain or declared LUKS2-encrypted QCOW2 payload bytes; no decryption |
| `helm-oci` | `<output-name>.tgz` containing the unchanged compressed chart archive |
| `oci` | A standard OCI layout retaining exact native OCI or Docker schema-2 manifests, configs and layers; an aggregate OCI index includes every declared native platform and its full payload closure |
| `apt-repository` | The complete checked repository tree of regular files and owner-readable, searchable directories; every file, including indexes, is digest-bound to the archive, and links and special files are rejected |

The destination parent must already exist, and existing destinations are rejected.
Failure or cancellation before commit exposes no destination. A failed parent-directory sync after commit
reports failure even though the complete destination may exist. Delivery owns admission, metadata and payload
checks, destination publication and private temporary-data cleanup; lifecycle bounds the overall deadline.
Unknown selections fail with a coded usage error; malformed or unsupported format metadata fails with a coded
error before payload or destination work. The command does not accept a local layout, infer a host platform,
select only one aggregate platform, install or execute downloaded files, or load images into a runtime. It runs on macOS and
Linux and checks byte integrity only: signatures and consumer policy remain unverified, no verification receipt
is issued, and no accepted Release or stronger evidence lane is implied. Explicit `artifact-publication`
verification retains its complete payload and policy checks; `authenticity` retains its signature/evidence-only scope.

ADR-16's hosted artifact-check path uses a narrower delivery owner: it verifies the complete publication one
output at a time and releases non-executable payload scratch after each output's format, descriptor and
content checks. All named outputs and every declared native image child remain mandatory. The owner retains
checked control documents, evidence and exact executable bundle bytes for subsequent qualification and smoke work;
it exposes no layout or disk export for discarded payloads. Late failure or cancellation prevents issuance
and closes retained bytes. This is a retention distinction, not metadata-only verification or a new evidence
lane. The ordinary held `Publication` and verified Release export paths keep their complete payload owners.
Registry image-index import copies the already verified held closure directly into the caller's private stage,
without an intermediate layout or second complete snapshot. The import borrows the owner through completion,
preserves exact index and child bytes, and retains complete publication binding and payload checks.

The public CLI constructor emits only a visibly `diagnostic` root and requires `--allow-diagnostic` before push.
It cannot set publication identity, publication-manifest digest, or clearance digest. Qualified publishing is
not implemented. The unused qualified constructor, legacy filesystem publication adapters, clearance authentication and
release-only receipt implementation are removed. A future qualified publisher must consume
authenticated clearance, match its exact ReleaseSet, Inventory, publication manifest, payloads, referrers and
detached proof bytes, and implement the complete root commit protocol before it may emit a qualified root.
Full publication inspection verifies structural and content closure but does not authenticate a claimed qualified
root. Qualified consumption requires an implemented clearance verifier and the operator's trust policy;
no current diagnostic operation grants that authority.

The diagnostic `publish chart` command verifies a pinned local Helm OCI layout
and extracts its unchanged archive to a new local path. Its descriptive result
grants no Release authority; it uploads nothing and is not remote publication or
a production signing/verification gate.

### Runnable image indexes — pipeline-attested lifecycle integration

The delivery module can assemble and open a standard runnable OCI image index over an explicit, nonempty,
unique set drawn from `linux/amd64` and `linux/arm64`. Definition v5 admits an OCI output with that explicit
`platforms` set instead of `platform`, selecting one producer independently on every required platform.
Compilation freezes all native Build Digests, results and platform-scoped evidence before index assembly.
The aggregate has its own typed logical Build Digest; it does not change native producer identity. Lifecycle
production, index-subject signing/attestation, publication and consumption are implemented and contract-tested
for the pipeline-attested lane. This does not establish a dual-native hosted release or qualified acceptance.

Each assembly input names one platform, one single-manifest OCI layout and its exact native manifest digest.
The complete request is admitted before filesystem access. Delivery captures bounded regular files through
held directory descriptors into anonymous immutable capabilities, then verifies every manifest, config,
compressed layer and expanded layer identity. Native config platform must equal its requested platform;
platform metadata on input descriptors, when present, must agree. An opened aggregate requires platform
metadata on every child. The shared native-image verifier admits baseline AMD64 (omitted variant) and
baseline ARM64 (omitted variant or `v8`) for single-image inspection/import as well as aggregate children.
This intentional native-admission restriction rejects AMD64 `v3` with `CodeVerifyFailed`; it does not
rewrite image bytes or declare that variant invalid OCI. `TestContainerImageNativeVariantAdmission`
records the standalone contract. Other variants,
unknown structural index/manifest fields, duplicate JSON keys, nested indexes, artifact/attestation manifests,
subject-bearing manifests, missing or repeated platforms/descriptors, unreachable blobs, wrong sizes,
symlinks and nonregular files fail closed. Standard image-config metadata is retained without rewriting it.

Assembly emits canonical index bytes with one descriptor per platform in sorted order. The index and native
child manifest digests are exact OCI blob identities, not logical Build Digests. Child manifest/config/layer
bytes remain unchanged; identical blobs shared by children are stored once. A held `ImageIndex` exposes
only defensive descriptions and an atomic no-replace layout writer; caller paths and descriptions cannot
substitute another child after validation. Closing joins active operations and releases the held files; shallow handle copies share synchronization and revocation.
The writer requires an existing parent directory, writes and syncs a private sibling through directory
descriptors, and atomically commits without replacing an existing destination, then syncs the parent.
Cancellation before commit removes the private stage. A directory-sync failure after commit is reported as
failure even though a complete layout may exist; callers must inspect that result before retrying.

Release v4 and PublicationRoot v4 bind aggregate identity and exact platform/child Build, payload and evidence
membership. Every child retains required native-image SBOM coverage; equal Build Digests do not substitute
for another named output/platform use. Consumer validation checks the complete membership before exposing
it, and release navigation describes the index and each child without claiming signature authority. Helm
bindings require their chart platform to belong to the index's declared and observed coverage and embed the
complete index digest. No post-build manifest rewrite or `platform: multi` is admitted. Explicit discovery
aliases follow the post-readback rule in section 2. M2 ARC/per-output metadata-only publication composition
and a dual-native hosted build/sign/publication remain separate, unexercised integration work. See
[multi-architecture CWE delivery](../multiarch-cwe-delivery.md).

### 5. Credentials by reference, never embedded

The target config names a registry or repository plus a **credential reference** resolved from the CI engine's
secret mechanism, which is transport. **No secret** enters any artifact, manifest, SBOM, log, or the config
itself (SEC-09); signatures remain detached (SEC-03).

Publication verification admits a declared credential before registry resolution or pull. A missing username
or secret returns `NVKR-E-TRUST-POLICY-MISSING`, without consulting ambient Docker credentials or writing a
verification record. Resolution and closure retrieval use the same admitted credential values.

### 6. Pipeline-attested publication

NVIDIA's hosted workflow publishes signed artifact bytes to its configured OCI registry. ADR-04 owns keyless
signature/attestation storage and ADR-16 owns evidence tiers and full release acceptance. The compiler generates
one release index from named outputs; normal authors do not wire payload → root → route.

GitHub-hosted GHCR publication admits the exact destination packages before payload upload. The target
package and all output/evidence sibling packages must have API-confirmed private/internal visibility and
no repository linkage, using the publication credential independently of CI carrier storage.
NVKR-produced Helm manifests retain source URLs in the chart archive and Helm config, but do not
promote them to `org.opencontainers.image.source`: GHCR can use that annotation to create repository
linkage after admission. This deterministic packaging rule applies across execution targets; publication
never rewrites an already identified artifact to remove the annotation.
The source repository metadata lookup uses the job's automatic repository token, passed separately only to
GHCR publication jobs. Package-scoped publication credentials therefore need no source-repository scope
for this admission check. The repository token does not replace the publication credential for package
metadata or registry operations and is not added to producer execution.
Repeated metadata checks may use ADR-18's bounded operation-local authenticated ETag revalidation; no
admission check is skipped, and errors cannot reuse an earlier successful policy response.
Public, unknown or inaccessible policy fails closed. A missing package can receive only a metadata-only
seed before admission. The existing metadata inspector derives sibling references without opening product
payloads; the actual push must retain that inspected root digest. Hosted GHCR layouts outside this metadata
contract are rejected rather than falling back to unchecked publication. Local and other-registry transport
contracts remain unchanged. These checks cannot lock future provider visibility or establish independent
package grants; public-source access configuration and hosted execution remain separate operational evidence.

The signed metadata references each deliverable manifest once in its own sibling repository: binary bundle,
unchanged native container image, Helm artifact or exact-QCOW2 wrapper. The OCI index directly references only its metadata manifest. Helm has one artifact manifest, its real Chart.yaml-derived config, and one chart-archive layer — a
standards-conformant Helm OCI chart, readable by `helm pull`/`helm show`/`oras pull` without an NVKR-specific
decoder — and its former nested publication root is removed. The native image is pullable from its own output repository by its manifest
digest, without the release index. Local OCI-layout index files are handoff scaffolding, not registry wrappers.

Full-layout SBOM attestation consumes one disposable private preparation into a new final layout. The caller transfers
exclusive ownership with no concurrent users or writers; source and destination must share a filesystem.
Admission rejects overlapping paths, unexpected layout entries, symlinks within the source tree and
hardlinked files before signing. The inventory has the existing bounded OCI blob count.
Delivery prepares and checks the signed bundles and new metadata before claiming the source directory,
and checks its original file inventory before and after ownership transfer. Claiming atomically moves that
exact source directory into private staging without replacement. Only unchanged blobs reachable from the
final closure are moved into the final stage; old preparation metadata and raw SBOM objects are discarded.
The complete final closure is inspected before atomic no-replace publication. Payload bytes and their
descriptors remain unchanged, while signed
bundles and publication metadata derive the final root identity. No hardlink connects a retained source
layout to the final layout.

Before the source is claimed, failure preserves it. After the claim, delivery owns cleanup of the private
trees on returned failure; retry requires restoring or rebuilding the preparation. Interruption can leave
private staging but cannot publish a partially attested final layout. This is disposable workflow ownership,
not a durable resume protocol or authority recovered from a pathname. Callers must stop using the consumed
preparation; the handoff does not revoke independently held writable descriptors. Every later publication
or consumption boundary still authenticates its exact bytes. SBOM attestation and registry credentials
remain on generic hosted runners, outside the ARC producer boundary.

The explicit GitHub-only `publication.mode: per-output-v1` uses one output-local publication route per
required output, retaining the full compiled Plan. Each route observes its exact payload and producer records,
attests its required SBOMs, publishes the output and evidence, then signs and verifies its own metadata root
at `<prefix>/releases-<output>`. These roots are internal publication handoffs, not additional product outputs.
Their signed metadata binds the exact Plan digest and workflow invocation, including run attempt.
Partial retries may reuse an authenticated output-local root from an earlier attempt of the same GitHub
run. The server, repository, run ID and workflow builder must match exactly; canonical positive attempt
numbers must satisfy preparation ≤ signing ≤ the consuming attempt. Different runs, future attempts,
changed Plans or missing evidence fail closed. Signing may retry against an earlier prepared root without
rewriting its metadata or original predicate. This exception applies to pipeline-attested metadata
assembly/finalization and ADR-16's independent artifact checks; it does not relax qualified execution or live
disk ownership. Qualification authenticates the successful completion attempt separately and preserves each
output's original preparation and signing identities rather than requiring every output to be rebuilt.
Output-local chart reproduction transports its bound image packages and their producer/evidence
closure. Those packages are validation dependencies; only the selected chart enters that route's
published output membership.

Per-output GitHub handoffs move only the bytes required by each role. The preparation producer retains
complete-byte inspection and exports a bounded control/evidence layout with the same root digest. Its
payload carrier goes directly to the publisher; the isolated OIDC SBOM job receives only those controls
and canonical documents, and returns only attested controls and bundles. Before any publication side
effect, the publisher re-inspects the original complete preparation and requires the returned metadata to
preserve every output descriptor, repository, payload digest/size, Build Subject and release identity.
Only raw CycloneDX evidence may become a verified bundle containing the identical decoded document.
The publisher verifies those bundles under the admitted signer policy, consumes unchanged prepared blobs
through the exclusive rename mechanism, and inspects the complete final layout before committing it.
There is no payload download or re-upload in the SBOM job, no hardlinked retained preparation, and no
caller-supplied verification flag.

After the existing full-byte/privacy/push gate, the publisher exports separate bounded signing metadata
whose root must equal the digest-pinned publication reference before the metadata is committed. This is a
pre-sign description, distinct from the remote verifier's authenticated output handoff. The signer checks
its exact route, named output, Plan, Definition, Environment and invocation bindings, re-derives all signing
subjects, and verifies required SBOM bundles before signing. The subsequent independent remote verifier
still retrieves and authenticates the complete served payload closure. Signing metadata cannot issue a
full-payload receipt, bypass remote readback, or authorize finalization by itself. Legacy and aggregate
routes retain their existing contracts. This reduces repeated transport within the pipeline-attested lane;
it does not establish qualified execution or a stronger SLSA level.

Release assembly authenticates every output-local root and its exact production predicate under the configured
workflow identity/issuer, verifies its SBOM bundles, and joins its named output to the independently frozen
requirements. It merges shared evidence only when document, subject and stage bindings agree, choosing the
first sorted output's existing bundle and repository. It publishes a single aggregate root at `<prefix>/releases`.
The aggregate identifies its current assembly invocation. Each output's `publication` binding preserves
the digest-pinned output-local root reference and its original preparation and signing invocation IDs.
These descriptions acquire authority only through the authenticated release; assembly derives them from
the verified output receipt. A finalizer retry may authenticate an unchanged aggregate from an earlier
attempt of the same run under the same ordering and Plan checks.
Assembly, aggregate signing/verification and Run finalization use bounded manifests, configs, evidence bundles
and digest references; they never retrieve output payload layers. Metadata inspection issues no full-payload
verification capability. It admits at most 512 MiB of control/evidence blobs per root and rejects extra blobs.
Each output route still verifies its full payload closure. The independent Release consumer's
`artifact-publication` scope retrieves and verifies full payloads through its existing held owner.

In this mode finalization requires every provider job to succeed, then reauthenticates the aggregate root,
exact Plan/invocation-bound production predicate, output signatures/provenance and SBOM bundles. Persisted
success records and unverified metadata cannot authorize a complete Run. This is the pipeline-attested lane:
it trusts the admitted producing workflow's output-local byte checks, not qualified reproduction. Registry
read access suffices for assembly and finalization; neither receives OIDC minting or package-write permission.
Production GitHub per-output publication additionally admits the fixed `github:NVKR_REGISTRY_WRITE`
policy. Publishers and signers receive that secret and its explicit account; verification, assembly and
finalization receive `NVKR_REGISTRY_READ` and its explicit account. These jobs request no workflow-token
package permissions. Maintained production and nightly inputs use this policy. Legacy anonymous and
job-scoped `github:GITHUB_TOKEN` configurations remain supported but are unsuitable for private binaries
in a future-public source repository; arbitrary static credentials remain rejected. Generated production
qualification requires the fixed production writer policy on both the publication and every target, matching
its emitted credentials and permissions; legacy workflow-token producer profiles cannot generate that pair.
The explicit signed-development contract below instead requires separately scoped read/write
credentials. Legacy publication without this mode retains its full-closure assembly/finalization path. Local
execution and dry-run signing do not admit the new mode. Interrupted production runs can leave independently
published outputs; no production cross-run reuse, rollback, retention protocol or atomic multi-repository
transaction is added.

One canonical release metadata config, carried by one manifest because an OCI index has no config, owns
the Definition, Environment and compiled Plan identities, output names, sibling repository locators, descriptor
identities, output-to-Build-Digest/component mappings, content subject and
required evidence membership. There is no separately published lifecycle-binding object. Output identity is
the deliverable manifest digest (APT retains its typed repository identity); input layout-index hashes are not
Artifact identities. The qualified representation in §4 remains a separate protocol, not a compatibility path
for former pipeline publications. The current consumer rejects pipeline roots without this metadata.

The delivery module owns the one canonical release-content projection and its digest for both producer and
consumer. It contains sorted exact output names, kinds, artifact digests, Build Digests and components; repository
references are excluded. This ownership consolidation preserves existing canonical content bytes. The compiler
still joins independently frozen output requirements to observed payloads before publication. The consumer does
not reconstruct compiler records or derive both expectations and observations from release metadata.

Consumer `authenticity` scope uses the bounded metadata/evidence reader, then authenticates the root and
every required named output's signature and subject-bound production provenance under the external policy.
It independently verifies every required SBOM bundle and enforces exact membership, subject/stage bindings
and the policy's minimum disk evidence. Aggregate images retain native child manifests/configurations and
platform-scoped evidence. It retrieves no artifact payload layers and issues only ADR-17's distinct
authentication capability, which cannot authorize export or exact-input matching. A signed disk inventory
authenticates the producer's inspection claim; this scope performs no consumer-side disk inspection.

Ordinary full-publication consumer integrity checking opens an opaque delivery publication with an explicit
lifetime. It snapshots the index and blobs into bounded anonymous retained files, verifies every reachable descriptor and evidence slot,
and checks format-specific payload membership against that exact closure. Private extraction workspaces are
removed before the capability is returned. Later source-path replacement cannot change the checked output
projection, and defensive copies cannot mutate the held view; closing releases the file capabilities and rejects
further projections. Signature and policy verification remain in the lifecycle consumer. This capability proves
integrity and membership only, never signer authority by itself. Path-based diagnostic operations retain their
independent revalidation at each call.

This full-publication owner's large QCOW2 payloads use streamed anonymous filesystem snapshots rather than
payload-sized RAM files.
The consumer snapshot root uses the process `TMPDIR` (the platform temporary directory when unset),
including when registry acquisition has a separate workspace. Before payload copies, the consumer admits
aggregate unique QCOW2 bytes against writable free space and rejects detected tmpfs/ramfs storage with
`CodeEngineCapability`; the error identifies the scratch location and asks for a disk-backed `TMPDIR`.
This is point-in-time capacity, not a reservation or proof of physical backing. Linux overlayfs reports
capacity for its writable layer but does not identify that layer's backing mount, so a non-memory-backed
statfs result cannot certify physical disk storage. General VM ephemeral scratch may be memory-backed;
its separate execution admission uses the same allocation facts without this consumer snapshot policy.
Payload export checks the exact held digest and size again, and the original publication owner controls the
snapshot lifetime. These transport capabilities cannot restore the live reconciliation receipt used by
production packaging.

Registry and local release navigation use the bounded metadata reader. It validates exact metadata digests,
schemas and memberships across the complete release metadata and evidence closure, without downloading or
reading artifact payload layers. Its detached description joins exact sibling output and evidence references,
all output memberships, Build/component subjects and evidence stage/subject bindings. Locators are explicitly
labeled as derived from the supplied root reference; the detached view does not establish registry availability.
Registry acquisition fetches each sibling manifest, config and bounded evidence document. For aggregate
images it also fetches the exact finite native child manifests/configs bound by the index and release metadata,
never their layers. Child platform, digest and size substitutions fail before a metadata view is returned. Local inspection
accepts complete or metadata-only layouts and does not probe the supplied registry namespace. Executable,
image and disk platform labels describe checked config metadata; contradictory metadata labels are rejected.
Text, readable JSON and Mermaid explicitly label metadata inspection, leave payloads, signatures and consumer
policy unverified, and report qualification as a declaration. JSON escapes nonprinting Unicode without
changing decoded string values, and empty build-subject collections are arrays. Output filtering occurs only
after the whole metadata closure is checked. The diagram presents published relationships, not an inferred
build graph. Navigation adds no canonical identity or publication authority and leaves explicit
artifact-publication verification's full payload and policy checks unchanged. Loopback-registry, admission, cancellation and zero
payload-layer download contracts include disk releases; the [historical v3 metadata browsing validation record](https://github.com/dsx-ai-factory/nvkr/blob/a00a6b11ddf711268bc10a7ea2baa33f839ccec9/docs/evidence/2026-10-06-release-browse-metadata.md)
separately records the earlier external inspection scope. Current readers require publication-root v4 and
reject that v3 release; current-schema external acceptance is not established by the dated record.

Artifact descriptions expose checked OS/architecture and Helm chart version in both bounded metadata and
held full-payload projections. Chart version is read from the exact Helm config, separately from appVersion;
full inspection also compares config metadata with the archive's Chart.yaml. Native image indexes retain
their explicit child platform membership rather than a false single-platform label. Legacy chart outputs
without a declared platform remain unlabeled. These descriptive additions do not change canonical release
content bytes or substitute for the existing typed identities.

Release description export projects the complete metadata closure into `nvkr.dev/release-description/v1`.
It requires only the immutable publication reference as metadata input; an optional cached layout must match
that exact root. No separate Plan, mapping, platform selection, producer checkout or unpublished workspace is
accepted. Export and unfiltered JSON browsing share one explicit versioned wire description, backed by the
same delivery-owned metadata reader. The schema does not directly serialize internal authority-bearing
types or add a public lifecycle object, canonical identity or publication format.

Every named output is retained, without a consumer-role dictionary or producer allowlist. Native artifacts
keep their config-declared OS/architecture; aggregate images keep the published index digest and exact child
platform membership. Helm chart version remains distinct from appVersion and says nothing about workload
compatibility. Source repository/commit descriptions remain attached to their exact evidence/output/component
bindings. There is no release-wide source pin, inferred Git tag or rule requiring all uses of one repository
to share a revision. Missing or redacted source properties stay explicit descriptions, not invented pins.

Metadata inspection scope and the publisher's declared qualification are separate fields. Export reads no
artifact payload layers and authenticates no signatures or consumer policy. Its JSON cannot restore a held
capability or become a verification receipt. Consumer-owned code selects deployment roles, target platforms,
compatible source revisions and complete offline dependencies, then authenticates accepted artifacts under
its policy. Full release verification retains its existing payload and policy checks. Unpublished packages
remain Run artifacts and are not converted into a publication by this export interface.

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
preparation evidence. Legacy finalization regenerates canonical documents from raw handoffs. Per-output finalization authenticates
the same exact document bindings through the current Run's signed metadata and independently verifies their bundles.

For disk-root and material inventories, document byte integrity is checked before provenance properties are
read. Missing provenance is reported as `CodeProvenanceMissing` before the semantic identity and exact-subject
gate; if both defects exist, that first error does not imply the subject is valid. A provenance-complete document
with a substituted semantic identity or subject fails with `CodeVerifyFailed`. Both gates remain mandatory.

Signatures, SLSA provenance and SBOMs use the same standard Sigstore v0.3 bundle format as OCI referrers
(ADR-04); their predicate types remain distinct. One root signature covers evidence and output repository/digest bindings through metadata; one SLSA attestation targets that
same root. Signature/attestation attachments stay outside the signed index to avoid circular identity. Each deliverable manifest also receives its own signature and subject-bound production attestation in its
own repository for independent consumption. Signing processes deliverables before the root; hosted verification
requires every child signature and attestation before recording success. The Release consumer still authenticates
membership and SBOM coverage through the root. Standalone verification does not establish that release-wide claim. Run completion is a separate descriptive
record, not signed merely because its referenced bytes are signed. Production consumers resolve only sibling repositories within the chosen release namespace and fetch every pinned manifest.
Development consumers additionally admit the same-run earlier-attempt origins defined in the development
publication section below, preserving each output's original namespace.
Artifact-publication acceptance additionally verifies the full payload closure; authenticity explicitly leaves
payload integrity unchecked. Metadata cannot redirect credentials
to another host or namespace. The publisher validates local bytes first, uploads and checks deliverables in their
repositories, then publishes the release index last; output-local and legacy hosted verification pull the full distributed closure.
Per-output aggregate verification fetches only control documents and evidence. Plain `release inspect` is descriptive and exposes deliverables, not
SBOMs as extra products. No tag-based cosign signature substitutes for qualified clearance. The full graph and
wrapper rationale are in [publication architecture](../publication-architecture.md).

A Helm output accepts one observed source archive component carrying exactly one top-level chart directory —
its name discovered from the archive itself rather than assumed, the same shape a real `helm package` .tgz has
— extracted through the bounded transport adapter and repacked deterministically, re-rooted onto the chart's
own declared name exactly as `helm package` would produce it. Lifecycle observation checks the exact archive
against its producer result and independently reproduces the publication payload. Optional Definition Helm
settings contain only a named relationship map: `helm.images: {controller: cwe}` binds a chart slot to an OCI
output on the same platform, or an aggregate output containing that platform. The selected source producer owns `helm.chart` (`.` for the archive root or a
direct child directory) and each slot's `values.digest` path in `values.yaml`. Compilation joins the relationships with that output's resolved catalog,
including catalog overrides. Every catalog slot must be bound exactly once; missing mappings, unbound slots
and unknown slots fail. Mappings participate in catalog identity and are frozen in the plan, whose validation
re-derives the package bindings. The compiler adds each distinct image package as an explicit dependency and
checks exact typed source/image membership at execution. `imageOutput` is replaced by this relationship map;
old Definitions and generated plans must be migrated/regenerated.

Helm values and catalog pin sets share bounded dotted-field/literal `[key=value]` traversal in
`internal/documentpath`. Pin sets require complete selected-list membership; Helm mappings select explicit
values while permitting unrelated chart settings. All digest targets must exist as distinct string scalars.
A catalog slot's optional `values.digestFormat: sha256-hex` writes the validated SHA-256 hex
without its algorithm prefix for source templates that already supply `sha256:`. Omission
writes the full OCI digest. Unknown formats fail catalog/Plan admission; the format is catalog
identity and frozen package dataflow, not inferred from a value path or template. No template
rewriting or new digest algorithm is introduced.

Packaging inspects each native image or aggregate index package, writes its manifest or index digest to the mapped values and refreshes
wrapper-level checksums when the chart is nested. With `chart: "."`, no bookkeeping is written
inside the chart; any upstream `checksums.txt` remains chart content. Templates, repository and tag defaults remain unchanged. Source links and ambiguous value
selections fail. Values serialization preserves semantic values, not exact formatting; document-level
comments separated from the root mapping may be omitted. Template bytes remain unchanged. No Environment publication prefix enters chart bytes. An operator supplies a repository
containing the same image through deployment values. The source chart must already consume its digest
value; NVKR does not infer or rewrite template behavior. Normal Helm overrides remain possible, including
changing or clearing the digest. Deployment-value enforcement is outside this packaging contract.
Acquired charts remain immutable and cannot use these bindings.

Contract tests cover multiple slots, admission, unchanged templates and identical package bytes across
Environment repository choices. Opt-in real Helm tests render the pinned NVKE controller chart for two
repositories using the packaged digest value. This does not establish installation, signatures, runtime
availability or a new full CWE-plus-charts build. See the [binding implementation guide](../declarative-helm-bindings.md).

A Helm output may instead select one acquired `raw` chart package. The compiler requires an acquisition
producer and transports its result explicitly. Packaging checks the result and a bounded, no-follow held
snapshot against the pinned pull digest, validates the chart archive, and wraps the exact compressed bytes
as the OCI chart layer. The layer digest therefore remains the upstream package pin; the enclosing manifest
has its own digest. Acquired charts cannot be repacked or image-bound. Lifecycle observation independently
reproduces the wrapper and checks its manifest. This path is exercised by a
[local four-chart NVKE Run](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/producer-acquisition-observations.md#acquired-helm-charts); it establishes neither upstream
signature verification nor qualification. Dependency scanning of acquired `raw` charts remains unsupported.

Push, sign and pull-back verification are separate effects: later failure may leave bytes or partial evidence in
the registry. No atomic remote rollback or cross-run resume is claimed. A complete local Run does not establish
the deferred qualification/manifest acceptance in ADR-16. Retention and recovery require deployment ownership.

### Disposable signed development publications

The explicit GitHub development Environment can exercise the complete pipeline-attested publication graph,
including the admitted native ARC disks. It requires `per-output-v1`, real cosign signing and the GitHub OIDC
issuer. Its namespace is exactly `ghcr.io/<owner>/<repo>/development`, bound to that source repository's
`.github/workflows/nvkr-dev.yml@refs/heads/…` certificate identities with an anchored prescribed regexp.
Production workflow identities, arbitrary registry hosts/prefixes, anonymous/plain-HTTP access, mutable
image-tag configuration and arbitrary credential references fail admission before work. This is a distinct
trust policy within the existing evidence lane, not a new evidence tier or production authorization.

`pkg/nvkr/development` owns the canonical namespace grammar. The factory owner derives each actual target
as `<prefix>/runs/<run-id>/attempts/<attempt>/<repository-leaf>` from the admitted hosted invocation,
revalidating source repository, dev workflow identity and canonical positive run/attempt IDs. No GitHub
expression is interpolated into the committed Plan. The Plan binds the policy and route leaves; the runtime
projection changes only transport location. Every publication, signing and verification boundary checks the
same namespace grammar; metadata authentication admits only current or earlier attempts of the same run. Existing subject,
Plan, invocation, SBOM and signature checks remain mandatory. Native output digests and logical Build identity
are unchanged. Helm aliases remain scoped inside that attempt's output repository.

Development and production support failed-job and full-workflow retries within the same immutable GitHub
run. Retained work keeps its original source request, publication location, preparation and signing
invocations. New publication work uses the current attempt's namespace; signing and verification can
consume earlier immutable references after exact Plan, named-output and evidence checks. Assembly may
join outputs from different attempts of that run and records each original origin. Other runs, future
attempts, changed inputs and incomplete evidence fail closed. Qualification reauthenticates the full
closure against the successful completion attempt; it never treats a prior success flag as authority.
Payload and evidence locators are derived from each recorded publication origin's namespace, even when
the aggregate root is published in a later attempt. Delivery admits that change of namespace only for
canonical non-future attempts of the same development run; ordinary production references remain siblings.
Each attempt's packages remain independently discoverable for cleanup. Expired or deleted inputs require
rebuilding the affected dependency closure; a fresh dispatch does not implicitly reuse another run.

The fixed `github:NVKR_DEV_REGISTRY_WRITE` reference selects only the dedicated development publisher.
Generated publishing/signing jobs use that secret and `NVKR_DEV_REGISTRY_WRITE_USER`; verification,
assembly and finalization instead receive `NVKR_DEV_REGISTRY_READ` and `NVKR_DEV_REGISTRY_READ_USER`.
The usernames are provider variables. No development job gets repository-token package-write permission.
The operator must configure token scopes and package ACLs so these development principals cannot modify
production packages. The same development account may hold separate write, read-only and cleanup tokens;
token scope still differs by phase. Provider ACLs are external prerequisites, never inferred from a prefix.
Only artifact signing and SBOM attestation receive OIDC. The distinct default-branch cleanup environment
holds the cleanup token; branch execution receives no deletion credential.

Development qualification may attach its distinct predicate to existing immutable output subjects in the
selected successful source run's original publication namespaces. It never rebuilds or republishes their payloads, changes
aliases, or writes production qualification predicates. The final qualifier uses the dedicated development
writer; native smoke uses the reader. Original producer signatures and complete artifact verification are
required before qualification signing. Attachments are disposable with their subject packages. Qualification
does not reserve a namespace against cleanup: run while source packages and provider metadata remain
available. Concurrent cleanup or expired inputs cause failure, not a bypass or a rebuild.

The development cleanup owner supports read-only preview by default and explicit apply. It inventories
internal GHCR container packages under only the exact reserved namespace, authenticates account/package identity
and the matching `nvkr-dev.yml` manual-dispatch attempt, and requires completion plus at least 24 hours
of age for that attempt, the latest attempt of the same run, its package and every version. Source-repository linkage on package metadata is
not proof of ownership and is not required: preserved native images can carry upstream annotations.
Private, public, missing/empty and unknown visibility are rejected during inventory and again immediately before deletion.
Internal visibility permits enterprise-wide reading; deletion still requires package-admin authority.
The reserved namespace policy plus authenticated provider account, package and attempt checks determine
cleanup scope. Ambiguous names, malformed/unknown run records, incomplete inventories or API errors fail
closed. An active or young latest attempt retains all original packages of that run. All candidate admission precedes deletion; each candidate's
package/version metadata and run eligibility are rechecked immediately before deleting that whole package.

The handwritten repository cleanup workflow uses only default-branch code, a separate protected
`nvkr-dev-cleanup` Environment and dedicated `NVKR_DEV_REGISTRY_CLEANUP` credential. Its daily schedule
applies cleanup; manual dispatch defaults to preview. It never executes producer artifacts or feature-branch
code. Whole-package deletion removes its versions and referrers without guessing reachability from tags.
A report records candidates and API-confirmed deletions even on partial failure. Mutation is not retried
after an ambiguous outcome. Calls, response sizes and pagination are bounded and cancellation-aware.

The 24-hour age is eligibility, not an exact expiry SLA. GitHub's deletion/restoration and physical garbage
collection remain provider behavior; Sigstore transparency records remain external and are not erased.
Production packages, old shared prefixes and arbitrary registries are outside this protocol. The protocol
assumes deleted attempt namespaces are never repopulated by an external writer. Retry publication writes
new namespaces while signatures may attach to retained earlier subjects. Cleanup cannot reserve packages
against a retry starting after its final provider check; a missing subject fails closed. Provider ACL enforcement and successful real deletion require
separate external evidence.

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
- **NVKR hosts a registry or release-stream service.** Rejected — deployment owners supply storage and discovery. The v1 NVIDIA workflow publishes bytes; qualified publication remains operator-owned.
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
and its platform. The [combined hosted Run](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-10-combined-run.md) exercises CLI, CWE and
source Helm production, image-bound chart packaging, GHCR publication, real keyless signing and complete
finalization. This historical layout had three duplicated publications. Independent consumers authenticated all three; downloaded payload checks
exercise the native image, recovered CLI and rendered chart binding. Acquired native OCI and Docker-schema-2
images have contract tests for exact producer-result edges, platform, full blob closure, byte-preserving import,
offline scanner transport and unchanged upstream manifest subjects. Only the local layout index is normalized
as transport scaffolding; upstream manifest, config and layer bytes are unchanged. A native Docker manifest is
not relabeled as an OCI manifest. These tests do not prove hosted acquisition, scanner execution, mirroring,
signing or independent registry consumption of the new acquired-image outputs. Source Helm archive selection
and exact producer observation also have focused contract tests.
This adapter does not mint qualified execution or release authority.

Current contract evidence in `pkg/nvkr/factory/*_test.go` and `pkg/nvkr/delivery/*_test.go` covers target exclusion
from build identity, independently required output membership, exact evidence closure, semantic-versus-blob
digest separation, no implicit I/O, deterministic binary and
APT layouts, recursive OCI closure verification, extraction, and rejection of missing, extra, or substituted
bytes. The dated local NVKE record in reconciliation establishes development registry interoperability for
earlier inputs, not authenticated release publication or production-registry acceptance.

The single-entry layout and stage-bound evidence require a new authorized hosted publication run; the earlier
NVIDIA run does not establish their external behavior.

Synthetic disk transport tests cover both declared native platforms, distinct raw-payload and wrapper identities,
component evidence membership, held consumer projection and loopback-registry byte-preserving transport.
Guard tests reject missing or unrelated receipt owners and profiles, and local admission tests reject the
unwired publication graph without execution. The successful original-live-Run receipt-to-package-to-hosted-finalization
path has not been exercised by those tests. Synthetic QCOW2 fixtures and controlled inspection seams do not
prove a complete NVKE filesystem scan, the original live build-to-inspection join, native execution, keyless
signing or production publication. Both native-generated NVKE builds and independent signed disk consumption
remain required operational evidence.

Qualified end-to-end acceptance requires publishing one cleared mixed-artifact ReleaseSet to operator-selected targets,
discovering every referrer by immutable digest, authenticating the downstream clearance, and proving that
different destinations preserve every semantic and blob identity while omitted credentials never enter logs or
payload bytes.
