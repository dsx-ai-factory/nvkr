# Bounded disk-inspection diagnostics

## Scope

This change adds fixed phase/timing records, bounded sanitized timeout summaries and
an optional one-day hosted diagnostic upload. It does not increase an inspection
deadline, change inventory acceptance or complete a full native disk inspection.
The explicit M2 `build-provenance-v1` mode emits no inspection work or diagnostic upload.

The failure motivating this work is the post-build inspection timeout in
[AMD64 job 112211682115](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37445475840/job/112211682115)
and the corresponding ARM64 job. The existing logs do not establish which internal
phase exhausted the allowance. `export-startup` deliberately includes appliance
startup and auto-mount; first exported bytes identify extraction.

## Historical local validation

The final diagnostic source overlay on execution commit
`73395149a8fd3435c7cda7ba466a566bffc79b7d` passed:

- `make check`, including race/coverage, vet, lint, shellcheck and vulnerability scanning.
- `go test ./... -count=1`, with no disabled tests.
- `make workflows-check`, `make build` and `git diff HEAD --check`.
- Independent source review, including strict-upload mutations and deferred-mode rejection on both output architectures.
- Static inspection-helper compilation for Linux AMD64 and ARM64 using Go 1.27.1.

The generated M2 bundle remained byte-identical, with Plan digest
`sha256:2c2bf59ad5b300c55da19e2068006562f8ddf60b8899444dbdfd88cc058081f2`,
because its selected evidence mode defers inspection.

The worktree was then fast-forwarded to parent PR #97 at
`0dfa38b10999a92837c717d2475bf881c9f71af6`. That parent adds the already-matching
test lint correction and Packer shell portability fix. The composed tree also passed
`make check`, `make workflows-check`, `make build`, `git diff HEAD --check` and an
uncached `go test ./toolchains/nkd-packer -count=1`. The diagnostic implementation was unchanged during that
specific parent-composition gate. These historical checks do not validate later reporter or review fixes.

## Historical native contract validation

Controlled Linux ARM64 tests used the already-local Go 1.27.1 image
`266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`,
without pulls or network, with read-only source/modules, an init reaper, non-root
UID/GID 502:20 and private daemon-owned scratch storage.

| Contract suite | Recorded result |
| --- | --- |
| Full pipeline race tests | Passed, 16.317 seconds |
| Focused factory diagnostic/retention/upload race tests | Passed, 10.511 seconds |
| Final nil/canceled/expired retention-context admission | Passed, 1.016 seconds |

The final context-admission test recorded unchanged retention source and test hashes
before and after execution. Results are retained in the local execution transcript,
not standalone terminal log files. Earlier aggregate native runs failed on scratch,
child-reaping and macOS-shared FIFO harness limitations; corrected harness runs
passed without source changes or weaker assertions. An earlier full factory pass
predates the final retention edit and is not final whole-factory proof.

## Remaining execution boundary

Rebuild and repin the complete native inspector images before claiming deployment
of the modified helper. The installed-package diagnostic and full-content helper
retain their existing ten-minute ceiling. A larger full-content allowance requires
the separately reviewed deadline contract, not a silently extended context.
Real full-content inspection and exact-disk reconciliation remain unproven on both
architectures. Compilation and contract tests do not replace that acceptance.

## Review-fix validation

The review-fix source snapshot is based on `a9dfaedd861ad3f5deda6f1ecf076e4bf07ea015`.
It restores bounded, quoted non-timeout failure causes only in the returned error, marks diagnostic sampling
and byte eviction consistently, preserves event/member boundaries, separates filesystem staging/probing,
scan transport and member walking, joins concurrent cancellation causes, and atomically publishes complete
diagnostic files without replacement. Failure snapshots are folded and include the truncation marker;
successful runs do not dump them. The optional upload ignores missing files, while required evidence stays mandatory.

The corrected native harness uses the already-local Linux ARM64 image
`266d5d7746a154e2325a9650f759035641d09673eb6e2b3ccdb23cfb1b49f3d6`, `--pull=never`, no network,
read-only source/modules, non-root UID/GID 502:20, an init reaper and dropped capabilities.
The full pipeline suite needs daemon-owned disk-backed `TMPDIR`: a preceding tmpfs run correctly failed
the unrelated source-vendor capacity admission. Rerunning with the same assertions on a fresh, observed
local-driver daemon volume passed; the volume was removed after checking no container retained it.

