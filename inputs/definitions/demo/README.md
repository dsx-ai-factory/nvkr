# Local NVKE development on ARM64

For setup and the complete journey, use the
[Ubuntu ARM64 build-and-demo guide](../../../docs/local-nvke-demo.md). These inputs
target Linux ARM64 artifact builds. Native host tools have separate OS selections
in the shared tool catalog; Mac deployment does not use a Colima execution host.
They do not publish, sign or build a complete source-only NVKE distribution.
The separate KVM variants have their own hosted publication policies; see the
[NVIDIA nightly](nightly-nvidia.md) and [upstream nightly](nightly-upstream.md) instructions.

| Selection | Definition | Outputs |
| --- | --- | --- |
| Complete demo artifact set | `arm64-nvidia-tcg.json` | 32: disk, images, installer-aligned charts/CLI, tools, profiles and simulator in one Run |
| Explicit upstream OVN diagnostic | `arm64-upstream-tcg.json` | 32: replaces the disk, OVN image/chart and profiles outputs together; not NVIDIA-fork validation |
| Faster component loop | `../diagnostics/demo-arm64/components.json` | 22: historical components without the disk; not the complete demo selection |
| OS-only loop | `../diagnostics/demo-arm64/os.json` | One disk with its complete material closure |
| Source-built provider loop | `../diagnostics/demo-arm64/providers.json` | Two native provider images, no disk |
| NICoSim build loop | `../diagnostics/demo-arm64/simulator.json` | One executable bundle: source-built Linux ARM64 server, CLI and pinned seed/UI archive |
| Installer site loop | `../diagnostics/demo-arm64/site.json` | Five source-aligned candidates: CLI, controller image, native diagnostic profiles image, image-bound controller chart and NKTP chart |
| Source-built installer tools | `../diagnostics/demo-arm64/tools.json` | One five-component Linux ARM64 executable bundle; distinct from the acquired `operator-tools` |
| Diagnostic controller profiles | `../diagnostics/demo-arm64/profiles.json` | One native Linux ARM64 OCI image containing plain ARM64/Ubuntu 26.04 profile data at `/profiles`; not fleet profiles |

