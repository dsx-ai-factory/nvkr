# ADR-19 — Artifact membership and downstream deployment ownership

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-19 in the consolidated NVKR ADR set (artifact membership and downstream ownership).
- **Relates to:** ADR-05 (ReleaseSet / Inventory), ADR-12 (targets), ADR-16
  (release gates), ADR-14 (delivery), ADR-18 (build pipeline).

## Context

NVKR produces and verifies artifacts. The maintained NVKE Definitions require exact output membership,
pinned producers, packaging and evidence, but do not require a deployment topology. The former cluster-view
model added node roles, categories, declared Kubernetes/OS context, and generated composition records without
an implemented provisioning consumer or runtime-compatibility proof.

## Decision

### 1. Definition owns required outputs

A Definition selects named outputs, their exact producers, concrete target platforms, artifact formats and
byte-changing packaging relationships. There is no authored cluster view, tenant/operator type, node role,
role affinity, deployment category, cluster context, CNI declaration or cluster validation-requirement list.
Different deliverable sets can use separate Definitions. No replacement grouping object is introduced.

Each output remains an independent required use even if another output has the same Build Digest. Preparation
freezes the exact required set; execution and hosted finalization join producer results, terminal observations,
artifacts and evidence by output name and build identity. Missing, extra, duplicated or substituted outputs fail
closed. Removing the topology model does not remove these generic completeness checks.

An aggregate image still occupies one required named output. Its exact platform set is authored and frozen;
each native child carries an independent build/result/evidence use. Run observations retain both native
manifest and scanned archive identities. Completeness cannot shrink the platform set to whatever succeeded,
and read-only descriptions return independent copies of those requirements and observations.

### 2. Release owns exact artifact membership

Named artifacts retain their observed payload digests. Canonical release content binds the complete named
artifact set. The enclosing publication root carries the exact output, component/build and evidence bindings,
including sibling repository/digest references. Its transport identity is distinct from content identity and
cannot appear as one of its own content members. Consumers authenticate and recompute the exact membership
before accepting outputs. Diagnostic executable pulling separately checks the complete metadata membership and
selected payload integrity without granting signature or policy acceptance (ADR-20). Names inside publication
records cannot select arbitrary destination paths.

No cluster composition or realization is emitted or published. Definition v5, Run/Release v4 and publication
root v4 reject earlier topology-bearing fields rather than dropping them during decoding. Retired cluster
identity families remain reserved and cannot mint new identities. Historical signed bytes retain their original
meaning; current readers reject pre-v4 publication roots rather than granting a compatibility alias or authority
to a decoded legacy description.

### 3. Downstream systems own deployment

NVKE or another downstream owner decides topology, node placement, deployment timing, installation, Helm
application and reconciliation using accepted artifact references. NVKR has no provisioning-adapter interface or
cluster qualification result. Its `nvkr.dev/release-description/v1` export describes the complete immutable
publication using the same versioned JSON contract as release browsing. Exact output names and membership,
artifact kinds, platform declarations, aggregate children, chart versions and output-bound source/evidence
records are retained without translating component names into deployment roles or selecting one platform.
Multiple repositories and revisions remain distinct facts; a consumer decides which combination it accepts.
No Plan, mapping file or producer workspace is required alongside the pinned publication reference.

Consumers own artifact selection, deployment-role mapping, installer formats, source compatibility and
offline dependency completeness. These concerns do not become new Definition fields or an NVKR-owned
installation schema. The description reports metadata inspection separately from the publisher's declared
qualification and establishes no payload, signature, installation or release-policy acceptance. It owns no
new build or release identity. See [release navigation](../release-navigation.md) for the data contract and
the [NVKE handoff reference](../nvkr-to-nvke-demo.md) for that consumer's separate deployment boundary.
This decision reserves no unused runtime interface or qualification record.

Signer and authenticated-receipt roles remain mandatory security controls. Existing qualified release-policy
compatibility and conformance receipts retain their exact subject, signer, policy and expiry checks. They do
not gain deployment or runtime claims from removing cluster descriptions.

## Consequences

Authors describe the artifacts they need once. The compiler and consumer maintain exact release membership
without a parallel deployment taxonomy. NVKR no longer authenticates a tenant/operator grouping or asserts node
placement intent; downstream consumers that need such descriptions own them explicitly.

## Acceptance evidence

Contract checks must reject removed topology fields before output or subprocess work and preserve required-output
and consumer-substitution negatives. Repository validation is tracked in reconciliation. Earlier cluster fixture tests are
not retained as an unused product interface. No external provisioning or runtime qualification is claimed.