| Exact snapshot contract check | Result | Local execution record |
| --- | --- | --- |
| Native ARM64 full `go test -race ./pkg/nkd/pipeline -count=1` | Passed, 23.280 seconds | Session 81297, chunk 8a2720 |
| Native ARM64 focused diskdiagnostic race | Passed, 3.376 seconds | Session 15896, chunk c083f3 |
| Native ARM64 focused factory race | Passed, 9.268 seconds | Session 15896, chunk c083f3 |
| Native ARM64 focused sbom progress race | Passed, 1.190 seconds | Session 15896, chunk c083f3 |
| `make workflows-check` and `make build` | Passed; M2 Plan digest unchanged | Session 50894, chunk 821001 |
| `make check` | Failed at unchanged ten-minute factory/lifecycle package deadlines; vulnerability stage not reached | Session 92169, chunk bdae4a |
| Isolated active factory lifecycle test, unchanged assertions | Passed, 17.697 seconds; does not establish aggregate timeout cause | Session 34908, chunk e067d1 |
| Standalone `make vulncheck` | Passed, no vulnerabilities found | Session 78007, chunk cd1877 |
| Complete native ARM64 race/atomic-coverage `./... -count=1`, unchanged assertions/deadlines | Passed, exit 0; factory 194.702 seconds, lifecycle 30.032 seconds, pipeline 21.603 seconds, sbom 69.420 seconds | Session 22093, final chunk ebdfee |

The focused selector includes receiver validation/flooding, enriched byte eviction and sequential collectors,
failure-only cause isolation, actual child cancellation/join, retention/wiring/upload mutations, reporter
precedence, first-byte/member coordinates, material member walking and
`TestContainerScanProgress/{success,scanner_failure,timeout}`. Those last three rows executed natively,
not as macOS skips. The source snapshot has these SHA-256 identities:

| Source | SHA-256 |
| --- | --- |
| `internal/diskdiagnostic/progress.go` | `aa627919964427f04254f667e8a45c0739e83ef18b01fb6c254fafc7c99576e4` |
| `pkg/nkd/factory/github_disk_diagnostics.go` | `2234c1571b7b134d39a4df45da806722ce02b9ce1a83b06b34b52add1f25f626` |
| `pkg/nkd/factory/github_reporter.go` | `ded652ff7afd8394f9982273a132e56b89d557d59f70722c8bf70b9f6031c52e` |
| `pkg/nkd/pipeline/disk_inspection_runtime.go` | `d1d66e5d542c725aea1bb1402e444738f65ac50e1a9586d2f31ec326e6470eca` |
| `pkg/nkd/sbom/disk_filesystem.go` | `d2b20e77b7579e2ab3ef923c49716c2c350b29cb6c061a179db2e78995462e42` |
| `pkg/nkd/sbom/disk_materials.go` | `e072536ea62d61d48acf4022c11168a294a9a4eab6e95b950b1b2532357294d3` |

These are controlled contract tests, not actual guestfish appliance/ISO execution, native AMD64 execution,
full disk reconciliation or hosted acceptance. An existing diagnostic destination still fails no-replace
publication; it is not authenticated as this attempt's log. Ephemeral single-job workspace policy remains
required. At this historical snapshot a total accepted-record CPU ceiling was deferred; the separate
parsing-ceiling validation below supersedes that implementation limitation.

The [retained terminal record](2026-10-06-disk-inspection-diagnostics.log) contains the verbatim executed
native commands/selectors and actual compact terminal outputs above. Its SHA-256 is
`8d631c5f9c0bc1ea6c91ffe14fb6be4d9b1dc87078c9c519855b243e5b0402a7`.
The complete changed-Go diff, including all changed tests, has SHA-256
`58594e7b2cbfeaeb1ee5dbc2fdef1f70dd4b2024006760af996dd0a7533e50d4` relative to the stated base.

The failed macOS gate passed format, vet, lint and shellcheck first; its pipeline and sbom race/coverage
packages also passed. The captured factory/lifecycle stacks were in Darwin filesystem calls. Concurrent
full-package suites were observed, but the cause has not been established. The unchanged source then passed
the complete equivalent race/atomic-coverage test stage on the already-local native Linux ARM64 image
`1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb`, verified with Go 1.27.1,
jq 1.7, Git and the shell/archive tools. Tests had read-only source/modules, task-owned ignored `.nkd`
outputs and disk-backed temporary state; no network, new pull, deadline extension, test disablement or
acceptance relaxation was used. Together with the passed host non-test gates, build/drift and standalone
vulnerability scan, this validates the review-fix snapshot. It does not claim the macOS aggregate gate passed,
native AMD64 execution or full guestfish/disk acceptance.

