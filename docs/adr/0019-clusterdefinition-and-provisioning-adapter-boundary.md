# ADR-19 — Cluster views, realization & provisioning boundary

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-19 in the consolidated NKD ADR set (added to close the composition→cluster realization gap).
- **Relates to:** ADR-05 (ReleaseSet / Inventory), ADR-12 (categories, role/clusterType composition), ADR-16
  (release gates), ADR-14 (delivery), ADR-18 (build pipeline).

## Context

Authors need to describe how reusable recipes form a tenant or operator cluster, inspect it before artifacts
exist, and identify the exact delivered realization. Those are states of one cluster view. A second authored
topology document duplicates intent without supplying release authority. NKD defines and verifies inputs;
downstream engines own provisioning and fleet operation.

## Decision

### 1. One authored cluster view

A Definition contains optional named cluster views. Each view names its cluster type, concrete Kubernetes/OS/
architecture frame, CNI, selected named outputs, node-role subsets, cluster-level categories and validation
requirements. Node roles and cluster type belong to this view, not Recipe selection. Resolution produces an
immutable composition binding exact Recipe Digests and selected components.

AICR views also bind exact activation plus deployer, storage class, accelerated-node selectors/tolerations and
workload selectors. Output-affecting activation/generator inputs enter Recipe identity (ADR-12); deployment
choices enter composition identity. Labels or matching names cannot replace these bindings. Before execution a
composition makes no artifact, qualification or clearance claim and requires no invented ReleaseSet digest.

### 2. Generate the exact artifact realization

Release construction binds the exact composition to its release-content subject and exact selected artifacts.
Validate recipe/component/role/platform membership, AICR inputs and required outputs. A composition can have
multiple realizations; attaching a release digest to copied topology is insufficient. Neither a nullable release
reference nor a caller-set readiness flag is an authority transition.

Identity order is noncircular: named Artifacts retain their observed payload digest; canonical release content
binds payload membership; cluster realization binds that content and exact composition; the enclosing OCI root
carries those records and payloads. Evidence targets the content subject without including itself in that
subject. Route verification checks the enclosing published digest separately. Consumers recompute composition
and content identities and exact member joins before materialization. Storage paths are verifier-owned; names
inside a publication cannot select arbitrary filesystem locations.

The standalone cluster authoring CLI is removed. Product authors declare cluster views inside a Definition.
Internal composition and qualified publication records are descriptions until the owning consumer authenticates
them against exact content and authority; their presence does not create another public authoring workflow.

### 3. Provisioning belongs to downstream engines

NKD owns cluster intent and exact artifact relationships. A downstream adapter creates an engine-specific
Projection; the engine owns instance counts, instance types, site/provider bindings, concrete test selectors,
deployment timing, node installation, Helm deployment and reconciliation. NKE, Cluster API or another engine
may consume the same accepted view. NKD neither initiates provisioning nor operates qualification suites.

An operator/management cluster is another named view with `clusterType: operator`; the same boundary applies to
it and tenant clusters. Determinism means the same intent resolves the same pinned identities and requirements,
not a byte-identical running cluster. Authorization to mutate infrastructure belongs to the downstream system.

### 4. Post-clearance cluster qualification — deferred

An external harness independently provisions/tests and signs an observation using the passive ADR-16 protocol.
A future `QualificationResult` binds exact cluster realization (and, for the legacy wire, both
`clusterDefinitionDigest` and `releaseSetDigest`), Inventory, sorted vector digests, suite/policy, verdict,
exact opaque results digest, harness/environment digests, and operator-supplied effective/expiry epochs.
Its new `nkd.qualification-result` family and qualification role require implementation; no such result or
production handoff exists today.

The role is distinct from build, publish, conformance and clearance. This downstream composition claim is
**not a release gate and cannot authorize promotion**. Release conformance stays exhaustive over its ReleaseSet.
Coverage must satisfy the view's validation requirements under ADR-05's governed coverage profile. Dormant
e2e/cluster-validation evidence slots may bind this later result only, never the published reproduction vector.
ADR-20 registration may reference it only with exact composition-and-release subject equality. The v1
recipe/evidence gate is a different claim and does not satisfy this contract.

## Consequences

Authors maintain topology once; consumers inspect the same named view before and after production. The cost is
an exact compiler/verifier-owned binding, rather than aliases between different wire schemas. Qualification and
registration remain optional downstream contracts, and neither expands the seven-object public model.

## Acceptance evidence

Composition and lifecycle contract tests cover exact membership, role reuse, concrete frame, AICR inputs,
substitution, and complete amd64/arm64 GPU executable/APT/Helm publication closure. Consumer tests verify the
same payload identities across Run and Release and reject forged bindings. These use controlled producers and
do not prove a real provisioning engine or qualification harness ran.

External acceptance requires independent downstream adapters consuming the same authenticated view, refusing
uncleared/wrong-member inputs before provisioning, and an independently signed harness round trip. Negative
qualification cases include unsigned/boolean observations, wrong subject or signer role, inadequate coverage,
expired epochs, and attempted use of cluster qualification to authorize release promotion.
