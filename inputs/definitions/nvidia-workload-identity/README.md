# NVIDIA Workload Identity DSX IMDS NodeAttestor agent

This additive selection builds the agent half of the upstream `dsx_imds` SPIRE
NodeAttestor from public NVIDIA Workload Identity v1.15.0 as one source-built
host-package executable per Linux architecture. Registering the selection
changes catalog-maintenance bookkeeping only: the bundle is named as an
additional generation Run in `inputs/policies/maintained-generation.json` and
`inputs/policies/nvke-catalog-maintenance.yaml`, and the maintenance workflow is
regenerated to transport it. Existing source pins, production output selections
and pre-existing generated bundles are preserved byte for byte; no maintained
NVKE, AICR or NVSentinel selection is added or altered.

| Output | Platform | Producer |
|---|---|---|
| `dsx-imds-node-attestor-agent-linux-amd64` | linux/amd64 | `dsx-imds-node-attestor-agent` |
| `dsx-imds-node-attestor-agent-linux-arm64` | linux/arm64 | `dsx-imds-node-attestor-agent` |

The [catalog](../../catalogs/nvidia-workload-identity/registry.yaml) declares one
source key and one host-package producer that compiles the exact upstream package
`./cmd/dsx-imds-node-attestor-agent` from the repository root module. The build
mirrors the upstream agent-builder stage: the root `go.mod`/`go.sum`, the
repository `cmd/` and `internal/` packages, `CGO_ENABLED=0` and the target
`GOOS`/`GOARCH`. The upstream `Dockerfile.spire` server-plugin and DPF stages are
not inputs to this agent, and the pinned `third-party/` gitlink submodules stay
uninitialized. Vendor preparation downloads, verifies and vendors the root module
without changing the upstream `go.mod`/`go.sum` bytes; the offline build uses
`-mod=vendor`, `-trimpath` and `-buildvcs=false` on a digest-pinned Go 1.26.10
producer image per architecture, matching the source-selected upstream toolchain.

The two outputs are separate single-platform executables. Executable
`platforms[]` indexes are not implemented, so neither output declares one. The
plugin is started through the SPIRE plugin SDK and has no application CLI
contract; no `--version` or `--help` smoke check is claimed.

The [Environment](../../environments/github-wli-imds-agent.yaml) generates the
independent `nvkr-run-wli-imds-agent` production bundle on generic hosted
runners with manual dispatch only. It selects no publication, signing or
qualification policy: generating the workflow and its source artifacts authorizes
neither a dispatch nor any external publication. A Run on this selection is
build-only evidence; runtime SPIRE behavior, the paired server plugin and any
image or chart lane remain out of scope.

The pinned source is NVIDIA Workload Identity `v1.15.0`
(`cd76f1d5ab4a887b9577ff601ce7cda52d5003b4`, tree
`b9c85948e417d42d7529d2306f1b80db61c629ae`, committer epoch `1792708538`),
resolved by `catalog pin`. Validate and inspect with the standard sequence:

```sh
make build
bin/nvkr definition validate --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml
bin/nvkr run plan \
  --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml \
  --environment inputs/environments/github-wli-imds-agent.yaml
```

Actual binary production for both architectures requires the real hosted source
fetch, dependency vendoring and container builds; no such Run is recorded here,
and a completed build does not establish node-attestation behavior. Validation
scope and recorded evidence for this selection belong to the repository's
[implementation and evidence record](../../../docs/adr/reconciliation/README.md);
this guide describes declarations and generation only.
