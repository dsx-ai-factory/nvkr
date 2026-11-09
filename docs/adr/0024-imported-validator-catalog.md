# ADR-24 — Imported validator catalog and artifact bindings

- **Decision status:** Proposed (pending named-decider ratification)
- **Amendment:** This revision aligns the proposal with the draft validator-catalog technical design. It replaces
  typed adapters with a generic `container-command/v1` executor, the explicit `catalog.yaml` index with path
  discovery, profile selection with ordered ID/parent-path selection, local-plan/artifact-path profiles with
  completed-Run binding, and adds an invocation-scoped TestEnvironment. Implementation has not followed: the
  current code is the existing provisional slice (`catalog.yaml`, `nvkr.dev/validator/v1` entries, the typed
  `nvke-cli/v1` adapter and `ValidationProfile` local-plan binding) and is to be migrated under §10.
- **Implementation maturity:** Only the existing provisional slice is implemented: strict bounded index/entry
  loading, discovery and local-plan profile dispatch for the CLI suites. Nothing introduced by the amendment is
  implemented; see `reconciliation/README.md`.
- **Acceptance-test maturity:** The provisional slice's discovery/admission/snapshot and mocked CLI dispatch
  contracts are tested. No catalog execution against an actual upstream suite and no native-platform acceptance
  is claimed.
- **Deciders:** Pending; validation, artifact-production and upstream interface owners must be named before ratification.
- **Index:** Proposed ADR-24 supplements ADR-01–22 and proposed ADR-23; it remains outside the consolidated
  contract set until accepted.
- **Relates to:** ADR-01 (canonical records), ADR-03 (coded errors), ADR-16 (diagnostic and conformance boundaries),
  ADR-17 (package ownership and resource lifetimes), ADR-18 (execution admission), ADR-19 (downstream ownership),
  ADR-20 (artifact subjects), ADR-21 (generated workflows), ADR-22 (source and tool pins),
  ADR-23 (upstream artifact-test interfaces).
- **Scope:** Discovery, organization, selection, Run binding, test-environment admission and execution of
  imported validation suites across repositories. This decision defines diagnostic contracts; it does not
  implement hardware provider integrations, CI scheduling, authenticated publication membership or release
  qualification.

## Context

NVKR builds artifacts from pinned producer catalogs. [ADR-23](0023-upstream-artifact-behavioral-validation.md)
defines how it can import upstream tests and run
them against externally supplied artifacts without taking ownership of upstream assertions. The current CLI
diagnostic implements a narrow version of that flow: factory validates a complete canonical Plan and selects
one `nvkectl` producer; the source owner imports its exact commit and tree; pipeline acquires dependencies and
executes the upstream `contract` or `cli` suite against privately staged bytes. Suite selection, component
selection and execution conventions are specific to the CLI implementation.

Controller images, Helm charts and artifacts from additional repositories need the same discoverability.
Adding command branches, scattered scripts or a Go adapter per repository would hide requirements, repeat Plan
interpretation and make it difficult to answer which validators accept a particular output. Conversely, a
shell pipeline language would duplicate execution ownership and make a readable catalog responsible for
infrastructure and authority decisions. Upstream artifact-test interfaces are commands run in a container
from a source checkout, so one executor with fixed isolation can serve them without per-repository code.

