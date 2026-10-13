# NVKR dashboard implementation preparation

## Task and verified baseline

Prepare implementation of the supplied `02-nvkr-dashboard.md`: a live local Run
dashboard, followed by read-only Plan/Run inspection, retaining script output,
coded failures, capability admission, required outputs and execution ownership.
This document is preparation, not an implemented dashboard or runtime acceptance.

The supplied plan was read from the original checkout as
`docs/session-plans-2026-10-12/02-nvkr-dashboard.md`.
Its companion `01-native-mac-demo.md` was read only to identify the shared presenter
boundary. Both are untracked in the original checkout; they were not changed.
That checkout has other local documentation changes and an older code revision.

`origin/main` was fetched on 2026-10-12. Analysis uses
`540c5d3d8b8198ddd85450e413097fab0a69661b` in a clean, detached worktree. The supplied research baseline
`8f86087` is an ancestor of this revision. The configured remote remains
`git@github.com:dsx-ai-factory/nke-recipes.git`; the Go module is
`github.com/dsx-ai-factory/nvkr`.

Current AGENTS.md, ADR-03/17/18, reconciliation, DEVELOPMENT.md and docs/README.md
were inspected. No AGENTS.local.md was present at either checkout's exact root.
Three read-only subagents reviewed events, terminal reuse and CLI/validation.
No implementation, commit, push, tracker update or real deployment was performed.

The follow-up [UX/UI specification](02-nvkr-dashboard-ux-spec.md) adds the proposed
screen hierarchy, wireframes, keyboard/focus contract, responsive layouts,
state wording and task-based usability acceptance. Freeze it in phase 0; the
engineering checks below alone do not establish user-facing usability.

## Readiness findings

The live dashboard can begin without waiting for native Mac VM/network feasibility.
Reuse terminal machinery while preserving platform admission in factory. The
dashboard must still display a coded Linux-only VM rejection on macOS; it cannot
make that execution route supported.

