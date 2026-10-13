# NVKR dashboard UX and UI specification

## Scope and confidence

This is the proposed usability contract for the dashboard in
[implementation preparation](02-nvkr-dashboard-implementation-preparation.md).
It covers live local Runs and read-only Plan/Run inspection. Layouts, key bindings,
wording and acceptance targets below are proposals, not implemented or user-tested
behavior. Repository observations use revision `540c5d3d`. Two subagents reviewed
interaction/layout and state/error semantics. No real Run or user study was performed.

## User tasks and information priority

The primary user is an engineer building named outputs or diagnosing a Run.
The screen should answer, in order: what is happening; which output needs attention;
what it depends on; what was actually observed; and how to leave or cancel safely.

Use named outputs as the main list, with their selected operations and dependencies
in details. Operation IDs, complete digests, transport metadata and graph internals
belong in progressively disclosed details. A full dependency graph is optional
navigation, not the default screen. The main screen must work without knowing the
factory graph vocabulary.

Priority under space pressure: run identity/mode, current phase or causal failure,
observed/required count, selected output, focused-pane marker, applicable keys.
Activity and secondary metadata yield space first. Counts are output membership,
not a percentage of time or estimated completion. Show no ETA unless an owning
operation supplies an established estimate; the current proposal supplies none.

## Verified usability gaps in the reusable demo