AICR supplies a useful organizational precedent. Its [validator catalog](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/validators/catalog.yaml)
names checks and records execution metadata; [implementations](https://github.com/NVIDIA/aicr/tree/3a6e8609a6c3661b3e0220054232180b50388a67/validators/deployment)
live separately, and [recipes](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/README.md)
select checks. Its phase grouping and Kubernetes Job execution fit live-cluster checks. NVKR needs the same
separation of discovery, implementation and selection, organized around repositories and artifact consumers.
It must also preserve exact source relationships and support validators consuming several artifacts together.

There are already two relevant execution surfaces. `validators run` is a trusted-input local CLI diagnostic;
`validate` dispatches explicitly configured published-source `kube-conformance` checks through Hydrophone.
The latter retains publication authentication and separately admits complete local diagnostic Runs through
its existing conformance owner. `testresults` describes external report capture without parsing reports or
granting a passing verdict. The [CLI diagnostic](../artifact-testing.md) and
[external test output contract](../external-test-output-contract.md) document those implemented boundaries.
A common catalog must preserve these different input and result contracts.

## Decision

### 1. Register container commands while keeping assertions upstream

Introduce a validator catalog as diagnostic configuration, separate from the producer catalog. A
registration describes one reusable suite: its input roles, test origin, execution image, command and
requirements. Neither the catalog nor a TestEnvironment becomes a seventh public lifecycle object.
Definitions retain ownership of what is built, and Environments and execution owners retain their existing
policy responsibilities.

The dependency is one-way: NVKR consumes upstream sources and producer-neutral artifact-test interfaces.
Upstream repositories do not depend on NVKR code, catalogs, Plans or tooling. Assertions, fixtures, suite
inventories and entrypoints remain upstream. NVKR owns the executor that supplies checked artifacts to that
interface, captures results and interprets them under a named contract.

Every registration names the versioned executor `container-command/v1`. It declares:

| Field | Meaning |
|---|---|
| Input roles | Exact named Run outputs, component and artifact format |
| Test origin | Source relationship (`build-source` only, §5) |
| Execution image | The selected producer's pinned toolchain image; other pinned images need an admitted image owner |
| Working directory | Relative path beneath the verified test-origin source |
| Acquisition | Optional argument vector that prepares dependencies without the artifact |
| Command | Argument vector executed without shell interpretation |
| Environment | Literal values or references in the substitution grammar below |
| Parameters | Typed suite parameters whose values the invocation supplies explicitly |
| Requirements | Supported platforms and capabilities; logical credentials and network needs per phase |
| Timeout | Suite execution budget, bounded as in §7 |
| Results | Named, versioned result interpreter |

Environment values and parameter defaults use a fixed grammar: `${inputs.<role>.path}`,
`${inputs.<role>.version}`, `${inputs.<role>.digest}` and `${parameters.<name>}`, with `$$` as a literal
`$`. There is no host-environment lookup, nesting, default expression or command substitution; an unknown
role, field or parameter, or any other `${` form, fails admission. Substitution resolves values once and never
passes them through a shell. Argument vectors and the working directory accept literals only.

Isolation is executor-owned. A registration cannot declare mounts, devices, privileges, user, security
options, network modes, credential values or host paths, and cannot weaken the executor's defaults. The
parity baseline is the existing `nvke-cli/v1` behaviour: private artifact staging, acquisition without the
artifact, an offline test container with read-only source, artifact and root filesystem, dropped capabilities,
no new privileges, separate phase budgets, stop/join and bounded cleanup. A new executor version, not a
registration field, changes these properties.

Exit status zero alone is never success. Each registration names a versioned interpreter that owns parsing,
completeness and verdict rules for the command's output. The existing CLI result checks (failed, skipped,
empty, incomplete or missing contract-package results; unchanged staged bytes) become the parity interpreter.

This adopts AICR's catalog pattern without importing its API schema, Go dependency or Job runtime. NVKR does
not require every validator to be packaged as a standalone container image.

### 2. Discover registrations by project, artifact and suite

A registration lives at `{project}/{artifact}/{suite}.yaml` beneath one catalog root; its ID equals that
relative path without `.yaml`, and a declared `id` that differs fails loading. Each segment contains only
lowercase ASCII letters, digits and hyphens, starts and ends with an alphanumeric character, and is at most
64 bytes. Examples are `nvke/nvkectl/contract` and `nvke/controller/health`.

```text
<catalog-root>/
  nvke/
    nvkectl/
      contract.yaml
      cli.yaml
    controller/
      health.yaml
  other-project/
    ...
```

These paths illustrate organization, not registered capabilities; future controller/chart registrations must
not be installed as placeholders. The project segment is an ownership namespace, not a Git source identity
or implicit repository URL. Renaming a registration changes its ID; there is no fuzzy lookup or alias.
Execution category, platform and capability requirements are metadata and never determine the directory tree.