| Finding | Current evidence | Implementation implication |
|---|---|---|
| Local execution has no generic typed progress feed | [lifecycle options](../../pkg/nvkr/lifecycle/execute.go#L22), [factory scheduler](../../pkg/nvkr/factory/local.go#L351) | Add descriptive updates at the owning boundaries; do not parse `[start]`/`[done]` into state |
| Existing activity is narrow OS log metadata | [record vocabulary and bounds](../../internal/buildactivity/activity.go#L19), [nonblocking offer](../../internal/buildactivity/activity.go#L130) | Reuse for counts/age only; it proves neither health nor completion |
| Demo terminal ownership is reusable | [TTY admission](../../cmd/nvke-demo/dashboard.go#L25), [checked TTY](../../cmd/nvke-demo/dashboard.go#L164), [loop and joins](../../cmd/nvke-demo/dashboard.go#L233) | Extract the generic session machinery under one owner; retain demo provisioning and recovery in its adapter |
| Demo build presentation reads human lines | [build log projection](../../cmd/nvke-demo/dashboard_view.go#L174) | Reuse layout concepts, not that input model |
| Escaping does not establish privacy | [terminal escaping](../../cmd/nvke-demo/progress.go#L93), [prefix-filtered log emission](../../cmd/nvke-demo/progress.go#L257), [arbitrary dashboard writes](../../cmd/nvke-demo/dashboard_view.go#L145) | Safe panes receive closed typed projections, never arbitrary log suffixes |
| Plan inspection already has a deep owner | [owned snapshot and loader](../../pkg/nvkr/planview/inspect.go#L25), [CLI formats](../../cmd/nvkr/plan_views.go#L23) | Build the explorer from planview; preserve exact canonical JSON bytes |
| Stored Run inspection is a missing operation | [registered Run commands](../../cmd/nvkr/lifecycle.go#L63), [private validation](../../pkg/nvkr/lifecycle/run.go#L146), [attempt writer](../../pkg/nvkr/lifecycle/attempt_store.go#L17) | Add a lifecycle-owned bounded inspection API and facade in a later phase; no command-owned parser |
| Local Run JSON is not an existing CLI contract | [local command and summary](../../cmd/nvkr/lifecycle.go#L93) | Define an opt-in structured final result; protect stdout from diagnostics |
| Final local summary does not check writes | [summary fmt.Printf calls](../../cmd/nvkr/lifecycle.go#L132) | Introduce checked reporting, including short writes and late failures |
| A complete Run may coexist with an error | [committed disk cleanup failure](../../pkg/nvkr/lifecycle/execute.go#L271), [CLI retention behavior](../../cmd/nvkr/lifecycle.go#L117) | Show retained outputs and nonzero command outcome separately |
| Documentation has stale schema examples | [lifecycle examples](../lifecycle.md#L169), [compatibility text](../lifecycle.md#L289), [current schema constants](../../pkg/nvkr/lifecycle/model.go#L25) | Correct touched lifecycle sections from current models during implementation |

ADR-17/18 are proposed engineering contracts, pending named-decider ratification.
Reconciliation records implementation and scoped evidence separately. Presentation
work must not imply ratification, qualified execution or fresh runtime acceptance.

## Output-field evidence map

Every displayed field has one owning source. Proposed updates below are descriptive;
no new receipt, public lifecycle object, signed schema or identity family is needed.

| Requested display | Source and join | Rule |
|---|---|---|
| Named output and dependencies | Validated factory Plan, LifecycleOutputs, artifact graph and typed image-index members; planview snapshot | Bind exact output and target; BuildTarget alone is not universally an output name; equal Build Digests do not merge required outputs |
| Producer and stage | Factory node ID/kind and owner-emitted execution update | Display closed stages, not arbitrary console strings |
| Queued/running/observed/failed | Scheduler admission/start/return; output observer and lifecycle failure boundary | Producer return and observed artifact are different stages; canceled siblings are not the causal failure |
| Required and observed counts | Frozen PreparedRun description; latest output snapshot; returned Run.Expected/Observed | Never count dequeued events or shrink the denominator after failure |
| Elapsed and freshness | Injected monotonic clock; source sample plus receiver age | Fresh logs do not mean healthy execution; stale/absent samples are unknown |
| Lane and final state | Returned lifecycle Run plus operation error | Preserve dry-run pending state and actual lane; complete is not a verified Release |
| Coded failure | Original operation error and existing error-to-exit mapper | Safe pane shows code and fixed public summary; raw cause.Error() stays out |
| Diagnostic locator and activity | Owner-issued opaque locator, bounded typed public summaries, existing buildactivity metadata | No credentials, provider messages, private paths, source log bytes or terminal controls |

## Proposed implementation contracts

### Progress owner and transport

Factory owns descriptive operation progress; lifecycle owns named-output observation
and finalization. Add a concrete package-owned monitor
with a latest-state table initialized from the admitted Plan, defensive snapshots
and best-effort wakeups. Lifecycle adds its observation/finalization stages to the
same description. A presenter pulls snapshots; producers never call an arbitrary
renderer, callback or io.Writer for progress.

Proposed description fields are attempt/Plan binding, exact output, exact operation,
stage enum, state enum, sequence, sampled time, optional coded failure and opaque
diagnostic ID. Reject unknown bindings as unavailable telemetry. Keep this state
outside canonical Plan/Run serialization and identity. Preserve optional nil/no-op
behavior for SDK callers and the existing Activity FD route.

Keep operation states (`queued`, `running`, `completed`, `cached`, `skipped`,
`failed`, `canceled`) distinct from output states (`required`, `observing`,
`observed`, `failed`). Emit observed only after that output's owning observation
check succeeds. Derive index membership from typed graph/index bindings, not ID
string parsing or a producer's BuildTarget label.

Size node/output tables against the admitted graph, and separately bound the
activity ring and rendered page. The demo's 512-step display cap is not a new Plan
admission limit. Coalesce intermediate updates and expose truncation/loss; derive
counts from latest snapshots. Do not depend on receiving a terminal event: the
returned lifecycle result and error determine the final frame and command status.
Keep operation-completion counters separate from observed named-output counters.
Use a reviewed bound for lock-held work and format outside locks. No new protocol,
codec, parser or terminal framework is proposed.
The 4 MiB Plan file-load limit is not a count bound on compiled in-memory graphs.
A mutex-backed monitor is not lock-free; require bounded contention and no wait
for rendering, and use standard-library atomic slots if stronger progress isolation
is needed.

Factory emits after admission and at actual scheduling boundaries. Lifecycle
emits output observations after its exact-byte/binding checks, and finalizes the
display from its returned outcome after durable persistence/cleanup. Preserve
the scheduler's existing causal-error selection and owned subprocess joins.

### Shared presenter

One worker owns all new `internal/runpresenter/` files and the extraction seam in
`cmd/nvke-demo/dashboard.go`. Proposed interface: a session takes a context, a
read-only snapshot source, a presentation view, an explicit cancellation policy
and a work closure; it returns only after screen/event pump/work have joined.
The session owns terminal admission, checked TTY I/O, restoration and renderer
failure handling. Widget updates occur only on its render goroutine.
Return work and presentation failures as distinct fields. The CLI preserves the
existing work failure and exit status when adding a presentation failure; a
presentation-only failure after successful work is a checked reporting failure.
Do not copy the demo loop's error-join order without testing that precedence.

Views contain safe projected values. Demo's tenant/site phase maps, sudo handling,
resource cleanup, recovery guidance and force-exit policy remain demo-owned. The
new NVKR view contains outputs, dependencies, stage counts, age, coded failure and
bounded public activity. Do not move the complete demo dashboard data model into
a generic package or import nvkedemo from the NVKR presenter.

Reuse pinned tcell v2.13.10 and tview v0.42.0 from go.mod. Existing notices include
their licenses; no additional dependency is needed. Dependency security remains
subject to the repository vulnerability gate. Never display raw logs after merely
escaping them. Reuse standard-library quoting and tview escaping at the final
terminal boundary. Closed safe summaries and private diagnostic retention have
different contracts; a safe locator should be an opaque reference rather than a
copy of a private filesystem path.

### CLI and inspection

Recommended new presentation option: `--ui=auto|plain|tui`, independent of `--json`.
These names are proposed, not existing flags. `run local` defaults to auto:
only real interactive stdin/stdout, usable TERM and no JSON select the dashboard.
Redirected/injected output stays plain; never open /dev/tty behind redirection.
Explicit tui without eligibility is a coded usage failure before execution.
Explicit tui plus JSON is a coded usage conflict. Auto plus JSON selects structured
output. Initial terminal setup failure starts no work; runtime terminal loss
cancels, restores and joins before returning a checked failure.

Preserve existing plain summaries and Plan `--json` exact bytes (including its
absence of a trailing newline). Local `--json` is a new deterministic final-result
contract: one document on stdout with safe fields for outcome, code, lane and
output membership; diagnostics go to stderr/private retention. Final structured
bytes do not restore a Run capability. Define the exact result schema and failed
admission behavior before wiring it. All output modes consume the same owning
operation result; checked output failures may fail reporting but cannot revoke
already retained outputs or replace an existing causal code.

Keep existing `run plan` default output unchanged; add an opt-in explorer after
the live Run slice. Add proposed `run inspect --record ...` only after lifecycle
provides a context-first, bounded, no-follow loader using the existing canonical
codec and filesystem primitives. Inspection validates its descriptive schema and
bindings, labels file-recorded state, and treats interrupted/incomplete attempts
explicitly. It grants no resume, freshness, payload/signature verification or
health claim. Do not derive live elapsed time from file mtime or poll arbitrary
directories as though they were an authenticated Run.
Inspect the attempt marker and record together through lifecycle's existing
attempt-store semantics. A complete record plus marker can mean retained outputs
with incomplete cleanup; marker-only and interrupted-before-record are distinct
descriptions. Reusing private validateRun alone is not a payload verification check.

NVKR's existing signal-driven cancellation stays mapped through the current
CodeTimeout path (31). Do not copy the demo's force-exit status 130 or resource
confirmation into the generic Run command. The demo adapter retains its current
confirmation behavior where it owns live resources.

## Implementation phases and subagent ownership

| Phase | Worker and exclusive files | Deliverable and dependency |
|---|---|---|
| 0 | Coordinator | Freeze progress types, session API, safe field map, JSON result shape and error precedence; confirm no overlap with plan01 shared files |
| 1A | Events worker: new factory progress files, factory/local.go, lifecycle/execute.go and observation hooks, related tests, client/v1 option forwarding | Bounded typed snapshots; begin after phase 0; preserve all authority checks |
| 1B | Presenter worker: internal/runpresenter, cmd/nvke-demo/dashboard.go extraction seam, generic presenter tests and necessary demo regression fixtures | Checked terminal session and pure view seam; can run alongside 1A after API freeze |
| 2 | CLI worker: cmd/nvkr/lifecycle.go, new local presentation/result files and tests | Live Run dashboard, plain/JSON dispatch, checked summaries; integrates 1A/1B |
| 3 | Lifecycle/events worker: new lifecycle Run inspection owner + facade; CLI worker: plan explorer and Run inspection command | Read-only Plan/Run dashboard and fixtures; retain this requested scope after live Run completion |
| 4 | Coordinator/docs owner: docs/lifecycle.md, docs/plan-and-graph-inspection.md, docs/README.md, relevant ADR/reconciliation sections | Document only implemented behavior and exact evidence scope; read existing owners first |
| 5 | Independent reviewer then validator | Reconcile findings against the task; run proportional checks, full gate, targeted race and real TTY acceptance |

With four agent slots, run the coordinator plus events/presenter/CLI workers only
after file/interface ownership is agreed. The CLI worker can prepare result tests
while foundation workers implement. Start reviewer/validator after a slot is free.
No workers concurrently edit shared presenter files. Native Mac demo coordination
is an interface handoff, not authorization to implement its VM/network work here.

## Acceptance and validation

| Contract | Required proof |
|---|---|
| Mode dispatch | Table tests for TTY/redirected/injected writers, TERM absent/dumb, explicit mode errors, JSON conflicts; no work on rejected admission |
| Existing scripts | Golden byte checks for current Plan/text/JSON/pretty/graph/diff; existing local plain output; no terminal control bytes in redirected output |
| Bounded concurrency | Flood/coalescing, concurrent reports/draws, no-consumer/closed-consumer, large admitted graph, immutable snapshots, no producer wait on renderer |
| Required output identity | Missing/conflicting/extra output, equal-digest siblings and observation failure still reject; counts and original denominator stay correct |
| Finalization and errors | All nodes done but observation/persistence fails; complete Run with cleanup error; causal sibling error; output short-write and late teardown failure |
| Privacy and text | ESC/OSC/control/tview tags, malformed UTF-8, long text, private paths, provider/credential sentinels under allowed-looking prefixes; pane receives no raw suffix |
| Layout | Resize, 80x24, compact/very small screen, graph paging, overflow/truncation and focus |
| Cancellation and teardown | Interrupt/terminal loss/closed event stream/renderer panic, restored terminal before cleanup wait on failure, joined event pump and work, retained causal code |
| Inspection | Bounded no-follow canonical load; malformed/duplicate/unknown fields; record-plus-marker, marker-only and interrupted-before-record; complete record with incomplete cleanup; immutable views; metadata clearly separate from verification |
| Runtime evidence | Real TTY tests on Mac/Linux plus separately authorized real Run observation; simulations and mock execution do not prove runtime success |

After code changes, run `make check`, uncached `go test ./... -count=1`, and
`git diff --check`. Use targeted `go test -race` for affected event/presenter,
factory/lifecycle and demo packages. Do not enable whole-suite extended/race/coverage
by default. Use native Linux for affected execution contracts; cross-compilation
does not prove cancellation or race behavior. Changing published guide sections
also requires `make docs-check` as described in DEVELOPMENT.md.

Existing real-terminal fixture:
`NVKR_TEST_TERMINAL_AUTH=1 go test ./cmd/nvke-demo -run '^TestDashboardAuthenticationTerminalContract$' -count=1`
in an isolated interactive TTY. This existing fixture tests demo terminal behavior;
add NVKR-specific finalization/terminal-loss cases rather than treating it as full
dashboard acceptance. It does not deploy resources or perform privileged work.

## Checks performed during preparation

Installed toolchain: Go 1.27.1 darwin/arm64. Repository pin: Go 1.27.2.
`make go-version-check` fails with
`Go go1.27.1 does not match pinned go1.27.2`. No compiler was installed. The full
repository gate is therefore not verified; implementation completion requires
the pinned compiler and successful required checks.

The following existing contracts passed with the installed compiler and a writable
scratch cache. These are baseline tests, not tests of the proposed implementation:

```sh
GOCACHE="${TMPDIR:-/tmp}/nvkr-dashboard-go-cache" GOTOOLCHAIN=local go test ./cmd/nvkr \
  -run '^(TestRunPlanFileViewsRetainCanonicalBytes|TestRunGraphFormats|TestRunDiffCountsAllChangesAndSupportsExitStatus|TestPlanViewCommandsRejectBeforeOutput|TestLocalBuildActivityFlagAdmitsBeforeAuthoredInput|TestLocalBuildActivityIsSeparateAndOptional|TestBuildActivityFlagIsNotPublicHelp|TestRunLocalPrintsDurableDiskOutput|TestRunLocalPrintsRetainedDiskOutputWhenCommitCleanupFails)$' \
  -count=1

GOCACHE="${TMPDIR:-/tmp}/nvkr-dashboard-analysis-go-cache" GOTOOLCHAIN=local \
  go test ./cmd/nvke-demo ./internal/nvkedemo \
  -run 'Test(Dashboard|CheckedTTY|Progress|RetainedProgress)' -count=1

GOCACHE="${TMPDIR:-/tmp}/nvkr-dashboard-go-cache" GOTOOLCHAIN=local \
  go test ./client/v1 ./pkg/nvkr/lifecycle ./pkg/nvkr/errors ./pkg/nvkr/factory \
  -run '^(TestSDKLifecyclePreparationIsOpaqueAndDryRunHasNoOutput|TestSDKRejectsUnsupportedGenerationBeforeInputsAndUnconfiguredConsumer|TestPreparedFilesDryRunPreservesFrozenRequirements|TestPrepareFilesRejectsInputsWithoutIssuingCapability|TestExitCodeContract|TestExitCode|TestRetryable|TestGitHubStepExitCodeKeepsPublicMapping)$' \
  -count=1
```

The first command passed nine selected CLI contracts. The second passed both
package selections; opt-in real-terminal fixtures were not enabled. Native
`go run ./cmd/nvkr run --help` also succeeded and confirmed the current command
surface. No real TTY, Linux race run, fresh build or external operational evidence
was collected in this preparation pass.
The third command passed its selected SDK, lifecycle, error-map and exit-mapping
contracts across all four packages. The version-gate failure remains unresolved.

## Remaining decisions and boundaries

The API/JSON details above are implementation recommendations and must be frozen
in phase 0 before concurrent edits. This pass did not establish shared-file
ownership with another plan01 implementation session; confirm that handoff before
shared edits. The presenter worker proposed here owns those files for this task.
Toolchain patch alignment is a verified validation blocker, not a dashboard design
blocker. Real Run/deployment, external publication, commits and pushes require
their explicit authorization; local implementation and review have no dependency
on those actions.

The supplied plan estimates 8–12 engineer-days for the live dashboard, overlapping
with presenter reuse. This preparation did not measure a replacement estimate.
Treat that as a planning estimate, not a delivery date or validated runtime claim.
