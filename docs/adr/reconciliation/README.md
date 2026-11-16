# Architecture implementation and evidence

This ledger carries no global review date: each row is current as of the change that last edited it.
ADR-01–22 own the lane-specific engineering contracts pending named-decider
ratification. This ledger owns current implementation and remaining acceptance. Code and contract tests,
recorded external execution, and organizational approval are separate facts. Historical source and
schema results do not transfer to a changed candidate.

[ADR-23](../0023-upstream-artifact-behavioral-validation.md) additionally proposes upstream artifact
test interfaces outside that consolidated set until acceptance. Implementation and local CLI proof
are tracked below; governance, CI and broader acceptance remain pending.

[ADR-24](../0024-imported-validator-catalog.md) proposes an imported-validator catalog, explicit artifact-role
bindings and versioned diagnostic profiles outside the consolidated set. The provisional CLI catalog implements loading, discovery and frozen local-plan dispatch, with contract tests but no new
actual upstream catalog execution. `validators` (hidden, in development) is the sole NVKR interface for these suites. Published-profile migration and controller/chart adapters remain deferred.

[ADR-25](../0025-generic-oci-bundles-and-aicr-producers.md) additionally admits generic OCI bundles and
ordinary pinned AICR producers. Its contract and rendering evidence remain distinct from hosted delivery
and hardware acceptance, as recorded below.

[ADR-26](../0026-definition-composition.md) proposes composite Definitions: complete member Definitions pinned by
`nvkr.definition` digest, joined into one effective Definition, one Plan, one Run and one Release whose
membership is the exact union of member outputs. Implemented, contract-tested units are listed in its row
below, including named composite development bundles (U5). A historical
[83-output hosted publication](adr-26.md#hosted-gpu-site-publication) is recorded; current-candidate execution
and independent consumer verification remain unrecorded. Hosted complete-composite qualification binds composition and every output through handoff, checks, smoke,
qualification evidence and the production manifest. GPU-site production/development pairs select explicit
policies, including artifact-only bundle archive validation. Local composite qualification rehearsal and member
Release reuse remain deferred and fail closed; reuse follows the separately drafted result-reuse decision.
Contract coverage includes `TestQualificationAuthenticatesCompleteComposition` and
`TestGPUCompositeQualificationPairs`; these do not establish external service or hosted acceptance.
Retiring the combined NVKE Definition is a separate cutover gated on a hosted composite Run and consumer
verification.

[ADR-27](../0027-pinned-tool-producers.md) proposes pinned-tool producers: catalog components that run one released,
digest-pinned executable offline in a pinned container, with no shell, a fixed argument vector and typed material
inputs, emitting one file or a producer-written OCI layout. Units T1–T7 include the
layout-import handoff for one root with its complete explicit regular-file material closure. The AICR-specific
post-render validation boundary retains upstream verification before packaging. Current contract and local
execution evidence are recorded in [ADR-27](adr-27.md) and [AICR deployment bundles](aicr-deployment-bundles.md);
hosted Run and consumer verification remain unexercised. AICR v1.2.0-rc1 ships the local OCI layout output (NVIDIA/aicr#3538) that AICR
bundles need.

[ADR-28](../0028-multi-platform-executable-indexes.md) proposes multi-platform executables: one tool source and
one lock transaction cover every platform of a release, and an executable output may declare a `platforms` set
published as a standard OCI image index over minimal per-platform images. It is designed, not implemented: tool
declarations, tool pins and executable outputs remain single-platform, and executables publish only as Binary
Bundles. It also inventories the existing per-platform executable deliveries to retrofit.

[ADR-29](../0029-cluster-observation-and-artifact-attribution.md) proposes cluster information gathering and
optional descriptive comparison with selected NVKR artifacts. It is design only. The proposed observation
owner, shared in-process Kubernetes reader and AICR Go integration remain unimplemented and subject to
dependency admission. Existing conformance execution retains its separate strict admission and checks.

[ADR-30](../0030-authenticated-result-reuse.md) proposes pipeline-attested cross-Run reuse of complete named
outputs: a Plan-derived reuse key, untrusted discovery, owner-issued acceptance receipts, typed refusals and a
`reused` Run origin. It is designed, not implemented: no code, contract test, hosted Run or qualification
exercises reuse, and every Run still executes every required output.

[ADR-31](../0031-qualification-results-index.md) proposes a qualification results index: each production
qualifier attempt's unsigned outcome, and an errored outcome for a failed producer Run, published create-only
as a canonical record and a TestGrid build. Selection, projection, the publishing protocol and the
`nvkr-results-publish` helper are implemented and contract-tested. The generated publish job is not, so no Run
publishes and no TestGrid view has read an outcome.

## Ledger files

Each section lives in its own file; edit only the file that owns the affected row.

- [Current boundary](current-boundary.md)
- [AICR deployment bundles](aicr-deployment-bundles.md)
- [External test output and local diagnostic boundary](external-test-output-and-local-diagnostic-boundary.md)
- [M3 MVP scope and acceptance boundary](m3-mvp-scope-and-acceptance-boundary.md)
- [M4 release manifest issuance](m4-release-manifest-issuance.md)
- [Object-store publication](object-store-publication.md)
- Support by owner, one file per owner row group: [01–03](adr-01-03.md), [04](adr-04.md), [05](adr-05.md), [06](adr-06.md), [07–08](adr-07-08.md), [09](adr-09.md), [10](adr-10.md), [11–12](adr-11-12.md), [13](adr-13.md), [14](adr-14.md), [15](adr-15.md), [16](adr-16.md), [17](adr-17.md), [18](adr-18.md), [19](adr-19.md), [20](adr-20.md), [21](adr-21.md), [22](adr-22.md), [23](adr-23.md), [24](adr-24.md), [25](adr-25.md), [26](adr-26.md), [27](adr-27.md), [28](adr-28.md), [29](adr-29.md), [30](adr-30.md), [31](adr-31.md)
- [Feature-branch pipeline development](feature-branch-pipeline-development.md)
- [Ubuntu 24 node-image inputs](ubuntu-24-node-image-inputs.md)
- [Native disk execution and inspection](native-disk-execution-and-inspection.md)
- [Local ARM64 development inputs](local-arm64-development-inputs.md)
- [ADR-17 CLI presentation and inspection](adr-17-cli-presentation-and-inspection.md)
- [Local ARM64 demo presentation](local-arm64-demo-presentation.md)
- [Experimental OS source producers and OVN outputs](experimental-os-source-producers-and-ovn-outputs.md)
- [Output-local publication and metadata assembly](output-local-publication-and-metadata-assembly.md)
- [Hosted artifact transport lifetime](hosted-artifact-transport-lifetime.md)
- [Native ARC disk route](native-arc-disk-route.md)
- [Maintained input and schema boundaries](maintained-input-and-schema-boundaries.md)
- [External run references](external-run-references.md)
- [Remaining decisions](remaining-decisions.md)
- [Consumer acceptance scopes](consumer-acceptance-scopes.md)
