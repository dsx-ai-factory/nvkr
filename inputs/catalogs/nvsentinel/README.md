# NVSentinel development catalog

This catalog selects four output families from `NVIDIA/NVSentinel` at
`c1593477357299ab93dd5246f5b2cd236f8f07c8`, using the existing NVKR handlers:

| Selection | Outputs |
| --- | --- |
| `binaries-{amd64,arm64}.yaml` | 19 CGO-free Go executables; syslog/systemd and CUDA metadata-collector executables are explicitly excluded |
| `images-{amd64,arm64}.yaml` | 28 OCI images: 18 Go programs and 10 original Dockerfile variants |
| `image-{amd64,arm64}.yaml` | The platform-connectors OCI image only |
| `helm-{amd64,arm64}.yaml` | One unchanged upstream Helm chart |
| `debian-ubuntu-{24.04,26.04}-{amd64,arm64}.yaml` | The platform-connectors DEB only |

These are development inputs. Static Plans describe selected work; they do not establish fresh product
builds, installation, deployment, qualification, signing or publication. Current support and the retired
recipes' dated evidence are recorded in [reconciliation](../../../docs/adr/reconciliation.md#nvsentinel-catalog-and-package-inputs).

## Debian package

Two ordinary `host-package` producers package platform-connectors for Ubuntu 24.04 and 26.04, each
with explicit AMD64 and ARM64 selections. The sources `nvsentinel` and `nvsentinel-ubuntu2604` select
those distributions' existing SDK images. The original platform-connectors-only vendor command prepares
the source closure; the producer compiles offline with CGO disabled and creates the DEB with Ubuntu's
`dpkg-deb`. All staging stays under `nvkr-output`; no upstream source or build script is rewritten.

The archive contains `/usr/lib/nvsentinel/bin/platform-connectors`, the upstream license and control
metadata. Its human-approved local metadata is `Maintainer: Ilya Kislenko <ikislenko@nvidia.com>` and
`Version: 0~git20261013.c159347735`, without an upstream maintainer or release claim. It adds no
systemd unit, preset, maintainer script, runtime configuration or credential.

```sh
bin/nvkr run plan --definition inputs/definitions/nvsentinel/debian-ubuntu-26.04-arm64.yaml \
  --environment inputs/environments/nvsentinel/ubuntu-26.04-arm64.yaml --json
bin/nvkr run local --definition inputs/definitions/nvsentinel/debian-ubuntu-26.04-arm64.yaml \
  --environment inputs/environments/nvsentinel/ubuntu-26.04-arm64.yaml \
  --attempt ubuntu26-arm64-example --runtime docker
```

Local execution requires a matching native Linux host and the selected tool images. Ordinary
producer-result authentication and lifecycle staging retain the raw package. This route supplies no
package-content inspection, dependency SBOM, standalone production predicate or installation proof.
The read-only local archive is retained at `<attempts-root>/<attempt>/outputs/<output-name>.deb`;
the plain result prints `package:` with that location. Its Artifact transport reference stays empty,
and the local output remains unqualified, unsigned and unpublished.
The [toolchain guide](../../../toolchains/nvsentinel-debian/README.md) owns SDK construction and local
image lookup limits; native AMD64 package execution remains unverified.

## OS material placement

The [OS experiment](../../experiments/nvsentinel/README.md) keeps separate, byte-identical copies of
the Debian producer commands, with matching declarations, source blocks and pins. The offline catalog
regression checks those copies and all Definition/Environment pairs together.
Its command invokes the pinned NVKE composer unchanged, extracts its base archive into its own output
tree, copies the bound DEB into `debs/nvsentinel-platform-connectors.deb`, checks architecture and
size/count budgets, regenerates `debs/Packages` with `dpkg-scanpackages --multiversion`, and creates a
deterministic final archive. It also copies the two existing source network configurations omitted by
NVKE `115d07c8`'s composer. No upstream Python, shell file or provisioner is patched.

Packer already stages all `debs/*.deb` into `/usr/lib/nvke/debs` and registers that local APT repository.
Placement makes the package available; it does not install arbitrary packages or start services. The
corrected inputs make no NVSentinel installation claim. Ubuntu 24.04's separate NVKE-agent CGO builder
still uses Ubuntu 26.04; its guest ABI compatibility remains unresolved. AMD64 OS execution requires
native KVM; ARM64 retains the existing TCG development route. OS target/evidence acceptance is deferred.

## Executables and OCI images

The binary alias `nvsentinel-binaries` vendors the enabled modules with the pinned Go 1.27.0 SDK before
each ordinary producer compiles offline. Original main packages, CSP entrypoints and Slinky linker flags
are retained. The excluded syslog and metadata-collector programs require unverified native library,
systemd or CUDA closure; their separate source image variants remain selectable.

All OCI selections use the existing rootless BuildKit wrapped-build backend. The 18 Go image commands
stage unchanged source modules and local replacements into a private context. Their catalog-authored
Dockerfile downloads, verifies and vendors modules using pinned native Go SDK images, checks the main
module pins, then compiles with `RUN --network=none` into a pinned static runtime image. This is a
networked wrapped build, without a hermetic or exact-byte reproduction claim. The `nvsentinel-content`
alias has no fetch-time vendor command. The other 10 variants invoke unchanged upstream Dockerfiles;
their APT/pip resolution, tag-selected bases and downloads retain source-owned reproducibility and
security limits. No patched ko supplier is used.

```sh
bin/nvkr run plan --definition inputs/definitions/nvsentinel/binaries-arm64.yaml \
  --environment inputs/environments/nvsentinel/image-arm64.yaml --json
bin/nvkr run plan --definition inputs/definitions/nvsentinel/images-arm64.yaml \
  --environment inputs/environments/nvsentinel/image-arm64.yaml --json
```

## Upstream Helm chart

The chart-only Definitions archive the pinned source chart, values, templates and bundled dependencies
unchanged. Its templates concatenate repository and tag and have no image-digest field, so its upstream
image references do not select the separately built OCI outputs. Complete image closure and deployment
remain consumer work. The [inert image and Helm proposals](../../../docs/proposals/nvsentinel/README.md)
are retained only for discussion and have no executable catalog references.

```sh
bin/nvkr run plan --definition inputs/definitions/nvsentinel/helm-arm64.yaml \
  --environment inputs/environments/nvsentinel/image-arm64.yaml --json
```

All four NVSentinel source aliases share the same commit, tree and source epoch. Review them together
when updating source; write locks through the pin owner in both catalog directories. The OS experiment
copies only the two Debian source aliases. After repinning, update the experiment's copied resolved
producers and predecessor-bound OS overlays, then run `TestNVSentinelCatalogInputs`:

```sh
for directory in inputs/catalogs/nvsentinel inputs/experiments/nvsentinel/catalog; do
  for source in nvsentinel nvsentinel-ubuntu2604 nvsentinel-binaries nvsentinel-content; do
    if [ "$directory" = inputs/experiments/nvsentinel/catalog ]; then
      case "$source" in nvsentinel-binaries|nvsentinel-content) continue ;; esac
    fi
    bin/nvkr catalog pin --dir "$directory" \
      --commit c1593477357299ab93dd5246f5b2cd236f8f07c8 --timeout 5m "$source"
  done
done
```
