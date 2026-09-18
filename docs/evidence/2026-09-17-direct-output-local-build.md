# Direct output producers: local NKE build

Date: 2026-09-17. The Recipe-removal working tree based on
`c93a90289825b550c095e1afa4cd27e20d881ccb` built all three maintained NKE outputs, generated dependency SBOMs,
published them to the loopback OCI registry, and ran the recovered CLI. The Run completed in 359 seconds;
pull-back took 4 seconds. This is local execution evidence, with dry-run signing only.

## Invocation and inputs

```sh
GOTOOLCHAIN=local GOCACHE=/tmp/nkd-recipe-assessment.3hmxOm/cache \
  NKDCTL_BIN=/tmp/nkd-removal \
  NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke \
  bash scripts/demo-m1-local.sh \
    --attempt recipe-removal-20260917 \
    --consume .nkd/recipe-removal-consume
```

The CLI was built from the migrated working tree with Go 1.27 on macOS/arm64. Its exact binary SHA-256 was
`87ccfeb14e8123891064dfd9ddd3b1ef8526490cfd05a547f8bbb0ec783a36e0`. Docker executed the pinned Linux build
containers and reused the demo-owned loopback registry. Process-scoped Git redirection supplied the existing
NKE clone without changing the checkout, canonical catalog source URLs, or source/tool lockfiles.

The source declarations remain `https://github.com/dsx-ai-factory/nke`. CLI tag `cli/v0.1.0-rc.2` and controller
tag `controller/v0.1.1-rc.1` both pin commit `ed9fb09b04fd40bf331d25df2222206ffaca593e` and tree
`309887e8cdb8ee24a39785b864390eda3afce5ec`. Definition v3 selects `cwe`, `nke-helm`, and `nkectl` directly,
each on `linux/amd64`. Catalog v2 contains producer instructions, explicit supported platforms, and source/tool
pins; no authored Recipe or criteria selection participated.

The first invocation stopped before attempt allocation because generation had left obsolete graph files from
the former model. The generator now retires unchanged files owned by the previous manifest and refuses modified
or unlisted files. Regeneration and its byte-exact check passed before the successful build began.

## Recorded results

| Subject | Identity |
|---|---|
| Definition | `sha256:748b9ceddb1ef6901dd738bfaed345d31e437da4abca88b76efd0426f93a67ae` |
| Local Environment | `sha256:7cf1d657a181bed0982d3432b8582509cf363208228e6a78146cb70a36acf6f9` |
| Local Plan | `sha256:ad655930aac1df79ff3879e26d38fe117c2a5d343d3bb5463b9173f69c478fec` |
| CWE build | `sha256:d77c73b60bbaf9f15ac06965f450232ddfafbeb623c8f901db1ce05d3476cd5a` |
| Helm build | `sha256:b19dd2d01c63256da8aaa0790723f11af1bab7746b2492fd913bdb0bd3be9c59` |
| CLI build | `sha256:dbcffe9254707629e8aaf11b5c72433d248030b2f198e7712b093bf5c405b3f9` |
| CWE image manifest | `sha256:5ec35be77bd9a88aa846fe99a669f6585c69c7801d57aa512230af4080eef688` |
| Release content subject | `sha256:186581804a4b755823d38e216ec1673eb085c62f308de7ea6ecc55d28219ff88` |
| Published release root | `sha256:8d8a2b8cab0ec5c4f015335ef56a8b537476bbc57ed9815c91504e5c98edf64e` |
| Recovered CLI bytes | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` |
| Production predicate bytes | `sha256:8e643fe938e6aedf838da0d8687b726fe9a3831adf18f8c9f87fee504d01e8aa` |

The local Run record at `.nkd/m1-local/recipe-removal-20260917/lifecycle.json` has schema `nkd.dev/run/v2`,
`status: complete`, and lane `unqualified-local-execution`. It retains three expected outputs and three matching
observations, each bound to its output name, selected components, and generated build digest. Its exact file
SHA-256 is `7fbf1f510a5ae13886a2ce59a9145206bb7bcce330fd0c22640e1c112f3b995b`.

The Helm package references the observed CWE image digest and has refreshed checksums. Offline dependency
inspection recorded 248 components for CWE, 7 for Helm, and 27 for the CLI. Each output has one artifact SBOM
and an `nkd.dev/sbom-index/v2` index bound to its generated build digest.

The published reference is
`127.0.0.1:5000/nke/releases@sha256:8d8a2b8cab0ec5c4f015335ef56a8b537476bbc57ed9815c91504e5c98edf64e`.
Pull-back verified the closure and extracted the CLI. Only the CLI was executed after extraction; it reported
`nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)` under the pinned amd64 image.

Final audit changes rejected minting identities in the retired Recipe family and strengthened AICR profile
joins. A subsequent CLI build recomputed the same NKE Definition, Environment, and local Plan identities and
passed byte-exact NKE hosted generation. Those audit changes did not alter the recorded NKE build graphs.

## Repository verification

The following checks passed on the migrated working tree:

```sh
GOTOOLCHAIN=local go test ./... -count=1
PATH=/Users/nhensley/go/bin:$PATH GOTOOLCHAIN=local make check
git diff --check

docker run --rm --init --network none \
  -v /Users/nhensley/.codex/worktrees/1762/nke-recipes:/src:ro \
  -v /Users/nhensley/go/pkg/mod:/go/pkg/mod:ro \
  -w /src -e GOTOOLCHAIN=local golang:1.27 \
  go test -race ./pkg/nkd/sign ./pkg/nkd/pipeline -count=1
```

Native commands used writable temporary Go/lint caches; tests requiring local listeners ran with that access.
`make check` passed formatting, vet, golangci-lint 2.13.1 built with Go 1.27, ShellCheck, and the full race/coverage
suite (70.0% statements). Linux sign and pipeline race suites passed in 6.594 and 5.298 seconds respectively.
Final comment/error-wording and fixture-description cleanup received focused tests after the full gate; no
runtime or schema behavior changed in that cleanup.

The rebuilt CLI passed NKE hosted generation `--check` with Plan
`sha256:bc5403023d7d7d2eba4319a02b6f3f45992e63e0244c8a61bc54a447bf11d479`. The maintained AICR GitHub generation
demo generated and byte-checked Plan `sha256:a7ca043aa4d819fe5141b76a6f3b7898c49bf4bd311ed4573f45ad5d60d9ac8f`.
Both are static generation checks; no provider ran. The catalog-update demo exercised preview, exact lock writes,
changed build identity, and malformed-pin rejection in an isolated fixture. The tamper demo passed changed-byte,
qualified-boundary, overwrite, corrupt-carrier, and escaping-symlink negatives. `make run-resolve` succeeded with
explicit `kubectl` and `linux/amd64` selection.

## Scope

Signing and verification records say `dryRun: true`; `signatureVerified` and `attestationVerified` are false.
No cosign signing, OIDC exchange, transparency service, hosted provider, GPU execution, provisioning, or
authenticated Release consumption ran. These results establish local producer execution and publication
mechanics, not qualified reproduction, signed hosted acceptance, or M3/M4 completion. Run paths and loopback
registry contents are local working artifacts; this document records the inspected results and identities.
