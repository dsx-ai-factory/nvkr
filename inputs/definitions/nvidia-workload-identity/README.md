# NVIDIA Workload Identity DSX IMDS NodeAttestor agent

This independent selection builds the agent half of the upstream `dsx_imds` SPIRE
NodeAttestor from public NVIDIA Workload Identity v1.15.0 as one source-built
host-package executable per Linux architecture. It is the fourth maintained
bundle beside the three NVKE composite bundles, without adding a member to any
NVKE composite or changing its derived inventory.

The same [producer declaration](../../catalogs/shared/nvidia-workload-identity.yaml)
is imported into the node-images catalog with its own WLI pin. NVKE disk selections
consume the executable as the `imds-agent` material of `nvke-spire-agent`; they do
not add standalone executable Release members. Each disk output builds its own
complete closure. Cross-output and cross-Run reuse are not implied. The package
integration requires the optional-input NVKE source contract described in
[reconciliation](../../../docs/adr/reconciliation/adr-22.md#nvidia-workload-identity-agent).

The [generation inventory](../../policies/maintained-generation.json) and
[NVKE refresh policy](../../policies/nvke-catalog-maintenance.yaml) explicitly
include this build-only Run. That shared refresh keeps the generator, shared Plan
store and imported action-pin cohort coherent across every committed bundle.
It does not advance the independently pinned WLI source or toolchain; an invalid
WLI closure intentionally blocks that shared refresh. WLI pin changes retain the
catalog's own `catalog pin`/`catalog bump` owner. Regeneration owns all workflow,
Plan and manifest bytes.

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
independent `nvkr-run-wli-imds-agent` build-only bundle on generic hosted
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
make workflow-generator
bin/nvkr-exec definition validate --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml
bin/nvkr-exec run plan \
  --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml \
  --environment inputs/environments/github-wli-imds-agent.yaml
bin/nvkr-exec run generate --engine github \
  --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml \
  --environment inputs/environments/github-wli-imds-agent.yaml --root .
bin/nvkr-exec run generate --engine github \
  --definition inputs/definitions/nvidia-workload-identity/imds-agent.yaml \
  --environment inputs/environments/github-wli-imds-agent.yaml --root . --check
```

The installed `.github/workflows/nvkr-run-wli-imds-agent.yml` retains the manual
GitHub Actions dispatch entry after the change is merged onto the default branch.
Generating or checking it locally does not dispatch it. To preview independently,
use an existing empty output directory as `--root` for both generate and check; the same
manual-only workflow and complete Plan store are emitted there. That output tree
is a generated bundle, not a standalone NVKR source checkout or a hosted Run.

Actual binary production for both architectures requires the real hosted source
fetch, dependency vendoring and container builds; no such Run is recorded here,
and a completed build does not establish node-attestation behavior. Validation
scope and recorded evidence for this selection belong to the repository's
[implementation and evidence record](../../../docs/adr/reconciliation/adr-22.md#nvidia-workload-identity-agent);
this guide describes declarations and generation only.
