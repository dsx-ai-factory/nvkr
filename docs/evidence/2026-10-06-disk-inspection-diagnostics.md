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

## Local validation

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
uncached `go test ./toolchains/nkd-packer -count=1`. No A source changed during that gate.

## Native contract validation

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
