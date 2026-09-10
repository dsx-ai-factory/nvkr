# Combined NKE hosted acceptance

[Run 34434756868](https://github.com/njhensley/nke-recipes/actions/runs/34434756868), commit
`4557f64e5b8225929fb9c504ee643096e8505a4a`, passed all 20 generated jobs. The combined Definition built
`nkectl`, the full upstream CWE runtime image and the complete quickstart Helm bundle from NKE commit
`ed9fb09b04fd40bf331d25df2222206ffaca593e`. Publication, real keyless signing, hosted verification and
lifecycle finalization all passed. [Repository CI](https://github.com/njhensley/nke-recipes/actions/runs/34434756962)
also passed for that commit. All 20 generated job checks and repository CI had zero annotations.

## Exact Run and publications

The downloaded [canonical Run record](2026-09-10-combined-run.json) reports attempt `34434756868-1`,
status `complete` and lane `pipeline-attested`. Its frozen required names and observed names are exactly
`cwe`, `nke-helm` and `nkectl`.

- Definition: `sha256:b8dca6d0eb7cfafcd7ec45531dcec4fcd381429df14f8def9ab31627d52d6e47`.
- Environment: `sha256:b09239bfb56ce853bc844fdc68a2e7d88993feae20940b8dc56657c412a7597a`.
- Plan: `sha256:7d94d05f8cd4087767bbc56b9751814ccf1ad55d7f0e89ccc3a38a6e9a01720a`.
- Content subject: `sha256:df49ce8cf7aa1b6535eb07d7bbc3a01032b7a4273ce2c6300242998194f5a4af`.

The three publication repositories carry the same exact closure/envelope digest:

```text
ghcr.io/njhensley/nke-recipes/cwe@sha256:b8c4ddf4679ed7346a25c790da618c64b95f3cb5ee303d944faa919447a16345
ghcr.io/njhensley/nke-recipes/helm@sha256:b8c4ddf4679ed7346a25c790da618c64b95f3cb5ee303d944faa919447a16345
ghcr.io/njhensley/nke-recipes/nkectl@sha256:b8c4ddf4679ed7346a25c790da618c64b95f3cb5ee303d944faa919447a16345
```

The runnable CWE image is a member of that authenticated closure. Docker and the Helm Deployment use its
native manifest reference:

```text
ghcr.io/njhensley/nke-recipes/cwe@sha256:6d61e1b272c0ba63e7c233f87dd6b48ba813d7ae3373c2b623403e86cc210061
```

The Run's observed CWE index digest is distinct from both the native manifest and the outer publication
subject. These identities were checked at their corresponding boundaries, not substituted for each other.

## Independent consumption

A separate Linux/amd64 container used the owner's local Docker GHCR login and pinned cosign v2.6.3 bytes,
SHA-256 `7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4`. For each of the three exact
publication references, these checks exited zero outside the generated workflow:

```sh
cosign verify --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" --output json "$REFERENCE"
cosign verify-attestation --type slsaprovenance1 --certificate-identity-regexp "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" --output json "$REFERENCE"
nkdctl release verify --reference "$REFERENCE" --lane pipeline-attested --scope artifact-publication \
  --identity "$IDENTITY" --issuer "$ISSUER" --cosign /cosign/cosign-linux-amd64 \
  --cosign-digest sha256:7c78a7f2efc00088bd788a758db6e0928e79f3e0eb83eb5d3c499ed98da4c4f4 \
  --workspace "$WORKSPACE"
```

The identity policy is
`^https://github\.com/njhensley/nke-recipes/\.github/workflows/nkd-run\.yml@refs/heads/main$`;
the issuer is `https://token.actions.githubusercontent.com`. The public consumer descriptions for
[CWE](2026-09-10-combined-cwe-release.json), [Helm](2026-09-10-combined-nke-helm-release.json) and
[CLI](2026-09-10-combined-nkectl-release.json) each contain exactly the finalized Run's observed artifacts and
content subject. All three [hosted verification records](2026-09-10-combined-hosted-verifications.json) also
report `dryRun: false`, `signatureVerified: true` and `attestationVerified: true` with that identity and issuer.

## Payload checks

The independently verified publication was pulled and its exact CLI, native image and inner Helm object
inspected. [Recorded payload checks](2026-09-10-combined-payload-checks.json) cover:

- Docker pulled the native CWE manifest above. An offline container checked the CWE executable, SPIFFE JWT
  helper, UI, profiles, licenses and CA certificates, ran help, and reported
  `cloud-workflow-engine 0.0.0-ged9fb09b04fd`.
- The recovered CLI executed offline and reported
  `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.
- All 47 Helm checksum entries matched the downloaded tree. Helm `v4.2.3+g43e8b7f` rendered the three local
  charts: `002-postgresql`, `004-spire-crds-post` and `007-controller`. The controller rendered the exact
  native CWE digest when supplied `image.repository=ghcr.io/njhensley/nke-recipes/cwe`.

## Validation and limits

Before pushing, the full `make check` gate passed: formatting, vet, lint, shellcheck and race-enabled tests,
with 64.2% statement coverage. Uncached Linux pipeline/sign race checks, exact bundle regeneration and
workflow lint checks passed. The real rootless snapshot-storage regression failed under native at
164,436 KiB and passed under overlayfs at 33,256 KiB for the same 16 MiB fixture.

The [job timings](2026-09-10-combined-timings.csv) record a 4m40s CWE job and a 54s finalizer. The entire run
spanned 9m25s from creation to completion, including scheduling. Earlier native-snapshot CWE successes took
8m10s and 8m19s; these are individual observations on hosted runners, not a controlled performance benchmark.
The [implementation record](../combined-nke-pipeline.md) retains the failed attempts and their corrections.

This proves combined hosted `pipeline-attested` artifact publication and independent consumer acceptance.
It does not establish qualified reproduction, full Release-scope acceptance, Kubernetes deployment,
database-backed CWE startup or availability of the versioned external charts referenced by the quickstart.
