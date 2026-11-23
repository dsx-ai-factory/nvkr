# ADR-24 — Imported validator catalog and artifact bindings

- **Decision status:** Accepted (2026-11-14; revised 2026-11-20)
- **Implementation maturity:** `main` implements the earlier provisional slice (indexed catalog, typed
  `nvke-cli/v1` dispatch, `validators run --profile`). This model replaces it in stages (§9); see
  `reconciliation/README.md`.
- **Acceptance-test maturity:** No catalog execution against an actual upstream suite and no native-platform
  catalog acceptance is claimed.
- **Deciders:** njhensley
- **Relates to:** ADR-03 (coded errors), ADR-16 (diagnostic and conformance boundaries), ADR-17 (package
  ownership), ADR-21 (generated workflows), ADR-23 (upstream artifact-test interfaces), ADR-26 (composite
  Definitions).
- **Scope:** Discovery, selection, Run binding, test-environment admission, execution and result recording for
  imported validation suites. It does not define hardware provider integrations, CI scheduling, authenticated
  publication membership or release qualification.

## Context

[ADR-23](0023-upstream-artifact-behavioral-validation.md) lets NVKR run upstream tests against NVKR-built
artifacts without owning their assertions. The current CLI diagnostic does this for `nvkectl` only, with
suite, component and execution conventions hard-coded for that one case. Controller images, charts,
end-to-end suites and other repositories need the same capability, and per-suite command branches or a Go
adapter per repository would scatter requirements and repeat Plan interpretation.

Upstream artifact-test interfaces are commands run in a container from a source checkout. One executor with
fixed isolation can therefore serve every repository, provided a registration can declare what it needs but
cannot grant itself access, credentials or runners. AICR's [validator catalog](https://github.com/NVIDIA/aicr/blob/3a6e8609a6c3661b3e0220054232180b50388a67/recipes/validators/catalog.yaml)
is the organizational precedent: checks are named and described separately from their implementations and
from what selects them.

## Decision

### 1. Register suites as container commands

A validation suite is a registered container command that tests selected outputs of a completed NVKR Run.
Assertions, fixtures and entrypoints stay upstream; upstream repositories never depend on NVKR. One versioned
executor, `container-command/v1`, admits each registration's:

