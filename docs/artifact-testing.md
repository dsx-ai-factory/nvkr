# CLI artifact testing

Run NVKE-owned tests against an NVKR-built `nvkectl`, using the exact upstream
revision recorded in its build Plan. This is a trusted-input diagnostic; it does
not authenticate artifact membership in a Run or grant release qualification.

## Run locally

Keep the artifact and canonical Plan from the same NVKR build:

```sh
GOTOOLCHAIN=local go build -o bin/nvkr ./cmd/nvkr
bin/nvkr run artifact-test \
  --artifact /path/to/nvkectl \
  --plan /path/to/matching/plan.json \
  --suite contract --keep
```

Requires the repository-pinned Go version to build NVKR, Git, and Docker.
The command calls the Go Plan loader directly; no helper subprocess or local
NVKE clone is required.

Select `--output NAME` when a Plan contains multiple standalone CLI outputs.
Linux AMD64 and ARM64 are supported; foreign-architecture execution needs
emulation and may affect timing and signal tests. The selected toolchain image
must provide Go and Make; Plan validation does not prove those tools are present.

- `--suite contract` runs the upstream process contracts against the supplied binary.
- `--suite cli` runs the full upstream CLI Go suite. Its contracts use the binary;
  other unit tests exercise upstream source. Bootstrap E2E is separate.

## How it works

1. Factory validates the complete canonical Plan and selects one CLI producer
   using the same loader as `nvkr run artifact-test-inputs`.
2. Shallow-fetch the exact source commit, check its tree and clean worktree, and
   require the upstream `test-artifact` entrypoint.
3. Copy and hash the supplied executable in a private workspace. Dependency
   acquisition runs without the artifact; test execution mounts it read-only.
4. Run upstream Make offline, with dropped capabilities, no new privileges,
   explicit binary/version inputs, and writable temporary/cache/coverage paths.
5. Reject failed, skipped, empty, incomplete or missing contract-package results
   and check that staged bytes are unchanged. Package membership alone is not
   an upstream-owned guarantee that every required binary contract ran.

`--keep` retains the printed `.nvkr/artifact-tests/cli-*/` workspace. Source and
cache exist only after their phases run; `tests.jsonl` is created for test execution
and `result.json` only on success. Otherwise the workspace is removed on exit.
Each acquisition/execution command has a 30-minute timeout (`--timeout` overrides
it). Cancellation stops
and joins the client and requests container removal; removal is best effort.

Source import uses the existing Go local-source policy: global credential helpers
are projected only into fetch; ambient Git routing, hooks, filters and configuration
are excluded. Private access uses a developer credential helper or the supported
explicit local redirect, rather than `GIT_ASKPASS`. Test containers receive no Git
credentials. The verified scratch checkout must share a filesystem with the
workspace so it can be moved without exposing a partial import.

Errors use NVKR's coded exit contract; timeouts and cancellation use the timeout
code, while a failed suite preserves its positive process exit status. A failed
command never writes a successful result.

## Validation and remaining work

The Go replacement was exercised on 2026-10-15 from the main-based migration
working tree (base `c722be5`). A fresh recipe build and both separately imported,
cold-cache suites passed: **9 process contracts** and **532 top-level CLI tests
across 30 packages**, with no failures or skips. This was macOS ARM64 hosting
native Linux ARM64 in Docker's VM; it does not establish native Linux host or
Darwin executable acceptance.

Exact NVKE candidate `3da7d64940ffe94722a5a913ab5dc26b28430d72`, tree
`86d6b4cc3a9a021cc6d38aa445ed90f04da2b28b`, combines the supplied-binary interface
[#1578](https://github.com/dsx-ai-factory/nvke/pull/1578) with the independent
[FIFO startup fix #1637](https://github.com/dsx-ai-factory/nvke/pull/1637).
The selected output is `nvkectl`, with version equal to that commit and image
`golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c`.
Artifact SHA-256 remained
`df3de95b9df1ac89d6bca75eae7140a295cb0be51f99ed14531cefc784ca727c`.

Both Go suites ran from a working directory containing spaces, a colon, a comma
and a quote. Docker inspection records acquisition without an artifact mount and
execution with no network, read-only source/artifact/root filesystem, dropped
capabilities and no new privileges. Local raw records, candidate inputs, Plan,
fresh artifact and retained results are under `.nvkr/go-adapter-verification/`.
The separate [native Go runtime record](https://github.com/dsx-ai-factory/nvkr/actions/runs/38788495912)
uses the same Go implementation (`a553c56`) on native Linux AMD64 and ARM64.
Its diagnostic workflow and candidate inputs stay outside #138. The earlier
[native Go run](https://github.com/dsx-ai-factory/nvkr/actions/runs/38785478450)
passed full uncached repository tests, race checks and fresh recipe builds on
both architectures, then failed during unprivileged checksum collection from
root-owned attempt storage. The final runtime workflow corrects checksum reads
and transfers completed private workspaces before collecting their results.

Bounded real Go-command negatives rejected a freshly recipe-built
`v9.9.9-negative` executable and an `exit 7` executable with suite exit 2.
Maintained source `ed9fb09b` failed on its missing entrypoint with capability
exit 29. A two-second container timeout and SIGINT/SIGTERM delivered while a
container was running returned timeout exit 31. All six cases removed the default
workspace and owned container and published no success result; raw logs and
`negative-results.json` are in the same local record directory. The cancellation
exit contract deliberately differs from the historical Python 130/143 mapping.

Go regressions cover complete Plan admission before workspace creation, exact
shallow imports, wrong commit/tree refusal, poisoned ambient Git routing without
changing the caller checkout, both suite commands, private staging, byte changes,
result rejection and cleanup. Targeted process-lifetime race tests passed.
The [PR Merge Gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/38785476658)
passed on `5df8a63`. Local formatting, vet, lint, shellcheck, CI-report tests,
build, other package tests and vulnerability scan passed. Local `make check`
exhausted its default ten-minute lifecycle package budget; an uncached lifecycle
rerun with `-timeout=20m -count=1` passed in 596.7 seconds. This records successful
constituent checks without relabeling the timed-out aggregate invocation.
Compiler/loader fixtures cover both architectures, scalar/map precedence and
fallback, permitted relocation, unsupported producers and complete Plan admission.
The current main base admits renamed `NVKECTL_VERSION` stamps; loader contracts
cover renamed/equal aliases and reject conflicting values.

The checker deliberately retains the diagnostic limitation that a completed
ordinary test in the expected package can satisfy the package guard. A Go
regression records that behavior. Exhaustive acceptance needs an upstream-owned
inventory or marker; NVKR does not copy upstream test names or authenticate
supplied executable membership in a Run. Those review concerns remain open.

The [2026-10-14 native run](https://github.com/dsx-ai-factory/nvkr/actions/runs/38659640835)
and its [separate repository gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/38659644233)
are historical Python-adapter evidence, not validation of the Go replacement.
That candidate passed six fresh builds (scalar, native image map and permitted
registry relocation on native Linux AMD64/ARM64), each with 9 contracts and 532
CLI tests. Its raw records remain in the run artifacts and locally under
`.nvkr/validation-20261014/full-verification/`.

#156 and #172 have landed in the current main base. The upstream supplied-binary
interface still needs adoption before routine maintained use, followed by explicit
source/toolchain/build-input refresh and Plan regeneration. Production pins are
unchanged. Diagnostic success grants no conformance or qualification authority;
authenticated exact-output membership, exhaustive suite acceptance and review
approval remain prerequisites for stronger integration.
