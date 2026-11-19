# Catalog ownership

There are 12 maintained catalog entrypoints. Each owns one `registry.yaml` and `pins.lock.yaml` pair.
Architecture, Kubernetes version and site flavor are explicit producer selections within these catalogs;
Definitions select the required producers and concrete platforms. Shared declaration owners remain in
[`shared/`](shared/README.md), with digest-pinned imports and independent consuming locks.

| Catalog | Pin cohort and ownership |
|---|---|
| [`aicr`](aicr/registry.yaml) | NVIDIA AICR source products and acquired CLI release; independent AICR releases |
| [`capnico`](capnico/registry.yaml) | Cluster API provider for NICo; its own upstream repository and release selection |
| [`kubernetes-conformance`](kubernetes-conformance/registry.yaml) | Kubernetes test-version cohorts and Hydrophone; selected independently of the NVKE controller release |
| [`local-path-provisioner`](local-path-provisioner/registry.yaml) | Rancher provisioner release, chart and helper-image pins |
| [`nico`](nico/registry.yaml) | Infra-controller release with NICo/Core/Flow products; independent of NVKE |
| [`node-images`](node-images/registry.yaml) | OS-image material and builder cohorts, with release and diagnostic variants and ordered overlays |
| [`nvke`](nvke/registry.yaml) | NVKE release, module-tag and site producer variants, including controller profiles |
| [`nvsentinel`](nvsentinel/registry.yaml) | NVSentinel release and development products; independent upstream cadence |
| [`ovn/nvidia`](ovn/nvidia/registry.yaml) | NVIDIA OVN fork release and patch cohort |
| [`ovn/upstream`](ovn/upstream/registry.yaml) | Upstream OVN release cohort; it cannot advance the NVIDIA fork's pins |
| [`runtime-dependencies`](runtime-dependencies/registry.yaml) | Acquired runtime-image cohort, independently pinned from source-built NVKE producers |
| [`tools`](tools/registry.yaml) | Native tools, operator release versions, installer packaging and simulator producers |

The two OVN catalogs remain separate because their upstream repositories and release constraints are
independent. The other domain boundaries retain independently selected pin cohorts. The experimental
public OS inputs remain under [`../experiments/public-os-sources`](../experiments/public-os-sources/README.md).

## Platforms and variants

Release tools with the same version and acquisition contract share one source and producer across their
available platforms. `tool.platforms` is a sorted list; URL and archive members render `{{.OS}}` and
`{{.Arch}}`. The lock's `tools` entries retain the exact archive, executable and companion digests for each
platform. A missing platform fails rather than borrowing another platform's asset. Definitions continue to
select concrete native outputs; this does not introduce executable image-index publication.

Native tool names are `clusterctl`, `helm`, `kind`, `kubectl`, `oras`, `packer` and `packer-plugin-qemu`.
Operator Helm and kubectl retain their different versions as `helm-operator` and `kubectl-operator`.
Installer source builds and raw-output variants remain distinct because their build recipes or packaging
contracts differ from acquired release executables. Site and Kubernetes-version variants likewise retain
explicit names and pins. Renaming a producer changes its Build Digest; it does not authorize changing its
source revision, build command, native toolchain or acquired bytes.

[`node-images/variants/`](node-images/variants/) contains predecessor-bound producer replacements and
toolchain substitutions, not catalog entrypoints. The Ubuntu 24 overlay applies after the native-toolchain
overlay. NVIDIA OVN producer copies are generated through `inputs/policies/ovn-producer-overlays.json`;
regenerate those copies and their predecessor bindings after editing their owner.

## Declaration and file ownership

Fragments contain no locks or nested imports. Registries select exact named declarations with a file hash;
duplicate names fail rather than override. A fragment edit requires updating every consuming hash and
validating each consumer. Equal upstream URLs alone do not establish equal recipes or release cohorts.

Former leaf registry and lock files are merged into their domain pairs. Their handwritten patches and
notes are retained under the destination domain. Notes describe the relevant selection, while the domain
registry is the current entrypoint. The generated workflows and Definitions use the new domain paths.

Plan and graph comparisons establish preserved producer inputs and native pins. They do not establish a
fresh upstream build, exact-byte reproduction of a source build, hosted execution or runtime acceptance.
