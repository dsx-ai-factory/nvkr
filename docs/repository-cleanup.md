# Repository cleanup and module disposition

Reviewed 2026-09-09 against Definition → Run → Release, ADR-17/18, and the NKE scope: local and GitHub;
the NKE OS-image build owns all worker components. This review traces production imports and command
registrations against current product value. Backward compatibility is not a retention requirement; dormant
workflows and their contracts are removed when they do not serve the current product. The reconciliation remains the capability/evidence authority.

## Inputs and removed material

| Material | Disposition |
|---|---|
| Maintained NKE CLI and AICR inputs | `definitions/nke-v1/` and `definitions/aicr/`, each with its own catalog |
| Root `recipes/` and implicit catalog | Removed from production; useful data is explicit test fixtures. Product Definitions select their own catalog |
| Worker, multi-output, layered and operator examples | Moved to `testdata/examples/`; no longer advertised as product build instructions |
| AICR dependency on the worker catalog | Removed; its catalog contains only the pinned AICR profile and an empty source lock |
| Checked-in GitLab pipeline and bundle | Removed, including the root entrypoint, demo, and Environment files; Go adapter contracts retained |
| Bootstrap instructions and HTML/DOCX requirement exports | Removed as superseded duplicates; Git history and dated evidence retain traceability |
| Historical plans, design drafts and SRD | Removed after review against current ADRs and milestone requirements; history retains the originals |
| Unreferenced musl cross-build and QEMU smoke-test Dockerfiles | Removed; they belonged to the retired individual-worker/image demonstration |
| Worker-specific Go/CGO/rootfs contexts and image lock | Removed together with the old toolchain builder and disabled image/package commands |
| Unwired `pipeline/portable.go` prototype | Removed with its renderer-only test; the real helper-launch contract test is retained |
| Unregistered optional-signature verifier marker | Removed its dead command constructor, refusal helper and self-referential test; registered authenticated verification stays intact |
| Standalone cluster authoring CLI | Removed; authors declare cluster views inside a Definition |
| Old factory catalog/loaders and authoring CLI | Removed; lifecycle lowers into internal `CompileSpec` and `ExecutionSettings` |
| Factory GitHub bundle aliases/wrappers | Removed; both providers use `GeneratedBundle` and the shared writer/checker |
| Exported graph-format helpers | Internalized behind `RenderGraph` |
| Source-compatibility tombstones and aliases | Removed; callers use current context and authenticated-receipt APIs |
| Unused rootfs tar constructor | Removed; OS-image construction belongs to NKE |
| Unused vulnerability scanner/advisory prototypes | Removed; subject-bound report qualification remains for release gates |
| Orphaned mirror, advisory and factory-catalog digest families | Removed from the canonical registry |
| `pkg/nkd/purity` | Moved to `internal/architecture`; it is a repository test, not a domain module |

Recipe and Definition are not interchangeable: a Recipe supplies reusable build semantics; a Definition
selects Recipes and names required outputs and cluster views. Factory is an internal compiler/executor.
Test fixtures remain only where they exercise current resolution, packaging or authority behavior. They are not
migration guarantees. The old factory catalog, file loaders, authoring commands and frozen migration fixtures
are removed. Internal factory types are compile specifications and execution settings, not public authored objects.

## Every domain package

Three former domain packages are removed: `airgap`, `query`, and `variant`. The retained packages support the
current lifecycle or its evidence and release gates; tests alone are not justification for keeping an unused API.

