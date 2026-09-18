# Repository cleanup and module disposition

Disposition updated 2026-09-18 against Definition → Run → Release, ADR-17/18, and the NKE scope: local and GitHub;
the NKE OS-image build owns all worker components. This review traces production imports and command
registrations against current product value. Backward compatibility is not a retention requirement; dormant
workflows and their contracts are removed when they do not serve the current product. The reconciliation remains the capability/evidence authority.

## Inputs and removed material

| Material | Disposition |
|---|---|
| Maintained NKE CLI and AICR inputs | `definitions/nke-v1/` and `definitions/aicr/`, each with its own catalog |
| Root `recipes/` and implicit catalog | Removed from production; useful data is explicit test fixtures. Product Definitions select their own catalog |
| Worker, multi-output, layered and operator examples | Moved to `testdata/examples/`; no longer advertised as product build instructions |
| AICR dependency on the worker catalog | Removed; its catalog owns the exact AICR profile and pinned generator/evidence tools |
| GitLab pipeline and bundle | Removed inputs, demo, generated files, Go renderer and validator; GitHub is the sole hosted adapter |
| Bootstrap instructions and HTML/DOCX requirement exports | Removed as superseded duplicates; Git history and dated evidence retain traceability |
| Historical plans, design drafts and SRD | Removed after review against current ADRs and milestone requirements; history retains the originals |
| Unreferenced musl cross-build and QEMU smoke-test Dockerfiles | Removed; they belonged to the retired individual-worker/image demonstration |
| Worker-specific Go/CGO/rootfs contexts and image lock | Removed together with the old toolchain builder and disabled image/package commands |
| Unwired `pipeline/portable.go` prototype | Removed with its renderer-only test; the real helper-launch contract test is retained |
| Unregistered optional-signature verifier marker | Removed its dead command constructor, refusal helper and self-referential test; registered authenticated verification stays intact |
| Cluster views, classifications and provisioning interface | Removed; Definitions declare artifact outputs and downstream consumers own deployment |
| Old factory catalog/loaders and authoring CLI | Removed; the compiler accepts exact Definition/Environment inputs and returns a complete Plan |
| Factory GitHub bundle aliases/wrappers | Removed; GitHub uses `GeneratedBundle` and its writer/checker |
| Graph-format helpers and generic validator wrapper | Removed; no production consumer of the table/JSON/DOT/Mermaid renderer or forwarding validator |
| Source-compatibility tombstones and aliases | Removed; callers use current context and authenticated-receipt APIs |
| Unused rootfs tar constructor | Removed; OS-image construction belongs to NKE |
| Unused vulnerability scanner/advisory/report implementation | Removed with unreachable CLI helpers and qualified authorization; registered scanning requests fail before work |
| Orphaned mirror, advisory and factory-catalog digest families | Removed from the canonical registry |
| `pkg/nkd/purity` | Moved to `internal/architecture`; it is a repository test, not a domain module |

Catalog components supply reusable producer semantics. A Definition selects producers and platforms directly
on named outputs, including explicit AICR rendering settings. Factory is an internal compiler/executor.
Test fixtures remain only where they exercise current resolution, packaging or authority behavior. They are not
migration guarantees. The old factory catalog, file loaders, authoring commands and frozen migration fixtures
are removed. The compiler no longer exposes duplicate compiler-input documents. `definition` owns authored inputs and
`factory` derives complete executable dataflow directly from them.

## Every domain package

Former domain packages `airgap`, `query`, `variant`, `recipe`, `compose`, `criteria`, `cluster`, and `placement` are removed. The retained packages support the
current lifecycle or its evidence and release gates; tests alone are not justification for keeping an unused API.