One catalog root is loaded per invocation. NVKR ships a default catalog; `--validator-catalog <dir>` selects a
complete replacement with no inheritance, overlay or fallback to shipped registrations. (`--data` keeps its
existing producer-catalog meaning.) How the default is shipped is an implementation choice that must preserve
these semantics. Existing `inputs/testing/nightly-profiles.json` retains its separate coordinator ownership.

Loading is a bounded, cancellation-aware walk. The root contains only directories at the two parent levels and
regular registration files at the third; any other entry, symlink, non-regular file, escaping path or deeper
level fails loading. Reads are capped at 1 MiB per document, 16 MiB per catalog and 1,024 registrations. The
supplied root must itself be a real directory; ancestors may resolve before it is held. Hard links are
permitted: the loader confines traversal but does not claim exclusive file ownership or an atomic
multi-file snapshot beyond §9. Every registration is fully validated even when unselected, so one invalid
registration fails the invocation rather than being skipped.

### 3. Use a strict, bounded registration schema

| Document | Proposed API version | Kind | Owns |
|---|---|---|---|
| Registration | `nvkr.dev/validator/v2` | `Validator` | Executor configuration, roles, test origin and requirements |
| Test environment | `nvkr.dev/test-environment/v1` | `TestEnvironment` | Provider, capabilities, credential bindings, network and budgets (§6) |

The registration takes a new version so the provisional `nvkr.dev/validator/v1` shape is never reinterpreted.
Readers dispatch on exact version and kind and reject unknown fields, duplicate keys, invalid values and
unresolved references. YAML and JSON decode into the same typed model; YAML aliases, merge keys and
multi-document streams are rejected. A registration declares at most 64 input roles and 64 parameters.
Descriptions and limitations are bounded display text and cannot select behavior; executor and interpreter
limitations are emitted separately from authored text.

`coverage` describes the intended test surface, not measured completeness: `artifact-contract`, `source` or
`mixed`. The full CLI suite is `mixed` because ordinary unit tests compile imported source while its process
contracts execute supplied bytes. Package or test counts are never presented as a count of artifact contracts.

