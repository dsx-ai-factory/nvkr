# Build a node image from component materials

A disk output combines different kinds of inputs into one VM image. In the maintained NKE example,
producers build an agent Debian package, networking archives, an OCI image archive and a chart archive;
another producer acquires an installer ISO. Source-owned commands prepare those files for Packer, which
creates the final unencrypted QCOW2 disk. Selecting these components does not automatically install or
unpack them.

## Select the root and every material

This is the complete [ARM64 KVM Definition](../definitions/nke-os/definition-arm64-kvm.yaml):

```yaml
apiVersion: nkd.dev/definition/v4
kind: Definition
metadata:
  name: nke-os-arm64-kvm
  description: Build the ARM64 non-FDE NKE node QCOW2 on a native KVM host.
catalog:
  base: ./catalog
  overlays:
    - arm64-kvm.yaml
outputs:
  - name: nke-node-image-arm64
    platform: linux/arm64
    format: disk-image
    components:
      - nke-agent
      - nke-cni-calico
      - nke-cni-ovn
      - nke-image-inputs
      - nke-kubeadm-distro
      - nke-node-image
      - nke-patched-installer-iso
      - nke-spire-agent
      - ovn-kube-ubuntu
      - ovn-kubernetes-chart
      - ubuntu-installer-iso
```

`nke-node-image` is the disk-producing root. NKD requires exactly that root and all its direct and
transitive material producers. The explicit list repeats information reachable through the catalog graph:
today this is a selection consistency check. Missing dependencies, unrelated extras and multiple roots
are errors. **Selecting only the root does not expand its dependencies.**

## Connect producers by exact names

The [catalog](../definitions/nke-os/catalog/registry.yaml) declares these `materials` mappings:

| Consuming component | Binding name → supplying component |
|---|---|
| `nke-node-image` | `image-inputs → nke-image-inputs`; `installer-iso → nke-patched-installer-iso` |
| `nke-image-inputs` | `agent → nke-agent`; `calico → nke-cni-calico`; `kubeadm-distro → nke-kubeadm-distro`; `ovn → nke-cni-ovn`; `spire-agent → nke-spire-agent` |
| `nke-cni-ovn` | `ovn-chart → ovn-kubernetes-chart`; `ovn-image → ovn-kube-ubuntu` |
| `nke-patched-installer-iso` | `installer-source → ubuntu-installer-iso` |

The left side is the consumer's input label; the right side is an exact catalog component key.
Changing an output's selection cannot redirect these references. Update the consuming producer too,
or use a compatible overlay behind the existing key. See [catalogs and components](catalogs-and-components.md).

## Hand checked files to the source-owned build

During execution, NKD checks material bytes against their producer observations, stages them read-only,
and supplies a manifest recording each binding's component, architecture, digest and staged path.
Ordinary material-consuming commands read files under `/nkd/inputs/materials/`.

The NKE composer understands the package/archive contents and creates `image-inputs.tar`. The Packer
project understands that archive and the patched ISO. Its declared manifest projection labels those
inputs `archive` and `disk-image` without changing their component identities, digests or paths.
NKD provides the checked handoff; the source-owned build defines how those formats become a disk.

## Inspect without starting a VM

After [building the CLI](quickstart.md), run from the repository root. These commands need no Docker,
KVM or Packer execution:

```sh
NKD_OS_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nkd-os.XXXXXX")
bin/nkdctl run plan \
  --definition definitions/nke-os/definition-arm64-kvm.yaml \
  --environment definitions/nke-os/environment-local-linux-arm64-docker.yaml \
  --json > "$NKD_OS_DIR/plan.json"
bin/nkdctl run graph --plan "$NKD_OS_DIR/plan.json" \
  --view producers --format mermaid > "$NKD_OS_DIR/materials.mmd"
bin/nkdctl run local \
  --definition definitions/nke-os/definition-arm64-kvm.yaml \
  --environment definitions/nke-os/environment-local-linux-arm64-docker.yaml \
  --attempt os-preview --dry-run
```

Expect `pending`, `static-unqualified` and zero observed outputs. No attempt workspace or QCOW2 is created.
The Mermaid file describes dependencies; see [user concepts](user-concepts.md) for the identity distinctions.

## Execute with the matching runtime

Native Docker/KVM execution requires matching Linux architecture, a running Docker daemon and CLI, Git,
`jq`, KVM access, 8 CPUs, 10 GiB memory,
64 GiB free workspace storage, and access to pinned source/toolchain inputs. Follow the
[maintained wrapper instructions](../definitions/nke-os/README.md) for source-locked toolchain bootstrap
and a fresh immutable attempt. The separate macOS/Podman TCG route explicitly forbids KVM.

A successful local Run retains its disk and record under `.nkd/runs/<attempt>/`; it remains unqualified.
Material tracing and published `build-provenance-v1` evidence do not establish a complete installed-software
inventory or exact-byte reproduction. [Release inspection and verification](release-quickstart.md) apply
their own scopes; current evidence limits are recorded in [reconciliation](adr/reconciliation.md).
