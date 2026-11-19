# ADR-24 — Imported validator catalog and artifact bindings

- **Decision status:** Accepted (2026-11-14); registered container-command model below proposed for review in #567.
- **Implementation maturity:** The earlier provisional slice is what `main` implements: an indexed catalog,
  typed `nvke-cli/v1` dispatch and `validators run --profile` local-plan binding. Registration discovery,
  selection, Run binding, the `container-command/v1` executor, TestEnvironments and ValidationResult are
  migrated in stages under §10; see `reconciliation/README.md`.
- **Acceptance-test maturity:** The provisional slice's discovery/admission/snapshot and mocked CLI dispatch
  contracts are tested. No catalog execution against an actual upstream suite and no native-platform catalog
  acceptance is claimed.
- **Deciders:** njhensley
- **Index:** ADR-24 in the consolidated NVKR ADR set.
- **Relates to:** ADR-01 (canonical records), ADR-03 (coded errors), ADR-16 (diagnostic and conformance boundaries),
  ADR-17 (package ownership and resource lifetimes), ADR-18 (execution admission), ADR-19 (downstream ownership),
  ADR-20 (artifact subjects), ADR-21 (generated workflows), ADR-22 (source and tool pins),
  ADR-23 (upstream artifact-test interfaces), ADR-26 (composite Definitions).
- **Scope:** Discovery, organization, selection, Run binding, test-environment admission, execution and result
  recording for imported validation suites across repositories. This decision defines diagnostic contracts; it
  does not implement hardware provider integrations, CI scheduling, authenticated publication membership or
  release qualification.

## Context

NVKR builds artifacts from pinned producer catalogs. [ADR-23](0023-upstream-artifact-behavioral-validation.md)
defines how it can import upstream tests and run them against externally supplied artifacts without taking
ownership of upstream assertions. The current CLI diagnostic implements a narrow version of that flow: factory
validates a complete canonical Plan and selects one `nvkectl` producer; the source owner imports its exact
commit and tree; pipeline acquires dependencies and executes the upstream `contract` or `cli` suite against
privately staged bytes. Suite selection, component selection and execution conventions are specific to the CLI
implementation.

Controller images, Helm charts, end-to-end suites and artifacts from additional repositories need the same
discoverability. Adding command branches, scattered scripts or a Go adapter per repository would hide
requirements, repeat Plan interpretation and make it difficult to answer which validators accept a particular
output. Upstream artifact-test interfaces are commands run in a container from a source checkout, so one
executor with fixed isolation can serve them without per-repository code, provided registrations cannot
weaken that isolation.

