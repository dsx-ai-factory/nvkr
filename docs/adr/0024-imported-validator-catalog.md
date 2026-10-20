# ADR-24 — Imported validator catalog and artifact bindings

- **Decision status:** Proposed (pending named-decider ratification)
- **Implementation maturity:** Initial provisional CLI catalog, strict loading/discovery and local-plan profile dispatch
  are implemented. Broader migration, published adapters and stabilization remain deferred; see `reconciliation.md`.
- **Acceptance-test maturity:** Initial discovery/admission/snapshot and mocked CLI dispatch contracts are tested.
  No new actual upstream catalog execution or native-platform catalog acceptance is claimed.
- **Deciders:** Pending; validation, artifact-production and upstream interface owners must be named before ratification.
- **Index:** Proposed ADR-24 supplements ADR-01–22 and proposed ADR-23; it remains outside the consolidated
  contract set until accepted.
- **Relates to:** ADR-01 (canonical records), ADR-03 (coded errors), ADR-16 (diagnostic and conformance boundaries),
  ADR-17 (package ownership and resource lifetimes), ADR-18 (execution admission), ADR-19 (downstream ownership),
  ADR-20 (artifact subjects), ADR-21 (generated workflows), ADR-22 (source and tool pins),
  ADR-23 (upstream artifact-test interfaces).
- **Scope:** Discovery, organization, selection and artifact binding for imported validation suites across
  repositories. This decision defines diagnostic catalog contracts; it does not implement controller/chart
  adapters, cluster provisioning, CI scheduling, authenticated Run membership or release qualification.

## Context

NVKR builds artifacts from pinned producer catalogs. [ADR-23](0023-upstream-artifact-behavioral-validation.md)
defines how it can import upstream tests and run
them against externally supplied artifacts without taking ownership of upstream assertions. The current CLI
diagnostic implements a narrow version of that flow: factory validates a complete canonical Plan and selects
one `nvkectl` producer; the source owner imports its exact commit and tree; pipeline acquires dependencies and
executes the upstream `contract` or `cli` suite against privately staged bytes. Suite selection, component
selection and execution conventions are specific to the CLI implementation.

Controller images, Helm charts and artifacts from additional repositories need the same discoverability.
Adding command branches and scattered scripts for each suite would hide requirements, repeat Plan
interpretation and make it difficult to answer which validators accept a particular output. Conversely, a
generic shell pipeline language would duplicate execution ownership and make a readable catalog responsible
for arbitrary infrastructure and authority decisions.