| Configuration | Meaning |
|---|---|
| Input roles | Exact Run outputs, expected formats and the required producer component |
| Test origin | Source relationship to an input's producer (§4) |
| Execution image | The selected producer's pinned toolchain image |
| Working directory and command | A path beneath the verified source, and an argument vector run without a shell |
| Environment bindings | Literals, `${inputs.<role>.path}` (the staged input) or `${inputs.<role>.version}` (the producer's expected version) |
| Acquisition command | Optional dependency preparation, run without the artifact |
| Requirements and timeout | Platforms, capabilities and the suite execution budget |
| Credential and network requirements | Logical credentials and their delivery; destinations and ports, declared separately for acquisition and test |

Isolation is executor-owned: the schema has no fields for mounts, devices, privileges, credential values or
host paths, so a registration cannot weaken it. The baseline is the existing CLI diagnostic container
(non-root, read-only root and source, all capabilities dropped, no new privileges, offline test phase with the
artifact mounted read-only). Only a new executor version changes it. Arguments of `command` and
`acquisition.command`, like environment values, may contain `${inputs.<declared role>.path|version}`; each
reference is replaced with the bound input's text before the container starts, without shell evaluation, and any
other `${` text fails loading. Credential and network fields are added to `v1` as optional fields once the
executor can enforce them; until then such suites cannot be registered. An older reader refuses such a field as
unknown, and changing a field's meaning needs a new API version.

### 2. Discover registrations by repository, suite group and suite

A registration lives at `{repository}/{suite-group}/{suite}.yaml`; its ID is that path without `.yaml`. A suite
group usually names an artifact (`nvke/nvkectl`) but may be an end-to-end group consuming several. NVKR uses its
shipped catalog by default; `--validator-catalog <dir>` selects a complete replacement with nothing inherited,
and `--data` keeps its producer-catalog meaning. Loading is strict and bounded: any unexpected entry, symlink,
duplicate ID, unknown field or invalid document fails the whole catalog, even when unselected. Readers dispatch
on exact `apiVersion` and `kind` and refuse duplicate keys, nulls, YAML aliases, anchors, merge keys and
multi-document streams. A registration has at most 64 input roles, platforms and capabilities, and at most 256
command arguments and environment bindings.

```yaml
apiVersion: nvkr.dev/validator-registration/v1
kind: ValidatorRegistration
id: nvke/nvkectl/contract
description: Run upstream process contracts against a built nvkectl executable.
inputs:
  executable: {output: nvkectl, component: nvkectl, format: executable}
testOrigin: {fromInput: executable}
image: {fromInput: executable}       # the producer's pinned toolchain image
workingDirectory: cli
acquisition:
  command: [go, mod, download]
command: [make, --no-print-directory, test-artifact, GO=go, "GO_TEST_FLAGS=-count=1 -timeout=15m -json"]
environment:
  NVKECTL: "${inputs.executable.path}"
  NVKECTL_EXPECTED_VERSION: "${inputs.executable.version}"
requires: {platforms: [linux/amd64, linux/arm64], capabilities: [docker]}
timeout: 30m
```

`make test-artifact` is NVKE's supplied-executable entrypoint, proposed in
[NVKE #1578](https://github.com/dsx-ai-factory/nvke/pull/1578). It compiles the upstream harness and never
rebuilds `nvkectl`; on a revision without it, the suite fails rather than testing a rebuilt binary.

### 3. Select suites and bind them to a completed Run

Selections are exact IDs or parent paths, processed in user order with duplicates keeping their first
position; a selection matching nothing fails, and there is no implicit run-all. Input is either build-first or
an existing Run (`--run .nvkr/runs/<attempt>`). Build-first checks that every selected input is a planned output
before building, and a failed build binds nothing. Validation continues only from a successfully completed Run.

Lifecycle opens the Run through `CaptureLocalDiagnosticArtifacts`, which checks every output's bytes. Each input
role binds to exactly one named output with the declared format, a declared platform and one build subject of
the declared component. Callers supply no artifact paths or digests. An existing Run must be a local Run with
captured inputs: the default `run local` captures them, and a Run with an explicit `--environment` needs
`--capture-inputs-root`. Hosted Runs need lifecycle capture first;
composite Runs stay deferred under ADR-26.

```sh
nvkr validators run --validator-catalog ./company-validators --run .nvkr/runs/<attempt> \
  --test-env local nvke/nvkectl/contract
```

### 4. Derive the test origin from the artifact's build source

Factory derives the test source's repository, commit and tree from the origin role's producer in the Run's
Plan, and `sourcegit` imports and verifies it. Registrations and invocations cannot substitute a branch,
`latest` or a local checkout, or override toolchain pins. Other inputs keep their own source identity; sharing a
repository or version string does not establish a common revision.

### 5. Admit one TestEnvironment per invocation

A TestEnvironment is a versioned declaration of a provider, capabilities, credential bindings and permitted
network. It is diagnostic configuration, not the lifecycle Environment, and does not affect Build identity.

```yaml
apiVersion: nvkr.dev/test-environment/v1
kind: TestEnvironment
metadata: {name: forge}
provider: forge
credentialRefs:
  forgeAPIKey: {source: env, name: FORGE_API_KEY}
provides: {capabilities: [bare-metal]}
network:
  egress:
    - {host: api.forge.example.invalid, port: 443}
```

`--test-env <name>` selects it; the default is `local`, which provides Docker with no credentials and no
test-phase network. This is a resolution rule, not a fallback: an environment that cannot satisfy a selected
suite fails the invocation before any build, and a named environment never falls back to another. The first
stage ships only `local`; the environment directory and file option are fixed by the stage that adds declared
environments.

Registrations declare requirements; they do not authorize access, credential use or runners. Selecting a
network-capable environment never widens a suite's access. Credentials reach only the phases that need them and
are excluded from retained configuration and results. Test commands run without network. Until pipeline can
enforce declared destinations, acquisition keeps the current unrestricted egress (see Boundaries). Suites
needing test-phase network, credentials or a cluster fail admission until the executor can enforce that access.

### 6. Execute through the existing owners

`pkg/nvkr/validation` owns loading, selection, preparation, binding and result recording. Factory keeps Plan
interpretation, `sourcegit` source import, pipeline container execution and process lifetime, and lifecycle
Run opening and the local build path. The CLI and SDK delegate.

Suites run in order. For each: acquire source and image; prepare a private workspace and dependencies; prepare
runtime resources; stage checked inputs; execute; capture outcome and diagnostics; stop and join; clean up;
persist the result. Timeout and cancellation stop the suite, still run cleanup and are recorded as the suite's
termination. Setup and cleanup have bounded timeouts separate from the suite timeout. Cleanup failure is
reported without hiding a suite failure. Suites needing a Kubernetes cluster get an NVKR-managed kind cluster,
one per suite by default, with cleanup ownership registered before creation. Hardware suites need an explicitly
supported integration, such as Forge or DSX, with defined resource ownership and cleanup.

### 7. Record results without granting evidence authority

Four records carry an invocation from input to result:

| Record | Contents |
|---|---|
| ValidationRequest | Selections, catalog, input mode, TestEnvironment |
| PreparedValidation | Ordered suites, admitted registrations and their file digests, the admitted TestEnvironment, build inputs or Run locator (frozen) |
| BoundValidation | Completed Run identity, exact artifact bindings, resolved test origins |
| ValidationResult | Build outcome, environment, per-suite execution state, verdict, inputs, test origin, image, reports, timing and cleanup |

The ValidationResult (`nvkr.dev/validation-result/v1`) is created before any build and updated as execution
progresses. Each suite reports passed, failed or execution error. Exit zero alone is not a pass: the existing
CLI result checks reject missing, empty, skipped or incomplete results, and the result records which versioned
interpreter decided. Those checks cannot prove exhaustive coverage, so results say completeness is unknown.

The lane is **none (descriptive)**. A pass is a diagnostic result: it issues no ConformanceReceipt, satisfies
no ADR-13 evidence and cannot authorize signing or promotion (ADR-16).

### 8. Keep Build identity unchanged

Registration and TestEnvironment edits never change Build Digests or a Plan. Results are not reused because
Build Digests match. Raw SHA-256 records exact file integrity only; no new digest family or receipt is added.

### 9. Migrate in stages

The model replaces the provisional slice in order: discovery, selection and binding, execution and results,
then the `validators run` route. `validators list|describe` move from `--catalog <index>` to
`--validator-catalog <dir>`. The index, `nvkr.dev/validator/v1` entries, the `nvke-cli/v1` adapter and
`--profile` are removed when `validators run` migrates; profiles are never reinterpreted as Run bindings.
`validate --config` is unchanged.

Errors use existing ADR-03 codes; none are added. Invalid configuration is `invalid-catalog` or `usage`; unknown
selections and environments `unsupported`; unmet capabilities, platforms and formats, including a bound output
whose format differs from the registration, `engine-capability`; missing or ambiguous outputs and subjects and
producer disagreements `verify-failed`; an incomplete Run `gate-unmet`; source acquisition `fetch-failed`; a
failed suite keeps the current diagnostic's `build-failed`; timeout and cancellation `timeout`.

## Boundaries

| Boundary | Owner | Catalog limitation |
|---|---|---|
| Upstream interface | Upstream owners (for the CLI, NVKE #1578) | A registration cannot create a missing entrypoint |
| Suite completeness | Upstream inventory, checked by the interpreter | Exit zero and test counts do not prove exhaustive coverage |
| Run membership | Lifecycle checks local Run bytes; publication owners authenticate | Local binding is diagnostic, not authenticated membership |
| Network and credentials | Executor and pipeline enforce; a delivery owner injects | Acquisition currently has unrestricted egress, and the shared `/cache` mount is inside the trust boundary |
| Infrastructure | NVKR-managed kind clusters; supported hardware integrations | A declared capability is not capacity or a lease |
| CI and qualification | ADR-21 generator; ADR-16 evidence | Catalog files grant no workflow, runner or release authority |

## Consequences

Engineers find suites by repository and suite group and add new ones as data when the upstream interface fits
the executor. Run binding replaces caller-supplied artifact paths, and unsupported selections fail before any
build. The cost is one generic executor whose isolation must be reviewed once and changed only by version, a
TestEnvironment schema, and a staged migration. Registrations move a suite's entrypoint and arguments from
reviewed Go into catalog data, so the catalog owner reviews them; a registration can narrow a suite and still
pass, which is why completeness stays unknown.

## Alternatives considered

| Alternative | Decision |
|---|---|
| Typed Go adapter per upstream interface | Reject: Go changes for every repository; the executor keeps the same container isolation |
| Explicit `catalog.yaml` index | Reject: path-derived IDs give each registration one owner |
| Profiles binding a local Plan and artifact paths | Reject: Run binding checks bytes and removes caller paths |
| Shell commands, mounts or provisioning in YAML | Reject: lets registrations weaken isolation |
| Group by deployment, conformance or performance | Reject: repository and suite group are the navigation keys |
| Copy AICR's schema and Job runtime | Reject: no artifact roles; imposes cluster execution on CLI checks |
| Copy upstream tests into NVKR | Reject: duplicates assertion ownership |

## Acceptance and follow-ups

Each stage has its own acceptance.

1. **CLI through Run binding:** both `nvke/nvkectl` suites through `container-command/v1` in `local` on native
   Linux AMD64 and ARM64, from `--run` and build-first, at an NVKE revision with #1578. Negatives include a
   broken or wrong-version executable, changed bytes, unknown or ambiguous output, incomplete Run, missing
   entrypoint, unsupported capability, timeout and signals.
2. **Multiple inputs and a cluster:** enforced test-phase access confined to the suite's own kind cluster, then
   one suite consuming two named inputs with teardown after failure, timeout and cancellation.
3. **CI:** hosted Run capture first, then the same registrations on generator-owned workflows.
4. **Hardware:** a supported provider integration with resource ownership and cleanup.

Diagnostic results never become qualification evidence. Named validation, artifact-production and upstream
interface owners must approve the schemas and executor before maintained adoption.
