# Compact GitHub runner validation

Date: 2026-10-04 America/Los_Angeles. PR: #90. Parent:
`582ab01c26226fd61358d3067b1746cd79d8fc49`.
Scope: generated-workflow structure, local helper/transport contracts and one real pinned utility-container
failure path. This is not a hosted artifact build, publication, signed Release acceptance or qualification.

The renderer delegates node execution, input restoration, output packing and finalizer recovery to four
Plan-bound helper operations. Provider actions, job topology, conditions, permissions and source credentials
remain visible in YAML. No new authored input or transport manifest is introduced. Packing remains separate
from source execution. Utility launch is shared with local execution, while its mounted helper checks the
exact expected Plan digest. The host retains utility errors, cancellation and cleanup, with an internal
utility deadline derived from the existing stage budget.

## Generated result

[Recorded measurements](2026-10-04-ci-runner-encapsulation/generated-metrics.json) compare the base,
initial PR and compact helper using the same maintained six-output Definition and Environment.

| Item | Initial PR | Compact helper |
|---|---:|---:|
| Workflow lines | 5,079 | 1,145 |
| Workflow bytes | 345,920 | 59,933 |
| Jobs / logical operations | 23 / 33 | 23 / 33 |
| Visible steps | 212 | 209 |
| Download / upload actions | 36 / 44 | 36 / 44 |
| Finalizer steps | 10 | 7 |
| Largest decoded run script, bytes | 20,408 | 412 |
| Inline progress functions | 79 | 0 |

The comparison asserts exact equality of provider action steps, job scheduling/authority fields and
credential-bearing step environments. The only new step environment carries bounded provider result data
for recovery. The canonical Plan bytes remain unchanged, with exact blob SHA-256
`b58a29d86da34bab189c0bd7810a16b604475ac64766962aebb331b078fbe8ac` and Plan Digest
`sha256:7edadcb49c6ac7348c695e112f57df187c4de25efbb213cff26f20d4cb1b78a9`.
Workflow/manifest bytes were regenerated through the public command. Payload transfer count and size are
unchanged; this does not implement generated OS-image CI or solve large-image movement.

## Actual helper replay

[Command, log and summary records](2026-10-04-ci-runner-encapsulation/local-replay.json) identify the exact
native and Linux AMD64 helper binaries used. The unchanged maintained Plan was copied into private temporary
workspaces. Synthetic files stood in for two chart-build outputs, so transport success is not producer or
artifact-authentication evidence.

The actual CLI successfully packed both declared outputs and restored the exact consumer stage inputs.
Recovery visited all 42 artifacts and persisted two `restored:success`, 38 `missing`, one `restore-failed`
and one `job-failed` outcome. An unexpected provider job was rejected with exit 16 before creating carrier
or summary output. A missing fetch token returned exit 20 before fetching.

A real Docker invocation used the cached exact utility image
`busybox@sha256:29989570aeecad61a019f684218ea74d4b8c1c74f9e0abeb34ca926b81174ee1`, target `linux/amd64`,
network `none`, and the newly compiled Linux helper. The utility's missing producer result returned the
expected `NKD-E-VERIFY-FAILED` / exit 22 through the container and host reporter. The log identified
`execute controller-chart/terminal-observe`; neither `Completed` nor `Produced` was printed. This exercises
the actual shared launcher and child Plan-digest handoff, with a deliberately failing input.

[Six compiled-CLI pipe replays](2026-10-04-ci-runner-encapsulation/closed-pipes.json) closed the reader of
stdout, stderr or both. Successful packing still created both carriers and exited 0; missing-token execution
still exited 20. Diagnostic file-descriptor duplication prevents Go's SIGPIPE handling from replacing the
operation result. The final CLI error printer preserves its selected status when its reader has closed.

## Contract coverage

Direct helper tests cover complete-Plan and capability admission before effects, exact node/stage selection,
carrier-name collisions, internal versus external grouped-stage inputs, missing/corrupt mandatory inputs,
output packing failures, all recovery statuses, cancellation, outcome-storage failures and safe path handling.
Provider JSON tests reject unknown/duplicate jobs and fields, missing bootstraps, unknown results, invalid
UTF-8, oversize input and excessive token complexity. Ignored job outputs never enter logs.

Reporter and real-process tests cover primary runtime exits 0, 22, 23, 125 and 137, closed log streams,
absent/unwritable/symlink/FIFO summaries, literal escaped descriptions, missing-token preflight, Plan
substitution, Docker/Podman selection, Docker-only UID/HOME arguments, earlier cancellation and bounded
cleanup. Renderer tests retain exact command, ordering, credential, provider-action and workflow-byte
checks, plus the 21,000-byte guard for authored setup and bootstrap scripts. Large carrier graphs now retain
constant helper commands rather than growing recovery shell blocks.

## Completion gates and exact source

[Local gate results](2026-10-04-ci-runner-encapsulation/local-validation.json) record successful macOS ARM64
checks with Go 1.27.1 and `GOTOOLCHAIN=local`: `make check`, `go test ./... -count=1`,
`go test -race ./... -count=1`, `make build`, `make workflows-check`, actionlint, relaxed yamllint and
`git diff --check`. The full gate includes formatting, vet, zero-issue Go lint, ShellCheck, race/coverage and
the vulnerability scan. Yamllint returned success with long-line style warnings. The changed implementation,
test and generated-file bytes were rechecked unchanged against the [source identities](2026-10-04-ci-runner-encapsulation/source-blobs.json).

The [Linux snapshot record](2026-10-04-ci-runner-encapsulation/linux-validation.json) records the full
uncached `go test -race ./... -count=1` suite passing in 82.417 seconds and `go vet ./...` passing in 1.053
seconds. It used the existing exact Go 1.27.1 Linux ARM64 image, non-root UID 1000, an init process and a
private writable source snapshot. The snapshot/archive hashes bind the recorded run independently of the
later signed commit. No tests were disabled or skipped to obtain a passing gate.

Independent review verified command/Plan binding, credential scope, mandatory transport failures and recovery
outcomes. Its real SIGPIPE finding was fixed and re-reviewed before these final gates. The initial full suite
also exposed one remaining lifecycle test assertion for the old helper command; its exact node/runtime
assertions were migrated and both complete platform suites rerun successfully.
