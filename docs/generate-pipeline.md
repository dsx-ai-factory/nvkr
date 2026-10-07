# Generate a GitHub Actions pipeline

Turn a Definition and Environment into a reviewable pipeline bundle. A **Definition** selects the named
outputs to build; an **Environment** supplies execution, publication, and signing settings. This walkthrough
uses the three-output NKE tutorial: a CLI executable, a container image, and a Helm chart.

Start at the repository root with `bin/nkdctl` built as described in the [quickstart](quickstart.md).
Run these commands in the same terminal. They write to a temporary directory, leaving the repository's
generated pipeline untouched.

## Inspect the intended work

Create a canonical Plan, then read its output list and build instructions:

```sh
NKD_PIPELINE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nkd-pipeline.XXXXXX")
bin/nkdctl run plan \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --json > "$NKD_PIPELINE_DIR/plan.json"
bin/nkdctl run plan --plan "$NKD_PIPELINE_DIR/plan.json"
```

The Plan freezes the selected inputs and describes the work. It is generated from your authored files,
not another configuration file to maintain. `--json` preserves its exact canonical bytes; `--pretty` is
only a display format and cannot replace that saved Plan.

Export a diagram to trace producer instructions, artifacts, evidence, and publication dependencies:

```sh
bin/nkdctl run graph --plan "$NKD_PIPELINE_DIR/plan.json" \
  --view complete --format mermaid > "$NKD_PIPELINE_DIR/plan.mmd"
```

Open the `.mmd` file in a Mermaid-compatible viewer. A planned signing or verification step describes
intended work; it does not establish that the step succeeded.

## Generate and check the bundle

```sh
mkdir "$NKD_PIPELINE_DIR/generated"
bin/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root "$NKD_PIPELINE_DIR/generated"
bin/nkdctl run generate \
  --definition definitions/nke-v1/definition.yaml \
  --environment definitions/nke-v1/environment-github.yaml \
  --engine github --root "$NKD_PIPELINE_DIR/generated" --check
```

Under that scratch root, inspect:

- `.github/workflows/nkd-run.yml`: GitHub Actions jobs and their dependencies.
- `.github/nkd-run/plan.json`: the canonical Plan consumed by the workflow.
- `.github/nkd-run/manifest.json`: the generated bundle's ownership and content record.

`--check` regenerates in memory and compares exact bytes without writing files. Use the same CLI build
and inputs for both commands; the generator version contributes to generated content. Change authored
inputs and regenerate instead of editing generated files.

Generation neither submits the workflow to GitHub nor builds, signs, or publishes artifacts. Continue with
[Plan inspection and comparison](plan-and-graph-inspection.md) or the [Run lifecycle](lifecycle.md).