The CLI contract registration has this proposed shape. It invokes the upstream supplied-executable entrypoint
`make test-artifact`, which compiles the test harness and never rebuilds `nvkectl`; that interface is
proposed in NVKE [#1578](https://github.com/dsx-ai-factory/nvke/pull/1578), which is not yet merged:

```yaml
apiVersion: nvkr.dev/validator/v2
kind: Validator
id: nvke/nvkectl/contract
description: Run upstream process contracts against a built nvkectl executable.
executor: container-command/v1
inputs:
  executable:
    output: nvkectl
    component: nvkectl
    format: executable
testOrigin:
  mode: build-source
  fromInput: executable
image:
  fromInput: executable           # the producer's pinned toolchain image
workingDirectory: cli
acquisition:
  command: [go, mod, download]
command: [make, --no-print-directory, test-artifact, GO=go, "GO_TEST_FLAGS=-count=1 -timeout=15m -json"]
environment:
  NVKECTL: "${inputs.executable.path}"
  NVKECTL_EXPECTED_VERSION: "${inputs.executable.version}"
requires:
  platforms: [linux/amd64, linux/arm64]
  capabilities: [docker]
timeout: 30m
results:
  interpreter: nvke-cli-go-test/v1
coverage: artifact-contract
limitations:
  - Package-level result checks do not establish exhaustive artifact-contract coverage
```

The interpreter name is illustrative until the parity interpreter is implemented. The executor adds its own
minimum requirements (for example Git for source import); a registration cannot omit them.

### 4. Select suites and bind them to a completed Run

An invocation names one or more selections, each an exact ID or a parent path (`project/` or
`project/artifact`). Selections are processed in user order; a parent expands deterministically to its
registrations sorted by ID; duplicates are removed keeping the first occurrence. A parent matching no
registration fails. There are no globs, no fuzzy aliases and no implicit run-all: zero selections is a usage
error. At most 64 suites may remain after expansion. Suites execute sequentially in the resulting order and
stop at the first failure, retaining completed and partial diagnostics accurately.

Input mode is either build-first or an existing Run (`--run <attempt>`). Build-first reuses the existing local
Run path with the same Definition and Environment inputs, then continues exactly as `--run`. Validation
continues only from a successfully completed Run; incomplete, failed or uncommitted Runs fail.

Lifecycle opens the Run through `CaptureLocalDiagnosticArtifacts`, which replays its compiler reads and checks
every named payload's bytes. Each input role binds to exactly one named Run output matching the registration's
output, component and format; a missing, ambiguous or format-incompatible output fails. Factory derives the
test-origin source, toolchain image and expected version from the Run's Plan. Every suite retains its own
named-output bindings even when outputs share a Build Digest. Callers supply no artifact paths or digests.

For controller/chart suites, the controller image and chart package bind as separate roles. The interpreter
or upstream entrypoint must reject a chart that references a different image under test and prevent fallback
to a default tag or rebuilt component.

The example invocation, which depends on the #1578 interface at the Run's NVKE revision:

```sh
nvkr validators run \
  --validator-catalog ./company-validators \
  --run .nvkr/runs/<attempt> \
  --test-env local \
  nvke/nvkectl/contract
```

### 5. Derive imported suites from artifact source identity

For ADR-23 artifact suites, the test origin is exclusively `build-source`. The registration designates one
input role as the origin; factory derives the repository, full commit and tree from that role's producer in
the Run's validated Plan, and `sourcegit` imports and verifies it. Registrations and invocations cannot supply
a replacement branch, `latest`, independent test commit or local checkout.

Each other input retains its own source identity, native platform and artifact subject. Sharing a repository
URL, project namespace or version string never establishes a common revision. The interpreter or upstream
entrypoint must explicitly enforce any required same-source relationship; otherwise the result describes a
test of the supplied combination. An independently pinned external harness, such as the existing Hydrophone
or downstream nightly harness, remains a distinct protocol with its own pin owner. Supporting another
repository means adopting its producer-neutral interface, not adding repository-specific loader branches.

The producer Plan remains the owner of build pins and toolchain selection. Registrations cannot repeat or
override toolchain digests or use AICR-style release-tag rewriting. Additional tools resolve through an
admitted tool owner.

### 6. Admit one TestEnvironment per invocation

A TestEnvironment (`nvkr.dev/test-environment/v1`, kind `TestEnvironment`) is a versioned declaration of the
provider, provided capabilities, credential bindings, permitted network access and environment-owned budgets
available to a validation invocation. It is diagnostic execution configuration: it is not the lifecycle
`Environment` (`nvkr.dev/environment/v1`), not a public lifecycle object and does not affect Build identity.
It is also distinct from an upstream harness's own environment selection (for example NVKE
`-e2e.environment`), which is a suite parameter.

Suite fields (command, parameters) live only in registrations. Parameter values are supplied explicitly by the
invocation and are never derived from the provider name. Credential bindings name abstract sources, such as CI
secret injection, explicitly selected local variables or federated identity; values are delivered only to the
phases that need them and are excluded from retained configuration and results.

One TestEnvironment is selected explicitly per invocation, by name through documented resolution with no
fallback, or by file. Before any build, admission checks that the executor and provider integration satisfy
every selected suite's platform, capability, credential, network and budget requirements; incompatibility
fails then. A declared requirement grants nothing: the environment's admitted access bounds the run, and a
network-capable environment does not widen a suite's declared access. Until `pipeline` enforces per-phase
destinations and a credential delivery owner exists, the executor admits only the parity network model
(networked acquisition, offline test) and no credentials; declaring more fails admission.

The local environment:

```yaml
apiVersion: nvkr.dev/test-environment/v1
kind: TestEnvironment
metadata:
  name: local
provider: local
provides:
  capabilities: [docker]
credentialRefs: {}     # no credentials
network:
  egress: []           # no test-phase network
budgets:
  maxSuiteTimeout: 30m
```

### 7. Execute through the existing owners

