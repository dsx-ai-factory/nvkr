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

## Offline dependency scanning

A separate offline Linux/amd64 exercise used artifacts downloaded from
[Run 34453148217](https://github.com/njhensley/nke-recipes/actions/runs/34453148217),
Syft 1.51.1, and the actual SBOM executor with a regenerated Plan. The measured producer records
were local fixtures, not a new hosted Run or authenticated qualified producer report.

| Output | Unique dependency components | Observed breakdown |
|---|---:|---|
| nkectl | 27 | Go modules |
| CWE | 248 | 171 Go modules, 76 Alpine packages, one OS component |
| Helm | 7 | Three bundled charts, four external chart references |

Two workspace modules in each binary/image inventory retained `UNKNOWN` versions in Syft's report;
no release version was guessed. Counts describe those exact observed artifacts, not present-day inputs,
signed SBOM publication or a vulnerability scan. The [SBOM reference](../dependency-sboms.md)
owns current format/stage scope and the opt-in real-artifact executor configuration.

## Failed attempts and storage regressions

The following September 10 observations retain their original subjects; no new execution was performed
when this history was consolidated. A successful publication does not make a failed Run complete.

### Hosted attempts

| Run | Result and correction |
|---|---|
| [34430414883](https://github.com/njhensley/nke-recipes/actions/runs/34430414883), `d61ce88` | CLI and Helm source builds passed; CWE rejected the default Podman runtime. Both maintained Environments now select Docker, and graph admission rejects incompatible settings. The [failed Run record](2026-09-10-combined-first-failed-run.json) retains all three required outputs. |
| [34430831748](https://github.com/njhensley/nke-recipes/actions/runs/34430831748), `16bbddb` | All three builds passed; CWE took 8m19s. Root-owned private image directories blocked host archive handoff. Generated Docker utilities now use runner ownership. A real Linux-volume experiment reproduced the failure and verified the correction. |
| [34431910080](https://github.com/njhensley/nke-recipes/actions/runs/34431910080), `00656b2` | All builds, six package jobs, publication, signing and hosted verification passed. Independent consumers passed for all three repositories. Finalization failed because two source fetches restored the same receipt directory. Exact receipt-file carriers correct that collision. |
| [34433727700](https://github.com/njhensley/nke-recipes/actions/runs/34433727700), `3c2afb2` | CWE exhausted runner disk space at the Go linker under the forced native snapshotter. The launcher now requires overlayfs; the receipt fix remains in place. No publication succeeded in this attempt. |
| [34434756868](https://github.com/njhensley/nke-recipes/actions/runs/34434756868), `4557f64` | All 20 jobs passed, including all three builds, publication, real signing, verification and complete Run finalization. Independent cosign/NKD consumers and downloaded CLI/image/Helm checks passed. |

An offline replay of the second attempt's successful producer carriers also passed all six package/archive
handoffs. It verifies the packaging path, not new hosted execution or signatures.

The third attempt published the common envelope digest
`sha256:77b8f4f754820a0f59901e5f69a1ad1d49322f6d744cd9116ceb057ebf8e3504` to the `cwe`, `helm` and `nkectl`
repositories under `ghcr.io/njhensley/nke-recipes`. Independent cosign signature and production-attestation
checks and the public NKD `pipeline-attested` / `artifact-publication` consumer passed for all three references.
Its native CWE manifest
`sha256:38b30b65d4e8c333d9823f3376f9d623846c0474837e62182d973e4c8b7adecf` passed offline executable,
SPIFFE helper, UI, profiles, license and CA checks, reporting `cloud-workflow-engine 0.0.0-ged9fb09b04fd`.
The extracted CLI reported `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.
The downloaded Helm tree passed all checksums and rendered all three local charts with that exact CWE digest.
These successful publication checks do not change the third attempt's failed Run status.

### Hosted storage regression

The fourth attempt failed with `mapping output file failed: no space left on device`. The forced native
snapshotter copied the large source/toolchain tree across Dockerfile steps. A real Docker regression using
the launcher's exact settings and a 16 MiB repeated-copy fixture failed a 64 MiB storage budget at 164,436 KiB.
After switching only the snapshotter to overlayfs, it passed at 33,256 KiB. The test runs in about two seconds:

```sh
NKD_BUILDKIT_TEST_IMAGE=moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef \
  go test ./pkg/nkd/pipeline -run TestBuildKitSnapshotStorageBudget -count=1 -v
```

The [pinned BuildKit rootless documentation](https://github.com/moby/buildkit/blob/v0.33.0/docs/rootless.md)
requires Linux 5.11+ or an Ubuntu kernel for unprivileged overlayfs. The launcher does not add privileged mode,
a host socket, credentials or a FUSE device. Unsupported hosts fail instead of silently using native storage.
The miniature storage test isolates the storage regression. The subsequent full hosted attempt separately
passed the complete CWE build in 4m40s and finalized the combined Run.


## Validation and limits

Before pushing, the full `make check` gate passed: formatting, vet, lint, shellcheck and race-enabled tests,
with 64.2% statement coverage. Uncached Linux pipeline/sign race checks, exact bundle regeneration and
workflow lint checks passed. The real rootless snapshot-storage regression failed under native at
164,436 KiB and passed under overlayfs at 33,256 KiB for the same 16 MiB fixture.

The [job timings](2026-09-10-combined-timings.csv) record a 4m40s CWE job and a 54s finalizer. The entire run
spanned 9m25s from creation to completion, including scheduling. Earlier native-snapshot CWE successes took
8m10s and 8m19s; these are individual observations on hosted runners, not a controlled performance benchmark.
The [hosted attempts](#hosted-attempts) retain the failed Runs and their corrections.

This proves combined hosted `pipeline-attested` artifact publication and independent consumer acceptance.
Subsequent coverage tests found that this run's production classifier incorrectly labeled the mixed,
network-enabled build as `hermetic-build`. The published bytes and recorded signature checks remain real,
but that build-type claim is too strong. The classifier now selects `wrapped-build` when any recipe uses a
wrapped/network-enabled build; this code correction does not retroactively change the recorded attestation.
It does not establish qualified reproduction, full Release-scope acceptance, Kubernetes deployment,
database-backed CWE startup or availability of the versioned external charts referenced by the quickstart.
