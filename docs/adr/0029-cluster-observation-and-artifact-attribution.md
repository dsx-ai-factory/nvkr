# ADR-29: Cluster observation and artifact attribution

- **Decision status:** Proposed; pending owner review and ratification.
- **Date:** 2026-11-02
- **Implementation maturity:** Design only. This proposal implements no collector, comparison,
  evidence publisher or new qualification capability.
- **Acceptance-test maturity:** None for this proposal; existing diagnostic checks do not establish acceptance.
- **Deciders:** Pending; NVKR verification/evidence and downstream deployment/test owners must be identified.
- **Index:** Proposed ADR-29 is outside the consolidated ADR-01–22 contract set until accepted.
- **Relates to:** ADR-01 (identity), ADR-05/13 (evidence), ADR-16 (qualification), ADR-17 (lifecycle ownership),
  ADR-19 (deployment ownership), ADR-20 (publication), ADR-23/24 (upstream tests and artifact bindings).

## Context

A verified build establishes artifact identity and origin, not installed location or current contents.
A healthy cluster can still run different artifacts.

[ADR-19](0019-clusterdefinition-and-provisioning-adapter-boundary.md) leaves installation and topology
with downstream systems. [ADR-16](0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
permits selected OCI workload diagnostics without complete deployed coverage or qualification authority.
Broader observations need attribution rules that preserve this boundary.

## Decision

**We will represent cluster state and its comparison with an authenticated build as separate, scoped
observations.** Every attribution must join an authenticated expected artifact to identified observed
objects or nodes through a supported, explicit measurement or deployment record. Unknown joins remain unknown.

### Baseline and deployment ownership

The baseline comes from the exact Run/Release and subject-bound SBOM/provenance, authenticated by the existing
lifecycle owners under independently selected policy. A decoded release description or SBOM is insufficient.
An admitted unsigned local diagnostic retains its weaker trust level and cannot claim authenticated publication.

NVKE or another deployment owner supplies the applicable output-to-deployment mapping and expected cardinality.
The comparison freezes and records that mapping and its policy. Build-only tools, unused architectures and
contained dependencies retain their distinct roles; they are not all expected to appear as separate workloads.
Required applicability cannot be silently reduced after collection fails. Built, acquired and repackaged
artifacts remain distinguishable.

A snapshot is associated with existing Run/Artifact/Release identities, not a seventh authored lifecycle
input or installation schema. It changes no existing Build, Release, SBOM or provenance bytes.

### Discovery and attribution

Discovery inventories the selected cluster scope, including unrecognized software, and records exclusions,
denied reads, unreachable nodes and unsupported measurements. Observations bind cluster/object/node identities,
collector identity/version, interval and available resource versions. Collection is not atomic;
inconsistent reads remain visible.

| Artifact class | Required attribution |
| --- | --- |
| OCI image | Join the actual runtime identity to the authenticated image subject and native platform child using explicit digest semantics. Tags and version labels are hints. |
| Helm chart | Bind the consumed chart archive to a deployment-owner installation record, values and rendered resources; compare live resources under explicit mutation/defaulting rules. Stored manifests alone cannot recover archive identity. |
| OS image | Bind the delivered disk to a node through an authenticated provisioning/boot record and supported measurements. An OS version is insufficient, and a running mutable disk is not the original QCOW2 byte stream. |
| OS package or host binary | Compare measured installed payload files with subject-bound package/file evidence, using explicit rules for configuration, generated files and package transformations. Package names and versions alone are insufficient. |

All four classes are required for the complete feature. Missing lineage, SBOM coverage or host measurements
prevents the corresponding claim; an OCI-only implementation cannot report complete cluster attribution.

### Claims, coverage and trust

Separate **produced/acquired identity**, **installation attribution**, and **measured runtime content**.
An image digest does not prove unchanged writable layers, mounted binaries, configuration or process memory.
Retain unsupported measurements as gaps.

Each required comparison returns matched, mismatched, not observed, unverified, or explicitly non-applicable,
with supporting observations or a reason. Extra software remains visible; independent policy decides whether
it is allowed. An overall match requires every applicable required member and measurement to succeed.
Failed discovery, unknown identities or incomplete coverage cannot become a complete match.

Kubernetes API collection is read-only by default. Host collectors and test-driven mutations require separate
admission of access, privileges, resource ownership, bounded execution and joined cleanup. Collection does
not silently install collectors, use SSH, repair or provision resources. Authenticated APIs and signed
reports identify observation sources, not trustworthy contents on a compromised node. Independent host/hardware
attestation is outside this decision.

### Reuse before new implementation

We will prefer existing admitted collectors, inventory readers and evidence tooling over another implementation.
AICR has public snapshot, inventory, attestation and verifier packages; the
[feature design](../cluster-observation-and-artifact-attribution.md#aicr-reuse-assessment) records their fit
and missing guarantees. A successful collector call cannot hide omitted measurements, and an AICR recipe
subject cannot stand in for an NVKR Run/output subject.

Under current contribution policy, pinned subprocess integration or reviewed source adaptation are candidates;
direct Go imports require a separate explicit dependency-policy decision. Adoption requires pinned source/tool
identity, license and transitive-dependency review, compatibility checks and required security gates. This
proposal adds no dependency or tool admission. New code should implement only uncovered attribution contracts.

### Validation and portable evidence

Reuse [ADR-23](0023-upstream-artifact-behavioral-validation.md) and
[ADR-24](0024-imported-validator-catalog.md) for upstream-owned tests and explicit artifact bindings.
Tests remain upstream; deployment and hardware lifecycle remain downstream. Cluster adapters need explicit
admission. Record test subjects, suite/harness revision, selection and coverage. Functional verdict, capture,
artifact comparison and cleanup outcome remain separate. Before/after snapshots establish no continuous proof.

Retain original private observations and results. Define a shareable projection before signing; never redact
already-signed bytes. Exclude credentials, kubeconfigs, token-bearing values, private endpoints/server
identifiers and raw provider logs; document the projection's relationship to private originals and its limits.
A portable observation binds the exact baseline, mapping/policy, observed identities,
coverage, outcomes and an inventory of covered file digests and lengths. Structured identity uses ADR-01;
wire schemas and any new digest family require explicit registration before implementation.

A consumer independently selects the expected baseline/subjects, signer/issuer and applicability/freshness policy,
authenticates the signed observation, verifies every covered file, and recomputes comparison and coverage
against that baseline/policy from the covered observations. A signed overall verdict cannot replace this
evaluation; test verdicts retain their upstream suite contracts. The consumer checks
cluster, baseline and observation context rather than accepting a replay for another target. A locator or
reporter-supplied digest/pass boolean supplies no authority. Public summaries and TestGrid reference the same
verified evidence and disclose visibility/coverage limits; neither is the trust anchor.

A signed failed or partial observation remains useful, but cannot satisfy a required matching/passing policy.
This decision issues no qualification receipt or release authorization. ADR-16 retains ownership of the
separately reviewed, independently authenticated external-result handoff.

## Consequences

Consumers can distinguish build, installation and measured content claims. Healthy historical installations
may remain unverified without lineage. Full coverage requires chart/boot records and host observers.
Signatures authenticate the reporter's claims, not physical cluster existence or host integrity.

The [feature scope and delivery plan](../cluster-observation-and-artifact-attribution.md)
owns detailed workflow, verification cases and implementation sequencing. This ADR selects no CLI, collector
deployment mechanism, transport implementation or wire schema and does not establish operational acceptance.

## Alternatives considered

- **Match tags, versions or labels:** simpler discovery, but cannot establish artifact or installed-byte identity.
- **Treat passing tests as attribution:** useful behavioral evidence, but does not prove what software was tested.
- **Put deployment topology in Definition:** combines build and deployment ownership and contradicts ADR-19.
- **Require host attestation for every observation:** stronger against compromised hosts, but excludes useful
  API-based diagnostics. Report the observation trust boundary explicitly and admit stronger attestation separately.
