# CLI artifact testing

Run NVKE-owned tests against an NVKR-built `nvkectl`, using the exact upstream
revision recorded in its build Plan. This is a trusted-input diagnostic; it does
not authenticate artifact membership in a Run or grant release qualification.

## Run locally

Keep the artifact and canonical Plan from the same NVKR build:

```sh
GOTOOLCHAIN=local go build -o bin/nvkr ./cmd/nvkr
python3 scripts/test-artifacts.py \
  --artifact /path/to/nvkectl \
  --plan /path/to/matching/plan.json \
  --suite contract --keep
```

Requires the repository-pinned Go version to build NVKR, Python 3.12+, Git, and
Docker. Use `--nvkr /path/to/nvkr` to supply an already-built helper. The default
helper is `bin/nvkr` in this checkout; no local NVKE clone is required.

Select `--output NAME` when a Plan contains multiple standalone CLI outputs.
Linux AMD64 and ARM64 are supported; foreign-architecture execution needs
emulation and may affect timing and signal tests. The selected toolchain image
must provide Go and Make; Plan validation does not prove those tools are present.

- `--suite contract` runs the upstream process contracts against the supplied binary.
- `--suite cli` runs the full upstream CLI Go suite. Its contracts use the binary;
  other unit tests exercise upstream source. Bootstrap E2E is separate.

## How it works

1. Ask `nvkr run artifact-test-inputs` to validate the complete canonical Plan and
   select one CLI producer. Go owns platform-specific toolchain selection and
   authorized registry relocation. Python does not reconstruct those rules.
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
Each acquisition/execution command has a 30-minute timeout. Cancellation stops
and joins the client and requests container removal; removal is best effort.

Global/system Git configuration is disabled. Private source access can use a
process-scoped `GIT_ASKPASS` helper or an SSH agent for an SSH source locator.
Test containers receive neither helper nor Git credentials.

## Validation and remaining work

Local ARM64 validation on 2026-10-13 reused the recipe-built artifact from
[NVKE 6768541b](https://github.com/dsx-ai-factory/nvke/commit/6768541bef826f60bd7b151937046c2dc4882293).
The updated adapter passed six contracts and 460 top-level CLI tests across
27 packages, with no failures/skips and unchanged staged bytes. This was an
artifact reuse, not a new build or production-pin refresh. Its SHA-256 remained
`33776b39a264ae92b59097b72ce9f15ba3e6c5c789d9c7e844ca58ac9db35b19`.
Local logs/results are retained under `.nvkr/cli-artifact-proof/go-plan-*.log`
and the printed workspaces; they are not published immutable CI evidence.

Go compiler/loader fixtures cover both architectures, scalar/map toolchains,
map precedence, allowed relocation and rejection of altered transport. Adapter
regressions cover source import, mounts, offline execution, results, staging and
cancellation. Map-specific and relocated images have not been executed as runners.

The maintained production CLI pin lacks `test-artifact`. Upstream adoption,
pin/toolchain/linker/vendoring refresh and Plan regeneration remain prerequisites.
Before evidence-bearing integration, source/execution ownership must move into
existing Go owners and the exact named-output digest must be authenticated from
its Run. Source import and test execution remain Python diagnostics here.

Merge-triggered CI, CLI/controller/chart deployment in kind, CAPD lifecycle and
CAPNICo lanes remain later milestones. This change adds no production pins,
generated workflows, deployment or conformance evidence.
