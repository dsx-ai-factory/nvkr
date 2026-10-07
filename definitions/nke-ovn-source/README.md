# OVN source-build option

Candidate, selectable CNI producer catalog for the requested
[`dsx-ai-factory/ovn-kubernetes`](https://github.com/dsx-ai-factory/ovn-kubernetes) fork.
It adds an OVN option to PR #113; it does not remove Calico or change the maintained
OS/M2 Definitions. Do not stack it blindly with the old OS catalog: `ovn-kube-ubuntu`
and `nke-cni-ovn` deliberately preserve the existing producer names. A maintained
replacement must use the predecessor-digest-bound catalog overlay contract and
regenerate its dependent inputs.

## Before and after

| Boundary | Existing OS catalog | This candidate option |
|---|---|---|
| Source | Upstream `ovn-kubernetes/ovn-kubernetes`, 1.4.x | DSX fork, exact commit `26cc199de8e814a7fe6d899a770fc04df0bbc2f9` |
| Image | Source-owned upstream Ubuntu Dockerfile | Fork's source-owned `dist/images/Dockerfile.ubuntu` |
| Chart | Upstream `helm/ovn-kubernetes`, 1.4.0 | Fork's checked-in `charts/ovn-kubernetes`, chart version 1.5.18 |
| Debian packaging | Calls NKE `os-image/packages/cni-common` and `cni-ovn` | Offline catalog command using `dpkg-deb` in the digest-pinned compiler image |
| Consumer | `ovn` material is a flat tar of Debian packages | Same package archive shape and package/import paths |

The exact fork commit is a candidate source pin, not an assertion that the fork's
current main is the source of NKE v0.21.0's OVN 1.4.0 release. A rebuilt image has
new bytes and identity. The source lock was generated with:

```sh
bin/nkdctl catalog pin --dir definitions/nke-ovn-source/catalog \
  --commit 26cc199de8e814a7fe6d899a770fc04df0bbc2f9 --timeout 5m ovn-kubernetes
```

## Build and package graph

1. NKD fetches and verifies the exact fork commit/tree on the host. The fork was
   internal when checked on October 7, 2026, so anonymous public CI cannot acquire
   it. Source credentials must not be mounted in the producer containers.
2. `ovn-kube-ubuntu` invokes the pinned rootless BuildKit adapter on the unchanged,
   source-owned Dockerfile. It builds OVN-Kubernetes from the fork's Go source and
   emits one OCI image archive, named
   `localhost/nke/ovn-kubernetes:git-26cc199de8e814a7fe6d899a770fc04df0bbc2f9`.
3. `nke-cni-ovn` consumes that exact image through the factory's held-material
   boundary. It copies the chart from the authenticated fork checkout and packs
   both Debian packages offline. No NKE package-folder command is invoked.

```text
Pinned DSX OVN Git source
  ├─ source-owned Dockerfile → ovn-kube-ubuntu.oci.tar
  └─ checked-in chart + authenticated image material
       → nke-cni-ovn.tar
           ├─ nke-cni-ovn-charts-1.5.18_1+git.26cc199de8e8_<arch>.deb
           └─ nke-cni-ovn-images-1.5.18_1+git.26cc199de8e8_<arch>.deb
```

The chart package installs under `/etc/dgx/ovn-kubernetes`. Its `values-nke.yaml`
overrides all images used by the enabled control-plane/single-node-zone subcharts
to the locally built image, disables NGN configuration-map lookup and does not
require an NVCR pull secret. Cluster API address and pod CIDR remain deployment
inputs. Optional OVS-node, identity and DPU subcharts remain disabled; enabling
them requires separately binding and testing their image/configuration inputs.

The image package installs a deterministic gzip archive and SHA-256 sidecar under
`/usr/lib/nke/cni-ovn/1.5.18`. Its configuration hook checks the sidecar, bounds
containerd readiness and import, then imports the archive into the `k8s.io`
namespace. Import failures fail package configuration.

## Packer interface

This option does not modify Packer. The intended integration is to select this
`nke-cni-ovn` output as the existing image-inputs producer's `ovn` material. The
source-owned NKE composer expands the flat Debian archive into image-inputs.
Packer consumes the resulting authenticated image-inputs archive; it does not
clone Git or build OVN. CNI packages are staged in the OS for subsequent cluster
configuration, not proof that an OVN cluster has been deployed.

The current NKE composer still requires its `calico`, `kubeadm-distro`, `ovn` and
`spire-agent` archive bindings plus the agent package. This PR adds a choice of
OVN producer, not an OVN-only maintained image or optional-material composer.

## Tests and limitations

```sh
go test ./pkg/nkd/buildgraph -run '^TestNKEOVNForkGraphs$' -count=1

# Native Linux with host-side access to the fork, on a dedicated test host:
NKD_OVN_SOURCE_RUNTIME=podman go test -v ./pkg/nkd/buildgraph \
  -run '^TestNKEOVNForkLive$' -count=1 -timeout 70m
```

The graph test resolves both native architectures, checks the source and output
closure, rejects the missing image material, and compiles the complete diagnostic
factory Plan. Ordinary public-source CI runs only this credential-free contract.
The opt-in live test executes the real factory fetch/build operations and its
authenticated material handoff, then independently extracts the Debian packages
with `dpkg-deb` and checks metadata, layout, hook syntax and gzip/sidecar integrity.
It also builds Helm from the existing pinned public-source producer, renders the
packaged chart, rejects old/default image references and compares decompressed
image bytes with the exact source-built OCI export. The
[native ARM64 run](../../docs/evidence/2026-10-07-ovn-fork-source-build.md) passed.
It stops before SBOM, signing and publication operations. Rootful Docker test
ownership and SELinux workspace labeling follow the
[existing diagnostic setup](../nke-os-source/README.md#tests).

This is the standard Ubuntu Dockerfile, **not** the fork's private NVIDIA
FIPS/STIG image flavor. OVS, OVN, kubectl and runtime dependencies inside that
Dockerfile are acquired through APT; they are not compiled from pinned Git by this
option. The image lane has network access, source-owned build stamps and mutable
APT inputs. The catalog logs these limitations. No hermeticity, bit reproduction,
toolchain review approval, complete OS bake, runtime CNI acceptance or Release
acceptance is claimed. The pin/toolchain review and maintained integration gates
remain required.

The fork vendors Kubernetes libraries at 1.36.2. Its deployment compatibility with
all three NKE Kubernetes selections is not established by compilation or Helm
rendering. The container's Ubuntu base is also distinct from the node OS version;
this option does not change the node OS from 24.04 to 26.04.
