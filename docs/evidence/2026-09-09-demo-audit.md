# Maintained demo execution audit — 2026-09-09

Execution started from commit `28baf72` on macOS 26.6.2 arm64, Go 1.27.0,
Docker Engine 29.4.2. The CLI was built with `make build`; scripts received
`NKDCTL_BIN="$PWD/bin/nkdctl"`. Local logs and derived output are retained under
`.nkd/demo-audit-20260909/`. These local files are not committed evidence bundles.

The owner confirmed during this audit that the NKE OS build owns the entire worker
payload, including Kubernetes, containerd, runc and crictl. This agrees with
`docs/milestones.md`. Local execution and GitHub are the current delivery focus.
The old individually built worker-package journey below exposed stale demo scope;
its custom-toolchain registry is not a prerequisite for the intended NKE CLI/AICR
journeys. Those failed attempts are retained as historical audit findings.

## Results

| Demo | Actual result | Scope |
|---|---|---|
| `demo-m1-local.sh` | Passed with default arguments after fixing the generated attempt name: pinned NKE build, SBOM, OCI push/pull, extraction and recovered CLI execution | Local, unqualified; signing and verification records explicitly remain dry-run |
| `demo-catalog-change.sh` | Passed: isolated upstream Git fixture, preview, pin update, identity change and malformed-lock rejection | Actual CLI and local Git; no upstream publication |
| `demo-resolution-matrix.sh` | Passed: amd64/arm64 × worker/control-plane, unsupported architecture rejected | Resolution and dry-run build plans, not four builds |
| `demo-tamper-detection.sh` | Passed: six intended rejection cases and positive comparison/transport controls | Actual CLI with diagnostic fixture artifacts |
| `demo-ai-factory-github.sh` | Passed with default arguments against the maintained AICR Definition, including byte-exact regeneration | Generated hosted workflow; no GitHub jobs executed |
| `demo-ai-factory-gitlab.sh` | Passed with default arguments against the maintained AICR Definition, including byte-exact regeneration | Retained build-only development generator; no GitLab jobs executed |
| `demo-ai-factory-local.sh` | Passed with default arguments and separately with `--publish`; independent pull-back by digest also passed | Real AICR render, terminal observation, Helm package and lifecycle envelope; no embedded worker-component builds |
| `demo-release-consumer.sh` | Positive verification blocked: no real signed publication exists (confirmed by owner) | Missing-reference rejection and actual macOS capability rejection passed; neither proves signed consumption |

## Commands and observations

### Default invocation regression

The initial M1 run below used an explicit attempt and missed a real default-path
bug subsequently reported by the owner. The scripts generated uppercase `T`/`Z`
timestamp separators, but lifecycle attempt names accept lowercase characters.
Running M1 with no script arguments reproduced exit 2 and
`[NKD-E-USAGE] run preparation requires a canonical attempt name`.

Both local demos now call the shared `demo_default_attempt` helper, which uses
lowercase separators. `TestDemoDefaultAttemptsAreAcceptedByRunPreparation` sends
the actual generated names through real lifecycle preparation for both Definitions.
The corrected M1 default invocation completed in 40 seconds with attempt
`m1-20260909t152326z-83595`; its publication and recovered binary digests match the
earlier explicit-attempt run. Logs: `m1-default-before.log`, `m1-default-after.log`.

The final default-entry checks used no injected CLI and no custom workspace or
attempt flags:

```sh
NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke scripts/demo-m1-local.sh
scripts/demo-ai-factory-local.sh
scripts/demo-ai-factory-local.sh --publish
scripts/demo-ai-factory-github.sh
scripts/demo-ai-factory-gitlab.sh
scripts/demo-catalog-change.sh
scripts/demo-resolution-matrix.sh
scripts/demo-tamper-detection.sh
```

The last four logs have the suffix `-default.log`. All these commands passed.
The NKE clone is a documented source-transport prerequisite, not a CLI substitute.

### Current AICR lifecycle

`definition-aicr.yaml` declares one accelerated-runtime Recipe and one
`gpu-runtime` Helm output. It does not claim an OS image, individual worker
packages, or a complete cluster. Local and GitHub have separate AICR Environments;
neither needs a custom Kubernetes/runc toolchain or GitLab registry access.

The default local attempt `aicr-20260909t152337z-84364` and the published attempt
`aicr-20260909t152404z-86179` both completed, each observing one output. Their Run
records live below `.nkd/demo-ai-factory-local/`. The published reference is
`127.0.0.1:5001/nke/aicr@sha256:6f0f4ecabbff5b2bf62e935ba9a537ac527aa4ed352e2f656dcb4b1f0372c64b`.
An independent `publish pull` using that digest succeeded into
`.nkd/demo-audit-20260909/aicr-pullback`. The AICR registry on port 5001 coexists
with M1's registry on port 5000.

Logs: `gpu-aicr-local.log`, `gpu-aicr-local-publish.log`, `aicr-pullback.log`,
`gpu-aicr-github.log`. The GitHub generator's plan digest was
`sha256:83f442acbf5cb83181bc9fafd5e435465674e8b5b830d642e81a2755c3610aaf`.

