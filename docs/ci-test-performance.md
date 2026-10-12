# CI test performance diagnosis

The race-and-coverage test step dominates the Merge Gate. Five successful upstream runs inspected on
October 6, 2026 spent 8m24s–13m50s in tests, accounting for 85–87% of the `check` job. The latest fork
run shows the same pattern. Optimizing workflow setup alone will not remove most of the wait.

## Recorded GitHub timings

These are GitHub job and step timestamps, rounded to whole seconds. Workflow duration runs from creation
to the final update, so it includes scheduling and the aggregate gate.

| Run | Workflow | Check job | Race and coverage tests |
|---|---:|---:|---:|
| [Upstream PR 96 latest](https://github.com/dsx-ai-factory/nvkr/actions/runs/37495073047) | 10m27s | 9m54s | 8m24s |
| [Upstream PR 96 previous](https://github.com/dsx-ai-factory/nvkr/actions/runs/37492435442) | 16m34s | 15m58s | 13m50s |
| [Upstream main at 61ec66f](https://github.com/dsx-ai-factory/nvkr/actions/runs/37423352366) | 12m21s | 11m59s | 10m13s |
| [Upstream PR 95](https://github.com/dsx-ai-factory/nvkr/actions/runs/37421759982) | 17m40s | 15m40s | 13m31s |
| [Upstream main at 7c0762d](https://github.com/dsx-ai-factory/nvkr/actions/runs/37420775719) | 15m11s | 14m51s | 12m53s |
| [Fork main at 61ec66f](https://github.com/njhensley/nke-recipes/actions/runs/37426768864) | 16m25s | — | 13m46s |

The fork's `check` job succeeded; its overall gate failed in other jobs. Those failures are separate from
test performance.

The latest upstream check spent 16s setting up Go, 32s on vet, 24s on lint, 1s building, and 8s on the
vulnerability check. CodeQL ran concurrently and finished in 2m51s. Tests determined the critical path.

## Slow packages

The following durations come from the actual `go test` package summaries in
[the latest check log](https://github.com/dsx-ai-factory/nvkr/actions/runs/37495073047/job/112377503678)
and [the preceding check log](https://github.com/dsx-ai-factory/nvkr/actions/runs/37492435442/job/112368503196).
They measure package execution; packages overlap, so these numbers must not be summed as wall time.

| Package under pkg/nvkr | Latest | Previous |
|---|---:|---:|
| delivery | 235.922s | 410.821s |
| factory | 222.080s | 372.261s |
| sbom | 111.695s | 193.514s |
| sign | 95.132s | 165.491s |

Many other packages finish in roughly 1–4s. The package execution measurements rule out compilation as
the sole explanation. The broad timing variation is consistent with differing runner throughput, but
these logs do not establish CPU contention, memory pressure, or a specific runner cause.

## Work performed by the test target

[The Makefile](../Makefile) runs every package with `-race -covermode=atomic -coverprofile=coverage.out`.
This includes filesystem, local registry, subprocess, and large-document contracts alongside small unit
tests. In particular:

- [Delivery's inventory attestation test](../pkg/nvkr/delivery/disk_publication_test.go) constructs a nearly
  32 MiB SBOM and passes it through publication, attestation, reopening, and readback.
- [SBOM capacity tests](../pkg/nvkr/sbom/disk_inventory_capacity_test.go) construct inventories with
  8,000 and 50,000 file entries. Other SBOM tests exercise the full 32 MiB document ceiling.
- [Signing tests](../pkg/nvkr/sign/sbom_attestation_test.go) also exercise large inventories and document
  and bundle size limits.
- [Linux signing fixtures](../pkg/nvkr/sign/qualified_linux_test.go) may compile a separate static test
  executable when the race-enabled executable is dynamically linked.

The inspected GitHub runs record package totals without individual test timings. Follow-up Linux profiling
of the full-size delivery test confirmed expensive per-rune buffer writes in canonical string encoding,
alongside JSON decoding and race instrumentation. The optimization below addresses the former without
changing the document size or verification assertions.

## Cache observations

The inspected upstream check jobs and the latest notices job restore the same approximately 90 MB
`setup-go` cache, keyed by Linux, architecture, runner OS, Go 1.27.1, and the dependency hash. Their
post steps report an exact primary-key hit and skip saving the cache. Consequently, build entries added
during those executions are not persisted by that action.

Because jobs with different workloads share that key, a lightly populated cache can remain the shared
baseline. The logs establish shared restoration and skipped updates; they do not establish which job
originally populated the cache or the exact amount of compilation it lacks. In the latest test step,
the first package result appears about 35s after invocation, so initial build/startup work also exists.

## Implemented improvements

Canonical string encoding now copies spans of already validated UTF-8, escaping only the same ASCII
controls, quote, and backslash as before. Table-driven golden tests cover Unicode, line separators,
escaping, control bytes, long spans, and exact decoding. No identity schema, input admission, or digest
family changes. The production-size inventory fixtures remain intact.

The check job owns a separate Go module/build cache, keyed by platform, exact Go version, dependency
hashes, and commit. It restores the most recent compatible check cache on a new commit and saves the
updated cache only after successful checks. Notices and other jobs cannot populate its key. Actual hosted
cache reuse and end-to-end workflow savings require a run after publication of this change.

The workflow now requests the same `make test` flags with optional JSON output, seven-day event/coverage
retention, and a summary of the slowest packages and tests, including on test failure. The explicit Bash
shell preserves pipeline failure through `tee`; a local replay confirmed exit status 17 remains 17 and
the diagnostic log is retained. `make test-ci-tools` validates summary parsing, failure reporting,
ordering, and name escaping, and is included in `make check`. Tests and the aggregate gate remain required.

## Measured improvement

Measurements used Go 1.27.1 on Linux ARM64 in the existing image
`sha256:1262b0b545eb82aff8a3f79ee47f23177812ad4517a36fb54ccfe8b4df0d621f`, with race detection
and coverage enabled. These are local container measurements, not predicted GitHub wall times. The host
also runs other workloads; the full-size test observations are single runs, not a statistical benchmark.

| Measurement | Before | After |
|---|---:|---:|
| Canonical 1 MiB string, median of three 3-iteration samples | 253.43 ms/op | 54.46 ms/op |
| Full-size inventory attestation/readback test | 116.91s | 88.16s |

The string benchmark improved about 4.65 times; the full-size test fell about 25%. Its input and assertions
were unchanged. The focused delivery invocation instruments that package for coverage; the canonical
benchmark instruments the codec. Neither is a whole-suite CI timing comparison.

A separate before/after comparison serialized every valid Unicode scalar in one string, alongside mixed
escape boundaries. Both codecs emitted the same 4,382,789 bytes (exact-blob SHA-256
`787a7b4ae33e770f3beace3e45552722bfb6fb5315004af4ee8d44590da1a41b`). The baseline was
`61ec66fa4ffaf75fee291450f7572d244f2e624f`; the optimized codec source's exact-blob SHA-256 was
`d2bc4821f28fd9fcdc3a3462bc7367facd8c9d48fafa97f8ee6a7cab24329d39`.

Reproduce the focused measurements with:

```sh
go test -race -cover -run TestMarshalStringBytes -bench BenchmarkMarshalString -benchtime=3x -count=3 ./pkg/nvkr/canonical
go test -race -cover -json -run '^TestDiskPublicationLargeInventoryAttestationReadback$' -count=1 -cpuprofile=/tmp/delivery.cpu ./pkg/nvkr/delivery
```

## Remaining opportunities

Use the new timing artifacts to profile the expensive factory cases and remaining JSON decoding work.
Balanced test shards could shorten the critical path further, but should retain every package, race
check, and coverage output. Measure their extra compilation and cache costs before restructuring jobs.
Parallelizing lint/vet and tests offers a smaller 1–2 minute opportunity in the inspected runs.

## Validation

On October 6, 2026, the Linux ARM64 snapshot passed:

- `go test ./... -count=1`.
- `go test -race -p=2 -json ./... -count=1` and `go vet ./...`.
- `make check GO_TEST_FLAGS="-json -count=1 -p=2"`, including golangci-lint 2.13.1, shellcheck,
  Python report tests, uncached race/atomic coverage, and the pinned vulnerability scanner.
- Parsing the full gate's real event stream into the new summary: 41 terminal package results,
  no failures, 76.8% statement coverage. Package results include packages without tests.

The changed Go files, Makefile, workflow, and reporting scripts were byte-checked against the tested
snapshot. Two package workers limited this local validation's memory use; no package was omitted and
the workflow's default package concurrency was not changed. `actionlint`, YAML lint with existing-style
rules, formatting, and `git diff --check` also passed. Hosted cache restore/save and artifact upload
remain configured but unexercised until this change runs on GitHub.

The macOS `make check` attempt failed with five staticcheck diagnostics in the unchanged
`pkg/nvkr/pipeline/disk_inspection_runtime.go` and `pkg/nvkr/pipeline/source_vendor.go`: SA4023 reports
always-true error comparisons against unsupported-platform helpers. An untouched `HEAD` snapshot
reproduced the same diagnostics under pinned Go 1.27.1 and golangci-lint 2.13.1. These pre-existing
macOS gate failures remain unresolved; the Linux full gate is green. The follow-up native test run and
an initial unrestricted Linux race run were stopped to relieve contention; only the completed Linux
runs above establish full-suite success.