| Package | Actual role and disposition |
|---|---|
| `lifecycle` | Keep: owns Definition/Environment compilation, Run preparation/execution and consumer Release views |
| `factory` | Keep: lifecycle calls its graph compiler, packaging, generation and executor; remove redundant wrappers, not the executor |
| `pipeline` | Keep: shared acquisition/execution, sandbox, observations, terminal receipts and node transport; also contains separate qualified emitters |
| `catalog` | Keep: pinned catalog loading, stacking, validation and maintenance used by lifecycle/resolution |
| `compose` | Keep: catalog overlay inheritance and mixins; distinct from cluster realization |
| `recipe` | Keep: selection and logical Recipe resolution over catalog content |
| `criteria` | Keep: shared selection-axis validation and matching |
| `ir` | Keep: canonical single-Recipe graph and typed logical/execution identities |
| `canonical` | Keep: canonical codec and frozen typed digest families |
| `errors` | Keep: stable boundary codes and exit mapping |
| `aicr` | Keep: real pinned profile/generator integration used by the maintained AICR Run |
| `cluster` | Keep: exact named-view realization used by lifecycle, factory and delivery; no independent public cluster authoring workflow |
| `assemble` | Keep: byte packaging called by factory/pipeline; disk measurement contracts do not implement an OS-image builder |
| `delivery` | Keep: artifact layouts, APT/Helm/binary closure and OCI publication/consumption used by lifecycle |
| `sign` | Keep: signer/tool authentication and publication verification used by factory and lifecycle |
| `provenance` | Keep: subject-bound build evidence used by factory, attestation and release gates |
| `sbom` | Keep: observed-output SBOM generation and evidence checks used by factory and release |
| `trust` | Keep: authenticated documents and policy capabilities shared by signing, evidence, status and release |
| `profile` | Keep: execution/reproduction-profile contracts used by pipeline and attestation; qualified capability remains separately admitted |
| `attest` | Keep: qualified manifest/evidence and published-receipt contracts used by SDK and release; distinct from hosted provenance |
| `release` | Keep: qualified membership, gates and authorization used by delivery and transport; not a duplicate of the public lifecycle Release view |
| `status` | Keep: durable signed lifecycle/freshness contracts used by release authorization |
| `transport` | Keep: SDK qualified publication seam consuming release authorization; distinct from OCI layout construction in delivery |
| `airgap` | Removed: disconnected mirror lifecycle outside current product scope |
| `variant` | Removed: unused fallback classifier/approval API; necessary mismatch reporting stays with attestation |
| `vuln` | Retain deferred evidence contract: release gates consume it; scanner integration is outside v1 and no live scan acceptance is claimed |
| `query` | Removed: standalone legacy selector/diff workflow |
| `internal/heldfile` | Keep private authority utility: held executable/file identity for trust-sensitive operations |

`vuln` remains because current release authorization consumes its subject-bound reports; it is not a standalone
scanner product or a claim that scanning is exercised. `transport` remains because the SDK uses it to publish
authorized artifact sets. Neither package is retained to preserve obsolete callers.

`factory` and `pipeline` overlap in provider adapters but not in authority: the former compiles multi-output
lifecycle Runs; the latter owns single-Recipe execution and qualified receipts. Likewise, `delivery` creates
and verifies artifact representations while `transport` requires qualified release authorization before
publishing through injected backends. Merging those packages merely to reduce directory count would obscure
those boundaries. ADR-17/18 and the SADD describe the distinction.

## Validation

The final cleanup passed the full local gate (format, vet, lint, ShellCheck and race tests), uncached repository
unit tests, and native Linux ARM64 race tests for signing and pipeline execution. Linux ran with a read-only
checkout and module cache, networking disabled, and an init process.

The catalog-maintenance and tamper-detection demos passed against the rebuilt CLI. Catalog inspection now uses
ordinary file comparison; the obsolete query/diff demonstration is removed. GitHub generation and AICR local
planning passed. The NKE generated bundle passed byte-exact regeneration through the public Definition and
Environment inputs.

The rebuilt CLI completed both maintained execution demos:

| Demo | Recorded result |
|---|---|
| M1 NKE CLI | `.nkd/m1-local/m1-20260909t202227z-29290/lifecycle.json`; built, packaged, published to loopback, pulled by digest, and ran recovered `nkectl v0.1.0-rc.2` |
| AICR local with publication | `.nkd/demo-ai-factory-local/aicr-20260909t202232z-29638/lifecycle.json`; pinned rendering, Helm packaging, envelope inspection and loopback publication passed |

The M1 publication is
`127.0.0.1:5000/nke/nkectl@sha256:c115be7e4be62aa43e43fee38917586c19e7a524711e4d6902d1f7eb6fe63562`.
The AICR publication is
`127.0.0.1:5001/nke/aicr@sha256:80d95992f5377ab7c61da8011c123ab8136261f1bc698fdedee00b05934780c4`.
Both remain `static-unqualified`; M1 signing was dry-run. At this cleanup audit, no real signed publication was available
for the Release-consumer demo. The later [signed hosted Run and independent consumption](evidence/2026-09-10-github-signed-run.md)
now provide that positive evidence.

These checks do not establish hosted CI, live signing, OS-image integration, or qualified production acceptance.
