# Code locality: local NKE build

Date: 2026-09-18. The working tree based on `16b3775` built the three maintained NKE outputs through the
shared producer dispatcher and one local dependency scheduler. The Run completed in 272 seconds; pull-back
took 4 seconds. Signing was dry-run only. This record accompanies the
[locality implementation](../codebase-locality-implementation.md); it does not establish hosted acceptance.

## Invocation and inputs

```sh
NKDCTL_BIN=/tmp/nkd-locality \
  NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke \
  bash scripts/demo-m1-local.sh \
    --attempt locality-20260918 \
    --consume .nkd/locality-consume
```

The CLI was built with Go 1.27 on macOS/arm64. Its exact binary SHA-256 was
`f4477b47cbdeebbc9b5572bb8876050d3b1a174f02e88bdc66a386dcc501b711`. Docker ran pinned Linux build
containers and the demo-owned loopback registry. Process-scoped Git redirection supplied the existing NKE
clone. Source/tool pins, producer commands and the NKE checkout were unchanged. The committed catalog's
invalid `linux/amd64f` target was corrected to `linux/amd64` before admission.

Definition v4 directly selected `cwe`, `nke-helm` and `nkectl` on `linux/amd64`. The complete compiler emitted
Plan v4 from the original Definition and Environment. Producer scheduler entries referenced their embedded
IR instructions; they did not carry another executable projection. CLI and controller source pins both select
commit `ed9fb09b04fd40bf331d25df2222206ffaca593e`, tree `309887e8cdb8ee24a39785b864390eda3afce5ec`.

## Recorded results

| Subject | Identity |
|---|---|
| Definition | `sha256:2f1d2c4a7083bdc0933865dd5ecf0201cff7769476737fd849843a804354d1a3` |
| Local Environment | `sha256:7cf1d657a181bed0982d3432b8582509cf363208228e6a78146cb70a36acf6f9` |
| Local Plan | `sha256:1024f7a80a53cb711a4f975d491f2508809ff2fc8a4e2cb744d5ad230fe363f3` |
| CWE build | `sha256:0236efca6da8aeb423d090cd6372f3c57dda8c08ca1f4d527db7892998285ae5` |
| Helm build | `sha256:aa9caeff69d946126f410d25627a87cf2fd10bfe7512e83a8d89567558082dc1` |
| CLI build | `sha256:d49b059a2d763ccecf1ee40617f53293a596f8c4d338ad801112ffceaea51ecf` |
| Release content subject | `sha256:def5b4bb388c2a44cc4e26b0625cd69a23521c4c439439b1879ef141a0e52bf5` |
| Published release root | `sha256:cdd31aea94ae5a698731a733dbbda6f9f9a345e126e65c548902fd8aeafd062a` |
| Recovered CLI bytes | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` |
| Production predicate bytes | `sha256:ac8680d9a0c57ed8a72c76aa12cbad7c188f23b672c3178275152ef6c6dd8e96` |

The Run at `.nkd/m1-local/locality-20260918/lifecycle.json` uses `nkd.dev/run/v3`, reports `complete`, and
contains three required outputs with three matching observations under `unqualified-local-execution`. Its exact
file SHA-256 is `9d731afebf23240d8767cfba28e59d12dab64bd61caedb7ada010e725591c230`.

The run built the CWE image and source Helm package, bound Helm to the observed image, built the CLI, generated
dependency SBOMs, and published the diagnostic closure. Pull-back checked the seven-object closure and
extracted the CLI. The recovered CLI ran under the pinned amd64 image and reported
`nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`. Only the CLI was executed after extraction.

The final CLI rebuild has exact SHA-256
`5951ae94fe93c1f435e0e87fd2f11c15708b17c807421c2fd12a68b5d0b28762`. It includes subsequent shared
producer-result validation and carrier-path normalization refinements. Those refinements do not change the
authored inputs or generated Plan bytes. Final-source re-observation of the recorded output bytes is tracked
below separately from the original build.

The final-source audit loaded the saved Plan and Run, recomputed their input identities, and checked all 20
completed operations against frozen requirements. `pipeline.ObserveResult` checked the three actual producer
records and payload bytes; `factory.ObserveLifecyclePayload` reconstructed output packages in temporary copies
and matched all three recorded observations. `delivery.ContentDigest` matched the recorded content subject.
`delivery.OpenPublication` held and checked both the produced root and pulled-back root, including their exact
Definition/Environment identities and output projections. Both roots rejected attested admission with
`NKD-E-PROVENANCE-MISSING` because their SBOM evidence was unattested. Fingerprints confirmed all 61 inspected
original files were unchanged. The temporary harness and log are `/tmp/nkd-locality-reobserve.go` and
`/tmp/nkd-locality-reobserve.log`; invocation was:

```sh
GOTOOLCHAIN=local GOCACHE=/tmp/nkd-recipe-assessment.3hmxOm/cache \
  go run /tmp/nkd-locality-reobserve.go > /tmp/nkd-locality-reobserve.log 2>&1
```

This is a check of recorded bytes and package reconstruction, not independent build reproduction.

## Repository verification

Full uncached `go test ./... -count=1` and `make check` passed on macOS/arm64 with Go 1.27. The latter used
`GOFLAGS=-count=1` and passed formatting, vet, golangci-lint 2.13.1 (zero issues), ShellCheck and the full
uncached race/coverage suite (71.0% statements). `git diff --check` passed; local file links in changed/new
Markdown files resolve. Logs are `/tmp/nkd-locality-unit-verified.log` and
`/tmp/nkd-locality-make-check-verified.log`. Tests used writable temporary Go/lint caches and local-listener
access. Initial integration runs exposed incomplete synthetic producer records and a cancellation fixture that
did not reliably put the failing operation in the first batch. Fixtures were corrected before both successful
full reruns; production checks were retained.

Linux/arm64 race tests passed from the final source in a network-disabled `golang:1.27` container with init and
read-only source/module mounts. Packages: sign (6.124s), pipeline (5.171s), factory (31.080s), delivery (3.461s),
and internal transport archive (1.400s); shared secure-path primitives compiled with their callers. The log is
`/tmp/nkd-locality-linux-complete.log`.

The final CLI passed both maintained hosted-generation checks. NKE regenerated byte-exactly with Plan
`sha256:57679a8afe274367c43f721b8f5b3d1fcaa92f26f31ed9148918c989e91a54ad`; the AICR generation demo produced
and checked Plan `sha256:b5fae3f5a57f37388e864fa2cd5239927176697d962cb4a008b80a3757ae7c24`. Actionlint passed
for the generated NKE workflow. Generation is static evidence; neither hosted provider nor the AICR generator
ran in this validation.

## Scope

Signing and verification records contain `dryRun: true`; `signatureVerified` and `attestationVerified` are
false. No cosign signing, OIDC exchange, transparency service, hosted execution, provisioning, or authenticated
Release consumption ran. These results exercise local builds, packaging, evidence generation, publication and
diagnostic pull-back. They do not establish qualified reproduction, signed hosted acceptance or M3/M4 completion.
The local scheduler runs bounded ready batches; no throughput improvement is claimed. Run directories and
registry contents remain local working artifacts. No repository commit, push or remote publication was performed.
