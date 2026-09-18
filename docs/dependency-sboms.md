# Artifact dependency SBOMs

The NKE Definition selects a catalog-pinned Syft executable using `sbomTool: syft`. The scanner
is acquired as a normal pipeline tool. SBOM jobs run offline after artifact observation and consume the exact
payloads plus their producer records. The implementation is selected by artifact format, not component name.

| Artifact | Discovery | Scope |
|---|---|---|
| CLI executable | Syft reads compiled binary metadata | Embedded Go modules and other recognizable binary packages |
| OCI image export | Syft reads the final image filesystem | Installed OS packages and recognizable application dependencies |
| Helm source archive | NKD reads Helm/AICR manifests | Source-build stage: bundled charts, chart dependencies/locks and declared external chart references |
| Bound Helm package | NKD re-inspects the final archive after image binding | Packaging stage: chart declarations/references in the exact delivered bytes |

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

The change is locally contract-tested; the first NVIDIA Run predates it and has unsigned standalone SBOMs.
Local publication and extraction were exercised with the separate-repository layout. A new hosted Run is required to demonstrate signed SBOM publication operationally.

## Verification on real outputs

Offline Linux/amd64 execution used the three artifacts downloaded from
[Run 34453148217](https://github.com/njhensley/nke-recipes/actions/runs/34453148217), Syft **1.51.1**, and the
actual SBOM executor with the regenerated plan. Producer records were measured local fixtures; this is not
a new hosted run or an authenticated qualified producer report.

| Output | Unique dependency components | Breakdown |
|---|---:|---|
| nkectl | 27 | Go modules |
| CWE | 248 | 171 Go modules, 76 Alpine packages, 1 OS component |
| Helm | 7 | 3 bundled charts, 4 external chart references |

Two workspace modules in each binary/image inventory have `UNKNOWN` versions in Syft's report. The artifact
and source commit still identify their bytes; no release version is guessed for them.

The opt-in `TestRealArtifactsThroughSBOMExecutor` test reads `NKD_SBOM_ARTIFACT_DIR`, `NKD_SBOM_PLAN`, and
`NKD_SBOM_SCANNER`; `NKD_SBOM_OUTPUT_DIR` optionally retains the resulting documents. It exercises graph
admission, real scanning, deterministic document validation and index digests for all three outputs. The
scanner bytes must match the generated plan's executable pin. Run on Linux with networking disabled.

Contract tests also cover stable ordering/version identities, detached dependency graphs, malformed/empty
reports, scanner substitution, changed subject bytes, unsafe archive members and missing payload/tool edges.
