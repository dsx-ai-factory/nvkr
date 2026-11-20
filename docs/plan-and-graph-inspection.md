# Inspecting Plans and explaining generated changes

Use the validated Plan views to review required outputs, producer instructions, artifact dependencies and
publication routes before executing a Run. These read-only views explain the complete generated Plan and
its changes. They do not observe output bytes, verify a release, or authorize execution.

The Plan in `.github/nvkr-run-nvke/plan.json` is a small index into the repository's shared `.github/nvkr-plan-store/`.
Start with the store's `index.json` to find catalogs, target steps, outputs and scripts by name and see which bundles share each variant.
The v3 tree omits the artifact graph and records its typed `graphDigest` in the root. Loading derives the
graph from the verified stored inputs and checks that digest and the complete Plan identity. Use `run graph`
below to inspect the complete graph; packed Plan exports retain it unchanged.
The [generated tree guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/generated-plan-tree.md) traces workflow job comments through the shared `index.json` to exact producer commands and explains the CI adapter boundary.
Loading verifies the frozen objects and reconstructs the canonical Plan under [ADR-21's storage contract](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/0021-generated-pipeline-maintenance-and-lifecycle.md#1-the-emitted-pipeline-is-a-generated-artifact-not-an-owned-scaffold). Keep human views
outside `.github/nvkr-run-nvke/`: that directory admits only manifest-owned generated files. Inspection never
updates the Plan or its ownership manifest.

## Read the Plan

Inspect the current generated Plan without resolving catalogs or contacting an external service:

```sh
nvkr run plan --plan .github/nvkr-run-nvke/plan.json
nvkr run plan --plan .github/nvkr-run-nvke/plan.json --pretty
```

The text view lists required output slots and Build Digests, every producer instruction and dependency,
and each stored producer field in sorted order. This includes source and vendor pins, patch order, environment
values, build/prepare argv, Nix flake inputs, Packer/material inputs and artifact paths when present. It also
lists artifact operations and publication routes. Nonprinting characters are escaped for display. `--pretty` shows every stored field as indented JSON; it is a display view and is rejected as a
canonical Plan input. `--json` emits the exact canonical Plan bytes, with no appended newline.

The same views can compile authored inputs directly:

```sh
nvkr run plan \
  --definition docs/examples/nvke-deploy/definition.yaml \
  --environment docs/examples/nvke-deploy/environment-github.yaml --pretty
```

Use either `--plan` or the pair `--definition` and `--environment`. Compilation can need access to pinned
Git objects for catalog pin-set revalidation. Reading an existing generated Plan needs its bundle index, its manifest and the store objects that manifest lists;
a packed Plan exported with `--json` needs only that one file.
Readers require the current schema, canonical bytes, safe bounded regular files, and complete graph and
binding validation. Invalid input produces a coded error before any result bytes are emitted.

## Browse in the terminal

```sh
nvkr run plan --plan .github/nvkr-run-nvke/plan.json --ui=tui
```

The explicit terminal explorer uses validated typed output/operation membership
and dependencies. It leaves the Plan and generated ownership manifest unchanged.
Use arrows to select, Enter for details/dependencies, Tab or 1/2 for panes, Esc to go back,
`/` for output search, `f` for filters and `?` for help. Tabs identify Outputs
and Details, with frames separating the panes. Activity appears only when supplied
(for example, during live Runs); static Plans have no Activity tab. `a` selects
the Activity pane only when available. q asks before closing
this read-only view: y or Ctrl+C closes it; n, Esc or Enter keeps it open. Closure does not
execute a Plan or clean up a Run. Small terminals use a single pane.
`--no-color` and nonempty `NO_COLOR` preserve textual state and focus.

The default Plan output remains plain; `--json` retains byte-exact canonical
output and `--pretty` retains its display-only JSON format. Explicit terminal mode
requires usable interactive stdin/stdout and cannot be combined with JSON/pretty
output. Planned operations and declared evidence lanes are descriptions, not
observed artifacts or authenticated runtime/release claims. For recorded attempts
and live Run presentation, see [Lifecycle](lifecycle.md#choose-local-run-presentation).

## Follow linked graphs

```sh
nvkr run graph --plan .github/nvkr-run-nvke/plan.json --format mermaid
nvkr run graph --plan .github/nvkr-run-nvke/plan.json --format dot
nvkr run graph --plan .github/nvkr-run-nvke/plan.json --format json
nvkr run graph --plan .github/nvkr-run-nvke/plan.json --output controller-chart --format mermaid
```

`--output NAME` focuses any view on one exact required output and its upstream dependencies, such as the
images a chart packages, plus that output's own publication route. The whole Plan is admitted first, an unknown name is a usage error, and the result
is marked as filtered. The Plan and its digest are unchanged, and without `--output` the output is unchanged.

The default `--view complete` includes every embedded producer IR node, factory operation, intermediate
artifact, required output slot and publication route. Exact producer references join the instruction graph
to the artifact operation graph. This exposes chart/image packaging dependencies, SBOM and evidence flow,
release preparation, publication, signing and verification where the selected Plan contains them.

| View | Vertices and links | Use |
|---|---|---|
| `complete` | Producer dependencies, exact instruction references, operation input/output edges, required slots and routes | Trace one output through the complete planned workflow |
| `producers` | Every per-output IR instruction and its declared dependencies | Review source acquisition, build commands, materials and terminal producer checks |
| `artifacts` | Every factory operation and artifact, required slots and publication routes | Review observation, evidence, package/image bindings, signing and publication |

The focused views keep exact Plan references. The complete view joins them through edges labeled
`instruction`. A `requires` edge goes from an upstream producer to its consumer; `produces` and `consumed by`
connect factory operations through artifacts. `fulfills slot` links an artifact to its independently required
named output. `publication route` describes the selected transport destination. These are different
relationships; a route or a planned verification node is not evidence that publication or verification ran.

Mermaid labels use [numeric entity escaping](https://mermaid.js.org/syntax/flowchart.html#entity-codes-to-escape-characters),
DOT labels use DOT quoting for quotes, backslashes and line breaks; other nonprinting characters are shown
as literal Unicode escape text. Renderer node IDs are generated independently of input strings.
JSON preserves complete vertex keys, source references and labeled edges under
`nvkr.dev/plan-graph-view/v1`. Its `planDigest` names the validated source Plan; `authority: description-only`
does not grant a new identity or execution receipt. DOT can be rendered with Graphviz; Mermaid can be
embedded in documentation viewers that support it. Large Plans can produce large diagrams; use a focused
view to reduce visual load without dropping nodes from that chosen graph.

These graphs describe planned dependencies. Registry navigation cannot recover an absent executable Plan
from a publication root or infer producer edges from matching artifact names.

## Compare regenerated Plans

Save the previous exact Plan before regeneration, then compare it to the regenerated file:

```sh
nvkr run plan --plan .github/nvkr-run-nvke/plan.json --json > /tmp/nvkr-plan-before.json
GOTOOLCHAIN=local go run ./cmd/nvkr run generate \
  --definition inputs/definitions/nvke/gpu-site-release.composite.yaml \
  --environment inputs/environments/github-nvke-gpu-site.yaml --engine github --root .
nvkr run diff --before /tmp/nvkr-plan-before.json --after .github/nvkr-run-nvke/plan.json
nvkr run diff --before /tmp/nvkr-plan-before.json --after .github/nvkr-run-nvke/plan.json --details
nvkr run diff --before /tmp/nvkr-plan-before.json --after .github/nvkr-run-nvke/plan.json --format json
```

The root regeneration uses the committed maintained NVKE inputs and the generator identity described in the
[maintained NVKE Definition's instructions](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/nvke/README.md). Generate alternative Definitions into scratch roots
as shown in the [pipeline guide](generate-pipeline.md).

The default text report identifies changed, added or removed per-output Build Digests, counts every changed
field by category, and shows changes in shared authored inputs, provider tools and publication routes.
Repeated catalog snapshots, derived identities and generated references remain counted; `--details` prints
every changed path. `--details` is valid only with text output; combining it with `--format json` fails
before Plan input is read. Long text values are explicitly abbreviated at a UTF-8 boundary. The canonical
descriptive JSON report retains every complete before/after value, including added and removed objects.

| Change | Interpretation |
|---|---|
| Source commit, artifact pin, build argv or logical producer input changes | Affected output Build Digests change; output byte identities remain unknown until execution |
| Runner or source transport changes | Plan identity changes; unchanged Build Digests are counted separately |
| Generator version changes | Plan metadata changes; the comparison does not invent a logical build change |
| Named output added or removed | Its independent required slot and Build entry appear as additions/removals |

Named schema collections are matched by their keys, so inserting an output does not present every later
output as replaced. Paths use JSON Pointer escaping plus selectors such as `/builds/@name=nvke-controller` and
`/graph/nodes/@id=package~1nvke-controller`. Text paths use Go string quoting to escape control characters; JSON retains
the exact paths. Ordered arrays, including command arguments and patches, remain positional.
A changed ordering among surviving named entries is reported as `@order`. Generated IDs that change with
pins appear as removed and added references; no heuristic guesses that two different producer identities
are interchangeable. JSON numbers retain integer precision.

A difference is a successful descriptive result by default. Add `--exit-code` for exit status 3
(`NVKR-E-DIFF`) when the two Plans differ. Malformed Plans and unsupported view options remain ordinary
coded errors. A text summary is not a substitute for `run generate --check`: semantic comparison explains
two already generated Plans, while the drift gate regenerates all owned files from authored inputs and
checks their exact bytes. Workflow-only changes still require reviewing the generated workflow diff.

## Implementation and evidence

`pkg/nvkr/planview` owns validated snapshots, display projections and comparisons. The CLI only selects inputs
and formats. The module reuses factory-owned canonical loading and full Plan validation; it grants no
artifact or execution authority. The Plan schema, canonical codec, typed digest families and generated
wire representation are unchanged. GitHub bundle storage uses the generated file tree.

Contract tests cover exact-byte JSON, display-only pretty JSON, caller-mutation isolation, invalid complete
Plans, bounded/no-follow file loading, complete graph vertices and edges including the maintained controller
chart/management-plane image relationship, diagram escaping, stable named comparisons, ordered argv/patches, large integers,
absent-versus-null values, and transport/execution changes that preserve Build Digests. CLI tests cover
file views, graph formats, concise/full differences, exit status and failure before output. These
contracts live in [the Plan-view tests](https://github.com/dsx-ai-factory/nvkr/tree/main/pkg/nvkr/planview) and the CLI inspection tests.
This is local inspection/contract evidence; it adds no registry, runner, signing or OS execution acceptance.
