# NVKE node image

These development inputs select non-FDE NVKE QCOW2 roots with shared material producers. NVKR builds the NVKE agent, Calico, OVN,
kubeadm distribution, SPIRE package, patched installer ISO and aggregate image inputs before running the
source-owned Packer project. Architecture-dependent build and vendor argv use the compiler-owned `{{.Arch}}`
substitution. The AMD64 disk root and ARM64 execution overlay are explicit producer declarations, not one
architecture-neutral root.

The catalog pins NVKE source to `v0.22.0-beta.1` at commit
`6991e36bef8f15b42b6f6519ccff089c587a6e49`. The SPIRE package is an explicit
exception, retained at the pre-DSX commit `726f76944404e1106626b8333d3576bdf67b575b`.
The selected pins describe build inputs; they do not establish coordinated release acceptance.

Local development and CI are parallel supported paths sharing material producers, not substitutes for one another.
Their Definitions select explicit execution behavior:

- `os-arm64.yaml` requires native Linux ARM64 KVM and uses source-owned Packer defaults for the host CPU.
- `os-amd64.yaml` selects the native Linux AMD64 KVM route and its separate, digest-pinned Ubuntu installer ISO.

These inputs retain Ubuntu 26.04. For opt-in Ubuntu 24.04.5 builds, use the
[Ubuntu 24 inputs](ubuntu24/README.md). The same native local wrapper selects
them with `NVKE_OS_UBUNTU_VERSION=24.04`; they include a separately pinned agent builder
compatible with the older guest libc. Separate hosted Definitions cover plain and public-bootstrap
FDE disks on both architectures.