| Package | Actual role and disposition |
|---|---|
| `definition` | Owns authored Definition/Environment loading, validation and identity |
| `lifecycle` | Owns immutable Run preparation/persistence and policy-scoped consumer Release workflow |
| `factory` | Owns complete compilation, whole-Run scheduling, output joins, packaging and provider generation |
| `pipeline` | Keep: shared acquisition/execution, sandbox, observations, terminal receipts; unused qualified providers and bundle models are removed. Bounded archive transport is owned once in `internal/transportarchive` |
| `catalog` | Keep: pinned catalog loading, stacking, validation and maintenance used by lifecycle/resolution |
| `compose` | Removed: Recipe inheritance and mixins have no direct-output consumer |
| `recipe`, `criteria` | Removed: direct output membership and concrete target admission replace authored groups and matching |
| `buildgraph` | Derive exact pinned graphs from each output’s selected producers |
| `target` | Validate concrete target platforms and architecture routes |
| `placement` | Removed: deployment classifications have no build/release responsibility |
| `ir` | Keep: canonical output build graph and typed logical/execution identities |
| `canonical` | Keep: canonical codec and frozen typed digest families |
| `errors` | Keep: stable boundary codes and exit mapping |
| `aicr` | Keep: real pinned profile/generator integration used by the maintained AICR Run |
| `cluster` | Removed: exact output and evidence membership remain in lifecycle/factory/delivery |
| `assemble` | Keep: byte packaging called by factory/pipeline; disk measurement contracts do not implement an OS-image builder |
| `delivery` | Keep: artifact layouts, APT/Helm/binary closure and OCI publication/consumption used by lifecycle |
| `sign` | Keep: signer/tool authentication and publication verification used by factory and lifecycle |
| `provenance` | Keep: subject-bound build evidence used by factory and attestation |
| `sbom` | Keep: observed-output SBOM generation and evidence checks used by factory, publication and diagnostics |
| `trust` | Keep: authenticated documents and policy capabilities shared by signing, evidence, status and release |
| `profile` | Keep: execution/reproduction-profile contracts used by pipeline and attestation; qualified capability remains separately admitted |
| `attest` | Keep: qualified manifest/evidence and published-receipt checks used by registered diagnostics; distinct from hosted provenance |
| `release` | Keep: Inventory/ReleaseSet/PromotionPolicy documents, completeness and document authentication used by diagnostics; closed test-only authorization, gate receipts and selection helpers removed |
| `status` | Keep: signed declaration/checkpoint authentication and durable anti-rollback state used by status diagnostics; unused release-specific time and clearance integration removed |
| `transport` | Removed: its only production importer was an unreachable helper, and its qualified commit path was unavailable |
| `airgap` | Removed: disconnected mirror lifecycle outside current product scope |
| `variant` | Removed: unused fallback classifier/approval API; necessary mismatch reporting stays with attestation |
| `vuln` | Removed: its authorization consumer and CLI helper were both unreachable; scanning remains unsupported |
| `query` | Removed: standalone legacy selector/diff workflow |
| `internal/heldfile` | Keep private authority utility: held executable/file identity for trust-sensitive operations |

An import is not sufficient evidence of use. The transport audit found its apparent diagnostic consumer was
itself unreachable; the package and helper are removed. Following that chain also exposed a closed, test-only
authorization subsystem and vulnerability-report library. Shared archive transport remains used by actual
hosted Runs. Qualified release authorization/publication remain deferred contracts with no retained implementation.

`factory` compiles and renders multi-output Runs; `pipeline` executes producer instructions and owns runtime
receipts. `delivery` creates and verifies artifact representations. The separate qualified provider-rendering
chain, unused qualified publisher and caller-built clearance receipts no longer expand those interfaces. The
[usage and age assessment](unused-code-assessment.md) records removals, protected recent additions and checks.

## Operation navigation

`client/v1` contains configuration, lifecycle operations and direct producer diagnostics. Unused qualified
orchestration wrappers and authority injection options are removed. Tests sit beside those operation files.
Factory execution files use the `execute_` prefix; `outputs_observed.go` owns the independent output join.
Pipeline's unused qualified renderers and lowering are removed, together with the hidden `bundle` command and
its exclusive models and transaction helpers. GitHub generation remains in factory; retired engines fail admission.
Package comments identify the starting operations and owned invariants. See the
[locality implementation record](codebase-locality-implementation.md) for the migration and its validation.

## Historical validation — 2026-09-09

The following results apply to the cleanup and inputs exercised on that date. They do not validate the current
Recipe-removal migration, its new wire schemas, or the current generated bundle. That migration's completion
gate and maintained-input local build remain tracked separately in reconciliation.

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
