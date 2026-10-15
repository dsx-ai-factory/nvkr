# Create or change customer inputs

Translate the requested outcome into named outputs and supported capabilities before choosing files. Read the
selected revision's [catalog guide](../../../../docs/catalogs-and-components.md), [concept
reference](../../../../docs/user-concepts.md), [input index](../../../../inputs/README.md), and the nearest maintained example. Consult
[reconciliation](../../../../docs/adr/reconciliation.md) for execution/evidence limits. Customer requests for
"recipes" usually mean catalog producers; do not introduce a retired authored Recipe schema.

| Requested change | Authored owner |
|---|---|
| Outputs, components, platforms, formats, default and per-output catalog stacks (base + ordered overlays), purpose-selected tools, Helm/APT bindings | Definition |
| Runtime, runner/classes, resource ceilings, destination, credential references, signing policy | Environment |
| Source declarations, version constraints, complete producer commands, materials, output metadata and build toolchains | Catalog `registry.yaml` |
| Replace a producer or substitute an admitted toolchain for a defined scope | Catalog overlay referenced from a Definition catalog stack |
| Exact source or acquired-artifact pins | CLI-managed `pins.lock.yaml` |

Create a customer-owned input directory or edit the explicitly selected existing inputs. Copy a suitable
example only after checking its paths and scope; rebase relative references deliberately. Read the current
loader/schema and admitted examples rather than inventing fields. Prefer declarations using supported builders
over new NVKR Go code. Never place real credentials in YAML or generated Plans.

Keep the Definition/Environment boundary intact. Select exact producer keys and required material closure;
matching names or source URLs do not imply interchangeability. Output-specific catalog stacks are complete and
overlays bind exact predecessors. Do not silently refresh a stale overlay or independently bump a derived
pin-set member.

## Review changes with the CLI

Capture a baseline Plan if valid inputs already exist. Use a fresh scratch directory for Plans and alternative
generated bundles, keeping the customer's managed workflow untouched. The selected revision's [pipeline
guide](../../../../docs/generate-pipeline.md) provides exact scratch generation/check commands, and [Plan
inspection](../../../../docs/plan-and-graph-inspection.md) owns canonical JSON, graph, and semantic
comparison.

The normal validation sequence uses:

```sh
bin/nvkr definition validate --definition <definition-file>
bin/nvkr run plan --definition <definition-file> --environment <environment-file> --json
bin/nvkr run diff --before <baseline-plan> --after <changed-plan> --format json
```

Angle-bracket values are placeholders, not runnable examples. Save `run plan --json` stdout unchanged as the
canonical Plan; pretty JSON is display-only. For newly created inputs without a valid baseline, explain their
new required outputs instead of fabricating a comparison.

For source changes, inspect `catalog pin --help` and `catalog bump --help`; use their documented `--check`
preview where supported, then apply the explicitly requested update. Source pin flags precede the positional
source key. Use `catalog pin-artifacts` for its supported acquired-artifact path, reading its distinct options
rather than assuming every pin command has check mode. Derived members update through their owning source. The
[pin-set guide](../../../../docs/generic-pin-set-example.md) and [catalog maintenance
guide](../../../../docs/catalog-maintenance.md) cover coordinated updates and matching generator identity.

Never hand-edit `pins.lock.yaml`, generated `.github/workflows/nvkr-run.yml`, or managed `.github/nvkr-run/`
members. Use the selected inputs and same CLI identity for generation and `--check`. Root generation is
appropriate only when explicitly updating that root's owned bundle; alternate examples belong in a scratch
root. Follow the maintained Definition's own instructions when its committed bundle uses a different generator
stamp.

Review the authored diff, complete generated workflow diff where applicable, and before/after Plan. Explain
affected outputs/Build Digests, execution and destination changes, remaining capability constraints, and
validation failures. Plan differences predict changed logical build inputs; artifact byte identities remain
unknown until execution. Local validation does not authenticate a fetched producer or prove its command works.

A bounded learning exercise is the [authoring lab](../../../../docs/examples/authoring-lab/README.md). It uses
fresh local fixtures and retains identity comparisons without Docker or producer execution; its local fixture
Git commits are not customer repository commits. Use it only for a tutorial or explicit validation exercise,
not as a substitute for editing the customer's inputs.
