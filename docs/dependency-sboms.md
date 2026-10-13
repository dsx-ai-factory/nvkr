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

Ordinary `InspectDependencies` and held-only `InspectHeldDependencies` retain hint-free host-file scanning;
factory callers use those dependency APIs. The separate diagnostic's private `.deb` discovery hint supplies
no format, target-architecture or payload-coverage authority. The existing
digest-pinned `dpkg-deb` material reader owns authoritative Debian expansion and platform checks for disks.
Standalone Debian delivery and declared-format propagation remain deferred; this diagnostic does not admit
source-created packages through the executable/APT wrapper routes.

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

One release index references each required CycloneDX attestation bundle once in its owning artifact repository.
The document appears only inside that signed statement; raw SBOM files remain private job handoffs. Its metadata records exact subject,
covered stage, raw document digest, typed document identity and output membership. Build-time per-build
indexes remain internal handoffs. Packaging verifies their output/build/producer/scanner bindings, transforms the
documents to explicit stage subjects, and adds the final Helm packaging inventory. Before publication a
separate stage creates and verifies subject-bound Sigstore bundles, then derives the final release index. The native CWE
SBOM targets the delivered image manifest and records the scanned export digest. Signing the release index
covers all these exact bytes recursively. Source-archive evidence is never relabeled as final-chart evidence.
See [the registry graph](publication-architecture.md) for every remaining manifest and attachment.

Current contract coverage and hosted publication evidence are recorded in
[reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md). Signed producer evidence in the M2 disk mode explicitly records
deferred inspection; it is not the dependency inventory described for binaries, images and charts above.
The strict disk inventory/reconciliation contract is separate and remains an M3 requirement.

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