Use `../../environments/local-arm64-tcg.json` with native Linux ARM64 Docker, the
launcher's default runtime. The OS image build uses TCG. Standard deployment
uses the same native owner on both hosts: KVM on Linux and explicit TCG on Mac
for the selected 64 KiB image. Mac builds prepare installed native dependencies
through NVKR and build the Linux utility image through Docker Buildx; follow the
[native guide](../../../docs/local-nvke-demo.md#native-linux-and-mac-path) for
prerequisites and current validation limits.
The full demo uses one NVKR/demo bootstrap and a measured
Packer/Go/GCC toolchain across its selected output scopes; that image also includes
digest-checked Helm 4.2.4 and ORAS 1.3.3. There is no separate Helm-wrapper image.
The combined selection creates one immutable Run. Catalog
overlays remain bound to each exact predecessor digest. `../../catalogs/node-images/variants/nvidia-tcg.json` depends on the M2
OS catalog's digest: repinning that catalog requires reviewing this binding.
The upstream selection stacks `../../catalogs/node-images/variants/upstream-tcg.json` after `../../catalogs/node-images/variants/nvidia-tcg.json`;
its `requiresCatalogDigest` binds that exact predecessor, not the base catalog
alone. Regenerate and review both bindings after changing either predecessor.

`arm64-upstream-tcg.json` substitutes these four outputs as one matched
diagnostic selection; the other 28 output names remain unchanged:

| Output | Format | Role |
| --- | --- | --- |
| `nvke-node-image-upstream-ovn-arm64` | `disk-image` | QCOW2 with the selected upstream OVN packages |
| `upstream-ovn-image-arm64` | `oci` | Standalone upstream OVN image |
| `upstream-ovn-chart-arm64` | `helm-oci` | Chart bound to the standalone image |
| `controller-profiles-upstream-ovn` | `oci` | Profiles selecting the matching upstream chart stream |

Select all four together for the upstream demo. These output bindings do not
establish NVIDIA-fork compatibility or completed tenant acceptance.

```sh
bin/nvkr definition validate --definition inputs/definitions/demo/arm64-nvidia-tcg.json
bin/nvkr run local \
  --definition inputs/definitions/demo/arm64-nvidia-tcg.json \
  --environment inputs/environments/local-arm64-tcg.json \
  --attempt arm64-preview --dry-run

NVKE_LOCAL_CLONE=/absolute/path/to/nvke \
  bash scripts/nvke-full-arm64-local.sh arm64-components-01 1 components

# No host Go, Make or installed nvkr needed for the launcher:
bash scripts/nvke-full-arm64-local.sh --check providers
bash scripts/nvke-full-arm64-local.sh arm64-providers-01 1 providers

# Build simulator tools separately; this does not deploy a demo.
NVKE_LOCAL_CLONE=/absolute/path/to/nvke \
  bash scripts/nvke-full-arm64-local.sh arm64-simulator-01 1 simulator
```

Use a fresh attempt name. `NVKE_LOCAL_CLONE` supplies pinned Git objects, not
uncommitted checkout bytes; omit it for canonical remote fetch. Compilation can
require authenticated Git access because NVKE chart pin sets are checked against
the pinned tree. Dry-run/preflight are not runtime acceptance.

The site catalog pins CLI, controller and charts to one exact NVKE candidate;
the installer and every NVKE source, including SPIRE, select v0.22.0 at
`400c24fb6465b042be883d6b62472528520d6291`. The demo uses its site-spec renderer
and deployment entrypoint with verified local charts and image digests. Tenant
setup creates MachineImage, ClusterRelease and NodePoolRelease resources from
the observed profiles container and selected disk before requesting the tenant.
Chart dependencies are acquired at exact archive digests before offline packaging,
not independently source-built or upstream-signature-verified. The tools catalog
compiles the installer's five Linux binaries from pinned Git. The separate older
acquired `operator-tools` selection remains in the combined artifact membership.

The local OS replaces the upstream OVN producers with PR #113's
[NVKE/NVIDIA fork producers](../../catalogs/ovn-nvidia/README.md). Its chart/image Debian
packages and demo tenant select stream `2.4.0`. Packer still consumes staged
materials; it does not fetch the fork or compile OVN. A fresh bake and tenant
acceptance are separate gates from compiling this selection.

The shared OS catalog selects NVKE `v0.22.0` for every NVKE producer, including SPIRE
packaging revision `4`. That source-owned package excludes DSX IMDS and requires no DSX
build credentials. The updated inputs do not establish a new runtime acceptance result.

The combined selection also produces standalone OVN OCI image and image-bound
Helm OCI outputs. They have their own build identities; do not assume their bytes
equal the disk's embedded image/chart unless original-owner reuse is established.
Local packaging does not enable production publication or signing.

The launcher accepts `ATTEMPT JOBS all`; for example use `first-all 4 all` on a
suitably sized host. In `nvke-demo e2e`, use YAML `build.jobs` or `--jobs`.
Independent ready producers and packaging operations run concurrently up to
that limit, while dependencies still gate Packer and publication-shaped packaging.

Profiles contain configuration and runbooks in a native OCI image built with
the pinned NVKE source-owned `controller/profiles/example/Dockerfile` and the
existing rootless BuildKit lane. Its Alpine 3.24.1 tag is overridden through a
BuildKit named context with the existing digest-pinned ARM64 Alpine 3.24.2
runtime. No new Dockerfile or upstream source mutation is needed. Only the
authenticated local OS profile, two pinned NICoSim configuration profiles and
pinned fixture runbooks enter the image. NVKE's chart uses `/bin/sh` and
`cp -a /profiles/. /profiles-out/` in its standard profiles init container;
the app mounts the shared profiles volume read-only. The diagnostic OS profile
remains separate from production fleet profiles and does not authenticate the
selected disk or establish tenant compatibility. Local image construction is
a networked wrapped build, not qualified execution or publication authorization.

See [reconciliation](../../../docs/adr/reconciliation.md) for implementation and
test boundaries. Ordinary tests do not execute the full demo; a fresh complete
build/deploy/cleanup acceptance record remains required.
