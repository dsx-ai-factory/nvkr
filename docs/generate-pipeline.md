# Generate a GitHub Actions pipeline

Turn a Definition and Environment into a reviewable pipeline bundle. A **Definition** selects the named
outputs to build; an **Environment** supplies execution, publication, and signing settings. This walkthrough
uses the three-output NVKE tutorial: a CLI executable, a container image, and a Helm chart.

Start at the repository root with `bin/nvkr` built as described in the [quickstart](quickstart.md).
Run these commands in the same terminal. They write to a temporary directory, leaving the repository's
generated pipeline untouched.

## Inspect the intended work

Create a canonical Plan, then read its output list and build instructions:

```sh
NVKR_PIPELINE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-pipeline.XXXXXX")
bin/nvkr run plan \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --json > "$NVKR_PIPELINE_DIR/plan.json"
bin/nvkr run plan --plan "$NVKR_PIPELINE_DIR/plan.json"
```

The Plan freezes the selected inputs and describes the work. It is generated from your authored files,
not another configuration file to maintain. `--json` preserves its exact canonical bytes; `--pretty` is
only a display format and cannot replace that saved Plan.

Export a diagram to trace producer instructions, artifacts, evidence, and publication dependencies:

```sh
bin/nvkr run graph --plan "$NVKR_PIPELINE_DIR/plan.json" \
  --view complete --format mermaid > "$NVKR_PIPELINE_DIR/plan.mmd"
```

Open the `.mmd` file in a Mermaid-compatible viewer. A planned signing or verification step describes
intended work; it does not establish that the step succeeded.

## Generate and check the bundle

```sh
mkdir "$NVKR_PIPELINE_DIR/generated"
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated"
bin/nvkr run generate \
  --definition docs/examples/nvke-tutorial/definition.yaml \
  --environment docs/examples/nvke-tutorial/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated" --check
```

Under that scratch root, inspect:

- `.github/workflows/nvkr-run.yml`: named GitHub Actions jobs, dependencies, runners and credential policies.
- `.github/nvkr-run/actions/`: shared actions with fixed interfaces for execution, preparation and transport.
- `.github/nvkr-run/action-data.json`: generated invocation parameters selected by stable job/segment keys.
- `.github/nvkr-run/plan.json`: the canonical Plan consumed by the workflow.
- `.github/nvkr-run/manifest.json`: the generated bundle's ownership and content record.

Those are the default production paths when the Environment omits `githubBundle`
or sets it to an empty string and leaves `githubDevelopment` false. To generate
a separate production project bundle, author `githubBundle` in that Environment.
A name contains at most 48 lowercase letters, digits and
hyphens, with alphanumeric ends. Its paths are derived by the generator:

| Environment selection | Workflow | Generated records directory |
|---|---|---|
| Production, omitted or empty `githubBundle` | `.github/workflows/nvkr-run.yml` | `.github/nvkr-run/` |
| Production, `githubBundle: <name>` | `.github/workflows/nvkr-run-<name>.yml` | `.github/nvkr-run-<name>/` |
| `githubDevelopment: true`, empty `githubBundle` | `.github/workflows/nvkr-dev.yml` | `.github/nvkr-dev/` |

Named production workflows display as `nvkr-project-<name>`. Default production
retains `nvkr-run-<Definition name>` and development retains
`nvkr-dev-<Definition name>`. These distinct names separate workflow-based concurrency
and keep named producers outside the default qualifier's display-name trigger, even
when the bundles select the same Definition. File paths remain as listed above.

