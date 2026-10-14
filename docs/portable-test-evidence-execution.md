# External test output contract

This is a preparatory interface reference for external test producers. It defines how a producer
can describe its original result files and available metadata. It provides no runner, result parser,
file collector, publication, signature verification or qualification integration.
[Reconciliation](adr/reconciliation.md) owns implementation and disabled-path status.

## Producer boundary

A producer returns an output description with references to original JUnit XML, CTRF JSON, or both.
Each reference names the format and original file and records its exact byte digest and length.
The references are descriptions: validating them does not open the file, recompute its digest or
establish that the producer ran. A future collector must independently check the original bytes
before treating a reference as verified integrity. No conversion or rewriting of a result file is
part of this contract.
Independent JUnit/CTRF reports need no conversion link. A converted report can declare `DerivedFrom`
matching another included original report reference; this change performs no conversion or normalization.

The Go contract owner is `pkg/nvkr/testresults`: `Producer.Produce(context.Context) (Output, error)`
returns `Output` with schema `nvkr.dev/test-results-output/v1`, `ProducerIdentity`, `SuiteSelection`,
optional `HarnessIdentity`, original `Reports []ReportReference`, `Environment []EnvironmentFact`
and an optional NVKR `Binding`. Suite name/selection/revision and harness name/version stay separate.
`Output.Validate()` checks the description only. A standalone producer needs no NVKR binding;
a supplied binding reuses `lifecycle.ExactInputDescription` and `sign.Subject` descriptions without
authenticating their input identities or exact subjects.
The interface supplies no concrete producer, Kubernetes runner or cluster provisioning capability.
Implementations own validated suite/harness configuration before `Produce(ctx)`, including arguments,
permitted environment settings and access requirements. Raw credentials do not belong in this output.
Execution configuration and credential scoping remain deferred to concrete adapters.

The proposed upstream-owned artifact-test direction in
[PR139 / ADR-23](https://github.com/dsx-ai-factory/nvkr/blob/4025c7f602a8413f145165058e7df9493f4df153/docs/adr/0023-upstream-artifact-behavioral-validation.md)
keeps assertions upstream, selects the artifact's matching test revision and delegates Plan interpretation
to existing Go owners. This output contract supplies no competing execution or source-selection interface.

Metadata describes producer, suite/harness and scoped environment facts with `before`, `during`,
`after` or `unknown` phase. Status distinguishes `declared`, `observed`, `unknown` and `unavailable`;
unknown/unavailable metadata carries no value. Missing information remains absent or explicitly unknown. Optional NVKR bindings do not fabricate a producing Run or attempt.
A consumer must distinguish a producer declaration from an observation and from independent
authentication; populated metadata is not proof. Do not synthesize a producing Run,
installed artifact identity, timestamp, successful cleanup or passing result to fill missing fields.
Producer errors may accompany partial diagnostic output and cannot become passing evidence.

## Result formats

Report references declare `junit-xml` or `ctrf-json` and explicitly select their profile.
CTRF uses `ctrf-0.2.0`; JUnit names the selected producer dialect.
JUnit identifies original XML produced by the selected upstream tool. There is no universal JUnit
schema admitted by this change. A future adapter must select and document the producer's XML profile;
the initial planned adapter is the unchanged Ginkgo output used by Kubernetes conformance. Its
XML parsing and result semantics are deferred, including required-case and suite-hook handling.

CTRF identifies original JSON under an explicitly selected upstream specification. The reference is
[CTRF working draft 0.2.0](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/spec/ctrf.md)
and its normative
[Draft-07 JSON Schema](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/schema/ctrf.schema.json),
pinned to commit `e325eb33c7662a208bfa4d093a41cc55439d6e6a` from October 10, 2026.
The upstream [license](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/LICENSE)
is MIT. This source reference establishes neither dependency approval nor format validation.

Authoritative validation of either result format is deferred. The current dependency set has no
JSON Schema validator, and this change adds none. Contract validation checks the wrapper, reference
shape and metadata only; it does not validate CTRF against its schema, parse JUnit, recompute counts,
evaluate coverage or decide whether results pass. A format label is not a validity claim.

## Authority and remaining work

An external output description is generated diagnostic data, not a new authored lifecycle object or
an authenticated receipt. It creates no structured identity or digest family. Raw file digests describe
byte integrity only. It grants no artifact verification, qualification, release authorization or
current cluster-health claim. Existing `attest.Manifest` validation-vector v3, including
`ConformanceClaim` and `ValidationVectorRef`, and qualification-evidence v2 remain unchanged.
This output cannot substitute for their authenticated evidence or activate qualification signing.
The relationship to public Verification Policy remains deferred.

Concrete producer admission and execution, original-file capture, format adapters, consumer coverage
policy, environment observation, durable retention, signing and registry transport require separate
implementation and review at their existing owning boundaries. This reference exposes no executable
CLI or SDK workflow and claims no external runtime or service acceptance.
