# External test output contract

`pkg/nvkr/testresults` describes outputs supplied by external test producers. Its lane is **none
(descriptive)**. It supplies an interface, wrapper validation and deterministic serialization, with no
producer implementation, execution, report parser, file collector, signing or qualification integration.
[Reconciliation](adr/reconciliation.md) owns implementation and disabled-path status.

## Producer boundary

`Producer.Produce(context.Context) (Output, error)` permits external implementations to execute
outside the qualified verifier. Implementations own suite/harness configuration, arguments, permitted
environment, access requirements and child-process lifecycle. No implementation ships at this boundary.
A future user-invoked diagnostic adapter must remain separate from qualification verification; see
[ADR-16's passive handoff](adr/0016-release-gates-and-digest-first-selection.md#6-passive-external-qualification-handoff--deferred-producer-protocol)
and [ADR-05's trust-object separation](adr/0005-trust-object-model-and-digest-authentication.md#4-publisher-evidence-and-later-observations-are-different-trust-objects).
No suite-selection dispatcher, cluster provisioning or test-execution workflow is implemented.

`Output` uses schema `nvkr.dev/test-results-output/v1`. It describes `ProducerIdentity`,
`SuiteSelection`, optional `HarnessIdentity`, `Reports []ReportReference`,
`Environment []EnvironmentFact`, `CaptureStatus` and optional NVKR `Binding`. Suite
name/selection/revision and harness name/version stay separate. Raw credentials do not belong in output.

`CaptureStatus` is `complete` or `partial`. Complete requires at least one report reference; partial
may contain none. This declares capture availability, not suite success, format validity or passing tests.
An error may accompany partial diagnostic output. Metadata status is `declared`, `observed`, `unknown`
or `unavailable`; unknown/unavailable carries no value. Environment facts have explicit scope and
`before`, `during`, `after` or `unknown` phase. Observed metadata is descriptive, not independently
authenticated. Missing data stays absent or unknown; never fabricate a Run/attempt, timestamp,
installed artifact, successful cleanup or passing result.

## Original report references

A producer may reference original JUnit XML, CTRF JSON, or both. `ReportReference` declares format,
profile, opaque location, exact SHA-256 and byte length. Validation never opens that location or
recomputes its digest. A future collector must independently check the original bytes. Independent
JUnit/CTRF files need no conversion link. Optional `DerivedFrom` names another included original
reference, with one derivation step only; it proves no conversion. No report bytes are rewritten.

Formats are `junit-xml` and `ctrf-json`. JUnit requires a named producer dialect; no universal JUnit
schema or planned adapter is admitted. CTRF requires profile `ctrf-0.2.0`, referring to
[working draft 0.2.0](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/spec/ctrf.md)
and its normative
[Draft-07 JSON Schema](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/schema/ctrf.schema.json),
pinned to commit `e325eb33c7662a208bfa4d093a41cc55439d6e6a` from October 10, 2026.
The upstream [license](https://github.com/ctrf-io/ctrf/blob/e325eb33c7662a208bfa4d093a41cc55439d6e6a/LICENSE)
is MIT. Referencing that source establishes neither dependency approval nor format validation.

Authoritative format validation is deferred. The dependency set has no JSON Schema validator, and
no JUnit profile parser is supplied. Wrapper validation does not interpret result counts, evaluate
coverage or judge results passing. A format label is not a validity claim.

## Optional post-publication bindings

Standalone outputs need no NVKR binding. A supplied `Binding` may describe input identities, named
subjects, or both. `Inputs` reuses `lifecycle.ExactInputDescription`: it is a **post-publication**
description requiring `PolicyDigest`, `DefinitionDigest`, `EnvironmentDigest`, `PlanDigest`,
`ContentSubject` and an exact immutable `ReleaseSubject` together. It cannot describe a partially
populated pre-publication Plan. `PolicyDigest` remains a descriptive policy reference; validation
neither authenticates that policy nor applies its enforcement rules.

Named subjects use `SubjectDescription { ref, digest }` and optional concrete native platform.
They are descriptions, not signing subjects or authenticated artifact receipts. Absent platform
and native platform descriptions cannot be mixed for one named output. Reference shape, input
identities and subject digests are validated by their existing owners without authenticating referents.

## Validation and serialization

`Output.Validate()` checks wrapper shape and declarations. `Encode(Output)` validates, defensively
copies and sorts descriptions through the existing canonical codec. `Decode([]byte)` admits at most
16 MiB, validates exact canonical bytes and rejects non-normalized collection order. Selection,
report and subject collections are bounded to 1,024 entries each; environment facts to 10,000.
Optional empty collections have one omitted wire representation. These operations perform no file
I/O, original-byte hashing, result parsing or execution. Malformed descriptions use `CodeUsage`;
unsupported versions, enums and profiles use `CodeUnsupported`. Nested owner errors retain their
coded cause and context.

Deterministic serialization supplies no new digest family or structured identity. Output is
**producer-supplied descriptive data**, not an authored lifecycle object, authenticated receipt or
publisher evidence. Existing validation-vector v3 (including `ConformanceClaim` and
`ValidationVectorRef`) and qualification-evidence v2 remain unchanged. This output cannot authorize
artifact verification, qualification signing, promotion or release, nor establish current cluster health.
Concrete producer integration, file retention, format adapters, coverage policy and authenticated
external handoff remain unimplemented; no CLI/SDK execution or external runtime acceptance is claimed.
