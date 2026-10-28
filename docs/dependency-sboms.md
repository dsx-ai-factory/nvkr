# Artifact dependency SBOMs

The NVKE Definition selects a catalog-pinned Syft executable using `sbomTool: syft`. The scanner
is acquired as a normal pipeline tool. SBOM jobs run offline after artifact observation and consume the exact
payloads plus their producer records. The implementation is selected by artifact format, not component name.

| Artifact | Discovery | Scope |
|---|---|---|
| CLI executable | Syft reads compiled binary metadata | Embedded Go modules and other recognizable binary packages |
| OCI image export | Syft reads the final image filesystem | Installed OS packages and recognizable application dependencies |
| Helm source archive | NVKR reads Helm/AICR manifests | Source-build stage: bundled charts, chart dependencies/locks and declared external chart references |
| Bound Helm package | NVKR re-inspects the final archive after image binding | Packaging stage: chart declarations/references in the exact delivered bytes |
| Separate Debian archive metadata diagnostic | `InspectDebianArchiveMetadata` uses Syft v1.51.1 with the fixed private name `subject.deb`, independent of the caller's filename | Package identity only; `syft-debian-archive-metadata/v1` is rejected by dependency SBOM attachment and serialized BOM validation; no maintained factory output invokes this diagnostic |
| Standalone Debian content operation | `DiskInspectionRuntime.InspectDebianPackageContent` captures exact package bytes and reuses the complete admitted Linux runtime and pinned `dpkg-deb` data-filesystem reader | Architecture validation, recognized payload components and file hashes through the existing immutable observation; controlled contract tests; local scheduling uses original retained producer bytes, historical developer-local ARM64 package checks are recorded in [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md); broader operational acceptance remains deferred |

Ordinary `InspectDependencies` and held-only `InspectHeldDependencies` retain hint-free host-file scanning;
factory callers use those dependency APIs. The separate diagnostic's private `.deb` discovery hint supplies
no format, target-architecture or payload-coverage authority. The existing
digest-pinned `dpkg-deb` material reader owns authoritative Debian expansion and platform checks for disks
and the separately selected standalone content operation. That operation admits the full runtime/profile
and reserves capacity before opening a path pinned by archive digest and exact length (at most 1 GiB).
It owns the snapshot and cleanup, scans the package data filesystem without running maintainer scripts,
and returns an immutable observation. `DiskContentDocument` binds its report to the original archive bytes;
no control-script inventory, exhaustive dependency coverage or original producer/Run join is inferred.
The local standalone route retains original source-created bytes through inspected-content and terminal
owners; this diagnostic does not admit packages through executable/APT wrapper routes.

The scanner executable is authenticated before use. Subject bytes must match the producer digest. Its output
is normalized into CycloneDX 1.5: deterministic identities/order, package URLs, versions and artifact dependency
edges. File-location records are excluded from dependency counts. Missing versions remain unknown. Missing
inputs, substituted bytes/tools, scanner failures and empty inventories are errors, not successful empty SBOMs.

Helm dependency inspection reads standard `Chart.yaml`/`Chart.lock` files and Helm references from AICR's
`recipe.yaml` export. It reads version declarations directly and never executes shell scripts. External charts
are references, not vendored packages; their transitive dependencies are not claimed. The Helm subject is the
built source archive, preceding the separate image-binding and packaging operation.

A Definition without `sbomTool` still emits declaration-only source metadata and does not claim dependency
inspection. This does not enable vulnerability scanning or qualified reproduction.

For the compiled standalone Debian producer, per-artifact source metadata rederives the complete Build
before recording its selected route toolchain, distribution profile and platform. The source-tag version
describes the source recipe; it does not supply the Debian control Version. This description neither proves
production nor adds inspected dependencies. A private factory operation now checks the original live Run,
named producer archive and Environment inspection profile before scanning those retained bytes. It composes
the inspected inventory with the Build-rederived source subject and stores bounded canonical document bytes.
The recipe version and observed Debian package Version stay distinct. Controlled native fixtures cover this
join, substitution, cancellation, checked cleanup, mutation isolation and complete-document size rejection.
The local scheduler consumes these original joins before terminal observation; the description alone is
not a terminal or release receipt. Historical developer-local Ubuntu 24.04/26.04 ARM64 Runs and
install/help/remove checks are recorded in [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md).
Their original standalone payloads are unretained; native AMD64, live service/API/GPU behavior, upgrades,
qualification and publication remain unverified. The controlled fixtures prove their own contract scope.

