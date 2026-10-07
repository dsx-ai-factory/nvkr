# Producer and acquisition observations

These records preserve actual local rendering, upstream acquisition, and source/registry read-back.
They are historical observations, not current release acceptance. Current inputs and operator instructions
belong beside their [Definitions](../../definitions/) and in [reconciliation](../adr/reconciliation.md).

## Local rendering and packaging

On September 10, 2026, the macOS/Docker demo audit completed the local NKE three-output journey
and AICR rendering/Helm packaging. The [NKE Run](2026-09-10-demo-language-nke-run.json) selected
`cwe`, `nke-helm`, `nkectl`, Plan `sha256:3a4c0d47a0deea69f026b7877761c7615399fdc1bf2b42f2c5725969566444b6`.
CLI pull-back matched its SBOM subject and reported `nkectl v0.1.0-rc.2` at upstream commit
`ed9fb09b04fd40bf331d25df2222206ffaca593e`. Execution took 262 seconds; pull-back took two seconds.

The [AICR Run](2026-09-10-demo-language-aicr-run.json) completed `gpu-runtime` and published
`127.0.0.1:5001/nke/gpu-runtime@sha256:8ff50d3ecfea403a778a6d6ab4484c8e5ae290e25d46f00ef048cd572f00ab36`.
Both were `unqualified-local-execution` working-tree observations with dry-run signing, not hosted
or authenticated qualified producer evidence. Loopback endpoints were disposable workstation artifacts.
The actual executions preceded message-only script edits; no separate execution source revision was retained.

## Acquired Helm charts

On October 1, attempt `acquired-helm-01` completed four Helm OCI outputs at base `42229e5` plus
the working-tree implementation, using NKE controller tag `controller/v0.1.1-rc.9`, commit
`340009f1e15d8e26940f1e03c9696f1ad251c0ae`, tree `95caaf5f49416cc676ff8d86f5a0417bdc655c30`.
The host was macOS ARM64 with Go 1.27.1 and local Linux Docker utility execution. SPIRE, SPIRE CRDs
and Temporal came from public HTTPS; PostgreSQL came from anonymous OCI. Run digest was
`sha256:1d65e47d9fe916ccb2dff4c24244a302ebb921dd2561418b97dcbc3e6afce8da`.

Every delivered chart layer equaled its downloaded archive byte-for-byte and matched the lock and
acquisition digest. Each manifest/config was rehashed and matched the completed Run.

| Chart | Version | Bytes | Preserved archive SHA-256 | OCI manifest SHA-256 |
|---|---|---:|---|---|
| PostgreSQL | 18.5.15 | 89,389 | `c350143d2e21c1e1a3670dd32ab64fd11d49198a66fae87c63a3112531c0cd17` | `879c8373581613b0a50e10d8bd88c4a07ed362e26f10b31549dc783a96d7d753` |
| SPIRE | 0.30.1 | 346,108 | `7c32126bb1909076cdf9fd0419dbf9e5b012f152bb09a96a4259917d70581fac` | `8a98b44f8ae62577b3703ee20759149cc0d725c4af07951edf13a595d134ccb2` |
| SPIRE CRDs | 0.6.1 | 4,488 | `ce982e63fc375e392b014fc99e621a55442ab886052413e9bb052f72d66580a8` | `b995224bcedc0d270c9a527e54bf2204033329638ed9b6a87d626be948904d04` |
| Temporal | 0.74.0 | 181,251 | `14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37` | `5175a6f550767e4160470fd6dd93f75bfe3293c61c4bd2e321db9d3fbb5e2f3c` |

These local outputs were not published, signed or installed. Composition metadata did not establish
acquired-chart dependency discovery or upstream-signature verification. Source/Run files remained local;
standard Helm-client pull interoperability was not exercised.

## APT capture correspondence

