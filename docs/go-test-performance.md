# Go test performance investigation

## Current result

The final default suite completed in **25 seconds wall-clock** on Linux arm64
with Go 1.27.1. This measured `go test -json ./... -count=1`: compilation caches
were warm, test-result caching was disabled, and Go used its normal package
parallelism. All 39 test packages passed. A preceding run from a fresh container
also passed. This is a local measurement, not a new hosted-CI timing.

The default now omits 51 extended cases and runs without race/coverage
instrumentation. Five core disk-evidence scenarios remain enabled; the full
40-case matrix still passed when explicitly enabled. The newly extended
document-size, source-alias and GPU-publication matrices also passed with opt-in.

`make check` passed, including vet, lint, shellcheck, and vulnerability scanning.
The retained disk-evidence scenarios passed a targeted Linux race run in 3.06
seconds. Actionlint and `git diff --check` passed. Plain yamllint reports 36
pre-existing style findings in the workflow; its diagnostics match HEAD exactly.

## CI evidence

Inspected the GitHub Actions history on October 7, 2026. The
[successful Merge Gate run 37692886512](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37692886512/job/113037289794)
spent 9 minutes 48 seconds in race-and-coverage tests, out of an 11 minute
19 second check job. Its JSON test events provide the timings below. The
[earlier successful run 37690164951](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37690164951)
showed the same dominant suites, with higher timings.

| Package | Latest sampled run | Earlier run |
| --- | ---: | ---: |
| factory | 379.5 s | 717.7 s |
| delivery | 218.0 s | 365.3 s |
| sbom | 97.0 s | 181.9 s |
| CLI | 96.1 s | 148.9 s |
| sign | 71.6 s | 96.4 s |
| lifecycle | 59.9 s | 79.3 s |
| planview | 42.2 s | 74.8 s |
| catalogmaintenance | 39.5 s | 59.2 s |
| catalog | 34.8 s | 65.3 s |
| pipeline | 22.5 s | 30.5 s |

These runs are newer than checkout `61ec66f`; `planview` and several expensive
reference-release tests do not exist in this checkout. Timings identify shared
hotspots, not an exact before/after comparison for this branch. Package durations
overlap and must not be added to predict workflow elapsed time.

## Fast default and explicit extended tests

Both ordinary `go test ./...` and `make test` omit the cases below.
`make test` runs ordinary tests without race or coverage instrumentation.
`make test-race` adds both for the default selection; `make test-full` also
enables extended fixtures. `make check` and the Merge Gate use the fast default.
Concurrency changes require targeted race checks, not an automatic instrumented
run of the entire repository on every agent turn.

| Package | Opt-in test or subcases | Sampled CI time |
| --- | --- | ---: |
| delivery | `TestDiskPublicationLargeInventoryAttestationReadback` | 146.95 s |
| sign | `TestSBOMAttestationCreationRequiresExactPredicateAndVerification`: both large successful inventories | 60.30 s |
| sbom | `TestAttestedDocumentInventoryAndBundleSizeBounds` | 38.67 s |
| factory | `TestMaintainedPinSetCatalogCredentials` | 36.64 s |
| CLI | `TestAuthoringLabReplacementJourney` | 29.14 s |
| sbom | `TestCompleteDiskFileInventoryCapacity`: 8,000 and 50,000 files | 21.96 s |
| factory | `TestPublicationGraphRequiresSBOMInputs` | 18.59 s |
| catalog | `TestSourcePinCheckAndWriteShareExactSerializedBound` | 15.68 s |
| factory | `TestMaintainedWorkflowMakesImageBindingVisible` | 12.70 s |
| sbom | `TestAttestedDocumentInnerInventoryBound` | 11.79 s |

Additional selection after profiling the remaining default suite:

| Test | Fast default | Extended |
| --- | --- | --- |
| `TestDiskEvidenceIssuerCoreOwnsCompleteJoin` | amd64 issuance, subject substitution, missing edge, scan cancellation, close during scan | Other 35 architecture/scenario combinations |
| `TestCompleteDocumentBytePolicy` | Ordinary document | 2 MiB and over-limit serialization cases |
| `TestSourceCheckoutWithOnlyAliasedRoots` | Skipped; repository collision and ordinary refresh checks remain | Complete alias/mode matrix |
| `TestCompleteGPUPublicationClosureAcrossFormatsAndArchitectures` | Skipped; focused output observation and substitution checks remain | Complete multi-format, two-architecture publication journey |

The initial selection above accounted for approximately 393 seconds of test work in the
sampled run. No claim is made that CI elapsed time falls by that amount.

The capacity fixtures repeatedly encode, decode, canonicalize, attest, and read
back 25–32 MiB documents, or project tens of thousands of files. The workflow
cases render or repeatedly validate the complete maintained product plan. The
authoring lab launches the real CLI repeatedly in two executable-demo journeys.
These remain available for focused investigations but are not routine PR gates.

The default suite keeps ordinary publication readback, signer invocation and
verification failures, subject substitution, credential isolation on smaller
fixtures, workflow labels, SBOM graph contracts, and byte-admission checks. A
16-file inventory projection exercises complete membership in the routine suite;
the two scale cases remain opt-in. The specific near-limit decoding and complete
maintained-plan checks listed above are no longer established by a default pass.

Enable all extended cases with:

```sh
make test-full
```

Or select the relevant cases explicitly:

```sh
NKD_TEST_EXTENDED=1 go test -race ./pkg/nkd/delivery -run '^TestDiskPublicationLargeInventoryAttestationReadback$' -count=1
NKD_TEST_EXTENDED=1 go test ./pkg/nkd/factory -run '^TestMaintainedPinSetCatalogCredentials$' -count=1
```

The opt-in requires the exact value `1`. Go's `-short` flag overrides it. Existing
external-tool and platform prerequisites still apply; the extended switch does
not provision external services or turn contract tests into operational evidence.

## Remaining work

The review covered every package timing in the sampled logs, including lifecycle,
catalog maintenance, pipeline, signing, and the newer planview suite. Smaller
contract matrices remain enabled. In the newer CI revision, reference-release
authentication and maintained private-image credential tests are also expensive;
they need review in the checkout that contains them. Further improvements should
reduce repeated fixture setup and use small representative plans for behavior
tests, while retaining focused negative tests. Race and coverage instrumentation are
explicit diagnostic modes, not default PR requirements.

## Earlier baseline

Before removing default instrumentation and narrowing the additional matrices,
validated the initial reduced suite on Linux arm64 with the pinned Go 1.27.1:

```sh
go test -p 1 -json ./... -count=1
go test -p 1 -race -covermode=atomic -coverprofile=/reports/nkd-linux-default-coverage.out -json ./... -count=1
go vet ./...
golangci-lint run
make shellcheck
make vulncheck
make go-version-check fmt-check
git diff --check
```

All checks passed. Packages were serialized to avoid memory pressure in the
local Docker environment. The ordinary run's package times totaled 68.4 seconds;
the race-and-coverage run totaled 275.3 seconds, excluding compilation. These
local observations are not a hosted-CI before/after benchmark. The remaining
largest race package was factory at 123.0 seconds, followed by lifecycle and
SBOM at 20.6 seconds each, catalog maintenance at 19.3 seconds, and pipeline at
15.3 seconds. This leaves substantial cumulative contract-test cost in factory.

An audit of the ordinary run confirmed all 12 intended test/subtest exclusions,
with the new 16-file inventory case passing. A focused Linux publication test
skipped with `NKD_TEST_EXTENDED=0`, also skipped with `NKD_TEST_EXTENDED=1` plus
`-short`, and passed when explicitly enabled without short mode (6.50 seconds,
without race instrumentation). Before the change, the same test took 105.73
seconds with race and coverage on macOS; the sampled hosted CI time was 146.95
seconds. These timings use different environments and instrumentation and are
reported separately.
