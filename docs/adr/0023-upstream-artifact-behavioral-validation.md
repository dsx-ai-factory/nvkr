# ADR-23 — Upstream-owned artifact test interfaces

- **Decision status:** Proposed (pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; draft code is not acceptance.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; local checks do not establish broader acceptance.
- **Deciders:** Pending; owners must be named before ratification.
- **Index:** Proposed ADR-23 supplements ADR-01–22; it remains outside the consolidated contract set until accepted.
- **Relates to:** ADR-03 (coded errors), ADR-13 (evidence), ADR-16 (conformance boundary),
  ADR-17 (resource lifetimes), ADR-18 (execution), ADR-19 (downstream ownership), ADR-20 (artifact subjects),
  ADR-21 (generation), ADR-22 (source pins).
- **Scope:** How NVKR reuses NVKE tests against its produced CLI, controller, and chart artifacts.
  CI triggers, build/test transport, runner allocation, test-job credential scoping, and release
  qualification are separate decisions. No CI or infrastructure lane is implemented by this ADR.

## Context

NVKE owns the existing test suites; NVKR produces artifacts from its pinned sources. Source unit tests
call functions compiled from source, so they do not validate the produced executable. Process-level
contracts can exercise that executable. We need to reuse upstream assertions without copying suites
or changing NVKE's normal build-and-test workflow.

## Decision

The source and tooling dependency is one-way: NVKR consumes NVKE's sources and test interfaces.
NVKE's build and test workflows must remain independent of NVKR code, recipes, Plans, and tooling.
NVKE's artifact-test entrypoints accept externally built artifacts through a generic interface usable
by any producer.

1. **Keep tests upstream.** NVKE owns assertions, fixtures, and test entrypoints. NVKR requires upstream
   artifact-test entrypoints that accept externally produced artifacts while preserving the upstream
   default build-and-test behavior. Separating construction from execution lets NVKR test its own
   artifact with those assertions. NVKE decides how to adopt that interface in its own repository.
2. **Make external artifacts explicit.** Artifact-test entrypoints require the supplied binary, image,
   or chart and expected build metadata. They reject missing inputs and never rebuild the component under
   test. Every reference to a component under test inside the environment, including chart image values,
   must resolve to the supplied digest or the run fails. Existing commands retain their default behavior.
3. **Use the matching revision.** NVKR imports tests from the source revision recorded for the artifact.
   This reuses the existing build pin; it introduces no independently maintained test-suite pin.
4. **Reuse Plan interpretation.** NVKR delegates complete Plan validation and toolchain selection to
   its existing Go owners. This keeps platform-specific image selection and permitted registry
   relocation in one implementation rather than reconstructing those rules in the test adapter.
5. **Share local and CI execution.** Both use the same adapter interface, upstream entrypoints, and
   assertions; input locations and infrastructure setup may differ. Initial orchestration remains a
   trusted-input diagnostic; source import and execution must use the owning Go boundaries with
   ADR-03 coded errors and capability admission before production evidence integration. CI integration
   remains deferred.
6. **Identify what was tested.** Record the exact named output, build digest, test revision, suite and
   result. For a binary, record the SHA-256 of the executed bytes; for images, record the ADR-20 OCI
   subject and executed platform child; for charts, record the chart OCI manifest and package bytes.
   Reject changed artifact bytes and unavailable, skipped, zero-test or incomplete required suites.
   Bind tested bytes to the Run's recorded output before evidence-bearing integration. Internal-function
   unit tests remain source tests, reported separately from tests that execute supplied artifacts.

The proposed execution flow is: build and retain the artifact and its Plan; validate and select test
inputs; import the matching upstream suite; then execute it against the supplied bytes.

### Incremental bootstrap diagnostic execution candidate

The stacked IV1 candidate reuses the existing upstream rootful native Linux AMD64
bootstrap environment for development and verification. Factory retains complete
Plan admission, named-output selection and private executable staging; sourcegit
retains matching commit/tree import; pipeline retains owned subprocess execution
and cancellation. Existing CLI suites retain their offline Docker execution.
Upstream continues preparing its fixtures and controlling networking/assertions;
the surrounding existing workflow/environment supplies host services and its
supporting controller image. This does not define new infrastructure, runner
allocation, production or qualification policy.

The candidate external-artifact target and optional `nvke.dev/bootstrap-e2e/v1`
terminal report are producer-neutral. The consumer requires the terminal marker,
unchanged executable observations and matching source interface. Missing or
unexecuted required results cannot pass. The report's `cleanup=unknown` preserves
the existing suppressed-inner-teardown limitation; suite success cannot imply
complete cleanup or close IV1-A4. Real native AMD64 fresh-build execution and its
negative/cleanup matrix remain prerequisites for acceptance, recorded separately
from contract tests in reconciliation.

## Consequences

NVKR depends on NVKE adopting and maintaining the required interface at the artifact's source revision.
The upstream adoption decision belongs to NVKE. Results here are explicitly unqualified diagnostic output
with no authority-bearing evidence role: never a ConformanceReceipt, ADR-13 evidence, or qualification
input. Passing behavioral tests does not authenticate provenance or establish release qualification.
ADR-16's independently produced conformance results remain a separate, passive qualification handoff.

## Alternatives considered

- Copy suites into NVKR: creates duplicate ownership and assertion drift.
- Run only source unit tests or version smoke checks: insufficient artifact validation.

## Follow-ups

Milestones: local CLI proof and maintained-pin adoption; CLI/controller/chart validation in kind;
merge-triggered CI using the same interface; CAPD lifecycle coverage; then CAPNICo lanes.
Future acceptance must use upstream-owned suite definitions, with full-run positives and filtered/missing-case negatives.
Before evidence integration, exercise both native CLI outputs and reject wrong Run/output/revision/platform
bindings, substituted bytes and sibling reuse despite equal Build Digests.
Later adapters inherit [ADR-17](0017-cli-sdk-foundation-and-package-topology.md)'s timeout, cancellation,
failed-suite cleanup and owner Close obligations. At adoption, link diagnostic vocabulary and test strategy
from [CONTEXT](../../CONTEXT.md) and [SADD](../sadd.md); ADR-23 stays outside the baseline until accepted.
Detailed orchestration, credential isolation and runner policy belong in follow-up decisions.
Managed Run workflows remain generator-owned under ADR-21.
