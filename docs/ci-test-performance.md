# Test signal and runtime

The ordinary gate runs contract tests without race or coverage instrumentation. Use
`make test GO_TEST_FLAGS="-json -count=1"` to collect package and test timings. Capacity fixtures and
external integrations retain their explicit opt-ins; passing ordinary tests proves their exercised
contracts, not registry, signing-service or production acceptance.

`make test` gives each package an explicit 20-minute timeout, including the hosted Merge Gate.
The factory suite exceeded Go's implicit 10-minute package limit in
[PR #188's check job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38831324801/job/116750572956)
while still executing contracts. The preceding green
[PR #187 check](https://github.com/dsx-ai-factory/nvkr/actions/runs/38823898898/job/116726770763)
already spent 515.003 seconds in that package. Override `GO_TEST_TIMEOUT` for diagnostics when needed; race,
coverage and extended fixtures remain separate opt-ins.

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