`validation` orchestrates and owns registration loading, selection, preparation, binding and result
recording. Factory keeps Plan interpretation, `sourcegit` source import, pipeline container execution,
process lifetime and network enforcement, and lifecycle Run opening, byte checks and the build-first Run path.
Proposed files (`catalog*.go`, `selection.go`, `request.go`, `build.go`, `artifacts.go`, `execute.go`,
`kind.go`, `results.go`) are thin orchestration over those owners. The CLI and SDK delegate; package placement
preserves the dependency direction and creates no import cycle.

For each suite: acquire the test origin and image; prepare a private workspace and dependencies; prepare
required runtime resources; stage checked inputs and resolve invocation values; execute; capture outcome and
diagnostics; stop and join; clean up; persist the suite result. Every blocking phase observes context.

The suite timeout bounds the execution phase and is itself bounded by the lower of the executor's and the
TestEnvironment's maximum, not a single global cap. Source, dependency acquisition and cleanup have separate
finite executor-owned budgets; a shorter caller context wins. Registrations at `nvke-cli` parity keep its
current bounds: 30-minute test and dependency budgets, fixed Git-command budgets and the reaper's cleanup
budget. Cleanup uses an independent bounded context; its failure is reported without replacing an already
observed suite failure. Partial diagnostics never become success.

Disposable local clusters (kind) may be NVKR-managed or owned by the upstream harness; this is decided per
executor or suite. Whichever owner is chosen registers cleanup ownership before creation. Hardware providers
(for example Forge or DSX) require an explicitly supported provider integration with defined resource
ownership and cleanup; none is defined here. A registration's requirements cannot allocate a runner, grant
credentials, permit privileged execution or instantiate a provisioning engine. Hosted generation remains owned
by ADR-21; this catalog defines neither event triggers nor runner labels or workflow permissions.

### 8. Separate captured reports, diagnostic verdicts and evidence authority

Retain original reports and raw test events. Record each suite's ID, resolved nonsecret parameters, executor
and interpreter versions, test-origin revision, execution image, platform, every role's Run output binding and
artifact observation, process outcome, verdict and cleanup outcome. Unknown observations remain unknown;
report fields cannot manufacture a Run, installed image, suite completion or passing verdict.

| Input | Required subject description |
|---|---|
| Executable | Exact staged-byte SHA-256 and before/after integrity checks |
| Controller image | Exact OCI subject, selected native child and the executed platform |
| Chart | Exact package-byte integrity and OCI manifest when present; rendering/install observations kept separate |
| Several artifacts | Every role's own output, source and subject, with the checked relationships recorded |

`testresults.Output` remains a descriptive handoff; `CaptureStatus=complete` describes capture, not valid
reports, exhaustive tests or passing behavior. Only the named interpreter reports a verdict, under its own
parsing and completeness contract; the Hydrophone route cannot inherit another interpreter's interpretation.

For exhaustive artifact-suite acceptance, the upstream revision must own a machine-checkable inventory or
completion protocol, and the interpreter rejects missing, zero-test, skipped, filtered or incomplete required
coverage. NVKR does not maintain copied test-name lists or expected counts. The existing CLI package guard is
weaker and its parity interpreter must disclose that exhaustive completeness is unknown; a stronger `coverage`
label cannot upgrade it.

The lane remains **none (descriptive)**. A result does not issue a ConformanceReceipt, satisfy ADR-13
evidence, authorize signing or promote a release. ADR-16 qualification consumes independently produced and
authenticated conformance evidence; running a cataloged suite does not satisfy that handoff.

### 9. Freeze diagnostic configuration without changing Build identity

Execution progresses through package-owned records:

| Record | Contents | Establishes |
|---|---|---|
| ValidationRequest | Selections, catalog root, input mode, TestEnvironment reference | An invocation was supplied |
| PreparedValidation | Ordered suites, admitted registrations, source-document digests, the admitted TestEnvironment, build inputs or Run locator | Configuration is valid; a build may start |
| BoundValidation | Completed Run identity, exact artifact bindings, resolved test origins | Inputs are checked; runtime preparation may begin |
| ValidationResult | Build outcome, per-suite execution state, verdict, termination, logs, cleanup status | Outcomes and cleanup are recorded |

