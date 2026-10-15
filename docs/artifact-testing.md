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

[Native verification on 2026-10-14](https://github.com/dsx-ai-factory/nvkr/actions/runs/38659640835)
passed six fresh recipe builds: scalar, per-architecture map and permitted registry
relocation on native Linux AMD64 and ARM64 hosted runners. NVKR candidate
`d4aefd3757096c2d276ffbfe0117e417cc06f77b` contains the helper from #156, adapter
from #138 and the separately reviewed [vendoring relocation fix #172](https://github.com/dsx-ai-factory/nvkr/pull/172).
Both PR heads remain separate from this diagnostic branch. No emulation was used.

Exact NVKE candidate `3da7d64940ffe94722a5a913ab5dc26b28430d72`, tree
`86d6b4cc3a9a021cc6d38aa445ed90f04da2b28b`, combines the supplied-binary interface
[#1578](https://github.com/dsx-ai-factory/nvke/pull/1578) with the independent
[FIFO startup fix #1637](https://github.com/dsx-ai-factory/nvke/pull/1637).
Each build selects output `nvkectl` and version equal to that exact commit.

Every newly built executable passed **9 process contracts** and **532 top-level CLI
tests across 30 packages**, with zero failures/skips. Each suite separately imported
fresh source and began with empty dependency and build caches. Its inventory was
independently reconciled against all nine process contracts declared in the imported
source. Process contracts execute the supplied binary; other CLI unit tests exercise
imported source. Neither a retained binary nor warm test caches supplied this proof.

| Native platform | Artifact SHA-256 before and after all suites |
| --- | --- |
| Linux AMD64 | `0eea30f2eb1b0291ff6d8314205df06aad42c9e960398e67279b2131fa33ee85` |
| Linux ARM64 | `df3de95b9df1ac89d6bca75eae7140a295cb0be51f99ed14531cefc784ca727c` |

The scalar image is
`golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c`.
Maps select its native child manifests: AMD64
`sha256:2d658507a011f1eadc0114d7a421b312b2e77d9042a24036a26e267cc9231677`, ARM64
`sha256:a83e961ad968e2e7abc2a59554e356f00c2a270a15fbb39f2c84a4caf13be9c3`.
The relocated case copies that native manifest without changing its digest into a
disposable loopback registry, then selects `localhost:5000/nvkr-toolchain-cli@DIGEST`.
All three independently built variants produced the same bytes per architecture.
This is observed byte reproduction, without qualification.

Real Docker accepted space/colon/comma/quote source and staged-artifact mounts.
Inspection and write/network probes confirmed read-only source/artifact/root filesystem,
dropped capabilities, no new privileges, offline execution and acquisition without the
artifact. On both architectures, timeout/SIGINT/SIGTERM probes returned 1/130/143 and
removed the owned container and default workspace. A deliberately wrong-version
**recipe-built** executable and an `exit 7` executable both failed with exit 2;
fresh remote import of maintained revision `ed9fb09b` failed on its missing entrypoint
with exit 1. None produced a success result. The fixed FIFO contract failed immediately
for the broken executable instead of hanging; other signal negatives retain 30-second
waits. These bounded negatives use separate test workspaces.

The checker still accepts a completed ordinary-test-only result in the expected
package. Both native runtimes reproduced that behavior with filtered upstream
`TestReportFailure` and a broken supplied executable, reusing verified test caches.
This is a diagnostic limitation, not acceptance of either official suite. Stronger
exhaustive acceptance requires an upstream-owned inventory or marker; the production
checker does not copy NVKE test names. The official inventories above were checked
independently for this exact revision.

Run artifacts retain all six canonical Plans and hashes, exact authored candidate
inputs, imported trees, test inventories, raw results and Docker inspections.
A reconciled local copy is in `.nvkr/validation-20261014/full-verification/`.
The [separate repository Merge Gate](https://github.com/dsx-ai-factory/nvkr/actions/runs/38659644233)
passed on the same candidate, independently of runtime verification. Local candidate
`make check`, `make build`, focused uncached regressions, workflow lint and whitespace
checks also passed. Local gates alone do not establish CI readiness or review approval.

The earlier documented macOS ARM64/Docker path also passed a fresh recipe build
against unpatched interface candidate `be36d63c`: 9 contracts and 530 CLI tests / 30
packages, with empty caches and unchanged artifact
`a6af3fc46cb5f30ba1fe1e6a46f768fbcea7ec43433abcb2d523fc6d816fdb35`.
That was native Linux ARM64 in Docker's VM; Darwin execution and emulation were not
claimed. Its raw records remain under `.nvkr/validation-20261014/`.

Go compiler/loader fixtures cover both architectures, scalar/map precedence and
fallback, permitted relocation, unsupported producers and complete Plan admission.
Renamed `NVKECTL_VERSION` stamps are still rejected by the IR allowlist; candidate
inputs use legacy stamps and translate them in the build command.

All upstream changes above remain separate PRs. Routine maintained use needs explicit
source/toolchain/build-input refresh and Plan regeneration after upstream adoption;
production pins were unchanged. Existing operator-toolchain vendoring needs #172's
fix: the original recipe failed by pulling the bare image rather than its declared
mirror. Current #172 main-based macOS full gate encounters unrelated Linux-only
method lint findings; its Linux Merge Gate passed. Source/execution Go ownership,
authenticated named-output membership and review approval remain prerequisites for
stronger product/evidence integration. Diagnostic runtime success does not clear
those outstanding review concerns.