AICR supplies a useful organizational precedent. Its [validator catalog](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/validators/catalog.yaml)
names checks and records execution metadata; [implementations](https://github.com/NVIDIA/aicr/tree/3a6e8609a6c3661b3e0220054232180b50388a67/validators/deployment)
live separately, and [recipes](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/README.md)
select checks. Its phase grouping and Kubernetes Job execution fit live-cluster checks. NVKR needs the same
separation of discovery, implementation and selection, organized around repositories and artifact consumers.
It must also preserve exact source relationships and support validators consuming several artifacts together.

There are already two relevant execution surfaces. `run artifact-test` is a trusted-input local CLI diagnostic;
`validate` dispatches explicitly configured published-source `kube-conformance` checks through Hydrophone.
The latter retains publication authentication and separately admits complete local diagnostic Runs through
its existing conformance owner. Its local Run route is distinct from the new CLI local-plan binding.
`testresults` describes external report capture without parsing reports or granting a passing verdict.
The [CLI diagnostic](../artifact-testing.md) and [external test output contract](../external-test-output-contract.md) document those implemented boundaries.
A common catalog must preserve these different input and result contracts.

## Decision

### 1. Catalog adapters and suite references while keeping assertions upstream

Introduce a validator catalog as diagnostic configuration, separate from the producer catalog. A validator
entry describes a reusable check; a validation profile selects named instances of those checks and binds
their artifact inputs. Neither becomes a seventh public lifecycle object. Definitions retain ownership of
what is built, and Environments and execution owners retain their existing policy responsibilities.

The dependency is one-way: NVKR consumes upstream sources and producer-neutral artifact-test interfaces.
Upstream repositories do not depend on NVKR code, catalogs, Plans or tooling. Assertions, fixtures, suite
inventories and entrypoints remain upstream. NVKR owns the adapter that translates validated artifact inputs
into that interface, captures results and checks the adapter's documented obligations.

An entry chooses a versioned adapter and one of that adapter's supported suite selectors. It cannot name an
arbitrary executable, embed shell commands, supply unrestricted Make targets, or override container mounts,
security settings or credentials. Adding a new interface requires an admitted adapter implementation and
contract review. New entries using an existing supported interface can remain data changes.

This adopts AICR's catalog pattern without importing its API schema, Go dependency or Job runtime. NVKR does
not require every validator to be packaged as a standalone container image.

### 2. Organize entries by repository, artifact and suite

The authored owner is `inputs/testing/validators/`. Its root `catalog.yaml` explicitly indexes entry files;
the entry files are the single owners of their metadata. Discovery never recursively executes or registers
files because they happen to exist on disk. Existing `inputs/testing/nightly-profiles.json` retains its
separate coordinator ownership until an explicit migration is implemented.

The proposed layout is:

```text
inputs/testing/
  validators/
    catalog.yaml
    nvke/
      nvkectl/
        contract.yaml
        cli.yaml
      controller/
        health.yaml
        capd-lifecycle.yaml
      charts/
        render.yaml
        install.yaml
    other-project/
      ...
  profiles/
    nvke-local.yaml
    nvke-ci.yaml
```

These paths illustrate organization, not registered capabilities. The first implementation registers only
supported CLI entries; future controller/chart entries must not be installed as executable placeholders.

Entry IDs have exactly three lowercase path segments, `project/artifact/suite`; each segment contains only
ASCII letters, digits and hyphens, starts and ends with an alphanumeric character, and is at most 64 bytes.
Examples are `nvke/nvkectl/contract` and `nvke/controller/health`. IDs are unique across the loaded catalog.
The project segment identifies an ownership namespace, not a Git source identity or an implicit repository URL.
Filesystem location is excluded from execution semantics: a move preserving the entry's ID and content does
not change what runs. Renaming an ID requires explicit profile migration; no fuzzy lookup or implicit alias.

Execution category, platform and capability requirements are metadata. They do not determine the primary
directory tree or imply an order among checks. Controller installation may require a chart and an image;
placing its entry under `controller/` does not restrict it to a single input.

Go implementations stay under the existing package ownership model. Repository/artifact naming may be
mirrored in adapter code where useful, but several entries may share an adapter. Do not create a parallel
top-level tree containing copied upstream test implementations.

### 3. Use strict, bounded schemas with one metadata owner

Define separate authored schemas:

| Document | Proposed API version | Kind | Owns |
|---|---|---|---|
| Catalog index | `nvkr.dev/validator-catalog/v1` | `ValidatorCatalog` | Explicit entry paths |
| Validator entry | `nvkr.dev/validator/v1` | `Validator` | Reusable adapter, suite and input requirements |
| Validation profile | `nvkr.dev/validation-profile/v1` | `ValidationProfile` | Selected instances and concrete bindings |

Readers dispatch on exact version and kind and reject unknown fields, duplicate keys, duplicate IDs, invalid
values and unresolved references. YAML and JSON decode into the same typed model; YAML aliases, merge keys and
multi-document streams are rejected. There is no overlay inheritance, recursive index inclusion, merge by
name, implicit built-in fallback or last-writer-wins behavior. One explicit catalog is loaded per invocation.

The index contains only its schema envelope and an `entries` list; entry paths are unique and their list
order has no execution meaning. For the initial two CLI entries:

```yaml
apiVersion: nvkr.dev/validator-catalog/v1
kind: ValidatorCatalog
entries:
  - nvke/nvkectl/contract.yaml
  - nvke/nvkectl/cli.yaml
```

Index paths are relative to its directory, remain beneath that root and identify regular files. Absolute
paths, parent traversal and symlink traversal are rejected. The loader uses context-aware bounded reads:
at most 1 MiB per document, 16 MiB across one catalog, 1,024 entry files and 16 directory levels. A profile has
at most 64 uniquely named check instances and each entry declares at most 64 input roles. Unknown adapters can
be described as unavailable during discovery, but any selected unavailable adapter fails execution admission.
Unselected entries still undergo complete schema, reference and uniqueness validation.

An entry declares its ID, description, adapter protocol/version, suite selector, input-role requirements,
suite-source relationship, platform constraints, execution requirements, timeout, coverage category and known
limitations. Limitations are bounded display text; they disclose constraints without changing admission rules.
Adapters own typed configuration and supported values; free-form fields cannot bypass their validation.
Descriptions are display text and cannot select behavior.

For example, the CLI contract entry would have this proposed shape:

```yaml
apiVersion: nvkr.dev/validator/v1
kind: Validator
id: nvke/nvkectl/contract
description: Run upstream process contracts against a supplied nvkectl executable
adapter: nvke-cli/v1
suite: contract
inputs:
  executable:
    component: nvkectl
    format: executable
suiteSource:
  mode: build-source
  fromInput: executable
platforms: [linux/amd64, linux/arm64]
requires: [docker, git]
timeout: 30m
coverage: artifact-contract
limitations:
  - Package-level result checks do not establish exhaustive artifact-contract coverage
```

The initial CLI slice accepts this provisional entry shape; it does not establish stable compatibility.
`coverage` describes the intended test surface, not measured completeness: `artifact-contract`, `source`,
or `mixed`. The full CLI suite is `mixed` because ordinary unit tests compile imported source while its
process contracts execute supplied bytes.
Package/test counts cannot be presented as a count of artifact contracts.

### 4. Bind named artifact roles explicitly and validate the complete selection

A profile selects an explicit nonempty list of check instances. Each names one validator ID and binds every
required role to an exact named output. No glob selection, automatic run-all, inferred output by filename,
or silently omitted inapplicable check is admitted. Discovery can suggest candidates; execution still needs
explicit selection. With no individual check selector, execution runs the profile's selected checks only.

The new profile binds runtime inputs separately from reusable entries. This example shows a local diagnostic
with one explicit check; file paths resolve relative to the profile, and artifact locations are not content
identities or proof of membership:

```yaml
apiVersion: nvkr.dev/validation-profile/v1
kind: ValidationProfile
catalog: ../validators/catalog.yaml
source:
  kind: local-plan
  plan: ../../../.nvkr/example/plan.json
checks:
  - name: cli-contract
    validator: nvke/nvkectl/contract
    inputs:
      executable:
        output: nvkectl
        artifact: ../../../.nvkr/example/nvkectl
```

Published mode instead selects the existing immutable publication reference and independent policy; local
artifact paths are forbidden in that mode. Mode-specific fields are strictly disjoint. Optional adapter
configuration is typed and cannot override the selected suite, source relationship or input requirements.
Checks execute sequentially in authored profile order and stop at the first failure, retaining completed and
partial diagnostics accurately. Index/discovery order is sorted by ID and does not alter profile order.
There is no dependency graph, parallel scheduler or automatic provisioning in the initial profile protocol.

Each role declares component compatibility and artifact format. An adapter may additionally constrain source
interface, native platform and relationships among roles. A profile binds output names, not a re-authored
producer graph. Multiple matching outputs require an explicit choice. Every required role must resolve exactly
once; unknown or extra roles fail. An output may fill more than one role only when the adapter explicitly
permits it. Every check invocation retains its own named-output bindings even if outputs share a Build Digest.

The initial new-profile source modes are explicit `local-plan` diagnostics and the existing admitted
`published` route. A profile selects one mode; mixing trust modes within one profile is deferred. In local
mode, a complete canonical Plan and supplied artifact locations are mandatory. Factory validates the complete
Plan before selecting named outputs; paths and hashes remain caller-supplied descriptions until owning checks
run. Published mode retains existing publication authentication, policy and held-output consumption; local
mode cannot inherit those capabilities merely by using the same catalog entry.

Before workspace creation, source acquisition, artifact staging or subprocess execution, validate the complete
profile, catalog references, selected adapters, role key sets, Plan shape and statically knowable capabilities.
Admission does not prove that an upstream entrypoint exists or an execution environment is available. Those
checks occur in their owning bounded phases, before the corresponding suite can run, with cleanup on failure.

For controller/chart checks, bind the controller image and chart package as separate roles. The adapter must
reject a chart that references a different image under test and prevent fallback to a default tag or rebuilding
the selected component. Runtime environment requirements are also explicit; a catalog declaration does not
prove cluster ownership, locality, privileges or available capacity.

### 5. Derive imported suites from artifact source identity

For ADR-23 artifact suites, the initial source mode is exclusively `build-source`. The entry designates one
input role as the suite source. Factory derives the repository, full commit and tree from that role's exact
producer in the validated Plan. The existing source owner imports and verifies it. Catalog entries and
profiles cannot supply a replacement branch, `latest`, independent test commit or local checkout.

The designated role is necessary for checks consuming several artifacts. Each other input retains its own
source identity, native platform and artifact subject. Sharing a repository URL, project namespace or version
string never establishes a common revision. An adapter must explicitly enforce any required same-source
relationship; otherwise the result describes a test of the supplied combination and cannot claim each input
was checked by tests from its own revision. A separately versioned CLI and controller are not silently aligned.

An independently pinned external harness, such as the existing Hydrophone or downstream nightly harness,
remains a distinct adapter protocol with its own existing pin owner. It cannot be represented as an ADR-23
matching-source suite. Supporting another repository means implementing or adopting its producer-neutral
interface and source mapping, not introducing repository-specific branches in the catalog loader.

The producer Plan remains the owner of build pins and toolchain selection. The CLI adapter reuses its selected
toolchain; another adapter needing additional tools must resolve them through an admitted tool owner. Catalog
entries cannot repeat or override toolchain digests or use AICR-style release-tag rewriting. General external
catalog distribution, independent harness-source modes and new tool-pin mechanisms require separate adoption.

### 6. Preserve package ownership and execution isolation

`validation` owns catalog/profile admission, discovery and dispatch. Factory owns complete producer Plan
interpretation and artifact-test input selection; `sourcegit` owns exact source import; pipeline owns container
execution and process cleanup; lifecycle, delivery and conformance retain published-output authentication and
their existing diagnostic ownership. `testresults` continues to own descriptive output contracts. The CLI and
SDK delegate; they do not reproduce source selection, digest interpretation or execution rules.

Adapters are registered Go implementations with explicit supported source modes, suite selectors, input roles,
platforms and configuration. They do not implement new producers or bypass capability admission. Package
placement must preserve the existing dependency direction; a shared dispatcher is not permission to move
factory or lifecycle invariants into command code or create an import cycle.

Entry requirements must satisfy the adapter's minimum capabilities; an entry may narrow platform support or
add requirements, but cannot weaken isolation, omit required capabilities or extend adapter-owned maximum
budgets. Disagreement is an admission error, not an instruction to trust the entry over executable contracts.

Local and CI execution resolve the same entry and use the same adapter contracts. Runtime locations and
admitted infrastructure may differ. For the first CLI migration, retain Docker execution, private artifact
staging, dependency acquisition without the artifact, and offline tests with read-only source/artifact/root,
dropped capabilities and no new privileges. Catalog introduction does not widen network or credential access.

`timeout` bounds the suite execution phase. Source acquisition, dependency acquisition and cleanup have
separate finite adapter-owned budgets; it is not an end-to-end deadline. A shorter caller context wins, and
profile configuration cannot increase catalog limits. For CLI parity, retain its current per-container bound
and fixed Git-command budgets. Every blocking phase observes context, and each owner stops and joins its
children before releasing their workspace. Cleanup uses an independent bounded context; its failure is
reported without replacing an already observed suite failure. Partial diagnostics never become success.

Cluster setup, leases, registry transport and teardown require an admitted adapter or downstream environment
owner. An entry's `requires` list cannot allocate a runner, grant credentials, permit privileged execution or
instantiate a generic provisioning engine. Hosted generation remains owned by ADR-21; this catalog defines
neither event triggers nor new runner labels or workflow permissions.

### 7. Separate captured reports, diagnostic verdicts and evidence authority

Retain original reports and raw test events. Record the selected entry and profile configuration, adapter
version, upstream suite selector and revision, harness identity where applicable, execution platform, each
named input/output binding, artifact observations, process outcome and cleanup outcome. Unknown observations
remain unknown; report fields cannot manufacture a Run, installed image, suite completion or passing verdict.

Required artifact observations depend on the format:

| Input | Required subject description |
|---|---|
| Executable | Exact staged-byte SHA-256 and before/after integrity checks |
| Controller image | Exact OCI subject, selected native child and the executed platform |
| Chart | Exact package-byte integrity and OCI manifest when present; rendering/install observations kept separate |
| Several artifacts | Every role's own output, source and subject, with the checked relationships recorded |

`testresults.Output` remains a descriptive handoff. `CaptureStatus=complete` describes report capture and does
not mean valid reports, exhaustive tests or passing behavior. The catalog supplies none of those verdicts.
An adapter may report a diagnostic verdict only under its own explicit parsing and completeness contract;
the existing Hydrophone route cannot inherit the CLI diagnostic's pass/fail interpretation. Original report
capture and adapter verdicts must remain separately inspectable.

For exhaustive artifact-suite acceptance, the upstream revision must own a machine-checkable inventory or
completion protocol. The adapter rejects missing, zero-test, skipped, filtered or incomplete required
coverage. NVKR does not maintain copied test-name lists or hard-coded expected counts. The existing CLI
package guard is weaker: cataloging it preserves diagnostic behavior but must disclose that exhaustive
completeness is unknown. A validator entry cannot upgrade that guard by declaring a stronger coverage label.

The lane remains **none (descriptive)** with unqualified diagnostic results. A result does not issue a
ConformanceReceipt, authenticate artifact membership, satisfy ADR-13 evidence, authorize signing or promote
a release. ADR-16 qualification consumes independently produced and authenticated conformance evidence;
running a cataloged suite does not satisfy that separate handoff.

### 8. Freeze diagnostic configuration without changing Build identity

Resolve the complete selected configuration once before execution. Execute that admitted snapshot rather than
reopening mutable entry files between phases. Retained diagnostics include canonical resolved configuration
and exact source-document byte digests, so a reviewer can distinguish an entry edit from an artifact change.
Credential values are excluded; runtime secret handles remain with execution owners.

Use the canonical codec for generated structured descriptions and raw SHA-256 only for exact file/report
integrity. This ADR allocates no new authenticated receipt or semantic digest family. A future evidence-bound
validation identity requires registration and governing authentication rules under ADR-01 and ADR-16.

Validator/profile edits do not change producer Build Digests or modify an existing Plan. Changed validators,
input bindings, suite revisions, tools or platform observations are distinct diagnostic executions; results
cannot be reused solely because Build Digests match. No result cache or cross-output reuse is introduced.
File paths and display-only descriptions cannot determine artifact identity.

### 9. Migrate through explicit compatibility boundaries

First add catalog discovery and strict loading, then catalog the existing `contract` and `cli` suites through
their current Go owners. Preserve the hidden `run artifact-test` diagnostic as a compatibility interface;
its default suite and flags map explicitly to those entries when the new path is admitted. Preserve its
documented error/exit behavior, cleanup and diagnostic limitations. There is no automatic switch from a
failed catalog lookup back to hard-coded execution.

The existing unversioned JSON `validate --config` profile keeps its current published-source semantics and
its independently admitted complete-local-Run conformance route.
The new versioned profile is an explicit format and capability transition; legacy documents are not silently
reinterpreted, and local support is not enabled by relaxing the legacy source check. Discovery reports
availability and requirements; it does not execute checks or interpret a descriptor as proof of support.

The first catalog exposes exact-ID list and describe operations through the shared owner. CLI spelling and
SDK method signatures must be reconciled with ADR-17 when implemented. New selection remains explicit and
errors preserve ADR-03's existing distinctions among invalid configuration, unsupported capability, fetch
failure, suite failure and timeout. This ADR allocates no new exit codes.

## Boundaries the catalog cannot resolve

| Boundary | Required owner or prerequisite | Catalog limitation |
|---|---|---|
| Upstream adoption | Upstream owners provide and maintain the external-artifact interface at the selected revision | Registration cannot create a missing entrypoint or approve its adoption |
| Maintained pin readiness | Producer catalog owners refresh explicit pins and regenerate Plans | An entry cannot patch a checkout or substitute a convenient test revision |
| Suite completeness | Upstream owns an inventory/completion protocol; the adapter validates it | Package presence, test counts and process exit zero cannot establish exhaustive coverage |
| Run/output membership | Artifact owners authenticate exact attempt, output, platform and bytes | A supplied Plan plus matching checksum is still a trusted-input diagnostic |
| Installed identity | Image/chart adapters observe actual selected and running subjects | Requested references and successful rendering do not prove which bytes executed |
| Infrastructure | Execution/downstream owners admit tools, networking, privileges, leases and cleanup | A declared requirement is not available capacity or environment authority |
| Report validity and verdict | Adapter-specific parsers and completeness checks | A report format label or complete capture is not a passing result |
| CI policy | Workflow generator and downstream coordinators own transport, triggers and credentials | Catalog files do not authorize workflow generation, scheduling or runner changes |
| Qualification | Independent evidence production, authentication and policy under ADR-16 | Diagnostic success cannot strengthen itself into release authority |

These boundaries are acceptance prerequisites for stronger claims, not reasons to hide useful diagnostics.
Discovery must show the supported interface and known limitations so operators can distinguish a cataloged
diagnostic from an exhaustively checked or authenticated result.

Discovery availability means the installed adapter supports the entry's declared protocol and static
requirements. Upstream entrypoint readiness, artifact membership and runtime capacity remain unverified until
their owning phases run. Discovery must not fetch sources or launch infrastructure to turn those unknowns
into an availability claim.

## Consequences

Engineers can find validators by project and artifact, inspect their requirements, and reuse suite definitions
without copying assertions or maintaining another source-pin registry. Named input roles support controller
and chart combinations without committing the catalog to a single-artifact model. Existing execution and
authentication owners remain the places where correctness is enforced.

The cost is a strict schema/loader, adapter registration, explicit profile migration and an additional reviewed
configuration surface. Entries cannot make unsupported interfaces work, so adding a repository may require
upstream adoption and Go adapter work. Maintaining separate diagnostic and evidence contracts is intentional;
a single green catalog summary would conceal materially different claims.

## Alternatives considered

| Alternative | Decision |
|---|---|
| Copy AICR's schema and Kubernetes Job execution | Reject: does not express NVKR's artifact roles and Plan-derived source relationships; imposes cluster execution on CLI diagnostics |
| Group primarily by deployment/conformance/performance | Reject: repository and artifact are the primary navigation keys; execution requirements remain metadata |
| Add commands or scripts for each suite | Reject: scatters discovery and repeats binding and execution logic |
| Put validation entries in producer registries or Definitions | Reject: conflates diagnostic selection with build declarations and pin ownership |
| Copy upstream tests and expected test-name lists into NVKR | Reject: duplicates assertion ownership and lets imports drift from their source revision |
| Allow arbitrary commands, overlays and embedded provisioning in YAML | Reject: creates another execution language and bypasses typed adapter admission |
| Independently pin tests to the newest compatible branch | Reject for ADR-23 imports: weakens the matching-source contract; independently versioned harnesses remain separate protocols |
| Build a universal plugin runtime before cataloging the CLI | Defer: registered adapters and a small strict catalog cover the first use case without speculative infrastructure |

## Acceptance and follow-ups

Acceptance is staged. Initial CLI catalog support requires items 1–4 and the local compatibility checks in
item 6. Artifact expansion and hosted integration have separate acceptance; neither is implied by CLI support.

1. **Loading and discovery:** deterministic list/describe, exact version dispatch, bounded cancellation-aware
   reads, safe paths, strict role/configuration validation, duplicate/unknown-reference refusal and admission
   before executable work. Reordering index entries must not change resolved behavior. Discovery must distinguish
   registered metadata from an available executable capability.
2. **CLI parity:** both suites on native Linux AMD64 and ARM64 against fresh recipe-built executables at an
   upstream revision exposing the required interface. Preserve separate source-test and artifact-contract
   reporting, byte integrity, offline execution, failure statuses, timeouts and cancellation/cleanup behavior.
   Existing recorded CLI runs are prerequisites and baselines, not proof of the new catalog path.
3. **Binding negatives:** unknown/multiple outputs, missing/extra roles, wrong format/platform/revision/tree,
   changed bytes and equal-Build-Digest sibling substitution. Refuse incompatible multi-artifact relationships
   before claiming that a supplied combination was validated.
4. **Coverage and authority negatives:** filtered/missing/skipped/empty required suites cannot become exhaustive
   acceptance; complete report capture cannot become pass; a caller-supplied binding cannot become authenticated
   membership; neither local nor published diagnostic results can become qualification evidence.
5. **Artifact expansion:** one controller/chart check consuming at least two named inputs, exercising exact
   supplied-image/package selection, refusal of default/rebuilt substitutes, runtime subject observation and
   failed-suite teardown. Then adopt a second repository without changing generic catalog-loading rules.
6. **Migration and CI:** explicit legacy/new-profile behavior, unchanged existing published-source admission,
   shared local/CI adapter semantics and generator-owned workflow changes. Hosted credentials, runner allocation,
   triggers and transport require their own acceptance; no workflow capability follows from merging this ADR.

The IV1-C1–IV1-C3 implementation exposes provisional `validators list|describe|run` diagnostics
(hidden from normal lifecycle help) and matching thin `client/v1` methods. The first versioned profile
admits local-plan only. It deliberately refuses new-format published mode; the existing legacy published
route retains authentication and capture-only semantics; its separately admitted local conformance route
also remains unchanged. Profiles have no adapter configuration extension in this slice: such fields are
unknown until a reviewed versioned interface owns them. Check instance and
role names use the same segment grammar as ID segments; output names retain existing Plan naming rules.

Adapter minimum limitations are emitted separately from authored display text. An unavailable adapter or
entry/adapter disagreement remains describable but cannot be selected for execution. Host Git and Docker
are minimum CLI prerequisites; the selected toolchain must provide Go and Make. The CLI adapter
currently refuses extra requirements with no installed admission owner. Catalog selection does not enable
those capabilities by naming them.

The legacy timeout contract needs an explicit migration decision: `run artifact-test --timeout` admits
arbitrary positive per-container bounds, while `nvke-cli/v1` caps catalog suite execution at 30 minutes and
retains a separate 30-minute dependency budget. Therefore this slice preserves the legacy command directly
through factory rather than mapping all its flags to a narrower catalog entry. No failed catalog admission
falls back to that route. Factory's opaque diagnostic invocation is shared by both paths; it freezes complete
Plan interpretation before execution without issuing an authenticated receipt. Artifact paths are frozen;
bytes retain the existing staging-time integrity contract. No claim of admission-time artifact authentication
is added. Exact admitted Plan document integrity is retained with catalog selection metadata.

Named owners must ratify the schemas, adapter protocols and migration before maintained adoption. Reconciliation
records implementation, contract tests and external execution separately. Any future authenticated evidence
integration must resolve exact Run/output membership and independent conformance ownership in the governing
ADRs before admitting results; catalog adoption alone does not close those decisions.