PreparedValidation is frozen: execution never reopens registration or environment files. A ValidationResult
is created before build or runtime work and updated through cleanup; secrets are excluded, and cleanup status
is recorded separately from the verdict. Exact source-document byte digests let a reviewer distinguish a
registration edit from an artifact change.

Use the canonical codec for structured descriptions and raw SHA-256 only for exact file/report integrity.
This ADR allocates no new authenticated receipt or semantic digest family; a future evidence-bound validation
identity requires registration under ADR-01 and ADR-16. Registration and TestEnvironment edits do not change
Build Digests or modify an existing Plan. Results cannot be reused because Build Digests match; no result
cache or cross-output reuse is introduced.

### 10. Migrate through explicit boundaries

The existing provisional slice — `catalog.yaml` index, `nvkr.dev/validator/v1` entries, the typed
`nvke-cli/v1` adapter and `validators run --profile` with `nvkr.dev/validation-profile/v1` local-plan
bindings, plus `client/v1` `ListValidators`, `DescribeValidator` and `PrepareValidatorProfile` — remains until
an explicit migration step. That step introduces the new schema versions and route and either removes the
profile route or retains it for a stated window; profile documents are never silently reinterpreted as Run
bindings, and a failed catalog lookup never falls back to hard-coded execution. `validators` stays hidden until
its schemas are ratified.

The unversioned JSON `validate --config` keeps its current published-source semantics and its independently
admitted complete-local-Run conformance route. Discovery reports availability and requirements without
fetching sources or launching infrastructure. Errors preserve ADR-03's distinctions among invalid
configuration, unsupported capability, fetch failure, suite failure and timeout; no new exit codes are allocated.

## Boundaries the catalog cannot resolve

