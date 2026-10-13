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
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-github.yaml \
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
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated"
bin/nvkr run generate \
  --definition definitions/nvke-v1/definition.yaml \
  --environment definitions/nvke-v1/environment-github.yaml \
  --engine github --root "$NVKR_PIPELINE_DIR/generated" --check
```

Under that scratch root, inspect:

- `.github/workflows/nvkr-run.yml`: named GitHub Actions jobs, dependencies, runners and credential policies.
- `.github/nvkr-run/actions/`: shared actions with fixed interfaces for execution, preparation and transport.
- `.github/nvkr-run/action-data.json`: generated invocation parameters selected by stable job/segment keys.
- `.github/nvkr-run/plan.json`: the canonical Plan consumed by the workflow.
- `.github/nvkr-run/manifest.json`: the generated bundle's ownership and content record.

`--check` regenerates in memory and compares exact bytes without writing files. Use the same CLI build
and inputs for both commands; the generator version contributes to generated content. Change authored
inputs and regenerate instead of editing generated files.

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
download, with its condition, timeout and failure policy still visible on the caller. Transfers whose
outputs are referenced remain inline. Source credentials are passed explicitly and consumed only by the
original source step, separately from packing and uploads.
Repeated native preflight, checkout and helper/parameter preparation steps use descriptive aliases such as
`*check-native-linux-amd64`, `*checkout-workflow-source` and `*prepare-stage-<runner-artifact>`.
Preparation selects the current job through `github.job`; job policies, dependencies and operation calls
remain explicit.

The header points to fixed Plan and manifest files. Provider source-fetch names and ordering follow the
target and its consuming components, so routine source, artifact, toolchain, transport-action or generator
updates can leave this workflow unchanged while generated data and action bodies change. This holds when
topology, labels, action interfaces and visible policy stay the same. A checkout-pin update, a split or merge
of shared source consumers, or a change to runners, runtime containers/services, permissions, secret
references, time limits, qualification or inline setup can still change the workflow. Always regenerate
and check the complete bundle after an input change, even when the workflow file has no diff.

The shared actions and invocation data are part of the same bundle: commit them with the workflow, Plan and
manifest when adopting a generated pipeline. `--check` covers every template and data file, and regeneration
retires unchanged obsolete actions through the previous manifest. Editing an action directly produces drift just like editing the
workflow. Jobs retain explicit dependencies and separate results; matrices and reusable callable workflows
are not currently emitted.

Generation neither submits the workflow to GitHub nor builds, signs, or publishes artifacts. Continue with
[Plan inspection and comparison](plan-and-graph-inspection.md) or the [Run lifecycle](lifecycle.md).