## Exact parent integration validation

The conflict union integrates parent `9ba0ad047658fcc0d0a67b0a2d0b48c5a56843e6` into
diagnostic head `4ea09f3be40623105d04a6d0d7d2109cd4b4c352` by local merge, preserving both histories.
The generated disk step remains bounded and is immediately followed by the parent's exact sealed Packer
failure upload. Strict-only inspection retention follows separately; required carriers remain mandatory.
Both complete diagnostic test sets are preserved, and the required-carrier mutation selects a carrier rather
than the newly earlier Packer diagnostic upload. Two independent source reviews found no blocking union defect.

The frozen tracked Go/module/version content manifest has SHA-256
`4149431f295e918a37b0a6396e8b0ddefa9666e49d646fa00963dd3b6ea109e3`.
The staged Go/module/version diff has SHA-256
`b46928ed64ad551b261a779c0f8df095eebb6ceb5d118446dd214bb94d67d922` relative to the diagnostic parent,
and `36b5c6241933e8bc37ce265068f6a2c456d2cc8af04b33a22d06a81eaeba2194` relative to the execution parent.

The complete native Linux ARM64 race/atomic-coverage `./... -count=1` gate passed without changed
assertions or deadlines: factory 226.379 seconds, lifecycle 39.492 seconds, pipeline 22.606 seconds and
sbom 74.689 seconds. It reused the verified local `1f87906be577585bff2bc2bf05662972bd9ddd2ddae29975f05e0501090d3abb`
image, read-only source/modules and task-owned ignored outputs, without pulls or network.
Host format, vet, lint, shellcheck, vulnerability, build and generated-workflow drift gates also passed;
the current generated Plan is `sha256:850cea27b78f621ccb48242b41a8210c4df6efaee139d4102bfc325270e6cb05`.
The focused host diagnostic/retention/deadline suite passed in 1.569 seconds.

Commands and actual terminal outputs are retained in the
[parent-union record](2026-10-06-disk-inspection-parent-union.log), SHA-256
`e0ffbeab789f5b50e5151599a0fb91a6cda1ae551c97acaef689ca6c6250fef7`.
This is controlled contract validation, not native AMD64 execution, a real full disk inspection or hosted
acceptance. The historical macOS aggregate failure above remains unresolved as an aggregate cause;
this union uses the complete native Linux equivalent test stage plus the host non-test gates.

## Diagnostic parsing-ceiling validation

The follow-up on base `ba87e272ada8c561c69819062a97fdcd66283ab8` bounds prefixed diagnostic candidates
before canonical or JSON decoding, including malformed candidates. The 5,424,414-record allowance is derived
from six fixed helper records plus 33 filesystem passes, each allowing 14 filesystem records, eight archive
records per root/selected member, each selected member's start/complete pair and 256 image-scan pairs. The
16,384 selected-member allowance includes an extra rejected-member start. Existing installer/image caps are
named without changing their values, and the SBOM parity test binds the ceiling to the owning caps.
Exhaustion fails inspection, drains without further framing/JSON work and preserves the last accepted phase;
no partial report is accepted and no rejected bytes enter retained diagnostics.

Boundary-minus-one, exact/over-ceiling, byte chunking, malformed candidates, post-ceiling flood/raw discard,
independent sequential receiver budgets, concurrent writes and actual child cancellation/join regressions pass.
The complete Linux ARM64 race/atomic-coverage `./... -count=1` gate passed on the same already-local image
and unchanged harness/assertions/deadlines: factory 271.377 seconds, lifecycle 42.567 seconds, pipeline
20.780 seconds, sbom 102.422 seconds and diskdiagnostic 5.638 seconds. Host format, vet, lint, shellcheck,
vulnerability, build and generated-workflow drift checks passed; the Plan digest is unchanged. The focused
host diagnostic race suite passed in 4.459 seconds. Two independent source reviews found no blocking issue.

The complete changed-Go diff, including tests, is SHA-256
`1526bc078de0b0b765eb8fa5436cd570866c1ae1dd415456037def015b9603f4` relative to the stated base.
The [verbatim command/terminal record](2026-10-06-disk-inspection-volume.log) has SHA-256
`678a28d10235113ede88b9884de7c4b60fbc2d9c9730114deec872c329bc1337` (trailing blank separator normalized).
This is contract validation, not native AMD64 execution, a new Mac aggregate-gate pass, rebuilt-helper
deployment, full NKE disk inspection or hosted acceptance. Historical limitations above remain separately scoped.