| Boundary | Required owner or prerequisite | Catalog limitation |
|---|---|---|
| Upstream adoption | Upstream owners provide and maintain the external-artifact interface at the selected revision (for the CLI, NVKE #1578) | Registration cannot create a missing entrypoint or approve its adoption |
| Maintained pin readiness | Producer catalog owners refresh explicit pins and regenerate Plans | A registration cannot patch a checkout or substitute a convenient test revision |
| Suite completeness | Upstream owns an inventory/completion protocol; the interpreter validates it | Package presence, test counts and exit zero cannot establish exhaustive coverage |
| Run/output membership | Lifecycle checks a committed local Run's bytes; publication authentication stays with its owners | Local Run binding is diagnostic: no signatures, producer identity or ArtifactCheckReceipt |
| Installed identity | Existing conformance owns strict test-bound OCI observations; proposed ADR-29 assigns standalone collection/comparison to the observation owner, with deployment owners supplying installation records | Registrations, requested references and rendering do not establish installed identity |
| Isolation, network and credentials | The executor owns isolation; pipeline enforces network; a delivery owner injects credentials | A declared requirement or TestEnvironment entry grants no access the executor cannot enforce |
| Infrastructure | Per-executor/suite cluster owner; explicitly supported provider integrations for hardware | A declared capability is not available capacity, a lease or cleanup ownership |
| Report validity and verdict | Named, versioned result interpreters | A report format label, complete capture or exit zero is not a passing result |
| CI policy | Workflow generator and downstream coordinators own transport, triggers and credentials | Catalog files do not authorize workflow generation, scheduling or runner changes |
| Qualification | Independent evidence production, authentication and policy under ADR-16 | Diagnostic success cannot strengthen itself into release authority |

These boundaries are acceptance prerequisites for stronger claims, not reasons to hide useful diagnostics.
Discovery shows the executor, interpreter and known limitations so operators can distinguish a cataloged
diagnostic from an exhaustively checked or authenticated result. Upstream entrypoint readiness, Run
availability and runtime capacity remain unverified until their owning phases run.

## Consequences

Engineers can find validators by project and artifact, inspect their requirements, and add suites for a new
repository as data when its upstream interface fits the executor. Run binding removes caller-supplied artifact
paths. Named input roles support controller and chart combinations without committing the catalog to a
single-artifact model. Existing execution and lifecycle owners remain where correctness is enforced.

The cost is a strict loader, a substitution grammar, versioned interpreters, a TestEnvironment schema and an
explicit migration from the provisional slice. A generic executor concentrates risk: its isolation, budgets
and grammar must be reviewed once and changed only by version. Registrations cannot make unsupported
interfaces work, so adding a repository may still require upstream adoption. A single green summary would
conceal materially different claims, so diagnostic and evidence contracts stay separate.

## Alternatives considered

| Alternative | Decision |
|---|---|
| Copy AICR's schema and Kubernetes Job execution | Reject: does not express NVKR's artifact roles and Plan-derived source relationships; imposes cluster execution on CLI diagnostics |
| Group primarily by deployment/conformance/performance | Reject: repository and artifact are the primary navigation keys; execution requirements remain metadata |
| Add commands or scripts for each suite | Reject: scatters discovery and repeats binding and execution logic |
| Put validation entries in producer registries or Definitions | Reject: conflates diagnostic selection with build declarations and pin ownership |
| Copy upstream tests and expected test-name lists into NVKR | Reject: duplicates assertion ownership and lets imports drift from their source revision |
| Typed Go adapter per interface; entries cannot name commands (earlier proposal) | Superseded: one executor with fixed isolation and a literal argument vector scales to more repositories without per-repository Go adapters |
| Shell commands, mounts, overlays or embedded provisioning in YAML | Reject: creates an execution language and lets entries weaken executor isolation |
| Explicit `catalog.yaml` index (earlier proposal) | Superseded: path-derived IDs give one owner per registration; strict layout, bounds and validate-all loading keep the safety properties |
| Profile binding of a local Plan and artifact paths (earlier proposal) | Superseded: Run binding removes caller-supplied paths and checks bytes through lifecycle |
| Treat TestEnvironment as the lifecycle Environment or a public object | Reject: conflates build identity with diagnostic execution access |
| Independently pin tests to the newest compatible branch | Reject for ADR-23 imports: weakens the matching-source contract; independently versioned harnesses remain separate protocols |

## Acceptance and follow-ups

Acceptance is staged; each stage has its own acceptance and none is implied by an earlier one.

1. **CLI parity through Run binding:** strict discovery and validate-all loading, ordered selection and
   deduplication, substitution-grammar and isolation refusals, and TestEnvironment refusal before any build.
   Both `nvke/nvkectl/contract` and `nvke/nvkectl/cli` run through `container-command/v1` with the `local`
   environment on native Linux AMD64 and ARM64, from `--run` and from build-first, at an NVKE revision
   exposing #1578 with refreshed maintained pins. Negatives: broken or wrong-version executable, changed
   bytes, unknown or ambiguous output, incomplete Run, missing entrypoint, unsupported capability, timeout and
   SIGINT/SIGTERM, with parity of isolation, budgets, failure statuses and cleanup. Existing recorded CLI runs
   are baselines, not proof of the new path.
2. **Multiple inputs and a disposable cluster:** one controller/chart suite consuming at least two named
   inputs, with exact supplied-image/package selection, refusal of default or rebuilt substitutes, runtime
   subject observation, a decided cluster owner and teardown after failure, timeout and cancellation. Then
   adopt a second repository without changing generic loading rules.
3. **CI route:** the same registrations on generator-owned workflows (ADR-21), with per-phase network and
   credential delivery either enforced or refused. Credentials, runner allocation, triggers and transport need
   their own acceptance; no workflow capability follows from merging this ADR.
4. **Hardware provider integration:** a later stage requiring an agreed upstream interface, artifact transfer,
   a supported provider integration with resource ownership and cleanup, and its own acceptance.

Across stages, filtered/missing/skipped/empty required suites cannot become exhaustive acceptance, complete
capture cannot become pass, and diagnostic results cannot become qualification evidence. Named owners must
ratify the schemas, executor, interpreters and migration before maintained adoption. Reconciliation records
implementation, contract tests and external execution separately.