### Earlier focused and advanced-fixture checks

The focused and provider demos were executed with separate workspaces:

```sh
scripts/demo-catalog-change.sh --work .nkd/demo-audit-20260909/catalog-change
scripts/demo-resolution-matrix.sh --work .nkd/demo-audit-20260909/resolution-matrix
scripts/demo-tamper-detection.sh --work .nkd/demo-audit-20260909/tamper-detection
scripts/demo-ai-factory-github.sh --work .nkd/demo-audit-20260909/ai-factory-github
scripts/demo-ai-factory-gitlab.sh --work .nkd/demo-audit-20260909/ai-factory-gitlab
scripts/demo-ai-factory-github.sh --definition examples/ai-factory/definition-arm64.yaml --work .nkd/demo-audit-20260909/github-arm64
scripts/demo-ai-factory-gitlab.sh --definition examples/ai-factory/definition-arm64.yaml --work .nkd/demo-audit-20260909/gitlab-arm64
```

The matrix resolves 8 worker and 15 control-plane components on each architecture.
Roles share the same Recipe digest within an architecture; amd64 and arm64 differ.
The catalog demo's preview and old-IR comparison return the intended exit 3, the
new-IR comparison succeeds, and the malformed lock returns `NKD-E-UNPINNED-SOURCE`.

Tamper logs confirm the actual causes: changed artifact bytes produce
`NKD-E-VERIFY-FAILED`; diagnostic observations lack publishable provenance;
changed source identity returns `NKD-E-DIFF`; an existing destination returns
`NKD-E-IMMUTABLE-VIOLATION`; a corrupt archive has an invalid tar header; and an
escaping symlink returns `NKD-E-RELEASE-INCOMPLETE`. Rejection was not inferred
from an unrelated command failure.

### Real NKE build and publication

```sh
NKE_LOCAL_CLONE=/Users/nhensley/dev/code/github/NVIDIA/nke \
  scripts/demo-m1-local.sh --attempt demo-audit-20260909 \
  --consume .nkd/demo-audit-20260909/m1-consume
```

The existing owned loopback registry was reused. Source transport was redirected
only for this process; the catalog and NKE checkout were not edited. The Run
completed in 38 seconds, with pull-back taking 1 second. The recovered executable
reported `nkectl v0.1.0-rc.2 (commit: ed9fb09b, built: 2026-08-18T15:22:55Z)`.

- Run: `.nkd/m1-local/demo-audit-20260909/lifecycle.json`, status `complete`, lane `unqualified-local-execution`.
- Publication: `127.0.0.1:5000/nke/nkectl@sha256:c115be7e4be62aa43e43fee38917586c19e7a524711e4d6902d1f7eb6fe63562`.
- Named executable Artifact: `sha256:99cfd9c354a2c8719053dd1b69ce9917ed4b05d21fbe0478122d2b05d94ee969`.
- Recovered binary/SBOM subject: `sha256:eeb21f04fb79f9276f571c51f8e8155c5f3e5bdd698493e4262b0c8eef75a28d`.
- Predicate exact bytes: `sha256:d9b294e41c3053e4db42e85ae2bae851c34e0d9316ec20b4963bd307458019a8`.

The binary and publication digests match the prior recorded local run. This is
same-machine repeatability, not independent reproduction. `dryRun` is true and
both `signatureVerified` and `attestationVerified` are false.

### Superseded worker-component journey

The first arm64 execution reached real source acquisition and persisted a failed
Run at `.nkd/demo-audit-20260909/gpu-arm64/lifecycle.json`. It scheduled Helm even
though the declared worker outputs do not select it. Helm's pinned source includes
an absolute symlink test fixture at
`internal/third_party/dep/fs/testdata/symlinks/invalid-symlink`; source containment
correctly rejected it with `NKD-E-FETCH-FAILED`, wrapped by `NKD-E-BUILD-FAILED`.
The source-containment rule and upstream pins remain intact.

The compiler now derives the execution selection from the union of declared output
components. The GPU target executes its six selected producers and their pinned
source dependencies; the full resolved Recipe graph, `AllComponents`, and Recipe
identity remain unchanged. A regression test checks omitted Helm execution and
retained source/receipt dependencies. Frozen legacy plan identities also pass.

Both provider demos passed again for both architectures after the fix, with fresh
workspaces named `<provider>-<arch>-fixed`. Their generated plans contain exactly
the selected worker producers and AICR tool acquisition/rendering. The committed
NKE workflow remains byte-current at plan digest
`sha256:1b999d9b9dfe2c7a2688c4a82aafe3b20267cb6f9d13d74a554d0c9fee5192e3`.

AICR executes inside the declared Linux utility container even when macOS runs
the orchestration. Its held-executable checks therefore apply inside that
container; a separate Linux host requirement for the GPU script would be incorrect.

