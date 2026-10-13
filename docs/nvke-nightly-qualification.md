# NVKE qualification after NVKR publication

The intended cycle is NVKR build → publish → run the existing pinned NVKE
`controller/test/e2e` Go suite → retain runtime and cleanup outcomes. Integration
with the special TestGrid destination is deferred.
The suite runs from a selected source already acquired by NVKR. A separate NVKE
workflow dispatch and a new qualification engine are unnecessary.

The local integration currently authenticates restored source, stages core
published artifacts, prepares the existing suite invocation, and optionally
compiles it without running tests. Runtime invocation, generated-job admission,
capacity/access checks and the complete hardware input closure are not yet
integrated. The generated build remains manual-only; schedules are not enabled.

## Source and publication handoff

Select an exact fetch node from the current complete Plan. Do not select the first
checkout with a matching commit: the maintained OS sources have distinct vendor
closures. Restore its source and source-receipt carriers from the same GitHub Run,
then use the existing source owner:

```sh
jq '.graph.nodes[] | select(.kind == "fetch") | {id, outputs}' .github/nvkr-run/plan.json
bin/nvkr factory inspect-source --help
bin/nvkr release verify --help
```

`factory inspect-source` requires the exact Plan digest and node. It rechecks the
receipt's commit/tree/vendor binding, current closure and Git checkout without
fetching or writing. Its description is a point-in-time observation; the caller
owns the private source directory's lifetime through compilation/execution.

The maintained OS source `ff21ba8c07cafff74b68e238a8876af11e5beded` already contains
the site-controller Go suite and existing input flags. The staged-image
digest/chart-override changes are proposed in [NVKE PR #1580](https://github.com/dsx-ai-factory/nvke/pull/1580),
with source head `0691b2649a2e088b7ff0e32ea6276e0a8e878fff`; they are not part of that older source pin.
Review and select a source containing those changes before activating that path;
no production locks have been silently updated.

`release verify --export-layout` requires the exact Definition, Environment and
Plan, plus `--expected-invocation`. The lifecycle owner checks authenticated
aggregate invocation metadata and exports its held closure without reopening the
original source paths. Existing destinations are refused. Independent extraction
then uses `publish extract`, `publish get`, and `publish image-export`.
Image export preserves native manifest/config/layer bytes, selects an explicit
platform for an aggregate image, and separates OCI subject from archive checksum.
This remains artifact-publication verification, not M3/M4 or runtime acceptance.

## Local post-publication adapter

```sh
python3 scripts/nvke-qualification.py --help
```

The driver supports `--plan-only` without executing commands or contacting
services, and `--stage` for authenticated read/extraction only. `--compile-only`
adds `go test -run '^$' -count=1 .` to the staged path. All modes allocate no
hardware and report runtime and cleanup as `not-run`. No TestGrid inputs are required.
Each invocation needs a fresh output directory. Failure records retain the
originating Run/publication and completed materialization phase.

The operator input JSON is a mapping into the existing suite configuration:

| Field | Required binding |
| --- | --- |
| `suite_config` | Existing NVKE config as JSON; explicit `site.deployPathRef`; preservation/reuse disabled |
| `bindings.cli` | `output` from the complete producing Run and exact bundle `install_path` |
| `bindings.images` | Exactly controller, capi-provisioned and profiles; each has an output or explicit fixture |
| `bindings.charts` | Exactly controller, postgresql, temporal, spire, spire-crds and nktp; each has an output or fixture |
| Fixture | `identity`, regular local `path` and `sha256`; image fixtures also declare `repository` and native `digest` |
| `deploy_fixture` | Explicit `commit` and `identity` when deploy assets differ from the inspected harness source |

Fixtures are reported separately from produced outputs. Their declaration and
checksum do not prove provenance or organizational acceptance. The CLI must come
from the producing Run. The complete selected output set remains mandatory even
when one runtime stream consumes a subset.

The driver bypasses `make site-inputs`: that route builds profiles and can build or
select unrelated CLI/controller inputs. It writes the existing Go command with
the site-controller label, frozen version/capability flags, keep-site disabled,
150-minute suite timeout and 220-minute Go backstop. The command is descriptive
and explicitly not admitted for live use yet. The existing suite cleanup behavior
is preserved: a tenant-sweep error can be logged while site teardown succeeds.
Joining cleanup failures and verifying a fresh owned-resource inventory remain
activation gates; a passing suite alone does not establish resource absence.

## Deferred result reporting

The special TestGrid integration is outside this PR. Its destination, publisher
contract, permissions and result/cleanup mapping must be established separately
before live qualification results can be published.

## Activation gates

Before one manual cycle, complete and verify the QCOW2/EFI OCI-to-HTTPS unchanged
byte handoff, installed material/version/worker-architecture bindings, reviewed
fixtures and suite-source selection. Restore source carriers after successful Run
finalization through generator-owned renderer/validator changes; do not append
an arbitrary job to the generated workflow.

Verify NVKR's own Forge/Teleport identity, usable capacity and shared-resource
serialization. NVKE's existing OIDC identity and ARC labels are not automatically
available to NVKR. Generic hosted utility runners remain the admitted default.
Confirm cleanup headroom, collect a fresh owned-resource inventory, and join test
and cleanup verdicts before acceptance. Resolve the exact TestGrid destination,
registered tab, publisher permissions and durable restricted-evidence destination.

Publication, live execution/allocation/cleanup and schedule activation remain
separately scoped external actions. Enable scheduling only after manual acceptance
and a second correctly correlated cycle. Runtime qualification does not promote
production releases or turn a whole artifact/matrix set green from one stream.
