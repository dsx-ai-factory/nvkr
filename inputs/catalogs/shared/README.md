# Shared catalog declarations

Edit a shared source or producer here, then update the exact `sha256` on every importing registry.
These files own instructions, not release selections. Each consumer retains its own machine-managed
`pins.lock.yaml`, so updating one cohort does not silently update another. Run `nvkr catalog validate --data`
for each consumer and `make workflows-check` after changing a shared declaration.

| Owner | Declarations | Consumers |
|---|---|---|
| [build-actions](build-actions.yaml) | `checkout`, `download-artifact`, `setup-go`, `upload-artifact` | `nvke/module-tags`, `nvke/release` |
| [capnico](capnico.yaml) | `capnico` | `capnico/local`, `nvke/release-components` |
| [controller-profiles](controller-profiles.yaml) | `nvke-profiles` | `controller-profiles/native-nvidia`, `controller-profiles/nvidia-arm64`, `controller-profiles/upstream-arm64` |
| [installer-tools](installer-tools.yaml) | `clusterctl`, `helm-linux` (source and component), `kind`, `kubectl`, `oras` | `tools/installer-hosted-arm64`, `tools/installer-local-arm64`, `tools/native`, `tools/operator-arm64` |
| [native-tools](native-tools.yaml) | `native-capi-exec-init-image` | `tools/native` |
| [node-images](node-images.yaml) | `nvke-os`, `nvke-cni-calico-source`, `nvke-kubeadm-distro-source`, `nvke-spire-agent-source`, `nvke-doca-ovs-source`, `nvke-doca-ovs`, `nvke-image-inputs`, `nvke-patched-installer-iso`, `nvke-patched-installer-iso-amd64`, `nvke-spire-agent`, `ubuntu-installer-iso`, `ubuntu-installer-iso-amd64` | `node-images/diagnostic`, `node-images/release` |
| [nvke](nvke.yaml) | `nvke-cli`, `nvke-cli-local-site-arm64`, `nvke-controller` (source and component), `nvkectl`, `nvkectl-release` | `nvke/local-site-arm64`, `nvke/module-cli-arm64`, `nvke/module-tags`, `nvke/native-site`, `nvke/nightly-site-arm64`, `nvke/release` |
| [syft](syft.yaml) | `syft-arm64`, `syft` | `nvke/release`, `nvsentinel`, `tools/sbom-arm64` |
| [kubernetes-tools](kubernetes-tools.yaml) | `kubectl` | `kubernetes-conformance/kubectl`, `kubernetes-conformance/kubectl-1.34` |

Registries explicitly map local names to fragment names. `sourceNames` changes only an imported
component's source key; commands, stamps and material edges remain exact declarations. Names such as
`nvkectl-release` distinguish different recipes from the same upstream repository. Platform, packaging,
patch and version variants stay in their consuming registries when their recipes differ.

Fragments have no nested imports, overrides, wildcards, patches or pins. Duplicate selected names fail.
A digest mismatch fails before loading or pinning a catalog. To obtain the new file hash after an edit, use
`shasum -a 256 inputs/catalogs/shared/<owner>.yaml` from the repository root, and update every matching import.
The schema and bounds are specified in [ADR-22](../../../docs/adr/0022-catalog-pinning-and-the-lock-contract.md).

For example, a registry two directories below `catalogs/` can select the shared checkout source:

```yaml
imports:
  - path: ../../shared/build-actions.yaml
    sha256: 89919dde5a129554223e63b23d9ac4f610e9c9a0e369fe8ab188b3f13575723b
    sources:
      checkout: checkout
```

Its lock continues to contain the exact `checkout` action pin. The fragment supplies the repository and
version constraint. Other sources in that fragment are not selected implicitly.
