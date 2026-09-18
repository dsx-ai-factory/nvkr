# Output-only model: local NKE build

Date: 2026-09-17. The working tree based on `c93a90289825b550c095e1afa4cd27e20d881ccb` built all three
maintained NKE outputs after removing cluster views, deployment roles/categories, and staging/composition
records. The Run completed in 337 seconds; pull-back took 4 seconds. This is local execution evidence with
dry-run signing only. The preceding Recipe-removal run remains separately recorded at its original scope.

## Invocation and inputs

```sh
GOTOOLCHAIN=local GOCACHE=/tmp/nkd-recipe-assessment.3hmxOm/cache \
  NKDCTL_BIN=/tmp/nkd-output-only \
  NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke \
  bash scripts/demo-m1-local.sh \
    --attempt output-only-20260917 \
    --consume .nkd/output-only-consume
```

The CLI was built with Go 1.27 on macOS/arm64. Its exact binary SHA-256 was
`dc06e8cb0dadeb77336375bd1e85d7049625ae85762ea16acfe48b81115be42b`. Docker ran the pinned Linux build
containers and reused the demo-owned loopback registry. Process-scoped Git redirection supplied the existing
NKE clone. The canonical source declarations, source/tool lockfiles, and NKE checkout were unchanged.

The catalog still declares `https://github.com/dsx-ai-factory/nke`. CLI tag `cli/v0.1.0-rc.2` and controller tag
`controller/v0.1.1-rc.1` pin commit `ed9fb09b04fd40bf331d25df2222206ffaca593e` and tree
`309887e8cdb8ee24a39785b864390eda3afce5ec`. Definition v4 selects `cwe`, `nke-helm`, and `nkectl` directly on
`linux/amd64`; catalog v3 describes their pinned producers. No Recipe, cluster, category, or deployment-role
selection participated.

## Recorded results

| Subject | Identity |
|---|---|
| Definition | `sha256:2f1d2c4a7083bdc0933865dd5ecf0201cff7769476737fd849843a804354d1a3` |
| Local Environment | `sha256:7cf1d657a181bed0982d3432b8582509cf363208228e6a78146cb70a36acf6f9` |
| Local Plan | `sha256:0b661c6d7747f3c8590845bfcfc7a538c79799e0f4d4aafc5ff38677bbbae768` |
| CWE build | `sha256:0236efca6da8aeb423d090cd6372f3c57dda8c08ca1f4d527db7892998285ae5` |
| Helm build | `sha256:aa9caeff69d946126f410d25627a87cf2fd10bfe7512e83a8d89567558082dc1` |
| CLI build | `sha256:d49b059a2d763ccecf1ee40617f53293a596f8c4d338ad801112ffceaea51ecf` |
| CWE image manifest | `sha256:8f57b8f65705c4c26f92d3aaca5d9a744a0f391ce472eb15b9b29dab935e2fe8` |
| Release content subject | `sha256:d9929b03073723f3242ef1ec5911a611514eef4107d0d68425fc808c7381b7eb` |
| Published release root | `sha256:ea8bc6bd47c1b1a86ed1b2135cab68383cfe6f1ad3c3dee734648ff0101148f9` |
| Recovered CLI bytes | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` |
| Production predicate bytes | `sha256:219adfa3a9241fe0738586dccdffad7bf88ec389809c584d25701e855c84934e` |

The Run record at `.nkd/m1-local/output-only-20260917/lifecycle.json` uses `nkd.dev/run/v3`, has
`status: complete`, and retains three exact required outputs and three matching observations under
`unqualified-local-execution`. Its exact file SHA-256 is
`1f5e5722fcc3be03f37c6e71fd4f8a8917149a5e64291461daeceb2f9ffbe266`.

The Helm package binds the observed CWE image and refreshes checksums. Dependency inspection recorded 248
components for CWE, 7 for Helm, and 27 for the CLI. Every output retains its own artifact SBOM and build-bound
`nkd.dev/sbom-index/v2` index. Removing deployment metadata did not remove output or evidence membership.

The published reference is
`127.0.0.1:5000/nke/releases@sha256:ea8bc6bd47c1b1a86ed1b2135cab68383cfe6f1ad3c3dee734648ff0101148f9`.
Pull-back checked the seven-object diagnostic publication closure and extracted the CLI. Only the CLI was
executed after extraction. It reported `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`
under the pinned amd64 image.

The final CLI rebuild, after prepared-run cleanup and the AICR test-fixture correction, has exact SHA-256
`31754f291629b6d9b1d4ffdce936d976bf1e829df33c4c4ea06451a50f40a2a3`. It recomputed the same NKE Definition,
Environment and local Plan identities. The cleanup removed unused prepared descriptions and copied per-output
AICR settings; it did not change the recorded NKE build inputs.

## Repository verification

Full uncached `go test ./... -count=1` and `make check` passed on macOS/arm64 with Go 1.27. The latter passed
formatting, vet, golangci-lint 2.13.1, ShellCheck, and the full race/coverage suite (70.1% statements).
`git diff --check` passed. Commands used writable temporary Go/lint caches and local-listener access for
integration tests. The initial run found one stale AICR emitter fixture with an empty effective deployer. The
fixture now uses the owner's normalized default; production validation was retained, and both full gates reran
successfully. Logs are `/tmp/nkd-output-only-all-tests.log` and `/tmp/nkd-output-only-make-check.log`.

Linux/arm64 `go test -race ./pkg/nkd/sign ./pkg/nkd/pipeline -count=1` passed in a network-disabled
`golang:1.27` container with read-only source and module mounts. Signing passed in 6.826 seconds and pipeline
in 5.194 seconds. The image identity was
`sha256:512690a5660563b57d37ecc31129e7f136e831db2aed24a1dbeb8ad7380dc0fa`.

Both maintained hosted-generation checks passed using the final CLI. NKE regenerated byte-exactly with Plan
`sha256:a0f6bfe6e8451796b79e49ebc5cc7a921c7875c0c70089259b9766a3219b7f52`; the AICR generation demo produced
and checked Plan `sha256:1fcf92414e363d884d7402133eb910446c6f2c247967eac6f22ecd75080d7d24`. The isolated
catalog-update and tamper-detection demos also passed. Generation is static evidence; no hosted provider ran.

Tests cover strict rejection of removed fields and old schemas; independent missing/swapped output and evidence
failures; retained signer authorization; and per-output AICR settings, default equivalence, identity sensitivity,
tamper rejection and renderer argument mapping. The AICR generator itself was not exercised by this NKE run.

## Scope

Signing and verification records contain `dryRun: true`; `signatureVerified` and `attestationVerified` are
false. No cosign signing, OIDC exchange, transparency service, hosted execution, provisioning, or authenticated
Release consumption ran. These results establish local build, packaging, evidence-production and publication
mechanics. They do not establish qualified reproduction, signed hosted acceptance, or M3/M4 completion.
Run directories and loopback registry contents are local working artifacts. No repository commit, push, or
remote publication was performed.
