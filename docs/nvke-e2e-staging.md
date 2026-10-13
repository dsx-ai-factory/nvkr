# Stage inputs for the existing NVKE end-to-end suite

This adapter prepares published CLI, image and chart inputs for NVKE's existing
`controller/test/e2e` Go suite. It can compile the suite without running it.
The external suite owns provisioning, execution and cleanup. NVKR remains passive
under ADR-16; this change does not start runtime tests or enable nightly schedules.

## Source and publication handoff

Select an exact fetch node from the complete producing Plan and restore its source
and source-receipt carriers from the same GitHub Run. Consult the selected Definition
and its referenced catalog locks for current pins. Do not select the first checkout
with a matching commit: separate source nodes can have different vendor closures.

```sh
jq '.graph.nodes[] | select(.kind == "fetch") | {id, outputs}' .github/nvkr-run/plan.json
bin/nvkr factory inspect-source --help
bin/nvkr release verify --help
```

`factory inspect-source` is an internal diagnostic command. It checks the exact Plan
node, receipt path, Git commit/tree, clean tracked worktree and current closure against
the restored unsigned receipt without fetching or writing. Index flags that hide tracked
files from worktree checks are rejected. A rewritten unsigned receipt
does not authenticate vendor or untracked source content. The description is a
point-in-time observation; the operator owns the private checkout's lifetime.
Authenticated restored source/vendor binding remains an activation gate.

[NVKE PR #1580](https://github.com/dsx-ai-factory/nvke/pull/1580) supplies digest image
inputs and optional chart overrides. Select a source containing those changes before
using that path. This adapter does not update production source locks.

`release verify --export-layout` verifies signatures, production attestations and the
complete output closure against the exact Definition, Environment and Plan. The
lifecycle owner requires its own opaque exact-input receipt and the expected originating
invocation before exporting held bytes to a fresh directory. See the
[verified export example](release-quickstart.md#verify-against-an-accepted-policy).
Metadata-only `release export` is useful for navigation but supplies no payload or
signer-policy verification; it cannot replace this step.

The Go adapter opens the exported publication with its authenticated root digest and
uses held delivery descriptors for binary/chart extraction. Image archives preserve
native manifest/config/layer bytes and select an explicit platform for aggregate images.
Archive checksums remain separate from native OCI subjects. These are artifact-publication
checks; they supply no runtime verdict or stronger release lane. For GitHub Enterprise,
pass the producer's HTTPS origin with `--github-server-url`; the default is
`https://github.com`.

## Local Go adapter

```sh
make nvke-e2e-staging
bin/nvke-e2e-staging --help
```

The implementation and regression tests are Go, under `cmd/nvke-e2e-staging` and
`internal/nvkee2estaging`. The command delegates to existing source, lifecycle and
delivery owners directly. Each invocation requires a fresh output directory.

`--plan-only` checks mappings without source, subprocess or publication access.
`--stage` verifies and extracts publication inputs. `--compile-only` additionally runs
`go test -c -o <private-cycle>/nvke-e2e.test .`; it never executes test initialization
or `TestMain`. Compilation reinspects the checkout and uses an offline vendor environment
with workspace and ambient Go configuration disabled. These checks do not confer source
authentication. All modes allocate no hardware and report tests and cleanup as `not-run`.

The operator input JSON maps artifacts into existing NVKE suite configuration:

| Field | Required binding |
| --- | --- |
| `suite_config` | Existing NVKE JSON config with canonical guarded keys, explicit `site.deployPathRef`, preservation/reuse disabled and no duplicate JSON fields |
| `bindings.cli` | Producing output and exact bundle `install_path`; selects one verified executable entry using its flat extraction name |
| `bindings.images` | Controller, capi-provisioned and profiles, each with an output or explicit fixture |
| `bindings.charts` | Controller, postgresql, temporal, spire, spire-crds and nktp, each with an output or fixture |
| Fixture | `identity`, regular local `path` and `sha256`; image fixtures also declare `repository` and native `digest` |
| `deploy_fixture` | Explicit commit and identity when deploy assets differ from the checked harness source |

Fixture bytes are copied while hashing the same opened regular file; symlink inputs
are rejected. The configuration uses these private copies. Fixture declarations and
checksums establish local consistency, without provenance or organizational acceptance.
Image fixture native digests are declarations checked by NVKE after import; this adapter
does not authenticate their origin. The CLI must come from the verified publication.
The complete producing output set remains mandatory even if one suite consumes a subset.

Reports distinguish unauthenticated Run records, verified publication artifacts and
fixtures. Source trust remains diagnostic. Validation and materialization have separate
outcomes, and failures retain the originating Run/publication and completed phase.
Stable owning error codes are preserved.

The adapter bypasses `make site-inputs`, which can build profiles and select other
CLI/controller inputs. It describes the existing suite command with the site-controller
label, operator-supplied expected version flags, keep-site disabled, a 150-minute suite
timeout and a 220-minute Go timeout. Versions and the initial supported NVKE stream
combinations are diagnostic input declarations; installed-version verification and an
accepted coverage profile remain separate gates. The live command is not admitted.

## Existing work and activation gates

[NVKE PR #1578](https://github.com/dsx-ai-factory/nvke/pull/1578) and the NVKR drafts
[PR #138](https://github.com/dsx-ai-factory/nvkr/pull/138) and
[PR #139](https://github.com/dsx-ai-factory/nvkr/pull/139) concern CLI behavioral tests
and their artifact handoff. This adapter prepares controller-suite deployment inputs.
Share the source and delivery owner interfaces before combining the paths; retain each
suite's existing assertions. Select source revisions containing the required NVKE
interfaces before integration. These links do not ratify additional qualification scope.

Before external execution, establish authenticated restored source/vendor binding and
checkout lifetime isolation, the complete QCOW2/EFI OCI-to-HTTPS byte handoff, installed
material/version/architecture bindings, reviewed fixtures and the accepted suite source.
Generated source-carrier restoration requires renderer and validator changes; arbitrary
jobs must not be appended to generated workflows.

External owners must verify Forge/Teleport identity, usable capacity, shared-resource
serialization and cleanup headroom. NVKE OIDC identity and ARC labels are not inherited
by NVKR. Generic hosted utility runners remain the admitted default. Join test and
cleanup verdicts and inspect a fresh owned-resource inventory: existing tenant cleanup
can log an error while site teardown succeeds, so a passing suite alone is insufficient.

The requested special TestGrid destination is deployment-specific reporting, with no
destination or publisher contract established here. It needs no inputs for staging;
its integration is deferred. Publication, external runtime execution and cleanup, result
reporting and schedule activation require separate operational evidence. Enable schedules
only after manual acceptance and a second correctly correlated cycle. One runtime stream
does not qualify an entire artifact or target matrix.
