# Local Plan inspection validation

Date: 2026-10-03 UTC. Base: `d1a4ec9`. Scope: local read-only Plan inspection and contract validation on
macOS ARM64 with Go `1.27.1` and `GOTOOLCHAIN=local`. This does not record a producer build, external hosted
Run, registry interaction, signing operation, or release acceptance.

## Tested source

The implementation was exercised as working-tree changes over the base. These Git blob identities identify
the exact executable source bytes independently of the later commit; they are not NKD Build Digests.

| Source | Git blob |
|---|---|
| `pkg/nkd/planview/inspect.go` | `e8047154ab78c33aadb5b991917d7ddb3b566ecd` |
| `pkg/nkd/planview/graph.go` | `1d693b3328e3b38206acab0e4cd4aefd7989e83c` |
| `pkg/nkd/planview/diff.go` | `965d85db4a2fee195296fe67b5eeb937890dbf5a` |
| `cmd/nkdctl/plan_views.go` | `afa5cb62b7aedc9b0b203eba71af8d86d3589ab0` |
| `cmd/nkdctl/lifecycle.go` | `5ce65f77ab320b6c5e8f91f990a2a26c38b9628c` |

## Maintained deploy Plan

A locally built CLI read `.github/nkd-run/plan.json`. Its Plan Digest was
`sha256:7edadcb49c6ac7348c695e112f57df187c4de25efbb213cff26f20d4cb1b78a9`.
`run plan --json` produced bytes equal to that exact file (`cmp` passed). The text view identified all six
required outputs, producer instructions, artifact operations and publication routes.

The complete JSON graph contained 96 vertices: 14 producer instructions, 33 operations, 42 artifacts,
six required output slots and one publication route. Its 155 edges comprised eight producer dependencies,
eight instruction-reference joins, 42 production edges, 90 consumption edges, six required-slot bindings
and one publication-route binding. Mermaid and DOT exports completed. Contract tests independently checked
every declared vertex/edge and the controller chart's dependency on the CWE package.

Comparing the original Plan to the exact exported copy with `run diff --exit-code` reported no changes and
returned success. CLI contract tests separately exercised a changed generator version with unchanged Build
Digests, full JSON/detailed reports and differences-present status 3. Package tests compiled source-pin,
transport, runner, added-output and removed-output variants and checked their logical identity effects.

No external Graphviz or Mermaid renderer was run. The recorded checks establish deterministic graph exports,
complete structural projection and syntax escaping, not a reviewed diagram layout in a particular viewer.

## Completion gates

All commands completed successfully against the tested source:

```sh
make fmt-check
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
golangci-lint run
make shellcheck
make vulncheck
git diff --check
make workflows-check
```

Lint reported zero issues and the vulnerability scanner reported no vulnerabilities. The generated bundle
check reported the same Plan Digest above and no drift. Local documentation links and whitespace in new
files were also checked. No production files under `.github/nkd-run/` or the generated workflow changed.
The changes touch neither signing nor pipeline execution code; this record makes no new Linux runtime claim.