For a first-time seven-lane Apple Silicon demo with Docker/Colima TCG, use the
[laptop build guide](../../../docs/local-development.md#build-the-complete-selection-including-the-os)
and its `nvke-full-arm64-local.sh ... os` launcher instead. The older wrapper in
this guide admits Docker only with native KVM. The laptop launcher validates and
freezes fresh bytes through an attempt overlay.

The AMD64 graph is authored, but neither Docker architecture has a stable maintained image ID in
`toolchains/nvkr-packer/toolchain.lock.json`. The catalog's AMD64 ID is a substitution-only baseline, not a
maintained pin or a claim that a subsequent build will reproduce it. The ARM64 baseline retains the historical
M2 Docker candidate publication; it does not prove reproduction of the current Dockerfile or bootstrap compiler.
For a local Docker attempt, the wrapper builds the
source-locked toolchain and freezes its observed exact ID into an attempt-local catalog overlay before compiling the
executable Plan. The runtime re-checks that the loaded image equals that frozen ID, a bind rather than provenance
authentication. This does not authorize publication or qualification.

The architecture-generic wrapper defaults to ARM64 Docker on native Linux and
selects the KVM Definition:

```sh
CONTAINER_RUNTIME=docker NVKE_OS_ARCH=arm64 scripts/nvke-os-local.sh local-arm64-docker-001 1
```

Native Linux AMD64 uses the same wrapper with `NVKE_OS_ARCH=amd64` and its AMD64 KVM Definition.

`scripts/nvke-os-arm64-local.sh` remains an ARM64 compatibility shim. The Docker Environment requires at least
8 CPUs, 10 GiB memory, 64 GiB free
workspace storage, KVM and producer-declared network access. NVKR authenticates those facts before starting QEMU.
The KVM Definitions reject a TCG-only class. CI uses the native Docker/KVM Definitions.

The wrapper rebuilds the source-locked native toolchain and freezes the exact selected image ID with the authored
inputs below `.nvkr/inputs/<attempt>` before execution.
Attempt names are immutable.

For Docker, the checkout and `.nvkr` workspace must be visible to the daemon at
the same absolute paths. The wrapper sets `TMPDIR` to a private attempt directory
under `.nvkr/bootstrap/<attempt>/tmp`, so temporary bind sources do not depend on
the runner and daemon sharing `/tmp`.

A successful attempt retains:

```text
.nvkr/runs/<attempt>/outputs/nvke-node-image-<arch>.qcow2
.nvkr/runs/<attempt>/lifecycle.json
```

This remains `unqualified-local-execution`. The [ARC build evidence](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md)
records successful committed-code ARM64 and AMD64 Docker/KVM builds, boot smoke and QCOW2 uploads, alongside the
earlier ARM64 Podman/TCG bake.
The standalone Docker/KVM validation workflow is an unqualified integration path. It does not publish or sign
these disks. [M2](../nvke/README.md) separately selects ADR-18's admitted native ARC generated route and
build-provenance publication scope. Neither route establishes FDE support or qualified reproduction.
The local image-ID check binds loaded bytes to the attempt's Plan; it does not authenticate toolchain provenance.

## Validate on native CI runners

The separate [NVKE OS validation workflow](../../../.github/workflows/nvke-os-validation.yaml) runs both native
Docker/KVM architectures on every push to `main`, without path filters or canceling earlier pushes.
Manual dispatch supports these modes:

| Mode | What runs | Source and credentials |
|---|---|---|
| `preflight` | Native KVM access check on one or both ARC pools | No NVKE acquisition or source credential; can inspect branch workflow bytes |
| `toolchain` | Native source-locked toolchain assembly and image-ID observation on hosted runners | No KVM or private NVKE source; trusted main workflow |
| `full` | Native local lifecycle build, exact QCOW2 checks and source-owned boot smoke | Trusted main selector/fetch, reviewed source handoff and KVM runners |

For a full dispatch, supply an exact main-reachable NVKR commit or the copy-pr-bot mirror of a current,
open, non-draft same-repository PR. Maintainers must vet the exact head first. The workflow does not
authenticate an `/ok to test` comment or independently enforce human approval. `approved_nvke_commit`
must match the selected NVKE catalog lock and source nodes in the trusted compiler's complete Plans.
Only the named SPIRE producer may use the exact pre-DSX commit/tree retained above; its catalog
snapshot and executable source edge must both match that exception.
This is a consistency check; the input is not itself source-owner approval.

The selector and source fetch use trusted main code. The fetch job alone receives the read-only
`NKE_SOURCE_TOKEN`; it verifies both exact source commit/tree pairs, removes persistent credentials, and
transports one Git checkout containing both commits to the candidate job. The consumer verifies the
archive digest and both pins before using that checkout. Candidate execution receives private source content and declared
build network, but no source token. The process-local Git transport override keeps canonical source pins.

The runner identity must already have read/write KVM access; the workflow prints device ownership/mode
and fails without changing permissions. Each full leg runs the required-KVM Packer exit-status regression;
that controlled test checks error classification, while the later bake/boot smoke supplies real integration
evidence. Runtime admission separately checks native architecture, Docker, CPU, memory, workspace and timeout.
Ephemeral runner isolation and workflow/ref access restrictions remain provider/operator obligations.
The source secret is repository-scoped: branch-authored workflows can reference it, so a workflow-local
selector is not a GitHub authorization boundary. Restricted runner groups and a main-restricted secret
environment are configuration follow-ups, not controls demonstrated by the existing diagnostic workflow.

The wrapper builds the native toolchain and freezes its observed Docker image ID before compiling the
attempt Plan. Source-lock Docker IDs remain null because diagnostic builds did not reproduce identical IDs.
Do not invent a stable pin or reuse another attempt's observation. General toolchain approval and candidate
published pins are separately described in [the Packer README](../../../toolchains/nvkr-packer/README.md).

Each successful full leg must retain a completed Plan/Run, exactly one QCOW2 without a backing file,
its SHA-256/size, and pinned source-owned boot smoke. Available Plan, Run, material manifest and bounded
serial/build logs are retained on failure. Missing/oversized operational logs produce an incomplete marker;
unsafe paths, substituted records and malformed evidence still fail. QCOW2 upload is automatic for main
pushes and optional with short retention for manual full runs. Toolchain-only results are image observations,
not NVKE build evidence.

## Acceptance boundary

The [ARC evidence](https://github.com/dsx-ai-factory/nvkr/blob/a187d6b702cbc1a70452760c606c2c4de1ed6618/docs/evidence/2026-10-01-nke-arc-os-builds.md) owns exact historical run inputs and
results. Candidate `d1e28ce` passed both full bakes and boot smoke through the trusted main workflow;
that dispatch exercised source/catalog inputs rather than the candidate's workflow edits. The later
`GOENV=off` acquisition input and changed workflow steps still require their own native full-build/boot
evidence and post-merge workflow checks. Unit and snippet tests cannot satisfy those external gates.

Source preparation uses bounded Go download/verify/vendor phases, exact `go.mod`/`go.sum` checks,
transport-only fallback and offline vendoring. Neither a checksum failure nor Packer/component execution
is retried. Those source instructions are catalog-owned, and architecture-specific prepared-source receipts
cannot be shared through an unauthenticated directory/cache copy.

The workflow owns selection and transport. NVKR modules own catalog validation, execution admission,
held disk observation and completeness. [ADR-18](../../../docs/adr/0018-pipeline-ir-and-ci-adapter-emission.md)
owns execution policy; [reconciliation](../../../docs/adr/reconciliation.md) owns current evidence status.
