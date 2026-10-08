# Inspecting Plans and explaining generated changes

Use the validated Plan views to review required outputs, producer instructions, artifact dependencies and
publication routes before executing a Run. These read-only views explain the complete generated Plan and
its changes. They do not observe output bytes, verify a release, or authorize execution.

The Plan in `.github/nkd-run/plan.json` remains canonical, compact JSON. Its exact bytes still participate
in Plan identity and generated-file drift checks. Keep human views outside `.github/nkd-run/`: that directory
admits only manifest-owned generated files. Inspection never updates the Plan or its ownership manifest.

## Read the Plan

Inspect the current generated Plan without resolving catalogs or contacting an external service:

```sh
nkdctl run plan --plan .github/nkd-run/plan.json
nkdctl run plan --plan .github/nkd-run/plan.json --pretty
```

The text view lists required output slots and Build Digests, every producer instruction and dependency,
and each stored producer field in sorted order. This includes source and vendor pins, patch order, environment
values, build/prepare argv, Nix flake inputs, Packer/material inputs and artifact paths when present. It also
lists artifact operations and publication routes. Nonprinting characters are escaped for display. `--pretty` shows every stored field as indented JSON; it is a display view and is rejected as a
canonical Plan input. `--json` emits the exact canonical Plan bytes, with no appended newline.

The same views can compile authored inputs directly:

```sh
nkdctl run plan \
  --definition definitions/nke-deploy/definition.yaml \
  --environment definitions/nke-deploy/environment-github.yaml --pretty
```

Use either `--plan` or the pair `--definition` and `--environment`. Compilation can need access to pinned
Git objects for catalog pin-set revalidation. Reading an existing generated Plan needs only its file.
Readers require the current schema, canonical bytes, safe bounded regular files, and complete graph and
binding validation. Invalid input produces a coded error before any result bytes are emitted.

## Follow linked graphs

```sh
nkdctl run graph --plan .github/nkd-run/plan.json --format mermaid
nkdctl run graph --plan .github/nkd-run/plan.json --format dot
nkdctl run graph --plan .github/nkd-run/plan.json --format json
```

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
`nkd.dev/plan-graph-view/v1`. Its `planDigest` names the validated source Plan; `authority: description-only`
does not grant a new identity or execution receipt. DOT can be rendered with Graphviz; Mermaid can be
embedded in documentation viewers that support it. Large Plans can produce large diagrams; use a focused
view to reduce visual load without dropping nodes from that chosen graph.

These graphs describe planned dependencies. Registry navigation cannot recover an absent executable Plan
from a publication root or infer producer edges from matching artifact names.

## Compare regenerated Plans

Save the previous exact Plan before regeneration, then compare it to the regenerated file:

```sh
git show HEAD:.github/nkd-run/plan.json > /tmp/nkd-plan-before.json
GOTOOLCHAIN=local go run ./cmd/nkdctl run generate \
  --definition definitions/nke-m2/definition.yaml \
  --environment definitions/nke-m2/environment-github.yaml --engine github --root .
nkdctl run diff --before /tmp/nkd-plan-before.json --after .github/nkd-run/plan.json
nkdctl run diff --before /tmp/nkd-plan-before.json --after .github/nkd-run/plan.json --details
nkdctl run diff --before /tmp/nkd-plan-before.json --after .github/nkd-run/plan.json --format json
```

The root regeneration uses the committed M2 inputs and the generator identity described in the
[M2 instructions](../definitions/nke-m2/README.md). Generate alternative Definitions into scratch roots
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
output as replaced. Paths use JSON Pointer escaping plus selectors such as `/builds/@name=cwe` and
`/graph/nodes/@id=package~1cwe`. Text paths use Go string quoting to escape control characters; JSON retains
the exact paths. Ordered arrays, including command arguments and patches, remain positional.
A changed ordering among surviving named entries is reported as `@order`. Generated IDs that change with
pins appear as removed and added references; no heuristic guesses that two different producer identities
are interchangeable. JSON numbers retain integer precision.

A difference is a successful descriptive result by default. Add `--exit-code` for exit status 3
(`NKD-E-DIFF`) when the two Plans differ. Malformed Plans and unsupported view options remain ordinary
coded errors. A text summary is not a substitute for `run generate --check`: semantic comparison explains
two already generated Plans, while the drift gate regenerates all owned files from authored inputs and
checks their exact bytes. Workflow-only changes still require reviewing the generated workflow diff.

## Implementation and evidence

`pkg/nkd/planview` owns validated snapshots, display projections and comparisons. The CLI only selects inputs
and formats. The module reuses factory-owned canonical loading and full Plan validation; it grants no
artifact or execution authority. The Plan schema, canonical codec, typed digest families and generated
bundle format are unchanged.

Contract tests cover exact-byte JSON, display-only pretty JSON, caller-mutation isolation, invalid complete
Plans, bounded/no-follow file loading, complete graph vertices and edges including the maintained controller
chart/CWE relationship, diagram escaping, stable named comparisons, ordered argv/patches, large integers,
absent-versus-null values, and transport/execution changes that preserve Build Digests. CLI tests cover
file views, graph formats, concise/full differences, exit status and failure before output. These
contracts live in [the Plan-view tests](../pkg/nkd/planview/) and the CLI inspection tests.
This is local inspection/contract evidence; it adds no registry, runner, signing or OS execution acceptance.
