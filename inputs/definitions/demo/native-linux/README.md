# Native Linux ARM64 demo inputs

These Definitions select Linux host tools from the [shared native tool catalog](../../../catalogs/tools/native/README.md). They do not copy its registry or source pins. Host release assets retain their original Linux digests and versions under distinct producer IDs; the verified private tool view uses conventional command names. Linux outputs do not select vmnet tools.

`definition-tools.json` selects the host bundle, including the CLI used for bootstrap and deployment, and the Linux site runtime companion. `definition-components.json` selects the same native NVKE controller/provider/profile owners as the Mac build. The separate `definition-components-upstream-ovn.json` retains its historical upstream OVN diagnostic selection. A tools Run does not imply a compatible site disk.

The shared `run` and `e2e` commands use the same host bootstrap, deployment, tenant and workload checks on both hosts. Linux selects KVM, session libvirt and the scoped bridge owner; Mac selects vmnet with explicit TCG for the selected 64 KiB image. Native Linux networking does not borrow a Docker Kind subnet: its versioned journal represents host-originated bootstrap transport. The temporary container backend integration and full native Linux runtime parity remain under validation. The shared native site/tenant owner starts one site guest and the requested tenant guests: four guests with conformance (one tenant control plane and two workers).

See the [native guide](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path) for the supported inputs and validation limits. The standalone `guest` command validates one guest and local HTTP delivery, not a Kubernetes distribution.
