# Test signal and runtime

The ordinary gate runs contract tests without race or coverage instrumentation. Use
`make test GO_TEST_FLAGS="-json -count=1"` to collect package and test timings. Capacity fixtures and
external integrations retain their explicit opt-ins; passing ordinary tests proves their exercised
contracts, not registry, signing-service or production acceptance.

`make test` gives each package an explicit 20-minute timeout for local diagnostics. The hosted
Merge Gate now has a five-minute job budget and partitions the ordinary suite as described below.
The factory suite exceeded Go's implicit 10-minute package limit in
[PR #188's check job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38831324801/job/116750572956)
while still executing contracts. The preceding green
[PR #187 check](https://github.com/dsx-ai-factory/nvkr/actions/runs/38823898898/job/116726770763)
already spent 515.003 seconds in that package. Override `GO_TEST_TIMEOUT` for diagnostics when needed; race,
coverage and extended fixtures remain separate opt-ins.

## October 19, 2026 follow-up

[PR #179's check job](https://github.com/dsx-ai-factory/nvkr/actions/runs/39301452391/job/118227493423)
passed at `c562181d7587bd45396febed61c61fc41e5693ef`, but its ordinary test step took 17m52s.
The retained `merge-gate-tests` event stream reports 852.897s for factory and 382.202s for lifecycle.
This audit changes the shared tests in the local checkout based on `b8a5095350ec38ef2401947a9b77309c6a8a3d25`;
that checkout is distinct from the PR head. CI durations below identify priorities, not predicted savings.

| Expensive work in the linked CI run | Seconds | Decision |
|---|---:|---|
| Maintained M2 private-image credential mutations | 103.05 | Keep one maintained positive check; exercise mutations with one native disk and its vendor fetch. Remove repeated caller substitutions already covered by the owning validator, retaining one composed substitution. |
| Output addition/removal matrix | 54.77 | Reduce 15 combinations to seven: all three insertion positions once, plus each distinct execution policy and the real collector boundary. Retain exact restoration and existing-job comparisons. |
| Maintained inline-job checks | 42.22 | Share the same four maintained renders with credential checks; consolidate three additional default-bundle regeneration tests into that parent. Retain byte-exact drift, input selection and size headroom. |
| Qualification evidence mutations | 36.43 | Assemble the release once, then copy its files for every mutation. Keep all eight rejection cases and no-partial-evidence assertions. |
| Finalizer command/environment admission | 34.86 | Use a small compiled snapshot with an authenticated PinSet credential instead of the complete product graph. |
| Tenant wait cancellation/deadlines | 32.26 | Keep cancellation at all six blocking stages and one real eight-second deadline at the last readiness stage. Remove three repeated waits on the same overall budget. |
| Private-image publication-policy matrix | 30.44 | Keep all three credential policies using the small native disk fixture. |
| Development graph and private toolchain reads | 17.98 | Use one disk and its vendor fetch; retain producer identities, graph membership, private reads and finalizer controls. |
| Nightly OVN credentials | 15.66 | Check the selected source configuration directly. Keep emitted credential confinement and substitution tests at the workflow renderer. |
| Dependency projection admission | 15.28 | Construct the projection from a small compiled collector plan instead of rendering the maintained product for a mutation table. |
| Registry failure classification | 12.96 | Move the status matrix to the owning error classifier. Retain real HTTP refusal, deadline, cancellation and staging cleanup; remove three repeated retry-backoff waits. |
| Collected-run recovery | 10.34 | Cross the actual collector threshold with six small outputs instead of the full product. |

Runtime delivery/install tests also share immutable compiled preparation and copy files into independent
roots. Every member retains corruption or old-byte substitution coverage; missing and nonregular-file
checks use representative cases at each distinct reader boundary instead of a full Cartesian product.
Lifecycle qualification fixtures now create only their final publication, rather than two discarded
publications along the way. Smoke tables share authenticated artifact preparation while retaining separate
invocations and mutable runtime state. The FDE regression checks the complete authored output
list, then compiles and renders its four native disks without rebuilding unrelated controllers and charts.
That deliberately removes full-product compilation from the default FDE test; the existing opt-in
`TestMaintainedM2AllOutputGraphs` and separate generated-bundle drift gate retain their broader scope.

Diagnostic operations now borrow one authenticated fixture, with source revocation last; joining and
close admission retain their own destructive fixture. Executable-source revocation checks both the active
stream and subsequent admission against the same receipt. Registry authentication retains every distinct
output transport and strict-policy acceptance/rejection, while dropping architecture/encryption
combinations that exercise the same metadata-only route. Negative evidence cases share immutable
registry bytes with independent verifiers and workspaces. Metadata archive rejection no longer builds
an unrelated signed release, and its successful transport round trip uses one output.

The PR-only
[`TestRunnerBlockedDiagnosticSinkRetainsUnjoinedOutcome`](https://github.com/dsx-ai-factory/nvkr/blob/c562181d7587bd45396febed61c61fc41e5693ef/internal/nvkedemo/support_test.go#L313)
deliberately consumes 45 seconds.
It protects a distinct deadlock regression when a diagnostic writer holds the capture mutex after cleanup
times out. Deleting it would lose useful coverage. Its assertion should move to a focused result-collection
boundary or use a per-owner injectable cleanup budget; the production ownership allowance should remain
unchanged. That test was absent from the audit's starting checkout; integrating PR #179 later brought it
into this branch unchanged.

The five-minute CI target also requires shortening the critical path: the linked job spent 70 seconds
in vet and 160 seconds in lint before starting tests. Its other jobs already finished within three minutes
(generated-bundle checks: 2m14s; CodeQL: 2m50s). Repository checks therefore separate static checks, lint,
vulnerability scanning and ordinary tests into concurrent jobs. Eleven deterministic test shards split
factory across four jobs, lifecycle across two, and the remaining packages across five. Discovery includes
new tests automatically; sharding must preserve each ordinary test exactly once and propagate failures.
The existing local `make test` and `make check` remain available without sharding or new opt-in conditions.
The workflow derives its matrix from the same shard definition used by the runner. Contract tests exercise
partition completeness, compiled examples/fuzz discovery, missing/extra results, child failures,
process-group timeout cleanup, and aggregate rejection of skipped, failed or canceled code jobs.

Two shards for the remaining packages were rejected: one accumulated 538 seconds of package execution
in the linked CI record. Scheduling those package durations alphabetically across two execution slots
estimated 311 seconds before compilation or job setup. Five package shards reduce that same scheduling
estimate to 151 seconds at the slowest shard. This is a distribution check against historical durations,
not a hosted benchmark; it omits compilation, runner queues and timing changes from these fixture cuts.

The NVKR product code is unchanged. The first hosted
[`workflow_dispatch` run](https://github.com/dsx-ai-factory/nvkr/actions/runs/39323382691), at commit
`d1b957a379fa2ea95bd93277f1bdd00db693e045`, completed in **273 seconds (4m33s)**. After integrating
current `main`, the [PR run](https://github.com/dsx-ai-factory/nvkr/actions/runs/39325384719) at
`23fa7cc6e0aae1fb7cc7c9daff0229c4bb392c11` completed in **266 seconds (4m26s)**. Both measurements run
from workflow creation through aggregate-gate completion; all eleven test shards and all 21 required
jobs passed. These are individual observations against a 22m55s baseline from a different commit,
not a controlled benchmark or a guarantee about runner queues. They establish repository CI timing,
not external release qualification.

Local validation on macOS ARM64 with Go 1.27.2 completed all 62 packages and 3,505 unique top-level
test/example/fuzz results, including existing skips, without failures. The full run used the initial
eight-way distribution; the final eleven-way assignment was then checked against the compiled inventory
and successful package events. Redistributing the other packages changes no package's test selection.
A separate real Go fixture exercised all eleven shards with 748 targets exactly once and verified that
an injected test failure propagated. All 16 Python tool tests and the executable aggregate-gate contracts
passed. Targeted race checks passed for the changed factory/runtime and lifecycle shared fixtures and
ownership tests. Formatting, license headers, vet, golangci-lint, shellcheck, vulnerability scanning
(no vulnerabilities), build, actionlint, yamllint and `git diff --check` passed. Raw local events and
check logs are in `/tmp/nvkr-test-audit-oct19/` on the measuring host; this is not a durable CI artifact
or evidence of Linux-only release execution.

## October 14, 2026 audit

The audit started from `540c5d3d` and measured the complete ordinary suite before and after test-only
changes, before rebasing onto `258ab29b` for publication. Both measured runs use the pinned Go 1.27.2
on macOS ARM64, default package concurrency and `-count=1`.
These are single local observations using the existing build cache, not a statistical benchmark or predicted
GitHub/Linux timings. Package execution overlaps; do not sum package durations into wall time.

| Package | Before | After | Reduction |
|---|---:|---:|---:|
| `pkg/nvkr/factory` | 559.53s | 236.16s | 57.8% |
| `pkg/nvkr/delivery` | 594.84s | 372.32s | 37.4% |
| `pkg/nvkr/catalog` | 131.37s | 89.41s | 31.9% |
| `pkg/nvkr/catalogmaintenance` | 175.29s | 77.87s | 55.6% |
| `pkg/nvkr/lifecycle` | 292.90s | 229.52s | 21.6% |
| `internal/nvkedemo` | 261.71s | 217.53s | 16.9% |
| `cmd/nvkr` | 135.86s | 96.39s | 29.1% |
| `pkg/nvkr/sbom` | 28.21s | 19.34s | 31.4% |

The first-to-last Go test event span fell from 598.35s to 375.94s (37.2%). This span excludes
initial compilation before the first event. Both runs completed all 45 packages without failures.
Test/subtest passes fell from 10,485 to 10,359; skips remained 160. Eleven standalone tests were
removed, with additional duplicate table rows consolidated. No new opt-in or skip condition was added.

The baseline passed, but delivery took nearly the default ten-minute package timeout. Factory's
`TestReferenceReleaseAuthenticatesEveryOutputBeforeAssembly` alone took 219.71 seconds: eight rows
repeated the same two-output release preparation. It now prepares the immutable release once and
isolates the signer, mutated records and assembly destination for every row. All authentication
assertions remain.

## Removed work and retained coverage

| Area | Removed or consolidated | Retained signal |
|---|---|---|
| Factory graph and bundle contracts | Three duplicate or misleading tests, including a malformed graph fixture that could fail before its intended guard | Coded graph/dataflow mutation tables, dependency ordering/cycle checks, canonical round-trip and noncanonical-byte rejection |
| Factory compilation and publication | Six-producer generic fixture reduced to two; repeated workflow renders, disk wrappers, image-index packaging and release/SBOM preparation shared | Multiple independent producer joins; every distinct substitution, authentication and incomplete-evidence case; isolated mutation files and output paths |
| Architecture matrices | Repeated architecture-independent malformed disk/provenance/credential cases; duplicate CLI metadata-browse architecture pass; expensive image-release Cartesian product | Both native architecture happy paths, mismatched-platform checks, the lower image/SBOM format matrix, and all CLI format/filter/corrupt-metadata checks |
| Delivery inspection | Four legacy tests overlapping stronger publication, APT and credential tables | Both `Inspect` and `OpenPublication` reject corrupted nested configuration and unreachable bytes; exact APT projection/hash checks remain |
| Delivery interruption | Repeated deadline/failure combinations and disk construction for generic registry interruptions | Active cancellation at all four read boundaries; a real deadline and permanent transport failure; the complete error-classification table |
| Delivery evidence limits | Redundant 64 MiB padded transport round trip | Exact and one-byte-over descriptor admission for separate document/bundle limits; real exact-size decoding remains in SBOM; small valid attestation/readback remains in delivery |
| Catalog | Git/TLS fixtures for invalid declarations, repeated parser failures through full pin transactions, repeated chart no-op/rollback phases and standalone exact-version integration | Public early admission without lock mutation, exhaustive extraction cases, actual transactional rollback, exact prefixed chart version in the existing round trip, provenance and native acquisition |
| Catalog maintenance | Repeated repositories and generated bundles for prefix spelling/location matrices; a test of Git's own prefix behavior; full Git transactions for an error-forwarding table | Every distinct collision location and accepted alias, both workflow/refresh entrypoints, unchanged lock/Plan assertions, real Git alias acquisition, real commit/regeneration/rollback journeys and exact coded-error forwarding |
| Directory capacity | 1,025 file creations solely to cross the aggregate entry counter | Actual filesystem walker with two entries and a seeded counter at the last accepted / first rejected boundary; public listing/depth/link tests remain |
| Lifecycle observations | Rebuilding the same binary publication for each description/producer-record mutation | Every original membership and metadata rejection, workspace relocation and exact positive observation; independent filesystem copies for destructive mutations |
| Nested OCI inspection | Architecture/compression Cartesian product for every malformed layout | Each distinct closure defect; plain/compressed and native-platform representatives; both-platform enumeration, actual scanner and post-scan revalidation tests remain |
| Demo test harness | Subprocess that reran six existing tests to check their fixture environment setup (39.10 seconds) | Those production tests still run once, including explicit Git override admission and transport isolation |

No production behavior, capacity limit, timeout, default test flags or workflow changed. Tests were
removed or consolidated rather than hidden behind new skips. Existing opt-in capacity and external
integration tests remain available.

## Keep future tests focused

- Put exhaustive malformed-input tables at the owning parser/validator boundary. Keep representative
  composed tests for actual transaction, transport and authority handoffs.
- Check that negative fixtures reach the intended guard. Prefer the stable error code and a diagnostic
  identifying the boundary over `err != nil`.
- Build the smallest valid graph that exposes the contract. Use two distinct producers when proving
  completeness or ordering; rebuilding an entire product is unnecessary for scalar validation.
- Share immutable preparation within a test. Copy mutable files, maps and records per row; do not
  introduce package-global fixtures or depend on test execution order.
- Keep real cancellation, child joining, rollback, byte-substitution and exact-boundary checks. Remove
  repeated combinations only when the omitted dimension does not alter the exercised behavior and
  its own boundary remains covered.
- Use the recorded slow tests to choose the next change. Do not remove a unique failure contract just
  because its integration fixture is expensive.

## Validation and limits

The audited tree before rebasing passed:

- `go test -json ./... -count=1` (all 45 packages, no failures).
- Targeted `go test -race` for factory's shared reference-release, disk observation, image-index,
  mixed-utility and SBOM fixtures; delivery's active pull/push interruptions; and lifecycle's binary
  observation/producer-metadata tables.
- Exact Go version and formatting checks, `go vet ./...`, golangci-lint 2.14.0, `make shellcheck`,
  `make test-ci-tools`, `make vulncheck` (no vulnerabilities found), and `git diff --check`.
- `make license-header-check` and `make build` after staging the deleted legacy test file.

Raw local events and gate logs for this audit are under `/tmp/nvkr-test-audit/` on the measuring host;
this scratch location is not a durable CI artifact. These macOS checks do not establish Linux-only
execution or an externally exercised release. No race/coverage performance comparison was made.

Reproduce the ordinary measurement with:

```sh
GOTOOLCHAIN=local go test -json ./... -count=1 > test-events.jsonl
```

Use the installed toolchain matching `.go-version`. Run targeted race checks when changing shared
fixture lifetimes or asynchronous test infrastructure. `make test-race` and `make test-full` remain
explicit diagnostic options; they are not substitutes for external operational evidence.
