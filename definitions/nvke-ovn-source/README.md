# OVN source-build option

Candidate, selectable CNI producer catalog for the
`[dsx-ai-factory/ovn-kubernetes](https://github.com/dsx-ai-factory/ovn-kubernetes)` fork.
It does not remove Calico or change the maintained
OS/M2 Definitions. Do not stack it blindly with the old OS catalog: `ovn-kube-ubuntu`
and `nvke-cni-ovn` are authored component names; packaged Debian names remain source-owned. A maintained
replacement must use the predecessor-digest-bound catalog overlay contract and
regenerate its dependent inputs.

## Before and after


| Boundary         | Existing OS catalog                                     | This candidate option                                                        |
| ---------------- | ------------------------------------------------------- | ---------------------------------------------------------------------------- |
| Source           | Upstream `ovn-kubernetes/ovn-kubernetes`, 1.4.x         | DSX fork `release-2.4`, exact commit `12ca20e67b516f1a023da85a22deeaf8d2d70d0b` |
| Image            | Source-owned upstream Ubuntu Dockerfile                 | Fork's source-owned `dist/images/Dockerfile.ubuntu.nv`                       |
| Chart            | Upstream `helm/ovn-kubernetes`, 1.4.0                   | Fork's checked-in `charts/ovn-kubernetes`, chart version 2.4.0               |
| Debian packaging | Calls NVKE `os-image/packages/cni-common` and `cni-ovn` | Offline catalog command using `dpkg-deb` in the digest-pinned compiler image |
| Consumer         | `ovn` material is a flat tar of Debian packages         | Same package archive shape and package/import paths                          |


The exact fork commit is a candidate source pin, not an assertion that the fork's
current main is the source of NVKE v0.21.0's OVN 1.4.0 release. A rebuilt image has
new bytes and identity. The source lock was generated with:

```sh
bin/nvkr catalog pin --dir definitions/nvke-ovn-source/catalog \
  --commit 12ca20e67b516f1a023da85a22deeaf8d2d70d0b --timeout 5m ovn-kubernetes
```



## Build and package graph

1. NVKR fetches and verifies the exact fork commit/tree on the host. The fork was
  internal when checked on October 7, 2026, so anonymous public CI cannot acquire
   it. Source credentials must not be mounted in the producer containers.
2. `ovn-kube-ubuntu` invokes the pinned rootless BuildKit adapter on the unchanged,
  source-owned Dockerfile. It builds OVN-Kubernetes from the fork's Go source and
   emits one OCI image archive, named
   `localhost/nvke/ovn-kubernetes:git-12ca20e67b516f1a023da85a22deeaf8d2d70d0b`.
3. `ovn-kubernetes-chart` archives the checked-in chart and its checked-in subcharts
  offline. `nvke-cni-ovn` consumes both that exact chart archive and image through
   the factory's held-material boundary and packs both Debian packages offline.
   No NVKE package-folder command is invoked.

At this pin the Dockerfile selects its own builder, `golang:1.26` bound to a
digest, and never declares or reads a `BUILDER_IMAGE` argument. The catalog still
passes one, but it cannot change the produced bytes; the effective builder is
source-owned and changes only with the pin. The fork's vendored JSON adapter uses
APIs removed in Go 1.27, so that Go 1.26 builder is a source requirement rather
than a catalog choice. NVKR/demo tooling remains on
Go 1.27.2; no host Go downgrade, source patch or custom builder is required.

```text
Pinned DSX OVN Git source
  ├─ source-owned Dockerfile → ovn-kube-ubuntu.oci.tar
  └─ checked-in chart → ovn-kubernetes-chart.tar
       + authenticated image material
       → nvke-cni-ovn.tar
           ├─ nke-cni-ovn-charts-2.4.0_1+git.12ca20e67b51_<arch>.deb
           └─ nke-cni-ovn-images-2.4.0_1+git.12ca20e67b51_<arch>.deb
```

The chart package installs under `/etc/dgx/ovn-kubernetes`. Its `values-nke.yaml`
overrides all images used by the enabled control-plane/single-node-zone subcharts
to the locally built image, skips the chart's Kubernetes lookup and does not
require an NVCR pull secret. The pinned chart no longer ships `values-nke.yaml` or
`values-images.yaml`, so this one generated file carries both the NKE settings and
the image bindings. Cluster API address and pod CIDR remain deployment
inputs. Optional OVS-node, identity and DPU subcharts remain disabled; enabling
them requires separately binding and testing their image/configuration inputs.