The fixed real attempts are `gpu-arm64-real` (log `gpu-arm64.log`) and
`gpu-amd64-fixed` (log `gpu-amd64-fixed.log`) below the audit directory. The original
source-failure log is retained as `gpu-arm64-darwin-failed.log`. Both fixed attempts
built containerd and crictl and acquired the pinned kubectl. Arm64 then failed to
materialize `nkd-toolchain-cgo@sha256:e2d93f718fc21fa61f9157c54e03336daf7649b0f9a866667e35dec51fecc56a`;
amd64 failed on `nkd-toolchain-go@sha256:2154cb16a59fee9fa8d53167de0899936ed09b3b1a816b465bcce363e485c0ee`.
Docker reported pull access denied, wrapped as `NKD-E-BUILD-FAILED`. Both attempts
retain failed lifecycle records; neither claims complete outputs or a realized cluster.

These digests match `toolchains/toolchains.lock.yaml`. The script's existing
preflight admitted differently named local tags by image ID, which did not prove
the requested registry manifest was available. The local registry contained only
`nke/nkectl`, not the custom toolchains. The checked-in local Environment does not
configure a registry prefix for the registry-agnostic toolchain names.

### Real AICR rendering and Helm packaging

To exercise the remaining branch independently of missing worker toolchains, the
actual generated plan's `tool/aicr-v0.18.0` node acquired and checked the remote
archive (`sha256:6a498b8ce0dcb0e28095de35cdae583391ad567f3922aae6657c851cc76275ee`)
and executable (`sha256:50287efe53d4b0aeb5c36c7ef792b4039509ae8f0ff5078d48369c09305a676f`).
The generated CLI requires workspace `.nkd/factory`, so these nodes ran from the
isolated directory `.nkd/demo-audit-20260909/aicr-probe`.

The `h100-runtime/aicr-render`, `h100-runtime/terminal-observe`, and
`package/gpu-runtime` nodes all passed in the plan's declared Linux/amd64 utility
image `golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161`,
with Docker `--init --network none`, a static Linux CLI, and the repository mounted
at `/repo`. The real generator used the held recipe's private loopback endpoint
and emitted 14 chart folders and 61 files. No deployment was attempted.

Local `publish inspect` then verified the generated Helm layout:

- Diagnostic publication root: `sha256:4c5a7995512e8f3697b9bbc77f48892521534d6c1118abc98de9884a6ba5b1d3`.
- Named AICR bundle semantic digest: `sha256:14381546e02b3dba642222b15c400ad405894e5fad681ff6f778effecd1ecfb9`.
- Terminal tree observation: `sha256:8094b2e765f65a2db95164af44ac90ec49496c90d1bf1821b30b9b794a551735`.
- Logs: `aicr-acquire.log`, `aicr-render.log`, `aicr-observe.log`, `aicr-package.log`, `aicr-inspect.log`.

This establishes actual generator execution and Helm packaging, not complete GPU
worker outputs, cluster realization, upstream chart installation, or release authority.

### Signed consumer prerequisite

Invoking the consumer without inputs returns exit 2 and `--reference is required`.
An invocation against the real local publication, using the installed macOS cosign
and its measured digest, returns `NKD-E-ENGINE-CAPABILITY`: held-executable OCI
verification requires Linux. No verification success was reported.

The script help and demo guide now explicitly require Linux and a static Linux
cosign executable. A positive run still needs the supported GitHub lane's real
signed, digest-pinned publication plus its expected signer identity and issuer.
The repository's placeholder GitHub configuration and local dry-run records cannot
supply those inputs.

## OS-image integration gap

The maintained NKE Definition currently declares only `nkectl`. ADR-10 explicitly
records `wrapped-build` and `embeds` as deferred implementation, and the current
reconciliation record agrees. There is no implemented NKD OS-image demo to certify.

Read-only inspection of the NKE checkout at
`a1a0b5fdf7c0f25ad5c805713868ad8a44f50899` found the upstream entrypoints
`os-image/packer/Makefile` targets `os-image-fde` and `os-image-non-fde`. These
initialize Packer, prepare an installer ISO, and build through QEMU; the Makefile
selects KVM when available and otherwise TCG. This inspection does not establish
a pinned NKD wrapped-build contract or a successful OS-image run. No NKE checkout
was edited and no direct upstream image build was substituted for NKD integration.

The next OS-image demo must wrap that upstream ownership boundary (or explicitly
declare the recorded prebuilt-image fallback), bind exact output bytes and source
lane, and exercise local plus admitted GitHub execution. It must not restore
separate NKD builds of embedded worker components to appear complete.

## Final repository checks

The final code and demo changes passed `go test ./... -count=1` and `make check`
(formatting, vet, golangci-lint, ShellCheck and the race/coverage suite; total
coverage 63.3%). The compatible linter was golangci-lint 2.13.1 built with Go 1.27.
Logs are `unit-complete.log` and `check-complete.log`. `git diff --check` passed.
The compiler regression preserves frozen legacy identities and the full Recipe
digest while deriving only the selected execution closure. Default-name regression
tests exercise actual lifecycle preparation. Operational successes are recorded
separately above; passing contract tests does not establish hosted execution,
OS-image integration, or signed Release consumption.
