# Cluster observation and artifact attribution

**Status:** proposed feature design; no implementation or runtime acceptance.
**Date:** 2026-11-02

[ADR-29](adr/0029-cluster-observation-and-artifact-attribution.md) records the architectural decision.
This design owns the proposed workflow, reuse assessment, delivery slices and acceptance criteria.
It does not allocate a command, wire schema, digest family or approved collector/dependency version.

## User outcome

Select an exact NVKR build or Release, inspect an existing cluster, and find which deployed artifacts match
that build and its SBOMs. Save a snapshot and comparison, run upstream-owned validation against explicitly
identified subjects, and share independently verifiable evidence with readable results and TestGrid links.
The complete feature covers OCI images, Helm charts, OS images, and host packages/binaries.

The report answers three separate questions: what was produced or acquired, what was installed, and what
content was measured at observation time. It reports differences and unknowns as useful results. A successful
functional test cannot establish installed identity; an intact signature cannot establish a truthful host.

## Workflow and ownership

1. Authenticate the selected Run/Release and subject-bound evidence through existing lifecycle owners.
   Hold that exact baseline; a mutable tag or decoded release description is not acceptance. An admitted
   local diagnostic stays explicitly unsigned/unqualified and acquires no hosted publication identity.
2. Freeze the downstream deployment mapping and comparison policy. NVKE owns topology, installation,
   supported configuration and output applicability. Distinguish standalone outputs, contained dependencies,
   acquired artifacts, build-only tools and unused architectures; do not expect every SBOM row to be a Pod.
   Reuse NVKE's pinned deployment and component release declarations for this mapping where supported,
   extending the [existing import](nvke-pin-set-usage.md) rather than authoring a competing stack list.
   Those declarations describe intended installation; they do not prove what is running.
3. Discover the selected cluster scope, including unrecognized software. Save cluster/object/node identities,
   observation intervals and available resource versions, plus denied reads, unreachable nodes, exclusions
   and unsupported measurements. Do not describe several changing API reads as an atomic snapshot.
4. Compare observed subjects/content with the authenticated baseline under that frozen policy. Preserve
   installation-lineage gaps and ambiguous digest domains. Do not shrink the required scope after a failure.
5. Invoke selected upstream-owned checks through admitted artifact/cluster adapters. Record source/harness
   identity, tested subjects, selection, completeness, skips, original results and cleanup separately.
   Tests that allocate tenants or create resources require capacity, access and lifecycle ownership even
   when they use product APIs. Hardware provisioning remains downstream, outside the verifier.
6. Retain private originals and construct a reviewed shareable projection before signing. Package covered
   observations/results and a canonical file inventory bound to baseline, policy and target context.
   Verify all covered files and recompute comparison/coverage independently before displaying an accepted
   result. Public views disclose inaccessible observations; TestGrid links to the verified evidence.