Each records directory contains its own `plan.json` and `manifest.json`. Default
and named bundles can coexist; each owns only its files and retirement. The
selection changes Environment and Plan identity without changing component Build
identity. Set the signing policy for the chosen workflow explicitly; selecting a
bundle name never rewrites that policy or permits authored generated paths.
Development uses its separate branch, signing, publication and retention policy;
it does not authorize production acceptance. Combining `githubDevelopment: true`
with a nonempty `githubBundle` returns `CodeEngineCapability` before catalog
resolution or generated-output work. See the [feature-branch development loop](https://github.com/dsx-ai-factory/nvkr/blob/98ef3ec595d86d017345a0e2d635b388d1429b49/DEVELOPMENT.md#test-pipeline-changes-on-a-feature-branch).

`--check` regenerates in memory and compares exact bytes without writing files. Use the same CLI build
and inputs for both commands; the generator version contributes to generated content. Change authored
inputs and regenerate instead of editing generated files.

Named production Environments can opt into `githubRuntimePlan: true`. Generate their initial complete
bundle normally. On each dispatch the first bootstrap compiles the repository's current catalog pins
with `run generate --runtime`, freezes the resulting Plan, and carries that exact data to every job.
Routine source-version updates therefore do not require a regenerated bundle commit. CI uses the same
inputs with `--runtime --check`: it checks baseline integrity and unchanged workflow/action bytes,
membership and Environment policy without writing files. Changes outside that contract require normal
regeneration and review. This mode neither follows floating upstream versions nor changes the default
or development bundle contract; ordinary `--check` remains a complete byte comparison.

Generated GitHub transfers retain JSON locators in Actions and move their payloads to
`ghcr.io/<owner>/<repository>/ci-artifacts`. Configure separate repository secrets
`NVKR_CI_STORAGE_READ_TOKEN` and `NVKR_CI_STORAGE_WRITE_TOKEN` with access only to that private/internal
storage package, plus repository variables `NVKR_CI_STORAGE_READ_USER` and `NVKR_CI_STORAGE_WRITE_USER`
for the corresponding token owners. The package must have no linked repository; public, unknown, inaccessible or linked
policy fails closed. A missing package can receive a metadata-only seed before the same checks are repeated.
There is no repository-token or public Actions payload fallback. The repository token separately authenticates
repository metadata; storage credentials belong only to transfer operations and do not add package permissions
to build jobs. Producer commands do not receive them.

The Go transport stores ordered chunks no larger than 1 GiB. Single-file uploads stream deterministic archive
framing around the held carrier, without another full archive on disk. Downloads extract directly from the
ordered chunks into private staging and verify each chunk, the complete archive, exact size and source EOF
before exposing restored files. The v2 locator format replaces the unshipped single-layer draft; old v1
locators require a new run. Final hosted GHCR publication separately checks every destination package with
its publication credential and binds the push to the inspected metadata root. Provider locator retention
does not expire private carrier blobs; configure registry retention separately.

Before making the source public, verify hidden package grants and anonymous, unrelated-user and fork access
for storage, toolchain and release packages. Previously retained Actions artifacts, caches, active runs and
old workflow refs also require separate handling; this change does not migrate or delete historical payloads.
Compiled-output caches and CodeQL database uploads are disabled. Candidate OS builds retain private files
only for the exact trusted workflow commit, so arbitrary candidate code cannot access a later storage writer.
The nightly coordinator attaches its constructed cycle summary; raw suite report files are not public artifacts.

Generated workflows admit at most 2,000 locator uploads per attempt, including conditional diagnostic and finalization
uploads. The pinned download action lists up to the same bound before selecting names; its default limit
of 1,000 otherwise omits older locators in the maintained full build. The generator rejects larger bundles
before writing them. This is not an unlimited inventory bound for accumulated reruns; use a fresh Run for a
complete build. This setting changes locator discovery only, not private payload storage.

The generator rejects workflows above 500,000 bytes or 50,000 expanded YAML parsing events. YAML aliases
reduce file size but do not reduce the parser's expanded count. Maintained rendering tests reserve five
percent headroom. Compatible offline observation, SBOM and packaging steps share their producer job;
signing, publication and remote verification keep separate jobs and permissions. Payloads consumed only
inside their producing job are not packed or uploaded unless finalization needs them. This avoids shipping
an intermediate disk package as well as its prepared publication layout.

The per-output pipeline gives each remaining payload transfer a specific purpose:

| Handoff | Bytes moved | Purpose |
|---|---|---|
| Preparation producer → private CI storage → publisher | Original prepared payload and its metadata | Cross the producer/publication credential boundary; validate and publish the native output closure |
| Preparation producer → SBOM attester → publisher | Bounded metadata, canonical SBOM documents and signed bundles | Keep OIDC outside the producer while binding evidence to unchanged subjects |
| Publisher → product registry | Complete validated output closure | Publish the independently consumable native artifact |
| Publisher → artifact signer | Exact-root-bound metadata and reference | Derive signing subjects and verify evidence without another payload download |
| Product registry → independent verifier | Complete served payload closure | Verify the bytes actually published, plus signatures, provenance and SBOMs |
| Verifier → release assembly/finalization | Metadata, references and evidence records | Authenticate membership and completeness without retrieving payloads |

The publisher authenticates the attested metadata against the original preparation, verifies each bundle
and its exact decoded SBOM, and moves unchanged blobs into the final layout before full-byte publication
checks. The SBOM and signing jobs do not download or re-upload output payloads. Metadata inspection alone
is not payload verification; the independent remote readback remains required. This is the existing
`pipeline-attested` claim, not qualification or a new SLSA level.

Local factory restoration still retains its carrier while extracting into a private tree, then rehashes
the held source before atomic publication. That copy supports the current mutation and incomplete-stream
checks. It is distinct from the removed outer GHCR archive staging. A future direct registry-to-factory
restore would need to preserve both transport authentication and the exact Plan-selected extraction gate.

The generated producer emits a manual `workflow_dispatch` pipeline, with no cron
or callable producer trigger. A separately authored downstream CI coordinator can
schedule dispatches; that scheduling and runtime-test ownership are separate
from generated bundle ownership.

Adding `--qualification-policy` also generates the admitted M3 qualifier and its
fixed native smoke worker. The qualifier uses `workflow_run`; the worker uses
`workflow_call` from that same-repository qualifier at the same commit. Their
independent policy, credentials and ownership are separate from the producer
bundle and the downstream nightly E2E adapter. They add no generated cron or
general callable producer interface.
Current qualification policy admission selects the default producer workflow,
`.github/workflows/nvkr-run.yml`; named-producer qualification is not an admitted
configuration. The named nightly producers instead use the separate downstream
E2E adapter.

Read the top-level workflow to review which jobs can run, their dependency order, permissions, protected
Environments and time limits. Follow a job's local `uses: ./.github/nvkr-run/actions/...` reference to inspect
the shared operations it calls, then find its invocation key in `action-data.json` for exact node and carrier
values. Calls such as `execute-node`, `execute-source` and `collect-node-outputs` name the operation rather
than a generated shape. Grouped jobs reuse the node interface for each operation; optional slots and further
individual upload calls preserve every output without adding output-count variants. Invocation parameters
have named roles. The helper recomputes those values from the admitted Plan at runtime; this data is generated,
not another file to author. Templates contain fixed commands
and pinned actions, not scripts supplied as parameters. Native preflight, checkout, operator setup and timed
producer execution remain inline. A controlled transfer delegates to an action containing one upload or
download, with its condition, timeout and failure policy still visible on the caller. Steps whose outcomes
or outputs are referenced remain inline, including finalization and the final Run upload read by the closing
summary. Recovery uses the shared `recover-inputs` action before that inline finalization. Source credentials are passed explicitly and consumed only by the
original source step, separately from packing and uploads.
Repeated native preflight, checkout and helper/parameter preparation steps use descriptive aliases such as
`*check-native-linux-amd64`, `*checkout-workflow-source` and `*prepare-stage-<runner-artifact>`.
Preparation selects the current job through `github.job`; job policies, dependencies and operation calls
remain explicit.

The header points to fixed Plan and manifest files. Provider source-fetch names and ordering follow the
target and its consuming components, so routine source, artifact, toolchain or generator
updates can leave this workflow unchanged while generated data and action bodies change. This holds when
topology, labels, action interfaces and visible policy stay the same. A checkout-pin update, a split or merge
of shared source consumers, or a change to runners, runtime containers/services, permissions, secret
references, time limits, qualification or inline setup can still change the workflow. Transport pins remain
outside the workflow only when their calls can be extracted; the required inline final Run upload makes
upload-action pin updates visible in the maintained workflow. Always regenerate
and check the complete bundle after an input change, even when the workflow file has no diff.

The shared actions and invocation data are part of the same bundle: commit them with the workflow, Plan and
manifest when adopting a generated pipeline. `--check` covers every template and data file, and regeneration
retires unchanged obsolete actions through the previous manifest. Editing an action directly produces drift just like editing the
workflow. Producer jobs retain explicit dependencies and separate results; producer matrices and general
callable workflows remain deferred. The optional qualification bundle has its own fixed reusable native
worker and selected-platform matrix (ADR-21).

## Recover interrupted qualification generation

When generation includes `--qualification-policy`, an interrupted write can leave files such as
`.github/nvkr-qualification/manifest.stage`. `--check` reports the incomplete generation without repairing
it. Changing policy or other inputs before retrying can produce `CodeWriteConflict`: the saved stage does
not match the newly requested generation, so the writer preserves it and the published files.

1. Restore the exact Definition, Environment, catalogs and pins, qualification policy, and CLI build used
   by the interrupted command. Keep the same paths and output root.
2. Repeat that original `run generate` command without `--check`, then run it with `--check` to confirm
   recovery completed.
3. Reapply the intended policy or input changes, regenerate, and check again.

Do not delete stage files or ownership manifests to force an update. If the original inputs cannot be
established, retain the files for investigation instead of guessing their ownership. The producer and
qualification bundles have separate owners; their writes are not one atomic directory transaction.

Generation neither submits the workflow to GitHub nor builds, signs, or publishes artifacts. Continue with
[Plan inspection and comparison](plan-and-graph-inspection.md) or the [Run lifecycle](lifecycle.md).
