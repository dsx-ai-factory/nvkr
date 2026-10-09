# Local NKE development on ARM64

For setup and the complete journey, use the
[Ubuntu ARM64 build-and-demo guide](../../docs/local-nke-demo.md). These inputs
target Linux ARM64; macOS supplies a Colima Linux VM, not Darwin artifacts.
They do not publish, sign or build a complete source-only NKE distribution.

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete demo artifact set | `definition.json` | 29: disk, images, installer-aligned charts/CLI, tools, profiles and simulator in one Run |
| Faster component loop | `definition-components.json` | 22: historical components without the disk; not the complete demo selection |
| OS-only loop | `definition-os.json` | One disk with its complete material closure |
| Source-built provider loop | `definition-providers.json` | Two native provider images, no disk |
| NICoSim build loop | `definition-simulator.json` | One executable bundle: source-built Linux ARM64 server, CLI and pinned seed/UI archive |
| Staged-installer site loop | `definition-site.json` | Four source-aligned candidates: CLI, controller image, image-bound controller chart and NKTP chart |
| Source-built installer tools | `definition-tools.json` | One five-component Linux ARM64 executable bundle; distinct from the acquired `operator-tools` |
| Diagnostic controller profiles | `definition-profiles.json` | One OCI-carried tarball containing plain ARM64/Ubuntu 26.04 profile data; not a runnable image or fleet profiles |

Use `environment-local.json` with rootless Podman, or
`environment-local-docker.json` with native Linux Docker. The OS uses TCG.
The full demo uses one NKD/demo bootstrap and a measured
Packer/Go/GCC toolchain across its selected output scopes; that image also includes
digest-checked Helm 4.2.4 and ORAS 1.3.3. There is no separate Helm-wrapper image.
The combined selection creates one immutable Run. Catalog
overlays remain bound to each exact predecessor digest. `os-tcg.json` depends on the M2
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
acquired `operator-tools` selection remains in the combined artifact membership.

The local OS replaces the upstream OVN producers with PR #113's
[NKE/NVIDIA fork producers](../nke-ovn-source/README.md). Its chart/image Debian
packages and demo tenant select stream `1.5.18`. Packer still consumes staged
materials; it does not fetch the fork or compile OVN. A fresh bake and tenant
acceptance are separate gates from compiling this selection.

The combined selection also produces standalone OVN OCI image and image-bound
Helm OCI outputs. They have their own build identities; do not assume their bytes
equal the disk's embedded image/chart unless original-owner reuse is established.
Local packaging does not enable production publication or signing.

The launcher accepts `ATTEMPT JOBS all`; for example use `first-all 4 all` on a
suitably sized host. In `nke-demo e2e`, use YAML `build.jobs` or `--jobs`.
Independent ready producers and packaging operations run concurrently up to
that limit, while dependencies still gate Packer and publication-shaped packaging.

Profiles contain configuration and runbooks, packaged deterministically as data.
NKD retains their OCI-carried bundle with the completed Run; the consumer uses
ORAS and verified extraction to supply files to NKE's profiles volume. No
profiles Dockerfile, BuildKit producer or runtime init image is needed. The
diagnostic OS profile remains separate from production fleet profiles and does
not authenticate the selected disk or establish tenant compatibility.

See [reconciliation](../../docs/adr/reconciliation.md) for implementation and
test boundaries. Ordinary tests do not execute the full demo; this PR supplies
no durable fresh-build/deploy acceptance record.
