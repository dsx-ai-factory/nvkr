# Local NKE development on ARM64

For setup and the complete journey, use the
[Ubuntu ARM64 build-and-demo guide](../../docs/local-nke-demo.md). These inputs
target Linux ARM64; macOS supplies a Colima Linux VM, not Darwin artifacts.
They do not publish, sign or build a complete source-only NKE distribution.

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete selected set | `definition.json` | 23: disk, images, charts and executable bundles |
| Faster component loop | `definition-components.json` | 22: everything above except the disk |
| OS-only loop | `definition-os.json` | One disk with its complete material closure |
| Source-built provider loop | `definition-providers.json` | Two native provider images, no disk |
| NICoSim build loop | `definition-simulator.json` | One executable bundle: source-built Linux ARM64 server, CLI and pinned seed/UI archive, not included in the 23-output set |
| Staged-installer site loop | `definition-site.json` | Four source-aligned candidates: CLI, controller image, image-bound controller chart and NKTP chart; not included in the 23-output set |
| Source-built installer tools | `definition-tools.json` | One five-component Linux ARM64 executable bundle; separate from the acquired `operator-tools` in `all` |
| Diagnostic controller profiles | `definition-profiles.json` | One OCI-carried tarball containing plain ARM64/Ubuntu 26.04 profile data; separate from `all`, not a runnable image or fleet profiles |

Use `environment-local.json` with rootless Podman, or
`environment-local-docker.json` with native Linux Docker. The OS uses TCG.
The full demo shares its first OS lane's NKD/demo executables and measured
Packer/Go/GCC toolchain across subsequent lanes; that image also includes
digest-checked Helm 4.2.4 and ORAS 1.3.3. There is no separate Helm-wrapper image.
Each lane still creates its own immutable Run and authenticates its catalog
overlay against the exact predecessor digest. `os-tcg.json` depends on the M2
OS catalog's digest: repinning that catalog requires reviewing this binding.

```sh
bin/nkdctl definition validate --definition definitions/nke-local-dev-arm64/definition.json
bin/nkdctl run local \
  --definition definitions/nke-local-dev-arm64/definition.json \
  --environment definitions/nke-local-dev-arm64/environment-local.json \
  --attempt arm64-preview --dry-run

NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-components-01 1 components

# No host Go, Make or installed nkdctl needed for the launcher:
bash scripts/nke-full-arm64-local.sh --check providers
CONTAINER_RUNTIME=docker bash scripts/nke-full-arm64-local.sh docker-providers-01 1 providers

# Build simulator tools separately; this does not deploy a demo.
NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh arm64-simulator-01 1 simulator
```

Use a fresh attempt name. `NKE_LOCAL_CLONE` supplies pinned Git objects, not
uncommitted checkout bytes; omit it for canonical remote fetch. Compilation can
require authenticated Git access because NKE chart pin sets are checked against
the pinned tree. Dry-run/preflight are not runtime acceptance.

The site catalog pins CLI, controller and charts to one exact NKE candidate;
chart dependencies are acquired at exact archive digests before offline packaging,
not independently source-built or upstream-signature-verified. The tools catalog
compiles the installer's five Linux binaries from pinned Git. The separate older
acquired `operator-tools` selection is unchanged.

Profiles contain configuration and runbooks, packaged deterministically as data.
NKD retains their OCI-carried bundle with the completed Run; the consumer uses
ORAS and verified extraction to supply files to NKE's profiles volume. No
profiles Dockerfile, BuildKit producer or runtime init image is needed. The
diagnostic OS profile remains separate from production fleet profiles and does
not authenticate the selected disk or establish tenant compatibility.

See [reconciliation](../../docs/adr/reconciliation.md) for the exact recorded
results. The reduced shared-toolchain/profile-data flow still needs a fresh
end-to-end run; older retained-artifact success does not validate these changes.
