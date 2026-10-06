# Multi-architecture CLI — local execution evidence

On 2026-10-03 UTC (2026-10-02 Pacific), `nkdctl run local` completed attempt
`nkd-cli-dual-20261003-02` with two independently observed executable outputs.
The implementation was the working tree on `codex/nkd-multiarch-cli`, based on `d1a4ec9`.
Lane: `unqualified-local-execution`. ARM64 containers ran natively on Docker Desktop's Linux ARM64 daemon;
AMD64 containers used explicit Docker platform emulation. This record does not establish native AMD64 hosted
execution, a signed eight-output deploy Release, an aggregate OCI image index, or qualification.

## Inputs and execution

The [exact Definition](2026-10-03-multiarch-cli-local/definition.yaml) selects `nkectl-linux-amd64` and
`nkectl-linux-arm64` from the existing CLI catalog. The [local Environment](2026-10-03-multiarch-cli-local/environment.yaml)
uses Docker, AMD64 utility tools and no publication/signing. Its absolute catalog path records this machine's
actual input; reproduce under `.nkd/evidence-cli/` with that catalog path adjusted for the new checkout.

Both builds selected CLI tag `cli/v0.1.0-rc.2`, commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`, tree
`309887e8cdb8ee24a39785b864390eda3afce5ec`. This is independent of the deploy Definition's umbrella `v0.21.0`
pin. Process-scoped Git `insteadOf` redirected the canonical source URL to an existing local clone; no NKE
checkout or source identity changed.

The builder image was `golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6`.
Its selected image manifests were `sha256:23fdfd3a6abc97c81e32a724cdd1cf541c06c416eb04d717815f4ed7c75623d0`
for AMD64 and `sha256:a0c0dd6888e2a1df54a00fcafe855e3035dcf9b5733cdf803d6ccf70a56df809` for ARM64.
Both Go builds used vendored modules, disabled CGO and `--network=none`. Source preparation rehashed each
checkout and private normalized snapshot before execution. Each SBOM found 27 dependency components.

```sh
GIT_CONFIG_COUNT=1 \
GIT_CONFIG_KEY_0=url./Users/nhensley/dev/code/github/NVIDIA/nke/.insteadOf \
GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke \
GOTOOLCHAIN=local go run ./cmd/nkdctl run local \
  --definition .nkd/evidence-cli/definition.yaml \
  --environment .nkd/evidence-cli/environment.yaml \
  --attempt nkd-cli-dual-20261003-02 --jobs 1
```

The [execution log](2026-10-03-multiarch-cli-local/run.log), [operation results](2026-10-03-multiarch-cli-local/run.json),
[completed Run](2026-10-03-multiarch-cli-local/lifecycle.json), terminal observations, source receipts and SBOMs
are retained together. Regenerating the [Plan](2026-10-03-multiarch-cli-local/plan.json) after implementation
produced the same [Plan identity](2026-10-03-multiarch-cli-local/plan-summary.log):
`sha256:54bca67bafa014f782f35c8fccdf98bfa4be8157009a4eb41c90b6597c3d1f6e`.

## Observed output identities

| Output | Build Digest | Executable bundle manifest |
|---|---|---|
| `nkectl-linux-amd64` | `sha256:7063593683d83bff157ae87d93f9c66a115361ed8b888c5ca973d87d5cf1b6b2` | `sha256:918ee3e3a7f3682277c9cf2487c0723a011ab7751428eb80abd1338ce0989a53` |
| `nkectl-linux-arm64` | `sha256:129039eab44ad65628dc4d107a946f880d5a837fa5835ccb963e43d6b5779a8c` | `sha256:123e7fefdadd61ed59599c1f0b7cbef5d1db9c96cbbbbe42af089e14ce31cc84` |

Each bundle's purpose, OS and architecture were independently inspected. A local diagnostic publication
root composed the two layouts only to exercise the delivery extractor; no registry publication occurred.
The [inspection program](2026-10-03-multiarch-cli-local/inspect.go.txt) and
[extraction results](2026-10-03-multiarch-cli-local/extraction.log) verify recovered bytes against both the
bundle config and original producer output.

| Platform | Recovered executable bytes | Size |
|---|---|---:|
| Linux AMD64 | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` | 9,248,894 |
| Linux ARM64 | `sha256:24aa1e1e667776097fae38b3d0d31c072b4ce5b0ddee45fbc21d94aadfd0763f` | 8,585,342 |

