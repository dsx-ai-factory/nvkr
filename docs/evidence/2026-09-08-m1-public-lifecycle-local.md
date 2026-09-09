# Current public lifecycle: local NKE demonstration

- Date: 2026-09-08.
- Scope: current uncommitted checkout, Definition/Environment inputs and immutable Run attempt.
- Result: local NKE build → SBOM → OCI publication → dry-run sign/verify → pull by digest → recovered binary execution passed.
- Evidence level: `static-unqualified` plan; Run lane `unqualified-local-execution`. No cosign, OIDC or transparency service ran.

## Invocation and environment

```sh
NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke \
  scripts/demo-m1-local.sh --attempt consolidation-20260908-2
```

The existing local NKE clone supplied source through process-scoped Git transport redirection. Catalog identity
remained `https://github.com/NVIDIA/nke`, tag `cli/v0.1.0-rc.2`, commit
`ed9fb09b04fd40bf331d25df2222206ffaca593e`, tree `309887e8cdb8ee24a39785b864390eda3afce5ec`.
Docker reused the demo-owned loopback registry and cached pinned images. The CLI build used the pinned
linux/amd64 toolchain with build networking disabled. The measured Run took 39 seconds.

An earlier invocation stopped at generated-workflow drift because the demo used the dirty, version-stamped
`make build` binary to check a bundle generated with `go run`. The script now checks with that same stable
development generator invocation. It did not overwrite generated files to hide the mismatch.

## Recorded identities

| Subject | Value |
|---|---|
| Attempt | `consolidation-20260908-2` |
| Definition | `sha256:e17bdea9d8953bffe64da71a90ebc94f1628f512bc012eee6280eaac62bc2d8b` |
| Environment | `sha256:01029d4ba91fe544b47df609c91c92c131cfd48961337f47a6695718ab573569` |
| Resolved Recipe | `sha256:439746ba5095230598792c43bcc7523946a1d2e31e4cf3c72525f72bd10a8861` |
| Plan | `sha256:33a849d7244d902c690c34939e4b1cb600f96cdc4011fbdb173721ac1fce21ec` |
| Named executable package | `sha256:99cfd9c354a2c8719053dd1b69ce9917ed4b05d21fbe0478122d2b05d94ee969` |
| Release-content subject | `sha256:3f820f8adb8104f32906fe64af6aa645cecfa13c5ce904b462fca6ffad355d42` |
| Published OCI root | `sha256:c115be7e4be62aa43e43fee38917586c19e7a524711e4d6902d1f7eb6fe63562` |
| Recovered `nkectl` bytes / SBOM subject | `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d` |
| Production predicate bytes | `sha256:3c14c82b2aef1e4508007f4050e994f47a4b108ff8a51affe01f16fa3946da30` |

Published reference:
`127.0.0.1:5000/nke/nkectl@sha256:c115be7e4be62aa43e43fee38917586c19e7a524711e4d6902d1f7eb6fe63562`.

The attempt's `.nkd/m1-local/consolidation-20260908-2/lifecycle.json` records `status: complete`, retains the
expected `nkectl` output/component/Recipe binding, and names the observed executable package. Its SBOM directory
contains one CycloneDX component document and an aggregate index. Signing and verification records both bind the
published root and say `dryRun: true`; `signatureVerified` and `attestationVerified` are both false.

Pull-back materialized the OCI closure, extracted `nkectl`, and checked its bytes against the SBOM subject. The
recovered binary reported `nkectl v0.1.0-rc.2` under the pinned amd64 image. Local attempt paths are working
artifacts, not a durable public registry or an authenticated evidence archive; the identities and outcomes above
record what was inspected.

## Limit

This validates the migrated local demo mechanics and its current inputs. It establishes neither live GitHub
execution nor signed Release consumption, M3/M4 acceptance, qualified clearance, independent reproduction,
GPU/AICR execution or provisioning. The earlier [2026-09-04 record](2026-09-04-m1-local.md) remains a separate run.