AICR supplies a useful organizational precedent. Its [validator catalog](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/validators/catalog.yaml)
names checks and records execution metadata; [implementations](https://github.com/NVIDIA/aicr/tree/3a6e8609a6c3661b3e0220054232180b50388a67/validators/deployment)
live separately, and [recipes](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/README.md)
select checks. Its phase grouping and Kubernetes Job execution fit live-cluster checks. NVKR needs the same
separation of discovery, implementation and selection, organized around repositories and suite groups. It must
also preserve exact source relationships and support validators consuming several artifacts together.

There are already two relevant execution surfaces. `validators run` is a trusted-input local CLI diagnostic;
`validate` dispatches explicitly configured published-source `kube-conformance` checks through Hydrophone.
The latter retains publication authentication and separately admits complete local diagnostic Runs through
its existing conformance owner. `testresults` describes external report capture without parsing reports or
granting a passing verdict. The [CLI diagnostic](../artifact-testing.md) and
[external test output contract](../external-test-output-contract.md) document those implemented boundaries.
A common catalog must preserve these different input and result contracts.

## Decision

### 1. Register container commands while keeping assertions upstream

A validation suite is a registered container command that tests selected outputs of a completed NVKR Run.
Its registration declares the required artifacts, test origin, execution image and runtime requirements; NVKR
builds or opens the Run, checks those artifacts and supplies them to the command. The validator catalog is
diagnostic configuration, separate from the producer catalog. Neither a registration nor a TestEnvironment
(§6) is a public lifecycle object. Definitions retain ownership of what is built, and Environments and
execution owners retain their existing policy responsibilities.

The dependency is one-way: NVKR consumes upstream sources and producer-neutral artifact-test interfaces.
Upstream repositories do not depend on NVKR code, catalogs, Plans or tooling. Assertions, fixtures, suite
inventories and entrypoints remain upstream. NVKR owns the executor that supplies checked artifacts to that
interface, captures results and applies the result checks (§8).

Registrations are admitted by one versioned executor, `container-command/v1`, implied by the registration's
API version rather than authored. A registration declares:

| Configuration | Meaning |
|---|---|
| Input roles | Exact Run outputs, expected formats and the required producer component |
| Test origin | Source relationship to an input's producer (§5); a packaged test origin is not yet specified |
| Execution image | The selected producer's pinned toolchain image; an explicitly pinned image needs an admitted image owner and is not yet accepted |
| Working directory | Relative path beneath the verified test-origin source |
| Command | Executable and argument vector, executed without shell interpretation |
| Environment bindings | Literal values or documented input references |
| Acquisition command | Optional argument vector that prepares dependencies without the artifact |
| Requirements | Supported platforms and capabilities |
| Timeout | Suite execution budget (§7) |
| Credential requirements and bindings | Logical credentials the suite needs and their explicit delivery to its command, such as named environment variables or private mounted files |
| Network requirements | Destinations and ports, declared separately for acquisition and test execution |

Documented references are `${inputs.<role>.path}`, the container path of the staged input, and
`${inputs.<role>.version}`, the expected version factory derives from that role's producer in the Run's Plan.
A role whose producer records no expected version fails binding when the reference is used. There is no
host-environment lookup and no shell; an unknown role or field fails loading. References are accepted only in
environment values; the same text in `command` or `acquisition.command` fails loading.

Credential and network requirements are part of the registration model, but the `v1` schema does not yet
contain them. They are added as optional `v1` fields once the executor can enforce declared destinations and
an owner delivers credentials (§6); existing registrations stay valid. Changing an existing field's meaning
requires a new API version. Until those fields exist, a suite needing test-phase network access or credentials
cannot be registered, so it cannot be admitted.

Isolation is executor-owned. The schema has no fields for mounts, devices, privileges, credential values or
host paths, and unknown fields fail (§3), so a registration cannot weaken the executor's defaults. The parity
baseline is the existing CLI diagnostic container: `--init`, the invoking user's UID and GID, a read-only root
filesystem, all capabilities dropped and no new privileges; a writable `/tmp` tmpfs; read-only source; a
writable private `/cache` mount; fixed Go settings (`GOTOOLCHAIN=local`, `GOWORK=off`,
`GOFLAGS=-mod=readonly -modcacherw`, `CGO_ENABLED=0`, `GOCACHE`/`GOPATH` beneath `/cache`, `HOME=/tmp`);
acquisition without the artifact; a test phase with `--network none`, `GOPROXY=off` and the artifact mounted
read-only; separate phase budgets, stop/join and bounded cleanup. The Go settings are the CLI baseline, not a
generic rule; a new executor version, not a registration field, changes any of these properties.

This adopts AICR's catalog pattern without importing its API schema, Go dependency or Job runtime. NVKR does
not require every validator to be packaged as a standalone container image.

### 2. Discover registrations by repository, suite group and suite

A registration lives at `{repository}/{suite-group}/{suite}.yaml` beneath one catalog root; its ID equals that
relative path without `.yaml`, and a declared `id` that differs fails loading. A suite group usually names an
artifact (`nvke/nvkectl/contract`) but need not: an end-to-end group may consume several artifacts
(`nvke/e2e/site-controller`). Each segment contains only lowercase ASCII letters, digits and hyphens, starts
and ends with an alphanumeric character, and is at most 64 bytes.

```text
<catalog-root>/
  nvke/
    nvkectl/
      contract.yaml
      cli.yaml
    controller/
      health.yaml
  other-repository/
    ...
```

These paths illustrate organization, not registered capabilities; future registrations must not be installed
as placeholders. The repository segment is an ownership namespace, not a Git source identity or implicit
repository URL. Renaming a registration changes its ID; there is no fuzzy lookup or alias. Platform and
capability requirements are metadata and never determine the directory tree.

One catalog root is loaded per invocation. NVKR uses its shipped catalog by default;
`--validator-catalog <dir>` selects a complete replacement with no inheritance, overlay or fallback to shipped
registrations. `--data` keeps its existing producer-catalog meaning. Existing
`inputs/testing/nightly-profiles.json` retains its separate coordinator ownership.

Loading is a bounded, cancellation-aware walk through the held catalog directory. It descends only through
real `{repository}` and `{repository}/{suite-group}` directories. Any other entry fails loading: a
non-directory at those levels, anything at the third level that is not a `{suite}.yaml` regular file,
anything deeper, an empty directory, a hidden file, a symlink or escaping path, a duplicate ID or an unknown
field. Bounds are 1 MiB per document, 16 MiB per catalog, 1,024 registrations and 1,024 entries per
directory. Every registration is fully validated even when unselected, so one invalid registration fails the
invocation rather than being skipped.

### 3. Use strict, bounded schemas

| Document | API version | Kind | Owns |
|---|---|---|---|
| Registration | `nvkr.dev/validator-registration/v1` | `ValidatorRegistration` | Input roles, test origin, image, command, environment, requirements and timeout |
| Test environment | `nvkr.dev/test-environment/v1` | `TestEnvironment` | Provider, provided capabilities, credential bindings and permitted network (§6) |
| Result | `nvkr.dev/validation-result/v1` | `ValidationResult` | Build, environment and per-suite outcomes (§8) |

Readers dispatch on exact version and kind and reject unknown fields, duplicate keys, nulls, invalid values
and unresolved references. YAML and JSON decode into the same typed model; YAML aliases, anchors, merge keys
and multi-document streams are rejected. A registration declares at most 64 input roles, platforms and
capabilities, and at most 256 command arguments and environment bindings. Descriptions are display text and
cannot select behavior. Package or test counts are never presented as a count of artifact contracts.

The CLI contract registration has this shape. It invokes the upstream supplied-executable entrypoint
`make test-artifact`, which compiles the upstream test harness and never rebuilds `nvkectl`; that interface
is proposed in NVKE [#1578](https://github.com/dsx-ai-factory/nvke/pull/1578), which is not yet merged:

```yaml
apiVersion: nvkr.dev/validator-registration/v1
kind: ValidatorRegistration
id: nvke/nvkectl/contract
description: Run upstream process contracts against a built nvkectl executable.
inputs:
  executable:
    output: nvkectl
    component: nvkectl
    format: executable
testOrigin:
  fromInput: executable
image:
  fromInput: executable      # the producer's pinned toolchain image
workingDirectory: cli
acquisition:
  command: [go, mod, download]
command: [make, --no-print-directory, test-artifact, GO=go, "GO_TEST_FLAGS=-count=1 -timeout=15m -json",
  COVERAGE_DIR=/cache/coverage]
environment:
  NVKECTL: "${inputs.executable.path}"
  NVKECTL_EXPECTED_VERSION: "${inputs.executable.version}"
requires:
  platforms: [linux/amd64, linux/arm64]
  capabilities: [docker]
timeout: 30m
```

This suite requires Docker, but no Kubernetes cluster or GPU, and no test-phase credentials or network access.
If the Run's revision lacks the `test-artifact` target, the suite reports an execution error; no other
revision is substituted. The executor adds its own minimum requirements (for example Git for source import);
a registration cannot omit them.

### 4. Select suites and bind them to a completed Run

An invocation names one or more selections, each an exact ID or a parent path (`repository` or
`repository/suite-group`). Selections are processed in user order; a parent expands to its registrations
sorted by ID; a repeated suite keeps its first position. A selection matching no registration fails. There
are no globs, no fuzzy aliases and no implicit run-all: zero selections is a usage error. Suites execute
sequentially in the resulting order.

Input mode is either build-first or an existing Run (`--run <attempt>`). Build-first compiles the supplied
Definition and Environment through the existing local Run path, checks before building that every selected
input is a planned output on a declared platform, builds a new attempt and then binds it exactly as `--run`
binds an existing one; a failed build binds nothing. Validation continues only from a successfully completed
Run; incomplete, failed or uncommitted Runs fail.

Lifecycle opens the Run through `CaptureLocalDiagnosticArtifacts`, which replays its captured compiler inputs
and checks every named payload's bytes. That capture admits only complete Runs in the local execution lane
with captured compile inputs and a non-composite Plan, so an existing Run must have been built with
`run local --capture-inputs-root`. Hosted Runs fail binding until lifecycle capture is extended to them;
composite Runs remain deferred under [ADR-26 §7](0026-definition-composition.md#7-evidence-lanes-no-stronger-than-the-weakest-contribution).

Each input role binds to exactly one named Run output matching the registration's output and format, on a
platform the registration declares, with exactly one build subject of the declared component; a missing or
ambiguous output or subject fails. Factory derives the test-origin source, toolchain image and expected version
from the producer of that output in the same captured Plan. Every suite retains its own named-output bindings
even when outputs share a Build Digest. Callers supply no artifact paths or digests.

```sh
nvkr validators run \
  --validator-catalog ./company-validators \
  --run .nvkr/runs/<attempt> \
  --test-env local \
  nvke/nvkectl/contract
```

### 5. Derive imported suites from artifact source identity

For ADR-23 artifact suites, the test origin is the producer's build source. The registration designates one
input role as the origin; factory derives the repository, full commit and tree from that role's producer in
the Run's validated Plan, and `sourcegit` imports and verifies it. Registrations and invocations cannot supply
a replacement branch, `latest`, independent test commit or local checkout.

Each other input retains its own source identity, native platform and artifact subject. Sharing a repository
URL, repository namespace or version string never establishes a common revision. The result checks or upstream
entrypoint must explicitly enforce any required same-source relationship; otherwise the result describes a
test of the supplied combination. An independently pinned external harness, such as the existing Hydrophone
or downstream nightly harness, remains a distinct protocol with its own pin owner. Supporting another
repository means adopting its producer-neutral interface, not adding repository-specific loader branches.

The producer Plan remains the owner of build pins and toolchain selection. Registrations cannot repeat or
override toolchain digests or use AICR-style release-tag rewriting. Additional tools resolve through an
admitted tool owner.

### 6. Admit one TestEnvironment per invocation

A TestEnvironment is a versioned declaration of the resources and access available to a validation run: its
provider, provided capabilities, credential bindings and permitted connectivity. It is diagnostic execution
configuration: it is not the lifecycle `Environment` (`nvkr.dev/environment/v1`) and does not affect Build
identity. It cannot add commands or arguments to a suite.

```yaml
apiVersion: nvkr.dev/test-environment/v1
kind: TestEnvironment
metadata:
  name: forge
provider: forge
credentialRefs:
  forgeAPIKey:
    source: env
    name: FORGE_API_KEY
provides:
  capabilities: [bare-metal]
network:
  egress:
    - host: api.forge.example.com
      port: 443
```

`credentialRefs` identify runtime sources, supplied through CI secret injection or explicitly selected local
environment variables; values are delivered only to the phases that need them and are excluded from retained
configuration and results. `provides.capabilities: [bare-metal]` admits suites declaring
`requires.capabilities: [bare-metal]`. The forge example illustrates the schema; its provider integration is
not defined here.

One TestEnvironment is selected per invocation. `--test-env <name>` resolves a named declaration from the
configured environment directory, and an explicit environment-file option selects a declaration directly.
NVKR ships a built-in `local` environment providing `docker`, with no credentials and no test-phase network.
A declaration in the directory cannot reuse a built-in name; that ambiguity fails resolution. When `--test-env`
is omitted, the documented default is `local`. This is a resolution rule, not a fallback: if `local` cannot
satisfy a selected suite's requirements, the invocation fails before any build, and an explicitly named
environment never falls back to another. ValidationResult records the selected environment and the digest of
its admitted declaration.

Before any build, admission checks that the executor and provider integration support every selected suite's
platform, capability, credential and network requirements. Registrations declare requirements; they do not
authorize infrastructure access, credential use or runner allocation. Selecting a network-capable environment
never widens a suite's declared access, and the executor enforces the admitted access during execution.
Dependency acquisition is admitted separately from test execution.

Until pipeline can enforce declared destinations, acquisition runs with the parity baseline's unrestricted
egress, without the artifact or credentials; this is a named limitation (see Boundaries), not admitted access.
The test phase has no network. Suites requiring test-phase network access or credentials fail admission until
enforcement and a credential-delivery owner exist.

### 7. Execute through the existing owners

`pkg/nvkr/validation` is the primary owner of the feature. It orchestrates and owns registration loading,
selection, preparation, binding and result recording. Factory keeps Plan interpretation, `sourcegit` source
import, pipeline container execution, process lifetime and network enforcement, and lifecycle Run opening,
byte checks and the local build path. Proposed files are thin orchestration over those owners:

| File | Responsibility |
|---|---|
| `catalog*.go` | Registration loading and discovery |
| `selection.go` | Ordered selection and deduplication |
| `request.go` | Static preparation and frozen requests |
| `build.go` | Existing build integration |
| `artifacts.go` | Run binding and checked-input ownership |
| `execute.go` | Shared suite execution |
| `kind.go` | Disposable cluster lifecycle |
| `results.go` | Persistence, aggregation and observability |

The CLI and SDK delegate; package placement preserves the dependency direction and creates no import cycle.

An invocation validates configuration, selections and declared compatibility; builds or opens the Run and
requires it to be complete; checks exact output bindings and artifact bytes; checks runtime readiness;
executes suites; and collects results and cleans up. For each suite: acquire the test origin and execution
image; prepare a private workspace and dependencies; prepare required runtime resources; stage checked inputs
and resolve invocation values; execute the command; capture outcome and diagnostics; stop and join; clean up;
persist the suite result. Every blocking phase observes context.

The registration timeout bounds suite execution; setup and cleanup have separate bounded timeouts, and a
shorter caller context wins. Registrations at CLI parity keep its current bounds: 30-minute test and
dependency budgets and fixed Git-command budgets. Cleanup uses an independent bounded context; its failure is
reported without replacing an already observed suite failure. Partial diagnostics never become success.

Suites requiring a disposable local Kubernetes cluster receive an NVKR-managed kind cluster, by default one
fresh cluster per suite. NVKR registers cleanup ownership before creation; creates a uniquely named cluster
with pinned tools and node image; writes a private kubeconfig; loads checked native images; establishes
test-container connectivity; captures diagnostics; deletes owned resources; and records cleanup status.
Hardware suites use an explicitly supported infrastructure integration, such as Forge or DSX, with defined
resource ownership and cleanup; none is defined here. Hosted generation remains owned by ADR-21; this catalog
defines neither event triggers nor runner labels or workflow permissions.

### 8. Record results without granting evidence authority

A suite reports passed, failed or execution error, retaining the observed exit status and diagnostics. Exit
zero alone is not sufficient: the executor applies a named, versioned result interpreter recorded in the
result. For the CLI suites this is the existing CLI result checks, which reject missing, empty, skipped or
incomplete test results and changed staged bytes. A registration whose output has no admitted interpreter
cannot pass. How a registration names its interpreter is fixed with the executor, as an optional `v1` field.

Execution creates a ValidationResult before build or runtime work and updates it as execution progresses.
Each suite records its execution state (not started, preparing, running or completed), verdict (passed,
failed or execution error) and cleanup state, separately. The outline:

```yaml
apiVersion: nvkr.dev/validation-result/v1
kind: ValidationResult
id: <unique validation attempt ID>
build:
  run: <build Run identity; null if unavailable>
  outcome: <not-started | running | succeeded | failed | cancelled>
testEnvironment:
  name: <selected TestEnvironment name>
  provider: <provider identifier>
  declarationDigest: <digest of the admitted declaration>
status: <running | passed | failed | execution-error | cancelled>
suites:
  - id: <registered validator ID>
    parameters: {}            # resolved nonsecret values; v1 registrations declare none
    execution:
      state: <not-started | preparing | running | completed>
      termination: <exited | launch-error | timeout | cancelled | null>
      exitCode: <integer; null if no exit status was observed>
      startedAt: <UTC timestamp; null if not started>
      finishedAt: <UTC timestamp; null while unfinished>
    inputs:
      - role: <validator input role>
        output: <named Run output>
        component: <producer identity>
        format: <artifact format>
        digest: <tested artifact digest>
    testOrigin:
      repository: <source repository>
      commit: <exact source commit>
      tree: <source tree identity>
    executionImage:
      reference: <digest-pinned execution image>
    results:
      status: <passed | failed | execution-error | null>
      reason: <optional explanation>
      testReport:
        format: junit
        path: <relative retained report path>
      interpreter: <versioned result interpreter>
      artifacts:
        - format: <report format>
          path: <relative retained report path>
    cleanup:
      state: <not-required | pending | running | complete | failed>
results:
  report: <relative CTRF path; null if unavailable>
```

Retain original reports and raw test events. Unknown observations remain unknown; report fields cannot
manufacture a Run, suite completion or passing verdict. `testresults.Output` remains a descriptive handoff;
`CaptureStatus=complete` describes capture, not valid reports, exhaustive tests or passing behavior. The
Hydrophone route cannot inherit another interpreter's interpretation.

For exhaustive artifact-suite acceptance, the upstream revision must own a machine-checkable inventory or
completion protocol, and the interpreter rejects missing, zero-test, skipped, filtered or incomplete required
coverage. NVKR does not maintain copied test-name lists or expected counts. The existing CLI package checks
are weaker, so their results disclose that exhaustive completeness is unknown.

The lane remains **none (descriptive)**: passing is a diagnostic result, not release qualification. A result
does not issue a ConformanceReceipt, satisfy ADR-13 evidence, authorize signing or promote a release. ADR-16
qualification consumes independently produced and authenticated conformance evidence; running a cataloged
suite does not satisfy that handoff.

### 9. Freeze diagnostic configuration without changing Build identity

Four package-owned records represent the progression from invocation to result:

| Record | Contents | Establishes |
|---|---|---|
| ValidationRequest | Selections, catalog root, input mode, TestEnvironment selection | An invocation was supplied |
| PreparedValidation | Ordered suites, admitted registrations, exact source-document digests, the admitted TestEnvironment declaration, build inputs or Run locator | Configuration and selections are valid; a build may start |
| BoundValidation | Completed Run identity, exact artifact bindings, resolved test origins | A completed Run exists, selected inputs are checked, and runtime preparation may begin |
| ValidationResult | Build outcome, suite outcomes, execution context, logs, metrics, cleanup status | Outcomes and cleanup are recorded |

PreparedValidation is frozen: execution never reopens registration or environment files. The SHA-256 of each
registration's exact file bytes lets a reviewer distinguish a registration edit from an artifact change.
Credential values are excluded from every record.

Use the canonical codec for structured descriptions and raw SHA-256 only for exact file/report integrity.
This ADR allocates no new authenticated receipt or semantic digest family; a future evidence-bound validation
identity requires registration under ADR-01 and ADR-16. Registration and TestEnvironment edits do not change
Build Digests or modify an existing Plan. Results cannot be reused because Build Digests match; no result
cache or cross-output reuse is introduced.

### 10. Migrate through explicit boundaries

The model replaces the earlier provisional slice in stages: registration discovery; selection and Run
binding; execution and ValidationResult; then the `validators run` route. Until a stage lands, the slice it
replaces keeps working unchanged:

- `validators list` and `validators describe` change from `--catalog <index>` to `--validator-catalog <dir>`,
  reading registrations from `inputs/testing/validator-catalog/` instead of the
  `inputs/testing/validators/catalog.yaml` index.
- `validators run --profile` with `nvkr.dev/validation-profile/v1` local-plan bindings and the typed
  `nvke-cli/v1` adapter remain until `validators run` takes selections, `--run` or build-first inputs and
  `--test-env`; they are then removed together with the index and `nvkr.dev/validator/v1` entries.
- `client/v1` `ListValidators` and `DescribeValidator` take a catalog directory; `PrepareValidatorProfile` is
  replaced when the run route migrates. ADR-17 records their provisional coupling to `validation` types.

Profile documents are never reinterpreted as Run bindings, and a failed catalog lookup never falls back to
hard-coded execution. `validators` stays hidden until its schemas are ratified.

The unversioned JSON `validate --config` keeps its current published-source semantics and its independently
admitted complete-local-Run conformance route. Discovery lists registration metadata; it does not fetch
sources, probe Docker or launch infrastructure. Once the executor lands, discovery reports whether each
registration is admissible in the selected TestEnvironment using the same admission check as execution.

Errors keep ADR-03's codes: invalid configuration `invalid-catalog` or `usage`; unknown selections and
environments `unsupported`; unmet capabilities and platforms `engine-capability`; binding disagreements
`verify-failed`; an incomplete Run `gate-unmet`; source acquisition `fetch-failed`; a failed suite keeps the
current diagnostic's `build-failed`; timeout and cancellation `timeout`. No new exit codes are allocated.

## Boundaries the catalog cannot resolve

| Boundary | Required owner or prerequisite | Catalog limitation |
|---|---|---|
| Upstream adoption | Upstream owners provide and maintain the external-artifact interface at the selected revision (for the CLI, NVKE #1578) | Registration cannot create a missing entrypoint or approve its adoption |
| Maintained pin readiness | Producer catalog owners refresh explicit pins and regenerate Plans | A registration cannot patch a checkout or substitute a convenient test revision |
| Suite completeness | Upstream owns an inventory/completion protocol; the interpreter validates it | Package presence, test counts and exit zero cannot establish exhaustive coverage |
| Run/output membership | Lifecycle checks a committed local Run's bytes; publication authentication stays with its owners | Local Run binding is diagnostic: no signatures, producer identity or ArtifactCheckReceipt |
| Installed identity | Existing conformance owns strict test-bound OCI observations; ADR-29 assigns standalone collection/comparison to the observation owner, with deployment owners supplying installation records | Registrations, requested references and rendering do not establish installed identity |
| Isolation, network and credentials | The executor owns isolation; pipeline enforces network; a delivery owner injects credentials | Acquisition currently has unrestricted egress, and the writable `/cache` mount shared by acquisition and test is inside the trust boundary; a declared requirement or TestEnvironment entry grants no access the executor cannot enforce |
| Infrastructure | NVKR-managed kind clusters; explicitly supported provider integrations for hardware | A declared capability is not available capacity, a lease or cleanup ownership |
| Report validity and verdict | Named, versioned result interpreters | A report format label, complete capture or exit zero is not a passing result |
| CI policy | Workflow generator and downstream coordinators own transport, triggers and credentials | Catalog files do not authorize workflow generation, scheduling or runner changes |
| Qualification | Independent evidence production, authentication and policy under ADR-16 | Diagnostic success cannot strengthen itself into release authority |

These boundaries are acceptance prerequisites for stronger claims, not reasons to hide useful diagnostics.
Discovery shows each registration's requirements and known limitations so operators can distinguish a
cataloged diagnostic from an exhaustively checked or authenticated result. Upstream entrypoint readiness, Run
availability and runtime capacity remain unverified until their owning phases run.

## Consequences

Engineers can find validators by repository and suite group, inspect their requirements, and add suites for a
new repository as data when its upstream interface fits the executor. Run binding removes caller-supplied
artifact paths. Named input roles support controller, chart and end-to-end combinations without committing the
catalog to a single-artifact model. Existing execution and lifecycle owners remain where correctness is
enforced.

The cost is a strict loader, documented references, versioned interpreters, a TestEnvironment schema and an
explicit migration from the provisional slice. A generic executor concentrates risk: its isolation, budgets
and references must be reviewed once and changed only by version. Registrations cannot make unsupported
interfaces work, so adding a repository may still require upstream adoption. A single green summary would
conceal materially different claims, so diagnostic and evidence contracts stay separate.

## Alternatives considered

| Alternative | Decision |
|---|---|
| Copy AICR's schema and Kubernetes Job execution | Reject: does not express NVKR's artifact roles and Plan-derived source relationships; imposes cluster execution on CLI diagnostics |
| Group primarily by deployment/conformance/performance | Reject: repository and suite group are the navigation keys; execution requirements remain metadata |
| Require every suite group to name one artifact | Reject: end-to-end suites consume several artifacts |
| Add commands or scripts for each suite | Reject: scatters discovery and repeats binding and execution logic |
| A typed Go adapter per upstream interface, with entries naming only a suite selector | Reject: needs Go changes per repository; one executor with fixed isolation and a literal argument vector keeps the same safety properties |
| Shell commands, mounts, overlays or embedded provisioning in YAML | Reject: creates an execution language and lets registrations weaken executor isolation |
| An explicit `catalog.yaml` index of entry files | Reject: path-derived IDs give one owner per registration; strict layout, bounds and validate-all loading keep the safety properties |
| Profiles binding a local Plan and caller-supplied artifact paths | Reject: Run binding checks bytes through lifecycle and removes caller-supplied paths |
| Put validation entries in producer registries or Definitions | Reject: conflates diagnostic selection with build declarations and pin ownership |
| Treat TestEnvironment as the lifecycle Environment | Reject: conflates Build identity with diagnostic execution access |
| Copy upstream tests and expected test-name lists into NVKR | Reject: duplicates assertion ownership and lets imports drift from their source revision |
| Independently pin tests to the newest compatible branch | Reject for ADR-23 imports: weakens the matching-source contract; independently versioned harnesses remain separate protocols |

## Acceptance and follow-ups

Acceptance is staged; each stage has its own acceptance and none is implied by an earlier one.

1. **CLI parity through Run binding:** strict discovery and validate-all loading, ordered selection and
   deduplication, reference and isolation refusals, and TestEnvironment refusal before any build.
   Both `nvke/nvkectl/contract` and `nvke/nvkectl/cli` run through `container-command/v1` with the `local`
   environment on native Linux AMD64 and ARM64, from `--run` and from build-first, at an NVKE revision exposing
   #1578 with refreshed maintained pins. Negatives: broken or wrong-version executable, changed bytes, unknown
   or ambiguous output, incomplete Run, missing entrypoint, unsupported capability, timeout and SIGINT/SIGTERM,
   with parity of isolation, budgets, failure statuses and cleanup. Existing recorded CLI runs are baselines,
   not proof of the new path.
2. **Multiple inputs and a disposable cluster:** one suite consuming at least two named inputs, with exact
   supplied-image/package selection, refusal of default or rebuilt substitutes, runtime subject observation, an
   NVKR-managed kind cluster and teardown after failure, timeout and cancellation. Then adopt a second
   repository without changing generic loading rules.
3. **CI route:** lifecycle capture of hosted Runs is a prerequisite. The same registrations then run on
   generator-owned workflows (ADR-21), with per-phase network and credential delivery either enforced or
   refused. Credentials, runner allocation, triggers and transport need their own acceptance; no workflow
   capability follows from merging this ADR.
4. **Hardware provider integration:** a later stage requiring an agreed upstream interface, artifact transfer,
   a supported provider integration with resource ownership and cleanup, and its own acceptance.

Across stages, filtered/missing/skipped/empty required suites cannot become exhaustive acceptance, complete
capture cannot become pass, and diagnostic results cannot become qualification evidence. The validation,
artifact-production and upstream interface owners, not yet named, must approve the schemas, executor,
interpreters and migration before maintained adoption. Reconciliation records implementation, contract tests
and external execution separately.