The separate private terminal operation can now issue a process-local receipt after independently
rechecking original producer/profile/document bindings and the complete archive bytes. Description and
export repeat these checks and bind the typed SBOM digest. Run close revokes the receipt; failed staging
may contain partial bytes and supplies no successful observation. A separate local provenance description
records original producer times, complete Plan dependencies and prepared-source digest under ADR-09's
unsigned local builder identity. Lifecycle stages raw package bytes, the inspected document and this
unsigned predicate through its existing output/record transaction; original Run cleanup must succeed before
durable commit. Authenticated provenance and release/publication remain deferred; controlled fixtures do
not prove a real package.

The compiled Debian scanner selection requires the native producer and utility platform to agree, a complete
Environment private-volume inspection profile, and the exact selected catalog scanner executable pin. Its
offline SBOM node retains archive/result/scanner edges and profile image through complete Plan
rederivation. The Debian terminal node requires that inspected SBOM, so inspection precedes observation.
Local execution consumes original retained completions rather than ordinary path-based SBOM work or
serialized results. Direct-node/hosted execution and signing/publication remain gated. Missing profiles and
target conflicts fail before catalog work.

One release index references each required CycloneDX attestation bundle once in its owning artifact repository.
The document appears only inside that signed statement; raw SBOM files remain private job handoffs. Its metadata records exact subject,
covered stage, raw document digest, typed document identity and output membership. Build-time per-build
indexes remain internal handoffs. Packaging verifies their output/build/producer/scanner bindings, transforms the
documents to explicit stage subjects, and adds the final Helm packaging inventory. Before publication a
separate stage creates and verifies subject-bound Sigstore bundles, then derives the final release index. The native management-plane image
SBOM targets the delivered image manifest and records the scanned export digest. Signing the release index
covers all these exact bytes recursively. Source-archive evidence is never relabeled as final-chart evidence.
See [the registry graph](publication-architecture.md) for every remaining manifest and attachment.

Current contract coverage and hosted publication evidence are recorded in
[reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md). Signed producer evidence in the producer-only disk mode explicitly records
deferred inspection; it is not the dependency inventory described for binaries, images and charts above.
The current qualification workflow preserves producer-only disk evidence. Strict disk inventory/reconciliation is separate deferred
hardening; qualification of source metadata does not prove installed disk contents.

## Verification on real outputs

[Recorded offline dependency scanning](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-09-10-combined-run.md#offline-dependency-scanning)
retains the exact artifact/scanner identities, inventory counts and measured-fixture limitations.
Unknown package versions remain unknown; an artifact or source digest does not supply a missing release version.

The opt-in `TestRealArtifactsThroughSBOMExecutor` test reads `NVKR_SBOM_ARTIFACT_DIR`, `NVKR_SBOM_PLAN`, and
`NVKR_SBOM_SCANNER`; `NVKR_SBOM_OUTPUT_DIR` optionally retains the resulting documents. It exercises graph
admission, real scanning, deterministic document validation and index digests for all three outputs. The
scanner bytes must match the generated plan's executable pin. Run on Linux with networking disabled.

The separate opt-in `TestRealDebianArtifactDependencies` exercises `InspectDebianArchiveMetadata` on controlled
gzip/xz fixtures, not a compiled factory output. [Development prerequisites](https://github.com/dsx-ai-factory/nvkr/blob/main/DEVELOPMENT.md#opt-in-integration-checks)
identify its tool pins and command. The same bytes scanned without the diagnostic's hint must return
`CodeProvenanceMissing`, with no accepted inventory, under the selected Syft version. Revalidate the discovery
premise when changing either the factory catalog Syft pin or the Syft pin in `toolchains/nvkr-disk-inspect`. The
[dated local observations](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md#external-run-references) record native ARM64 tool and
fixture identities; native amd64 execution has not been recorded. Non-archive bytes named `.deb` produce no
accepted inventory; corrupt payload members and complete embedded dependency discovery are not established.

Contract tests also cover stable ordering/version identities, detached dependency graphs, malformed/empty
reports, scanner substitution, changed subject bytes, unsafe archive members and missing payload/tool edges.