At this pin the chart ships `values-single-node-zone.yaml` as a link to
`values.yaml`, which image binding rejects because a link into its own write
target could alias that write. Copying it instead would freeze the source image
digests that binding later replaces in `values.yaml`, so installing with
`-f values-single-node-zone.yaml` would merge those source digests back over the
bound ones and deploy a different image. The chart producer therefore withdraws
the alias: the packaged chart has no `values-single-node-zone.yaml`, and the
fork's documented `-f` install fails on a missing file rather than silently
selecting the fork's own published image. Use the chart's bound root values.

The image package installs a deterministic gzip archive and SHA-256 sidecar under
`/usr/lib/nke/cni-ovn/2.4.0`. Its configuration hook checks the sidecar, bounds
containerd readiness and import, then imports the archive into the `k8s.io`
namespace. Import failures fail package configuration.

These packages select stream `2.4.0` and Debian version `1+git.12ca20e67b51`,
not the pinned NVKE agent's default stream `1.4.0`. `InstallCNI.version=2.4.0`
selects the existing stream-suffixed chart package; its logical `=2.4.0` suffix
is informational, not an enforced Debian revision. InstallCNI strips that
suffix before checking package presence and never runs APT. Install the exact
package names with `=1+git.12ca20e67b51` separately, for example through
InstallPackages, then validate chart deployment. These packages are not a
drop-in for an empty-version request using the default stream. See the
[pinned InstallCNI contract](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/agent/pkg/agent/proto/node_agent.proto#L525-L545)
and its [package-presence implementation](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/agent/internal/agent/cni.go#L826-L875).

## Native image and Helm OCI outputs

[definition.yaml](definition.yaml) declares four independent deliverables:


| Output            | Platform      | Delivery                                |
| ----------------- | ------------- | --------------------------------------- |
| `ovn-image-arm64` | `linux/arm64` | Native container image                  |
| `ovn-image-amd64` | `linux/amd64` | Native container image                  |
| `ovn-chart-arm64` | `linux/arm64` | Helm OCI chart bound to the ARM64 image |
| `ovn-chart-amd64` | `linux/amd64` | Helm OCI chart bound to the AMD64 image |


Each chart package depends on its corresponding image package. The existing
generic Helm binder authenticates that image and writes its manifest SHA-256 hex
into the six enabled image slots. `values.digestFormat: sha256-hex` is explicit
because the fork's templates supply `sha256:` themselves. Templates, repositories
and tags remain upstream-owned. Installers must set `global.useImageDigest=true`
and override the six repository values to the repository holding that image,
without replacing the bound `sha256` values. Enabling other subcharts needs
additional image bindings and acceptance.

Choose the matching architecture's chart and image repository together. Private
GHCR requires independently provisioned Helm and node pull credentials; clearing
`imagePullSecretName` is appropriate only when the node already has registry
access or uses locally imported bytes. API endpoint and pod CIDR remain deployment
inputs. Deployment overrides are not enforcement of the signed chart's defaults.

[environment-github.yaml](environment-github.yaml) uses the existing native OCI/Helm
publisher, SBOM/provenance and signing flow, not a separate OVN push script. Routes
are `ghcr.io/dsx-ai-factory/nvkr/m2-candidate/<output-name>@sha256:<digest>`.
Helm also receives its chart-version discovery tag; digest references and signed
Release membership remain authoritative. These are configured routes, not proof
that those OVN outputs have been pushed.

Generate and check the candidate in a disposable output root from the repository
root, without replacing the maintained M2 workflow:

```sh
ovn_bundle_root=$(mktemp -d /tmp/nvkr-ovn-bundle.XXXXXX)
go run ./cmd/nvkr run generate \
  --definition definitions/nvke-ovn-source/definition.yaml \
  --environment definitions/nvke-ovn-source/environment-github.yaml \
  --engine github --root "$ovn_bundle_root"
go run ./cmd/nvkr run generate \
  --definition definitions/nvke-ovn-source/definition.yaml \
  --environment definitions/nvke-ovn-source/environment-github.yaml \
  --engine github --root "$ovn_bundle_root" --check
```

Production execution requires installing that generated bundle in an admitted
consumer checkout. The generator owns one `nvkr-run.yml` per root; this candidate does
not replace the maintained M2 bundle. Dispatch remains default-branch-only,
with the existing protected `nkd-release` environment and main signer policy.
`NKE_SOURCE_TOKEN` must separately have read access to the internal OVN fork;
secret existence does not prove that access. Registry write and OIDC tokens stay
out of producer containers. Ordinary PR tests compile and regenerate without them.

**Packer handoff is separate.** The Debian diagnostic shares its source chart and
built image within one output, but adding an OS output alongside these deliverables
does not reuse their producers automatically. It also does not stage the final
digest-bound published chart. [Issue #116](https://github.com/dsx-ai-factory/nvkr/issues/116)
records the generic original-owner cross-output handoff required to build once,
publish, and feed those same image/chart bytes into Debian/image-inputs/Packer.
Independent rebuilds are not evidence of same bytes. This Definition contains
only OVN image/chart outputs, not OS outputs.

## Packer interface

This option does not modify Packer. The intended integration is to select this
`nvke-cni-ovn` output as the existing image-inputs producer's `ovn` material. The
source-owned NVKE composer expands the flat Debian archive into image-inputs.
Packer consumes the resulting authenticated image-inputs archive; it does not
clone Git or build OVN. CNI packages are staged in the OS for subsequent cluster
configuration, not proof that an OVN cluster has been deployed.

### NVKE with OVN, including both OS images

The following is an integration map, **not an executable OVN-enabled OS
Definition or a completed OS test**. It combines the candidate output with the
existing maintained producer names in `definitions/nvke-m2/os-catalog/registry.yaml`:

```text
For each native platform: linux/arm64 and linux/amd64
  DSX fork → ovn-kube-ubuntu → nvke-cni-ovn.tar  [candidate producer]
                                  │ bind as material "ovn"
  nvke-agent ───────────────────────┤
  nvke-kubeadm-distro ──────────────┤
  nvke-spire-agent ─────────────────┤
  nvke-cni-calico ──────────────────┤  still required by current composer
                                  ▼
                         nvke-image-inputs → image-inputs.tar
                                  │
  native patched installer ISO ───┤
                                  ▼
                    source-owned Packer → non-fde/nke-node.qcow2
                                  │
                       boot + agent CNI/version selection
                                  ▼
                             OVN workload cluster
```


| Native architecture | Existing OS producer    | Existing installer producer        | Candidate OVN package architecture |
| ------------------- | ----------------------- | ---------------------------------- | ---------------------------------- |
| ARM64               | `nvke-node-image`       | `nvke-patched-installer-iso`       | `arm64`                            |
| AMD64               | `nvke-node-image-amd64` | `nvke-patched-installer-iso-amd64` | `amd64`                            |


Both existing OS producers select `nke-node-non-fde.qemu.nke-non-fde`. The
material manifest schema is `nke.dev/os-image-materials/v1`; its inputs are
`image-inputs` (archive) and `installer-iso` (disk image). NVKR supplies exact
authenticated files and the source-owned Packer interface consumes them. Packer
does not choose the Git repository, download OVN or compile its binaries.

To make this map runnable, replace the old OVN producers using a catalog overlay
bound to the exact predecessor digest, regenerate matching toolchain/Plan inputs,
and verify composer/package compatibility plus fresh OS bakes on **both** native
architectures. Boot/import and agent CNI selection require separate acceptance;
the existing image may also contain Calico packages without deploying Calico.
The current fork's cluster-time configuration/version compatibility is not yet
proven. Publication/signing follows the selected Environment's existing OCI route,
not this producer diagnostic.

The current NVKE composer still requires its `calico`, `kubeadm-distro`, `ovn` and
`spire-agent` archive bindings plus the agent package. This catalog adds a choice of
OVN producer, not an OVN-only maintained image or optional-material composer.

## Tests and limitations

The OVN-specific Go tests below are regression coverage for this NVIDIA-supplied catalog. They are not
an NVKR extension or a requirement for adding another catalog. Catalogs using existing NVKR capabilities
need declarations and pins, not component-specific Go code. These tests exercise the generic compiler
and executor and check this catalog's packaging and chart behavior.

The generic native Linux live test also accepts `NVKR_OVN_SOURCE_RUNTIME=podman`; this does not restore the removed macOS/Podman disk-build route or change the Docker-only demo launcher.

```sh
go test ./pkg/nvkr/buildgraph -run '^TestNVKEOVNFork(Graphs|ReleaseOutputs)$' -count=1

# Native Linux with rootful Docker and host-side fork access on a dedicated test host:
ovn_source_test_dir="$(mktemp -d)"
go test -c -o "$ovn_source_test_dir/nvke-ovn-source.test" ./pkg/nvkr/buildgraph
cd pkg/nvkr/buildgraph
sudo --non-interactive env NVKR_OVN_SOURCE_RUNTIME=docker "$ovn_source_test_dir/nvke-ovn-source.test" \
  -test.run '^TestNVKEOVNForkLive$' -test.v -test.timeout 70m
```

The graph tests resolve both native architectures, check the source and output
closure, reject missing chart/image materials, and compile the diagnostic and
four-output publication Plans. They verify all six native Helm bindings,
cross-platform/missing-slot failures, publication-independent Build identity,
ordinary native hosted routing and byte-exact bundle regeneration.
Pin-binding tests cross-check the catalog's image tag, Debian revision, chart
stream and import paths against the reviewed lock and stream. Re-pinning requires
updating these declarations together. A local BuildKit argument fixture checks
both architectures and the explicit source epoch without building an image.
Ordinary public-source CI runs only these credential-free contracts.
The opt-in `TestOVNForkHelmDigestRendering` accepts
`NVKR_OVN_CHART_DIRECTORY=/path/to/pinned-fork/charts/ovn-kubernetes`. It renders
the actual unchanged subchart templates against a controlled digest for two
repository overrides, packages the bound chart as OCI, pushes to a temporary
loopback registry and verifies byte-identical real Helm pull. It does not prove
production GHCR publication or correspondence with a source-built image.
The opt-in live test executes the real factory fetch/build operations and its
authenticated material handoff, then independently extracts the Debian packages
with `dpkg-deb` and checks package name, architecture, version and maintainer,
layout, hook syntax and gzip/sidecar integrity.
It also builds Helm from the existing pinned public-source producer, renders the
packaged chart, rejects old/default image references and compares decompressed
image bytes with the exact source-built OCI export.
It stops before SBOM, signing and publication operations. These opt-in checks
do not establish production publication, OS integration or Release acceptance.
Rootful Docker test
ownership and SELinux workspace labeling follow the
[existing diagnostic setup](../nvke-os-source/README.md#tests).

This is the fork's NVIDIA image flavor, `dist/images/Dockerfile.ubuntu.nv`, with
the `BASE_IMAGE` pinned by digest to public `docker.io/library/ubuntu:24.04`.
Options `OVS_SOURCE=doca` and `DOCA_RELEASE=latest` install Mellanox DOCA based
Open vSwitch. OVN (`OVN_SOURCE=uca`), kubectl and runtime dependencies inside
that Dockerfile are acquired through APT.
The image lane has network access, source-owned build stamps and mutable
APT inputs. The image producer logs these limitations; this is diagnostic text,
not an authenticated exception or evidence waiver. The Debian route selects the
locally imported image by `git-<commit>` tag (`global.useImageDigest=false`), not
by manifest digest. A rebuild can assign different bytes to that tag and to the
same Debian version. Do not treat either as exact content identity; the separate
Helm OCI delivery route uses the authenticated digest binding described above.
The source epoch is passed explicitly to BuildKit's timestamp rewrite, but does
not freeze APT inputs or establish byte reproduction. No hermeticity, bit reproduction,
toolchain review approval, complete OS bake, runtime CNI acceptance or Release
acceptance is claimed. The pin/toolchain review and maintained integration gates
remain required.

The fork vendors Kubernetes libraries at 1.36.2. Its deployment compatibility with
all three NVKE Kubernetes selections is not established by compilation or Helm
rendering. The container's Ubuntu base is also distinct from the node OS version;
this option does not change the node OS from 24.04 to 26.04.