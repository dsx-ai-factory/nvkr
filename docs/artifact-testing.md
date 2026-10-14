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

On 2026-10-14, a fresh candidate recipe build and two independent cold-cache
adapter runs used NVKE `be36d63c23cbfd609dc54a9f036110b7f2afa1b0` (tree
`1a0e69257152ed766424c37c3ce1a539a2f0aa59`). Both ran natively in Docker's Linux
ARM64 VM on a macOS ARM64 host: nine process contracts and 530 top-level CLI
tests across 30 packages passed, with no failures or skips. The nine contracts
listed in the imported source were independently matched against both logs.
Each run shallow-imported fresh source and began with empty dependency/build
caches; the executable was newly built through NVKR, not taken from an earlier Run.
Its SHA-256 before and after was
`a6af3fc46cb5f30ba1fe1e6a46f768fbcea7ec43433abcb2d523fc6d816fdb35`.

Real Docker execution accepted source/artifact mounts containing spaces, colons,
commas and quotes. Inspection and write/network probes confirmed read-only
artifact/source/root filesystem, offline execution and acquisition without the
artifact. Bounded timeout, SIGINT and SIGTERM experiments removed the owned
containers and default workspaces. The wrong-version executable failed with exit 2. The broken executable emitted
contract failures, then hit a 45-second adapter bound because an upstream signal
test blocks opening a FIFO when the executable exits before opening it. Neither
negative produced a success record; the bounded adapter failure removed its container. A fresh import of maintained NVKE `ed9fb09b` through
local Git transport verified its exact tree and rejected its missing entrypoint.
The initial remote fetch for that negative reached its external time bound.

Raw build, suite and negative logs, canonical Plan, candidate inputs, Docker
inspection, inventories and before/after digests are retained locally in
`.nvkr/validation-20261014/`; these are local observations, not immutable CI artifacts.
The candidate selected output `nvkectl`, platform `linux/arm64`, expected version
`be36d63c23cbfd609dc54a9f036110b7f2afa1b0`, and image
`golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c`.

The checker still accepts an ordinary-test-only completed result in the expected
package. A filtered real upstream `TestReportFailure` run with a broken supplied
executable demonstrates that limitation; it is not acceptance of either official
suite. Exhaustive contract identity needs an upstream-owned interface before
stronger suite acceptance can be claimed. The diagnostic does not copy a list
of NVKE contract names into its production checker.

Go compiler/loader fixtures cover AMD64 and ARM64 metadata, scalar/map selection,
precedence/fallback, permitted relocation and unsupported producers. Native
AMD64 runtime and map-specific/relocated runner execution remain unverified.
Renamed `NVKECTL_VERSION` stamps are rejected by the current IR allowlist;
candidate inputs use admitted legacy stamps and translate them in the build command.

[NVKE #1578](https://github.com/dsx-ai-factory/nvke/pull/1578) remained open at the
validated revision. The maintained production CLI pin `ed9fb09b` lacks
`test-artifact`; upstream adoption and an explicit pin/toolchain/linker/vendoring
refresh with Plan regeneration remain prerequisites. Production pins were unchanged.
Before evidence-bearing integration, source/execution ownership must move into
existing Go owners and named-output membership must be authenticated from its Run.
Future CI wiring must be owned by NVKR's workflow generator.
