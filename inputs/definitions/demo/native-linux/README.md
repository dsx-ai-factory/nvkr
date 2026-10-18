# Native Linux ARM64 demo inputs

These Definitions select Linux host tools from the [shared native tool catalog](../../../catalogs/tools/native/README.md). They do not copy its registry or source pins. Host release assets retain their original Linux digests and versions under distinct producer IDs; the verified private tool view uses conventional command names. Linux outputs do not select vmnet tools.

`definition-host-tools.json` selects one host bundle, including the CLI used for bootstrap and deployment. `definition-tools.json` also selects the Linux site runtime companion. `definition-components.json` uses the maintained NVKE controller/provider/profile outputs; `definition-components-upstream-ovn.json` explicitly selects the upstream OVN profiles. These retain their independent source selections; a tools Run does not imply a compatible site disk.

The shared `run`, `e2e` and `native` commands use the same host bootstrap, deployment, tenant and workload checks on both hosts. Linux selects KVM, session libvirt and the scoped bridge owner; Mac selects vmnet and HVF. Native Linux networking does not borrow a Docker Kind subnet: its versioned journal represents host-originated bootstrap transport. The temporary container backend integration and full native Linux runtime parity remain under validation. The standard `e2e` action uses this shared three-VM owner.

See the [native guide](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path) for the supported inputs and validation limits. The standalone `guest` command validates one guest and local HTTP delivery, not a Kubernetes distribution.
