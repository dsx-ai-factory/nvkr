# ADR-19 — Artifact membership and downstream deployment ownership

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation/README.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation/README.md`; green unit tests alone do not
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
Proposed [ADR-26](0026-definition-composition.md) designs a composite Definition, implemented and contract-tested only,
whose Release membership is the exact union of complete member Definitions' named outputs; it selects,
overrides and deduplicates nothing.

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
before accepting outputs. Diagnostic output pulling separately checks the complete metadata membership and
selected payload integrity without granting signature or policy acceptance (ADR-20). Names inside publication
records cannot select arbitrary destination paths.

No cluster composition or realization is emitted or published. Definition v5, Run/Release v4 and publication
root v4 reject earlier topology-bearing fields rather than dropping them during decoding. Retired cluster
identity families remain reserved and cannot mint new identities. Historical signed bytes retain their original
meaning; current readers reject pre-v4 publication roots rather than granting a compatibility alias or authority
to a decoded legacy description.

### Internal tools are required Run artifacts

**Lane and maturity:** This contract applies to ordinary local and pipeline-attested Runs. The
supported graph, execution and receipt integration are recorded in reconciliation. Native hosted
inspection currently requires one disk consumer and its tool production in the same original owner;
other consumers, composite tool merging and cross-job capability transport remain deferred. It grants
no qualified execution, reproduction or new publication capability.

A Run may require catalog-produced tools that are consumed during execution but are not delivered.
These are ordinary pinned component producers with an internal role, not named product outputs or
another public lifecycle object. The same contract covers an inspection executable, a released static
tool such as crane or nfpm, and a built VM runtime image. Each uses its existing producer and pin
contracts; its internal role does not excuse dependency, license, SBOM or provenance requirements.

The compiler freezes an explicit internal-tool requirement for every selected use before work. Each
requirement binds the producer, its complete selected input closure, native execution platform, result
form, consumer and purpose. All tool and consumer edges belong to the same validated acyclic Plan.
Missing, ambiguous, unused or cross-platform selections and cycles fail admission. Resolution cannot
invent an unselected producer, and a tool cannot satisfy its own construction or evidence dependency.
Bootstrap inputs remain explicit pinned dependencies; a cached path is not a bootstrap authority.
An internal tool may select a pinned SBOM scanner from its own catalog for its native execution platform;
an omitted selector inherits the Definition scanner from the Definition catalog only when its platform
matches. Conflicting pins for one scanner name fail admission. This evidence selection belongs to the
Plan and does not change the tool producer's logical Build identity.

Internal results occupy a distinct membership set from named delivery outputs. Run completeness requires
both sets: every required internal tool must finish, be observed, and have its exact evidence joined to
every selected use. A failed, canceled, absent, substituted or unverified tool fails Run completion even
when all delivery producers succeeded. Equal Build or payload digests do not satisfy a different required
use. Shared production may avoid duplicate execution only when the owner authenticates each use against
the same admitted producer result; it cannot collapse membership or evidence obligations.

Only the owning execution module may issue a tool-use receipt from authenticated producer bytes and
evidence. The opaque receipt binds the Plan, Run attempt, exact producer requirement, platform, result
digest and size or complete image closure, and consumer purpose. An exported description, path, digest,
serialized result or caller assertion cannot mint that receipt. Cross-job transport must authenticate
the original producer result and its exact bytes before issuing a local consumption capability. Mutable
names and paths cannot select execution bytes. Admission, immutable staging, pre-use verification,
cancellation and cleanup belong to the consuming owner and retain its sandbox restrictions.

Tool production has its own logical Build identity. A tool used to construct consumer bytes is an
explicit logical input of that consumer Build, including its selected producer identity and invocation
binding; PATH and Environment cannot supply that build-semantic choice. An observation-only tool follows
ADR-13 instead: its identity is bound in the Plan, Run and observation receipts without changing the
observed producer's logical Build. Both kinds retain exact observed tool bytes in Run evidence. No
execution observation retroactively rewrites a frozen logical identity.

Release and publication membership remain exactly the named delivery-output set. Internal tools must
not appear as delivered payloads, member repositories, routes or consumer-required artifacts. Their
bindings may appear as provenance inputs or Run evidence without becoming delivery members. Evidence
changes can change enclosing signed/publication identities; unchanged membership does not promise
unchanged manifest bytes. Internal artifacts cannot be promoted to delivery by an executor flag or
omitted from Run completeness because publication excludes them.

There is no separate prepare, exception, staleness or publish lifecycle for these tools. Their
production, evidence and failure are part of the ordinary Run. A future reuse capability must satisfy
the same exact requirements through its authenticated owner; this contract grants no reuse authority.

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

Internal-tool acceptance must cover failed and missing producers, wrong architecture, changed helper
bytes, changed runtime images, mutable-path substitution, incorrect consumer binding, graph cycles,
and incomplete cross-job evidence. Tests must show that internal-tool failures prevent Run completion
and that successful internal tools never enlarge Release or publication membership. Executable injection
must preserve non-root, network-disabled execution, read-only subjects, scratch bounds, cancellation and
the consuming owner's exact receipts. Native execution evidence is separate from these contract tests.