`file` identified the expected statically linked x86-64 and AArch64 ELF executables. Both extracted binaries
ran with Docker networking disabled under their selected platform and reported
`nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.
The [AMD64 result](2026-10-03-multiarch-cli-local/amd64-version.log) used emulation; the
[ARM64 result](2026-10-03-multiarch-cli-local/arm64-version.log) used the native daemon architecture.

## Native admission and validation

The opt-in real-runtime test passed native ARM64 daemon/image admission and rejected a requested AMD64
native route on that daemon with `NKD-E-ENGINE-CAPABILITY`. Its
[record](2026-10-03-multiarch-cli-local/runtime-admission.log) is distinct from the local Run, which intentionally
omits the GitHub-only native routing map. Protocol tests assert daemon rejection happens before image pull.
A second [bounded native admission run](2026-10-03-multiarch-cli-local/runtime-admission-recheck.log)
explicitly used `NKD_TEST_NATIVE_PLATFORM=linux/arm64` and
`NKD_TEST_NATIVE_IMAGE=golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6`.
The retained [Docker image inspection](2026-10-03-multiarch-cli-local/native-builder-inspection.json) reports
Linux ARM64 and includes that selected index digest in `RepoDigests`.

Integration review added host-side utility container admission before its generated runtime launch, and an
exported factory-owned effective GitHub execution projection for readable plans. Utility protocol contracts
cover foreign daemon/image rejection before node effects and exact command validation rejects omitted
preflight. The in-container helper still validates the complete Plan and its native platform; it intentionally
has no nested Docker access. The local Run evidence above remains unchanged and does not prove hosted utility
execution.

Contract tests also cover route admission before catalog I/O, helper rejection before output, exact helper
transport, finalizer utility routing, separate Build identities and transport-independent logical identity.

The maintained eight-output deploy bundle regenerates byte-for-byte from its Definition/Environment;
`actionlint` and relaxed `yamllint` passed. Final `make check` passed on macOS ARM64 with pinned Go 1.27.1:
format, vet, lint, shellcheck, race/coverage tests (71.2%) and vulnerability scan (none found).
The full uncached test suite passed, followed by an uncached check of the final utility-route regression.
`git diff --check` passed. The exact changed code and generated inputs are fingerprinted in the
[diagnostic implementation file record](2026-10-03-multiarch-cli-local/implementation-files.json).

The complete `go test -race ./... -count=1` suite passed on native Linux ARM64, followed by 20 successful
repetitions of the subprocess-cleanup test. The [Linux log](2026-10-03-multiarch-cli-local/linux-race.log)
records Go 1.27.1, Linux aarch64 and UID 1000. The test container used `--init`, a source copy excluding the
macOS worktree's `.git` pointer, and [this Dockerfile](2026-10-03-multiarch-cli-local/linux-test.Dockerfile)
based on `golang@sha256:9fef6c185fa8277ea98a09c2067351103b002cce3a421771dceb241c25a22529`, with jq installed.
Its local image config was `sha256:2c63d4002cc398cac5ae1ca4c3d470841eff83d94460239720c685ef7335a05c`.
Initial test-container attempts failed on the inaccessible Git pointer, missing jq, root UID and process
reaping; correcting that environment required no production code changes or weakened assertions.
Full [host gate](2026-10-03-multiarch-cli-local/make-check.log),
[uncached tests](2026-10-03-multiarch-cli-local/uncached-tests.log) and
[byte-exact generation check](2026-10-03-multiarch-cli-local/workflows-check.log) logs are retained.

After the integration corrections, the fresh [full host gate](2026-10-03-multiarch-cli-local/integration-make-check.log)
passed with 71.3% coverage and no vulnerabilities, and the
[complete uncached suite](2026-10-03-multiarch-cli-local/integration-uncached.log) passed. Native Linux ARM64
[factory/pipeline race tests](2026-10-03-multiarch-cli-local/integration-linux-race.log) passed, as did
[byte-exact regeneration](2026-10-03-multiarch-cli-local/integration-workflows-check.log), `actionlint` and
`git diff --check`. Relaxed `yamllint` exited successfully with generated-script line-length warnings.
The maintained deploy Plan digest remains
`sha256:75096a3da7e54c373e1fa1215d2fb0de11896b9e6c8260e151f57599adf778b7`.
The implementation file record below reflects the final integration corrections.

The retained files are local diagnostic records, not authenticated hosted provenance. The generated workflow
still needs an authorized real GitHub execution on both generic hosted architectures, followed by complete
publication, signing, verification and named-output finalization to establish that separate acceptance claim.
