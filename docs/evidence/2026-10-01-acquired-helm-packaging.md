# Acquired NKE Helm packaging — local evidence

On 2026-10-01, `nkdctl run local` completed attempt `acquired-helm-01` with four observed Helm OCI
outputs on branch `feat/acquired-helm-packaging`, based on `42229e5` plus the working-tree implementation.
Lane: `unqualified-local-execution`. No signing, publication, chart installation or qualification was attempted.

## Inputs and execution

- NKE tag: `controller/v0.1.1-rc.9`.
- Commit: `340009f1e15d8e26940f1e03c9696f1ad251c0ae`.
- Tree: `95caaf5f49416cc676ff8d86f5a0417bdc655c30`.
- Go: 1.27.1; macOS ARM64 host with local Linux Docker utility execution.
- [Example inputs](../examples/nke-pin-set/definition.yaml), with `utilityPlatform: linux/arm64`.
- Canonical NKE Git access routed to an existing local clone using `insteadOf`; no upstream files changed.
- SPIRE, SPIRE CRDs and Temporal acquired from public HTTPS; PostgreSQL acquired from anonymous OCI.

The [usage guide](../nke-pin-set-usage.md#package-locally) contains the commands. The generated lock
and Run are retained locally under `.nkd/acquired-charts/` and `.nkd/runs/acquired-helm-01/`.
Run digest: `sha256:1d65e47d9fe916ccb2dff4c24244a302ebb921dd2561418b97dcbc3e6afce8da`.

## Exact-byte results

Every chart layer was compared byte-for-byte with its downloaded archive. Its SHA-256 matched both
architecture pins and the acquisition result. Manifest/config blobs were also rehashed; each manifest
digest matched the completed lifecycle record. Digests below are SHA-256.

| Chart | Version | Archive bytes | Preserved chart digest |
|---|---|---:|---|
| PostgreSQL | 18.5.15 | 89,389 | `c350143d2e21c1e1a3670dd32ab64fd11d49198a66fae87c63a3112531c0cd17` |
| SPIRE | 0.30.1 | 346,108 | `7c32126bb1909076cdf9fd0419dbf9e5b012f152bb09a96a4259917d70581fac` |
| SPIRE CRDs | 0.6.1 | 4,488 | `ce982e63fc375e392b014fc99e621a55442ab886052413e9bb052f72d66580a8` |
| Temporal | 0.74.0 | 181,251 | `14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37` |

| Output | OCI manifest digest |
|---|---|
| PostgreSQL | `879c8373581613b0a50e10d8bd88c4a07ed362e26f10b31549dc783a96d7d753` |
| SPIRE | `8a98b44f8ae62577b3703ee20759149cc0d725c4af07951edf13a595d134ccb2` |
| SPIRE CRDs | `b995224bcedc0d270c9a527e54bf2204033329638ed9b6a87d626be948904d04` |
| Temporal | `5175a6f550767e4160470fd6dd93f75bfe3293c61c4bd2e321db9d3fbb5e2f3c` |

Contract tests exercise HTTPS and OCI acquisition, unchanged gzip metadata, independent lifecycle
observation, and rejection of changed bytes/results, symlinks, unsafe archives, missing result transport,
cancellation and image rebinding. The real Run generated composition SBOMs only; it did not scan chart
dependencies or verify upstream signatures. Standard Helm-client pull interoperability was not exercised.

## Repository checks

Passed: `make check` (format, vet, lint, shellcheck, race/coverage tests and vulnerability scan),
`go test ./... -count=1`, Linux `go test -race ./... -count=1`,
`make notices-check workflows-check build`, and `git diff --check`.
Linux ran before the final lint-only change explicitly discarding a deferred file-close error.
The original working tree and index were unchanged; the temporary local VM was stopped after verification.
