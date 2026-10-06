# Multi-architecture CWE delivery

The implementation compiles an explicit Linux AMD64/ARM64 platform set into independent native CWE
producers and one standard runnable OCI image index. Delivery preserves each native child image's exact
manifest, config and layer bytes. The maintained Definition keeps one public `cwe` output and eight public
outputs overall, with nine native build selections. External execution and release acceptance are recorded
separately from the contracts below.

## Enabled delivery boundary

`delivery.AssembleImageIndex` takes an explicit required platform set and descriptions of exact native image
layouts. Each description includes platform, layout path and native manifest digest. Supported members are
Linux AMD64 and baseline ARM64; neither the host nor the first manifest chooses a target. The required set can
contain one or both supported platforms. All input membership is admitted before filesystem access.

The owning module captures the entire closure into bounded anonymous immutable files and independently checks
its exact descriptor graph, config platforms, layer digests and expanded layer identities. Unreachable blobs,
duplicate/missing platforms, unsupported variants, artifact manifests, incomplete closures and ambiguous JSON
are errors. The resulting `ImageIndex` has a lifetime; it exposes only defensive descriptions, layout writing
and closing. Descriptions carry no build, signer, evidence or release authority.

`delivery.OpenImageIndex` requires an exact index digest and exact expected platform set. Every descriptor in
an opened runnable index must declare a platform matching its image config. ARM64's omitted variant and `v8`
both mean the admitted baseline; assembly emits the variant-free baseline descriptor. The aggregate digest
identifies the exact index bytes, while child digests identify unchanged native manifests. These are OCI blob
identities, not a new logical Build identity family.

`WriteLayout` requires an existing parent directory. It writes held bytes into a private sibling using held
directory descriptors, syncs files and directories, installs the complete layout with an atomic no-replace
rename, then syncs the parent. Concurrent writers cannot replace an existing layout. A queued writer observes cancellation while waiting for an active copy. Cancellation before the
commit removes the private stage and leaves the destination absent; cancellation does not consume the held
result. A sync error after commit returns failure with a possibly complete destination, which must be
inspected before retry. `Close` joins active operations and releases the anonymous files. Source-path changes
after capture cannot affect later layout writes.

The closure allows at most 32,768 SHA-256 blobs and 64 GiB of blob bytes; index/config/manifest documents are
limited to 8 MiB and expanded layers to 64 GiB each. Capture and validation observe caller cancellation.
Each child source capture is separately bounded, so assembly can temporarily hold its accumulated closure
plus one bounded incoming child before deduplication. Layers are not extracted or executed by this API.

## Integration contracts

The compiler, lifecycle and delivery interfaces implement these membership contracts. Registry aliases,
authenticated navigation and externally exercised acceptance have separate owner and evidence records.

| Owner | Required change and invariant |
|---|---|
| Definition and target admission | Author one OCI output with a sorted, explicit finite platform set, mutually exclusive with a single-platform field. Reject unsupported/missing platform routes before work. Decide and test schema compatibility explicitly; no `platform: multi`, host default or implicit expansion. |
| Compiler and build identity | Expand that output into separately keyed internal `(output, platform, producer)` uses and one index assembly operation. Freeze every required child before execution. The build owner derives aggregate logical identity from a sorted typed child-build vector, independent of registry routes. Public output names remain authored names. |
| Terminal observations and completeness | Bind each exact child manifest to its platform, producer, observed successful result and child Build Digest. Equal Build Digests cannot satisfy another platform or output use. Reject missing, duplicate and substituted children before assembling an index. |
| SBOM and signed evidence | Preserve mandatory native-image SBOM and source/provenance subjects for every child. Extend evidence keys beyond the current output/stage/component tuple so two architectures cannot overwrite each other. Any aggregate summary supplements rather than replaces exact child coverage. |
| Release metadata and signing | Encode the index digest, sorted child descriptors and platform-scoped build/evidence membership under the signed output. Version changed durable schemas explicitly; never reinterpret an old single-manifest binding as aggregate authority. Sign/attest the intended index subject and retain independently required child evidence. |
| Registry and discovery | Teach the registry adapter to push and read back the exact index plus all children into the output repository before the release root. Define one optional non-authoritative CWE tag update only after complete publication/readback; a failed child must not advance that alias. The current automatic Helm aliases do not authorize a CWE tag policy. |
| Consumer and navigation | Authenticate the signed index/child membership and required evidence, then expose checked per-platform child manifest and evidence identities. A single guessed OS/architecture or flattened child list is insufficient. Navigation must preserve the same authenticated-versus-diagnostic distinction as other payloads. |
| Helm binding | Admit a chart's platform only when the image output's declared and observed platform coverage includes it. Bind the digest of the complete runnable index, preserving normal OCI platform selection. No arbitrary child substitution or post-generation manifest rewrite. |

These changes cross ADR-11/12 producer and target selection, ADR-13/16 evidence and completeness,
ADR-19 output membership and ADR-20 publication. Native hosted routing is a separate prerequisite owned by
ADR-18 and the multi-architecture CLI workstream. The original stage-one evidence remains scoped to the held low-level closure; it does not establish the
later compiler, evidence, consumer or registry admissions.

## Aggregate interface

The changed-membership schemas are Definition v5, Plan/ArtifactGraph v5, Run/Release v4 and PublicationRoot
v4. Changed legacy membership schemas are rejected rather than supported through parallel interpretation. Environment v1, native producer IR,
binary bundles and unchanged native image bytes do not need new versions for this capability.