The October 3 diagnostic attributed native ARM64 and emulated AMD64 one-package `hello` captures
to NKE `f1eddbf62ff5866d593a8eb05d8eb2fcac80be1c` (PR #1211), not its subsequent head.
The original recheck did not retain a verifier revision. On October 5, a separate held-file recheck
at NKD base `71a8a359c57f19c473f6eaee7b8f9e375ac10b4a` plus local review fixes used verifier
`pkg/nkd/material/acquisitions.go` blob `2efa85692b96055186e618b5ea144b641f7b48f9`.
The [annotated output](2026-10-05-apt-capture-recheck.log) preserves both successful rechecks.

| Architecture | Capture bytes | Capture SHA-256 | Manifest-blob SHA-256 |
|---|---:|---|---|
| ARM64, native capture | 30,720 | `684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c` | `37eec045cbe8c04ce08e14f8692c6e2ba49d584759945c4d6c5501422a40ec88` |
| AMD64, emulated capture | 30,720 | `4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5` | `d5162a0fbd87b1bdcb22a1eb8398f1910d7a696f79ea06e92295fc5c3446ab85` |

Both captures describe `hello 2.10-5build1`; package bytes/digests were ARM64 25,642 /
`8ab9e5cf329e88413dd502edd5e0907550132470ecb926d500d0d23a30c735d8`, and AMD64 26,068 /
`b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965`.
Archives remain local and are not recoverable from Git. Rechecks did not invoke APT/Packer/QEMU or
prove an original installer-to-disk handoff, acquisition origin or full SBOM. Cross-language synthetic
format fixtures and their generator belong in [testdata](../../pkg/nkd/material/testdata/README.md).

## M2 input acquisition

The October 5 candidate `9a7e1446bde5c14381b0e72f9ca9a41a29592816`, stacked on
`5fdf8338838be785bcc7e2026de131345f24f906`, refused generation with `CodeInvalidCatalog`
wrapping `CodeUnverifiedRef` at the PostgreSQL index. Its fresh output directory stayed empty:
all 24 image selections were blocked, rather than twenty successful acquisitions and four refusals.
An upstream read-back then reported NKE #1211 open at `b30117c25b2dcdf6c16021dae88a2eafdafcae61`,
`REVIEW_REQUIRED`, `UNKNOWN`, diverged ahead nine/behind twenty. Those values supplied no source approval.
No complete retained Helm/kubectl/Bash/native-layer acquisition diagnostic supports this early snapshot.

## Merged capture-source handoff

On October 6, canonical GitHub read-back reported NKE #1211 merged/approved, approved head
`7204e852dd3fc31022e9d91a8e1a829c62630008`, merge time `2026-10-06T03:29:54Z`, merge commit
`ff21ba8c07cafff74b68e238a8876af11e5beded`. `main` was protected at that commit and the comparison
was identical, ahead/behind zero. This is a point-in-time source-owner observation, not general policy.

Four normal catalog-owner pin calls updated `nke-os`, `nke-cni-calico-source`,
`nke-kubeadm-distro-source`, `nke-spire-agent-source` to that commit, tree
`b915345debd33ccd63dfbf8146415bf20e94c4ea`, epoch `1791257393`, preserving 24 native subjects
and 34 output declarations. Canonical bare Git inspection froze agent version `v0.2.1-81-gff21ba8`;
the closest `agent/v*` tag object `162ecb366577e42c9c820a46de59184869c3a4c0` peeled to
`33fb71b2f4224b470ebc628a9165fc4b92870406`. Merged source required `--architecture` and the
non-FDE capture path `<output_directory>/non-fde/apt-acquisitions.tar`.

Static graph validation atop `9854e5126eaf7337f1c66e6586189fa44e651fcc` used an existing complete
read-only NKE object store. An initial blobless bare clone could not serve six chart rows; that failure
was not native execution proof. The candidate then progressed to `CodeEngineCapability`/exit 29
for missing execution classes, with no output. This handoff did not build a capture-enabled disk.

## Temporary SPIRE source selection

The [retained October 6 Plan](2026-10-06-team-demo-pipeline/plan.json) records SPIRE's separate source
pin `726f76944404e1106626b8333d3576bdf67b575b`, while the OS, Calico and kubeadm producers retain
capture-capable NKE commit `ff21ba8c07cafff74b68e238a8876af11e5beded`. Its SPIRE instructions select
SPIRE 1.14.1 and TPM 1.9.0 without the DSX IMDS plugin.

The former implementation ledger at `ca6fc65d` attributed this temporary branch selection to explicit
user authorization after the DSX IMDS downloads returned HTTP 403 on both hosted architectures.
The Plan corroborates the mixed source selection and commands; it does not independently prove those
download failures or approval. This scoped test candidate does not establish complete M2 payload acceptance.
The separate merged-source approval above does not approve the older SPIRE pin or fill that coverage gap.

## Registry parent bindings

The later follow-up to `0b18ae1af2ad7383c8a8cdce3a2c3110c764274a` (replayed ancestor
`bd20877cb9b249533e747043c514e90de79e2f61`; applied protocol/lock in
`a8989402f317b941e2643ae81c606588ce3b4189`) authenticated both digest-backed parents through
`catalog pin-artifacts`. Exactly two `imageParentDigest` fields were added; all 24 native child
digests and source pins stayed unchanged. Repeating maintenance made no changes; lock SHA-256 was
`da53cf2c9f9be3229740838ed23ad325ac3076a674c7c286676bfe05622170ff`.

The working-tree follow-up on `2cd17d46563f8fd52c64fd9c829a40475582a08d` pinned all twelve image
components, adding ten tag-backed parents without changing native children. The repeat was a fixed point;
lock SHA-256 was `100ac0accd588d3bd9089ba5cbd052d94981edb3d505f33a1b61e877fded9ee7`.
These were real registry metadata reads, not upstream layer transfer, scanner discovery or release
acceptance. Controlled TLS/native-ARM64 tests do not extend that scope; their full dirty-source
manifests were not retained. The later [M2 pipeline](2026-10-06-team-demo-pipeline.md) has its own
exact source and observed Run and does not retroactively qualify these diagnostics.
