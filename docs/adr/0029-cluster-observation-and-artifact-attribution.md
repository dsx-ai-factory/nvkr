# ADR-29: Connect cluster test results to the artifacts tested

- **Decision status:** Proposed; pending owner review and ratification.
- **Date:** 2026-11-02
- **Implementation maturity:** Design only; no implementation is added by this proposal.
- **Acceptance-test maturity:** Not tested on a live cluster.
- **Deciders:** Pending; NVKR evidence and downstream deployment/test owners must be named.
- **Index:** Outside the consolidated ADR-01–22 contract set until accepted.
- **Relates to:** ADR-01 (identity), ADR-05/13 (evidence), ADR-16 (qualification), ADR-17/19
  (lifecycle and deployment ownership), ADR-20 (publication), ADR-23/24 (upstream tests).

## Context

A user wants to validate a build or run tests against a cluster. To trust those results for that build,
they need to know that **the software being tested is actually the software NVKR built or acquired**.
A passing test against a different image or binary does not validate the selected artifact.

Capture the tested environment, compare it with the selected artifacts and their SBOMs, and keep that
evidence with the test results. A cluster snapshot makes this connection inspectable and verifiable.

## Decision

**Connect validation results to the selected artifacts through recorded cluster observations and
comparisons. Integrate AICR through its Go APIs behind an NVKR adapter, subject to dependency-policy
approval. Add missing measurements or installation records rather than another collection framework.**

For example, NVKR built controller image A. The cluster reports the same version but runs image B.
Keep the functional test result, but do not report that it validates image A. Show the mismatch and
which environment the test actually exercised.

### Who does what?

- **AICR Go APIs:** call `Client.CollectSnapshot` / `Client.LoadSnapshot` from `pkg/client/v1` for
  snapshots, and `Read` / `ReadReleases` from `pkg/inventory` for installed-chart records. An NVKR adapter
  owns these calls and preserves the observations. Use Go libraries, not the AICR CLI. The
  [integration design](../cluster-observation-and-artifact-attribution.md#proposed-integration-go-apis-behind-an-nvkr-adapter)
  links the exact upstream APIs and identifies the measurements they still lack.
- **Deployment owner:** installs the software, defines where each output should appear, and supplies
  records connecting artifacts to installations and nodes.
- **Upstream artifact owners, including NVKE:** own test assertions, fixtures and entrypoints. NVKR
  invokes these through the existing test interfaces.
- **NVKR:** verifies the selected Run or Release and each output's evidence, compares observations with
  those exact artifacts, and connects the comparison to the suite, test execution and results. Reuse the
  existing SBOMs and deployment declarations.

A snapshot is an additional observation linked to the build. It does not change the build identity or
publisher SBOM, introduce another authored lifecycle input, or move deployment ownership into NVKR.

### What do we compare?

| Artifact | Evidence needed |
| --- | --- |
| OCI image | The actual running image identity, compared with the built image for that platform. A tag is insufficient. |
| Helm chart | A verified installation record identifying the exact chart archive, plus its values and rendered/live resources. Chart version alone is insufficient. |
| OS image | A verified boot or provisioning record connecting the delivered image to the node, plus supported node measurements. An OS label is insufficient. |
| OS package or binary | Installed file measurements compared with the build's package/file evidence. Package version alone is insufficient. |

The detailed design defines platform digest matching, chart changes, mutable disks and package/configuration
rules. An image identity match alone does not prove unchanged writable files or process memory.

### What must the report tell the user?

For each expected item, show **matches, differs, not found, cannot verify, or does not apply**, with a reason.
Show unexpected software too. Agree on required nodes, outputs and measurements before collection; do not
reduce them after a failure. Embedded libraries and build-only tools are not expected to become separate Pods.

An unreachable required node or a missing installation record leaves a visible gap. Report partial coverage
as partial. Full coverage must account for all four artifact classes; consumer policy decides what is required
and whether extra software is allowed.

### How do we collect and share it?

`CollectSnapshot` deploys an agent Job; it is not a read-only API call. It requires explicit permission
and cleanup. `LoadSnapshot` can read a captured local file without deploying an agent. Start with read-only
Kubernetes inspection; opt into agent collection separately. Host access and tests that create resources
need explicit permissions, limits and owned cleanup. Record which objects and nodes were observed, when,
by which collector, and what failed. A snapshot spans an interval; it cannot prove continuously unchanged state.

Keep private originals. Prepare a shareable copy without credentials, kubeconfigs, private endpoints/server
identifiers or provider logs before signing. State what was omitted and cannot be independently checked.

The signed evidence identifies the selected build, cluster, comparison rules, coverage and included files.
Someone verifying the report chooses the expected cluster, build and comparison rules, which signer to
trust, and how old observations may be. They verify the signature and included files, then repeat the
comparison and coverage checks. A signed “pass” label alone is insufficient. A signature identifies the
reporter; it does not prove that a compromised node reported truthfully. Unsigned local diagnostics remain
unsigned and unqualified.

Bind each test execution to its selected artifacts, suite revision, target cluster and observation intervals.
A claim that the selected artifacts passed validation requires both the upstream test verdict and all
required identity/coverage checks. Diagnostic tests may still run when identity cannot be established,
but their results must retain that limitation. Keep comparison and cleanup outcomes separate. Reuse the
artifact-test interfaces in [ADR-23](0023-upstream-artifact-behavioral-validation.md) and the validator
catalog and adapter contracts in [ADR-24](0024-imported-validator-catalog.md). Artifact tests consume
supplied artifacts without rebuilding them. Cluster checks keep their own required inputs and result contracts.
Public pages and TestGrid link to the verifiable evidence. Failed and partial reports remain useful,
but cannot satisfy a required passing policy.

## Consequences

Users can see which artifacts a validation result covers and what still needs proof. Historical installations
may remain unverifiable without installation or boot records. AICR reuse reduces duplication, but its current image
summary and chart inventory do not supply all the evidence required here.

The [feature design](../cluster-observation-and-artifact-attribution.md) defines the detailed requirements,
AICR source assessment, worked examples and delivery/acceptance plan. Integration choices still require
pinning, license, dependency and security review. The proposed Go-library integration needs an explicit
exception to current contribution policy before implementation; this ADR does not grant that approval.
No dependency is added here.

This is a proposed design, not an implemented capability. Commands and schemas remain undecided.
It grants no release approval or qualification receipt; [ADR-16](0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
continues to own that separate decision.

## Alternatives considered

- **Compare versions only:** simple, but misses replaced images and changed files.
- **Use passing tests as proof:** checks behavior without establishing what software was tested.
- **Put deployment topology in Definition:** contradicts the deployment boundary in
  [ADR-19](0019-clusterdefinition-and-provisioning-adapter-boundary.md).
- **Require hardware attestation first:** provides stronger host trust, but delays useful API-based checks.
  Keep their limits visible and add stronger attestation separately.