Cluster observations do not change producer identities or the signed publisher SBOM. They introduce no
seventh authored public lifecycle input. They issue no conformance receipt or release authorization;
[ADR-16](adr/0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
continues to own the separate authenticated external-result qualification handoff.

## Attribution requirements

| Class | Expected and observed relationship | Limit that remains visible |
| --- | --- | --- |
| OCI | Authenticated image/index and native child joined to actual runtime identity, workload/container/node and architecture | PodSpec strings are discovery hints; an image identity does not measure writable layers, mounted executables or process memory |
| Helm | Exact archive and dependencies joined through an installation record to values, rendering inputs and live resources | A release version or stored manifest cannot retrospectively prove which archive was consumed; mutation/defaulting rules must be explicit |
| OS image | Delivered disk and applicable content inventory joined through authenticated boot/provisioning records to node measurements | Kernel/OS labels are not disk identity; hashing a running mutable disk cannot reproduce original QCOW2 bytes |
| Host package/binary | Exact package or binary and composition/file evidence compared with measured installed payloads | Package-manager names/versions alone are insufficient; generated files, configuration and package transformations need explicit policy |

Each required member/measurement returns matched, mismatched, not observed, unverified, or explicitly
non-applicable with a reason. Extra software remains visible and is evaluated by independent policy.
Overall match requires complete applicable coverage. A collector error, missing node, absent SBOM content
coverage, unsupported runtime identity or missing installation record cannot produce a complete match.
OCI/Helm delivery may land first, but it remains partial until OS/package acceptance is exercised.

Reuse NVKR's existing [CycloneDX evidence and signing owners](dependency-sboms.md) for subject-bound
build evidence. The observation/comparison is an additional record; it must not rewrite the publisher's
SBOM or introduce another SBOM implementation.

## AICR reuse assessment

AICR already contains reusable implementations. The inspected source is
[NVIDIA/aicr at 4d4e2e93](https://github.com/NVIDIA/aicr/tree/4d4e2e93bdfcec45b07fb3847ecae9c57903716f).
These are source/API findings, not proof that the selected CLI release exposes every API or that NVKR can
already consume those records. Do not implement another collector or evidence stack before evaluating them.

| Existing capability | Reuse candidate | NVKR integration still required |
| --- | --- | --- |
| Node snapshots | Public [snapshotter](https://github.com/NVIDIA/aicr/tree/4d4e2e93bdfcec45b07fb3847ecae9c57903716f/pkg/snapshotter) API and collector implementations | Preserve collector failures/omissions and target/interval binding; a successful measurement call is not complete coverage |
| Installed Helm/Argo inventory | Public [inventory](https://github.com/NVIDIA/aicr/tree/4d4e2e93bdfcec45b07fb3847ecae9c57903716f/pkg/inventory) readers | Caller-scoped chart/version inventory is not whole-scope discovery, original archive-byte lineage or comparison with current live resources |
| Evidence construction/publication | Public [attestation](https://github.com/NVIDIA/aicr/tree/4d4e2e93bdfcec45b07fb3847ecae9c57903716f/pkg/evidence/attestation) APIs | Existing inputs/subjects describe AICR recipe, snapshot and phase results; NVKR source Run/output/baseline bindings need an admitted adapter or upstream extension |
| Independent evidence verification | Public [verifier](https://github.com/NVIDIA/aicr/tree/4d4e2e93bdfcec45b07fb3847ecae9c57903716f/pkg/evidence/verifier) API | Evaluate NVKR-specific expected subjects, observation trust, coverage and comparison; do not accept an AICR recipe digest as an NVKR artifact identity |

The current AICR Kubernetes image collector normalizes PodSpec image references and deduplicates them by
image name. That is useful discovery, but it cannot establish the actual runtime/native-manifest digest
required here. Its OS collector covers OS/kernel configuration; it does not establish a delivered disk's
boot lineage or measure all installed package payloads. These gaps require attribution adapters, upstream
extensions or other admitted collectors, not a duplicate snapshot/evidence framework.

AICR's root [license](https://github.com/NVIDIA/aicr/blob/4d4e2e93bdfcec45b07fb3847ecae9c57903716f/LICENSE)
is Apache-2.0. That alone does not approve a linked dependency closure. Current
[contribution policy](../CONTRIBUTING.md#the-invariants-your-change-must-respect) directs AICR reuse through
reading/source adaptation rather than importing. Evaluate a pinned, bounded CLI adapter where its exported
contract suffices, or reviewed source adaptation with required notices. A direct Go-library integration is
an alternative requiring an explicit policy decision plus version, transitive-license, provenance,
compatibility and vulnerability review. This PR adds no module, vendored code or executable pin.

[Proposed pinned-tool producers](adr/0027-pinned-tool-producers.md) and the separate AICR bundle work address
build-time production of recipe bundles. They are related reuse paths, not implementations of live cluster
observation, installed-byte attribution or this runtime evidence contract.

## Collection and evidence trust

Start with scoped, read-only Kubernetes API collection. Host access or installing a collector is opt-in:
admit privileges, resource/time budgets and owned stop/join/cleanup before work. Reusing AICR does not
implicitly admit its agent jobs, privileges, credentials or network access. Never hide SSH or deployment repair.

Record whether facts came from authenticated APIs, deployment records or host assertions. Signed observations
attest to a reporter and exact bytes, not physical existence or a trustworthy compromised node. Stronger host
attestation is a separately admitted capability. Check freshness and target/baseline context to reject reuse
for another cluster or build; before/after samples do not establish continuously unchanged contents.

Exclude credentials, kubeconfigs, token-bearing configuration, private endpoints/server identifiers and raw
provider logs from the shareable projection. Document its relationship to private originals. A public consumer
can verify only accessible covered observations and must retain other claims as unverified.

Preserve a valid signed failed/partial report. Keep evidence integrity/authenticity, artifact comparison,
functional test verdict, coverage, cleanup and publication outcomes distinct. A raw JUnit reference or process
exit status alone cannot establish an exhaustive passing suite. Reuse upstream verdict/completion contracts;
do not copy expected test-name lists into NVKR. The verifier recomputes the comparison from covered facts,
not from a reporter's pass boolean. Public dashboards are presentations, not trust anchors.

## Delivery slices and acceptance

| Slice | Deliverable | Required evidence |
| --- | --- | --- |
| Observation contract and API snapshot | Admit scope, trust and collector results; capture workloads/nodes and unfamiliar software | Real selected cluster; denied reads, unreachable targets and non-atomic observations remain visible; no API-collector mutations |
| OCI/Helm attribution | Add authenticated native-image joins and chart installation-lineage adapters | Positive plus wrong subject/platform, mixed replicas, reused tags, mutated values/resources and absent chart-lineage negatives; explicit partial host coverage |
| OS/package attribution | Add admitted host collection and boot/image-to-node joins with file/package reconciliation | Supported-node positive and changed payload, missing boot record, incomplete inventory and unsupported collector negatives; no overall match with required gaps |
| Portable evidence and reporting | Emit, sign/publish, independently verify, inspect drift and link public/TestGrid views | Independent consumer round-trip; wrong signer/subject/target, tampering, replay and missing covered files fail; intact failed observations remain inspectable |

Full acceptance requires a supported NVKE stack with all four classes accounted for, a controlled changed
component detected, and an independent consumer verifying the same published evidence. Contract fixtures
establish protocol behavior; they do not establish live collection, publication, hardware or production acceptance.
No collector, dependency, runtime workflow or accepted result is added by this proposal.

## Review boundaries

NVKR owns build/evidence authentication and comparison. NVKE/downstream owners define deployment mapping,
installation/boot records and upstream test assertions. Collector ownership and reusable AICR interfaces need
review with those owners. Reconcile accepted changes at ADR-05/13/16/19/23/24 before authority-bearing
implementation. Current required-suite qualification ingestion remains separate work under ADR-16.

The architecture review must agree on observation trust and required coverage. The implementation review
must select and verify the actual reusable tool/library contract, its dependency/admission boundary, target
identity and supported host measurements. No arbitrary shell adapter, unreviewed schema, or new authority
restored from serialized reports is admitted by this document. No schedule, approved owner or ratification
is inferred from including the proposal in the roadmap.