The delivery/publication owner provides these shared descriptive types. JSON uses precisely these field
names; all fields below are required. `requiredPlatforms` and `children` are sorted by the exact platform
string, nonempty, unique and equal as sets. Every child uses the one explicitly selected image component.
`size` is the child manifest's byte length. `buildSubject` is the observed producer's OCI-export archive
digest, distinct from the native manifest digest.

```go
type ImageIndexBinding struct {
    RequiredPlatforms []string                 `json:"requiredPlatforms"`
    Children          []ImageIndexChildBinding `json:"children"`
}

type ImageIndexChildBinding struct {
    Platform       string `json:"platform"`
    ManifestDigest string `json:"manifestDigest"`
    Size           int64  `json:"size"`
    BuildDigest    string `json:"buildDigest"`
    Component      string `json:"component"`
    BuildSubject   string `json:"buildSubject"`
}
```

`delivery.Output` and `delivery.OutputBinding` carry
`ImageIndex *ImageIndexBinding` as `json:"imageIndex,omitempty"`; it is absent for existing native single-image
outputs and mandatory for aggregate image outputs. The public publication output description will project
the checked same binding. No OS/architecture pair is assigned to the aggregate. Structural validation of
these descriptive values does not authenticate a producer or authorize a release; factory observation and
the authenticated consumer own those checks. `ImageIndexBinding.Clone()` must copy both slices.

`delivery.EvidenceBinding` gains `Platform string` as `json:"platform,omitempty"`. Aggregate native child
SBOM evidence must populate it. Each mandatory key is `(output, platform, stage, component)`; for an image
child, `stage=build-output`, `subjectKind=oci-manifest`, `subjectDigest=manifestDigest` and
`inspectionInputDigest=buildSubject`. Its expected build identity is that child's `buildDigest`, not the
aggregate recipe digest. Nonaggregate evidence keeps its existing key semantics with an empty platform.
The required set comes from frozen Definition/Plan membership, never from whatever children were observed.

The compiler owner exposes its own `factory.ImageIndexPlan` with `requiredPlatforms` and sorted
`children`. Each `factory.ImageIndexChildPlan` carries `platform`, `buildTarget`, `buildDigest`, `component`,
`artifactId`, `resultArtifactId` and `sbomArtifactId`. These are required work and exact graph references,
not observed native manifest identities. `LifecycleOutputPlan.ImageIndex` holds this plan; the Run freezes a
defensive copy before execution. Internal build identifiers must be injective and collision-checked from
output/platform use, and may never alias another public output. Each child remains a valid ordinary native
IR graph; the aggregate is a separate package operation with both children as direct required dependencies.

The IR owner provides `ir.ImageIndexBuildDigest(requiredPlatforms []string,
children []ir.ImageIndexBuildChild) (string, error)`, where `ImageIndexBuildChild` contains exact
`platform`, `buildDigest` and `component` JSON fields. It validates the required platform set and child vector,
and applies `canonical.KindBuild` to this distinct versioned logical projection:

```json
{"children":[{"buildDigest":"sha256:<native-build>","component":"controller","platform":"linux/amd64"},{"buildDigest":"sha256:<native-build>","component":"controller","platform":"linux/arm64"}],"requiredPlatforms":["linux/amd64","linux/arm64"],"schemaVersion":"nkd.dev/image-index-build/v1"}
```

The example digest placeholders are illustrative; actual values must be exact lowercase SHA-256 typed Build
Digests independently recomputed from each native IR. The new projection contains no output repository,
runner route, layout path, native manifest observation or other transport/runtime field. Existing native
`Graph.Digest()` bytes remain unchanged. Delivery can use this shared logical projection to check signed child
build membership against the aggregate `BuildDigest`; the function itself creates no authority receipt.

`delivery.ContentDigest` retains its existing projection of name, kind, index payload digest, aggregate Build
Digest and components. The index digest already binds all child payload bytes; the aggregate Build Digest binds
the required platform/build vector. Exact per-platform evidence remains separately mandatory in signed metadata.

The delivery module owns the image-index bytes and binding checks; factory and lifecycle own frozen work,
observations and completeness. Release navigation projects these bindings without adding authority.
The separate combined-pipeline work owns ARC execution and per-output metadata publication. Composing these
changes requires preserving admitted ARC routes and carrying image-index child metadata and platform-scoped
evidence through its metadata-only assembly. That composition is not established by this branch's tests.
ADR-01 describes the additional transport-free Build projection.

## Validation scope

Contract tests cover deterministic input permutation, shared-blob deduplication, byte-for-byte preservation of
all native child blobs, a standard `go-containerregistry` OCI reader, exact platform coverage, malformed
manifest/config/layer closures, wrong sizes/digests, ambiguous JSON, unsupported descriptors/variants, symlinks,
FIFOs, source replacement after capture, defensive metadata, closed/zero capabilities, cancellation cleanup,
independent competing writers and concurrent closing. The pre-existing single-image inspector still rejects
an aggregate index.

These fixtures establish the representation and ownership contracts. They do not establish a successful CWE
build, runnable application behavior, registry acceptance, a hosted job, signature verification or release
acceptance. The final integration stage requires real native AMD64 and ARM64 CWE production, exact child
SBOM/provenance, standard OCI pull/run through the index, failed-child/tag tests and complete signed release
consumption. Recorded execution must distinguish native and emulated checks.
