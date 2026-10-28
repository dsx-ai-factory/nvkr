# Build a node image from component materials

A disk output combines different kinds of inputs into one VM image. In the plain-image NVKE example below,
producers build an agent Debian package, networking archives, an OCI image archive and a chart archive;
another producer acquires an installer ISO. Source-owned commands prepare those files for Packer, which
creates the final unencrypted QCOW2 disk. Selecting these components does not automatically install or
unpack them.

## Select the root and every material

This is the complete [ARM64 KVM Definition](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/diagnostics/os-arm64.yaml):

```yaml
apiVersion: nvkr.dev/definition/v5
kind: Definition
metadata:
  name: nvke-os-arm64-kvm
  description: Build the ARM64 non-FDE NVKE node QCOW2 on a native KVM host.
catalog:
  base: ../../catalogs/node-images/diagnostic
  overlays:
    - ../../catalogs/node-images/variants/diagnostic-arm64-kvm.yaml
outputs:
  - name: nvke-node-image-arm64
    platform: linux/arm64
    format: disk-image
    components:
      - nvke-agent
      - nvke-cni-calico
      - nvke-cni-ovn
      - nvke-image-inputs
      - nvke-kubeadm-distro
      - nvke-node-image
      - nvke-patched-installer-iso
      - nvke-spire-agent
      - ovn-kube-ubuntu
      - ovn-kubernetes-chart
      - ubuntu-installer-iso
```

`nvke-node-image` is the disk-producing root. NVKR requires exactly that root and all its direct and
transitive material producers. The explicit list repeats information reachable through the catalog graph:
today this is a selection consistency check. Missing dependencies, unrelated extras and multiple roots
are errors. **Selecting only the root does not expand its dependencies.**

## Connect producers by exact names

The [catalog](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/catalogs/node-images/diagnostic/registry.yaml) declares these `materials` mappings:

| Consuming component | Binding name → supplying component |
|---|---|
| `nvke-node-image` | `image-inputs → nvke-image-inputs`; `installer-iso → nvke-patched-installer-iso` |
| `nvke-image-inputs` | `agent → nvke-agent`; `calico → nvke-cni-calico`; `kubeadm-distro → nvke-kubeadm-distro`; `ovn → nvke-cni-ovn`; `spire-agent → nvke-spire-agent` |
| `nvke-cni-ovn` | `ovn-chart → ovn-kubernetes-chart`; `ovn-image → ovn-kube-ubuntu` |
| `nvke-patched-installer-iso` | `installer-source → ubuntu-installer-iso` |

The left side is the consumer's input label; the right side is an exact catalog component key.
Changing an output's selection cannot redirect these references. Update the consuming producer too,
or use a compatible overlay behind the existing key. See [catalogs and components](catalogs-and-components.md).

## Hand checked files to the source-owned build

During execution, NVKR checks material bytes against their producer observations, stages them read-only,
and supplies a manifest recording each binding's component, architecture, digest and staged path.
Ordinary material-consuming commands read files under `/nvkr/inputs/materials/`.

The NVKE composer understands the package/archive contents and creates `image-inputs.tar`. The Packer
project understands that archive and the patched ISO. Its declared manifest projection labels those
inputs `archive` and `disk-image` without changing their component identities, digests or paths.
NVKR provides the checked handoff; the source-owned build defines how those formats become a disk.

## Inspect without starting a VM

After [building the CLI](quickstart.md#build-only-the-cli), run from the repository root. These commands need no Docker,
KVM or Packer execution:

```sh
NVKR_OS_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-os.XXXXXX")
bin/nvkr run plan \
  --definition inputs/definitions/diagnostics/os-arm64.yaml \
  --environment inputs/environments/local-linux-arm64-kvm.yaml \
  --json > "$NVKR_OS_DIR/plan.json"
bin/nvkr run graph --plan "$NVKR_OS_DIR/plan.json" \
  --view producers --format mermaid > "$NVKR_OS_DIR/materials.mmd"
bin/nvkr run local \
  --definition inputs/definitions/diagnostics/os-arm64.yaml \
  --environment inputs/environments/local-linux-arm64-kvm.yaml \
  --attempt os-preview --dry-run
```

Expect `pending`, `static-unqualified` and zero observed outputs. No attempt workspace or QCOW2 is created.
The Mermaid file describes dependencies; see [user concepts](user-concepts.md) for the identity distinctions.

## Execute with the matching runtime

Native Docker/KVM execution requires matching Linux architecture, a running Docker daemon and CLI, Git,
`jq`, KVM access, 8 CPUs, 10 GiB memory,
64 GiB free workspace storage, and access to pinned source/toolchain inputs. Follow the
[maintained wrapper instructions](https://github.com/dsx-ai-factory/nvkr/blob/main/inputs/definitions/diagnostics/README.md) for source-locked toolchain bootstrap
and a fresh immutable attempt. These instructions select the Linux Docker/KVM route;
they do not admit Podman execution. The separate
[native Ubuntu and Mac demo guide](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/local-nvke-demo.md#native-linux-and-mac-path)
describes native Packer preparation, shared three-VM deployment and its pending
complete customer setup/runtime acceptance.

A successful local Run retains its disk and record under `.nvkr/runs/<attempt>/`; it remains unqualified.
Material tracing and published `build-provenance-v1` evidence do not establish a complete installed-software
inventory or exact-byte reproduction. [Release inspection and verification](release-quickstart.md) apply
their own scopes; current evidence limits are recorded in [reconciliation](https://github.com/dsx-ai-factory/nvkr/blob/main/docs/adr/reconciliation.md).