| Existing behavior | Usability risk | Proposed correction |
|---|---|---|
| [Tab changes focus but arrows select build operations](../../cmd/nvke-demo/dashboard_view.go#L899) | The highlighted pane and acted-on pane disagree | Route navigation through the actual focused pane; display its name |
| [Build list sorts running rows first](../../cmd/nvke-demo/dashboard_build.go#L143) | Rows move as execution changes, making inspection harder | Stable output-name ordering; explicit filters, never automatic reordering |
| [Small build screens remain split](../../cmd/nvke-demo/dashboard_build.go#L260) | Names/details compete for very little width | Full-width list or detail at compact sizes |
| [Operation row state is color and a glyph](../../cmd/nvke-demo/dashboard_build.go#L174) | State is unclear without the color/glyph convention | A state word on every row; ASCII selection marker |
| [Focus is a border-color change](../../cmd/nvke-demo/dashboard_build.go#L318) | Focus is weak in monochrome | Textual focus indicator plus inverse selection |
| [Details show only two dependency links](../../cmd/nvke-demo/dashboard_build.go#L237) | Important dependencies require leaving the view | Scrollable dependency list with exact visible/total count |
| [Private log path appears in footer](../../cmd/nvke-demo/dashboard_view.go#L874) | Wholesale reuse conflicts with the new safe-pane contract | Opaque diagnostic reference; no private path in a safe pane |
| [Run failure clears observed membership](../../pkg/nvkr/lifecycle/run.go#L190) | Keeping a live observation count can imply retained outputs after failure | Reconcile final counts to returned Run; label earlier checks as history |

The existing layout and simulation tests establish controlled presentation
behavior, not successful task completion by real users. Preserve the reusable
terminal machinery while replacing these interaction contracts for NVKR.

## Proposed screen structure

All samples are illustrative. Output names and platform come from the retained
[nvke-v1 Definition](../../definitions/nvke-v1/definition.yaml); times and states
are synthetic. No sample describes an actual observed Run.

### Standard view, 80 columns by 24 rows

```text
NVKR  Live Run: run-001                                Elapsed 04:12
Building outputs                           Outputs observed: 0 / 3
Local execution; release verification not performed

[ Outputs ] | Details | Activity
--------------------------------------------------------------------
  Output          State                Current stage
> cwe             Building             Build image
  nvke-helm       Waiting              Dependency: cwe
  nvkectl         Check pending        Producer finished

Selected: cwe / linux/amd64
Last operation update: 2s ago
Activity sample: unavailable

Showing 3 of 3 outputs. Filter: All
--------------------------------------------------------------------
Up/Down select  Enter details  Tab pane  / search  f filter  ? help
q confirm cancellation  Ctrl+C cancel Run
```

The default is an output list with a short selected-output summary. Enter opens
a full-width detail view; Esc returns to the list without canceling. Details show
selected output, platform, producer operation list, incoming/outgoing dependencies,
observation state and safe diagnostics reference. Every dependency is reachable
through paging. Display exact full identity in details; clipped list labels must
not cause ambiguity or change the selection key.

### Wide view, at least 120 columns and 30 rows

Use a stable output list on the left and selected-output details on the right.
An activity strip below is secondary. Keep one run-status header and one footer;
avoid repeated counters or a grid of identical status cards. A detail section can
scroll without changing the selected output. Selecting a dependency navigates to
its owning output/operation with an explicit Back action.

### Compact and tiny terminals

| Available size | Proposed layout |
|---|---|
| At least 80x24, below wide threshold | Full-width list, short selection summary; separate detail/activity views |
| At least 40x12, below standard threshold | One full-width pane at a time; mode/status/count above it; focused pane and essential keys below it |
| Below 40x12 | Minimal status, count and cancel/close hint; request a larger terminal for browsing; execution continues and cancellation remains available |
| Zero usable dimensions | Retain state; skip drawing; continue input/owner handling and redraw on resize |

Dimensions are proposed boundaries to validate, not admission rules. Resizing must
preserve selection, filter, scroll anchor and focus. A clipped view says what is
hidden; no executable output or dependency disappears from the admitted model.
Restore the fuller layout automatically when space returns. Do not automatically
cancel a Run or switch streams merely because the terminal shrinks.

### Failure and finalization views

Promote a safe causal failure to the persistent header without moving keyboard
focus. Keep output membership visible. Separate affected output, causal operation,
canceled sibling work and reporting/cleanup failure.

```text
NVKR  Run: run-001
Run complete; cleanup incomplete
Outputs retained: 3 / 3       Command exit: 47
Code: NVKR-E-IMMUTABLE-VIOLATION

Retained outputs remain available.
Diagnostics reference: <owner-issued reference>
```

Live execution restores the terminal and prints a checked final summary, then
returns to the shell without waiting for a key. Do not lose the result when the
alternate screen closes. A later read-only viewer stays open until closed.
Keep the complete-plus-error case visible in both TUI scrollback and structured
output; do not turn the command green solely because outputs were retained.

## Keyboard and interaction contract

| Key/action | Proposed behavior |
|---|---|
| Tab / Shift+Tab; 1/2/3 | Next/previous pane or direct Outputs/Details/Activity selection; visibly announce focus by name |
| Up/Down | Select row or scroll in the focused pane; never operate on another pane |
| PgUp/PgDn | Page the focused list/details/activity |
| Enter | Inspect selected output, operation or dependency; never launch/retry work |
| Esc | Close the current layer or go Back; at the unlayered output list clear its active search/filter; never cancel execution |
| / | Bounded output-name substring search; normal text entry, not a regex or shell expression |
| f | Choose All, Active or Failed filter; display filter and hidden-count explicitly |
| ? | Context-sensitive help with current mode, controls and visible actions |
| End in Activity | Return to following newest public activity |
| q in live list/details/help | Open cancellation confirmation; y requests cancellation, n/Esc/Enter stays; repeated q never confirms; text-entry layers consume printable q as text |
| Ctrl+C during live Run | Request cancellation immediately, including from search/help/details; same owning operation semantics as plain mode |
| q in read-only viewer | Confirm closure with y; n/Esc/Enter stays; no record change, resume, execution or resource cleanup |

Search/filter entry owns its printable input: typing a q into a query is text,
not a cancellation. Esc dismisses it; Enter applies it. Help and details retain
the mode-specific q policy visibly. Confirmation defaults to staying, isolates
navigation keys, and continues to show fresh state. Owner completion and external
cancellation proceed without waiting for this prompt. During cancellation,
repeated q/Ctrl+C cannot add a force-exit path; show that owned work is still
stopping. Do not import the
demo's deployment confirmation/force-exit policy into NVKR.

Keep selection keyed by exact named output or typed operation reference, never
row index. Use fixed ordering while telemetry updates. A changed filter that hides
the selected row explicitly moves selection to a visible row and updates details.
Show `No outputs match this filter; clear it with Esc` when empty; this does not
mean the Run requires zero outputs. If there is no current selection, Enter is inert.
If telemetry changes make the selected output fall outside an Active/Failed filter,
keep its detail view pinned and label it `Selected output is outside current filter`.
Do not move focus or select a different identity automatically. The next explicit
list-navigation action selects a visible row; clearing the filter restores the
selected identity to the list. This is distinct from a user changing the filter.

Activity follows new entries only while at its end. Scrolling back pauses following
without pausing work, and displays `Following paused; End resumes`. Eviction shows
an explicit history-truncated notice and preserves a stable anchor where possible.
Navigation never waits for a network call or starts a build.

## State vocabulary and honest progress

Persisted Run status remains pending/failed/complete. Presentation phases below
do not introduce new wire statuses or stronger evidence claims.

| Owning event/result | User-facing wording | Constraint |
|---|---|---|
| Required output not started | Waiting; dependency or capacity reason only if known | Do not infer a blocker from elapsed time |
| Producer operation starts | Building; named safe stage | Count active operations independently |
| Producer returns | Producer finished; output check pending | Never label the output verified or retained |
| Lifecycle output observation succeeds | Output observed | Increment exact named-output observation count |
| All outputs observed, owner not returned | Finalizing Run; retaining result | N/N is not final success |
| Owner returns complete, nil error | Run complete; outputs retained | Show actual lane, no runtime-health or signed-release claim |
| Owner returns complete plus cleanup error | Run complete; cleanup incomplete; command failed | Preserve retained outputs and nonzero exit |
| Owner fails | Run failed; code and fixed safe explanation | Reconcile final membership; previous live observations are history |
| User cancellation requested | Canceling Run; waiting for owned work to stop | Do not claim cleanup or cancellation completed before join |
| Owner returns cancellation | Run canceled; timeout code | Show recorded outcome failed only if an owning failed record exists; retain current cancellation exit mapping |
| Dry-run | Dry-run; no execution; required N, observed 0 | No animated execution state or attempt-retention claim |
| Plan/file inspection | Plan inspection / Recorded Run; read-only | No live ages from file mtime, health or payload verification |
| Metadata samples disappear or age | Activity unavailable / Last sample Ns ago | Do not infer hung, stalled, failed or healthy |

Lifecycle observes named outputs after factory returns in the current owner flow
([execute.go](../../pkg/nvkr/lifecycle/execute.go#L168)); producer completion and
factory terminal-observation operations are not that final membership check.
`bindObserved` sets an intermediate complete description before persistence
([run.go](../../pkg/nvkr/lifecycle/run.go#L136)). Only the operation's returned
result/error selects the final success frame. On failure, label earlier successful
checks `Observed during execution; not retained in final Run record` rather than
silently presenting them as retained output membership.
When cancellation or admission fails before an attempt exists, or record persistence
fails, say `Run record unavailable` and preserve the command's failure code. Do not
invent a failed record, zero observed membership or confirmed cleanup for a nil
result. Distinguish a returned complete Run with a reporting failure from a
returned failed Run.

For a stored complete record, use `Recorded Run complete; record lists N observed
outputs; current payload availability not checked`. Never reuse the live final
panel's `Retained outputs remain available` as a statement about a loaded record.
Record-plus-marker inspection additionally labels incomplete cleanup; it still
does not check current artifact payloads.

Safe errors contain the code, affected output/stage, fixed explanation and only
an owner-supported next action. Unknown causes point to an opaque private-diagnostic
reference. No generic Retry/Resume/Cleanup button and no invented recovery command.
Cancellation/timeout does not imply retry is safe or that capacity is available.

## Visual and accessibility contract

Use the user's terminal font and background. Prefer terminal-default foreground
and a restrained status palette; do not force a dark theme. For true-color mode,
proposed secondary accents are cyan `#22D3EE` for focus, amber `#FBBF24` for pending
attention, red `#F87171` for failure and green `#86EFAC` for final successful outcome.
Validate these against light/dark terminal themes and provide monochrome operation.
Text words and `>` selection remain meaningful with all colors disabled. Avoid
emoji, icon-only state, decorative spinners and large progress bars.

Propose respecting nonempty NO_COLOR and adding `--no-color`; neither is an existing
NVKR guarantee from this analysis. They remove color styling, not TTY controls
required for a full-screen monochrome view. Color must be secondary to text;
this follows [W3C's use-of-color principle](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html),
without claiming WCAG compliance for the TUI.

Use terminal-cell width, not byte/rune count, for clipping and column layout.
Reuse the pinned terminal stack's width/escaping behavior before adding helpers.
Test wide/combining characters, malformed UTF-8 and directional controls; unsafe
controls must be rendered visibly or rejected by the safe projection. Short names
and age counters cannot overwrite adjacent fields. Suppress decorative redraws;
update important state promptly, but refresh visible age counters at most once a
second. No inference of source freshness from a ticking UI clock.

Provide `--ui=plain` with linear, safe, meaningful state transitions and no cursor
rewriting as the accessible baseline to test. Do not claim alternate-screen
screen-reader support without checking actual reader/terminal combinations.
Test VoiceOver on the chosen Mac terminal and an available Linux reader; record
actual outcomes and limitations. Monochrome and keyboard-only use are necessary
but do not alone prove accessibility.

[Command Line Interface Guidelines](https://clig.dev/) informs plain output,
discoverable help, explicit exit controls and color preferences. Its authors
explicitly exclude full-screen TUIs; this screen/key specification is a project
design proposal. Repository stop/join semantics govern cancellation even where a
general CLI guide recommends a second interrupt to bypass cleanup.

## Mode decisions to freeze before implementation

`--ui=auto|plain|tui` selects presentation; JSON selects structured output. Preserve
existing Plan output defaults. Auto plus JSON stays noninteractive; explicit tui
plus JSON fails before work. Explicit tui without usable terminal streams fails
before work. This does not silently open /dev/tty.

Recommended resolution for dry-run: auto stays plain; explicit tui opens a static
read-only Plan/requirements viewer, clearly labeled no execution. Closing it
returns the dry-run's original result and creates no attempt. The static viewer
and its loader should be implemented with the read-only exploration phase;
until then, reject explicit tui plus dry-run with a documented coded usage error.
Do not accidentally start real execution to provide an animated preview.

Finalize safe JSON fields and presentation-error precedence alongside these mode
rules. The architecture/session extraction cannot be considered UX-ready before
layout, focus, exit policy and terminal-result wording are agreed and fixture-tested.

## Task-based usability acceptance

These are proposed acceptance targets, not measured results. Use synthetic
fixtures with exact owner states, plus real isolated TTY tests. Test at 120x40,
80x24, 60x16, 40x12 and 20x6, including resize during navigation and cancellation.

| User task | Proposed pass criterion |
|---|---|
| Understand a live Run | Identify phase, actual required/observed count and elapsed time within 5 seconds at 80x24 |
| Find a blocker | Identify affected output and known dependency/cause within 10 seconds, without reading raw logs |
| Inspect dependencies | Reach any selected-output dependency with keyboard navigation; no two-link truncation dead end |
| Read activity history | Read an older entry while new events arrive, without losing selection or being returned to the end |
| Distinguish evidence | Correctly explain producer finished vs observed vs retained vs release verification in each state fixture |
| Recognize incomplete cleanup | State both outputs retained and command failed in the complete-plus-error fixture |
| Cancel foreground work | Discover the cancel key within 5 seconds; see Canceling promptly; return only after owner joins |
| Close an inspection | Close with q; record and marker bytes remain unchanged and no execution/cleanup starts |
| Use monochrome/keyboard | Complete every core task with no color or mouse; focus and state remain readable |
| Shrink the terminal | Status and stop/close controls stay available; restoring size restores browsing state |
| Handle filtering | Understand visible/total counts, clear an empty filter and retain the original required-output denominator |
| Recover after terminal failure | Terminal restored, worker joined, original outcome/code preserved and safe checked summary attempted |

Run a formative session with 3-5 representative engineers before freezing the
interaction contract. Ask them to perform these tasks without coaching, record
time/errors and revise observed problems. Do not infer usability from test
snapshots or count worker agreement as user validation.

## Implementation handoff

Phase 0 now includes this UX contract: approve one hierarchy/keymap, explicit mode
matrix, safe copy and representative state fixtures before parallel code edits.
The presenter worker owns layout/focus/navigation and simulated screens; the event
worker owns state truth and membership; the CLI worker owns mode dispatch and
checked final summaries. An independent reviewer checks the implementation against
task scenarios, then a validator tests real TTY behavior and reader limitations.

No runtime qualification, build capability or deployment acceptance follows from
this UX analysis. Human usability sessions, the new presenter, and its Mac/Linux
acceptance have not yet been performed.
