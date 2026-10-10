# Local NVKE site integration

The standalone site experiment has been consolidated into the Go local demo.
Follow [Build and run NVKE on Ubuntu ARM64](local-nvke-demo.md) for the maintained
setup, artifact paths, command and validation limits.

Deployment consumes completed tools, site, components, profiles, simulator and
OS Runs without rebuilding them. It copies fifteen exact image manifests to
its private registry, prepares six unchanged chart archives, and calls NVKE's
pinned site installer before creating a real QCOW2-backed tenant.

Chart validation and installation remain NVKE-owned; NVKR byte verification is
invoked through `nvkr`. See the [artifact handoff reference](nvkr-to-nvke-demo.md)
for transport, runtime identity and security boundaries. Site readiness alone
is not tenant/workload acceptance or a verified Release.
