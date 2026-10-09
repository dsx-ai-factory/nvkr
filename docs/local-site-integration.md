# Local NKE site integration

The standalone site experiment has been consolidated into the Go local demo.
Follow [Build and run NKE on Ubuntu ARM64](local-nke-demo.md) for the maintained
setup, artifact paths, command and validation limits.

Deployment consumes completed tools, site, components, profiles, simulator and
OS Runs without rebuilding them. It copies fifteen exact image manifests to
its private registry, prepares six unchanged chart archives, and calls NKE's
pinned site installer before creating a real QCOW2-backed tenant.

Chart validation and installation remain NKE-owned; NKD byte verification is
invoked through `nkdctl`. See the [artifact handoff reference](nkd-to-nke-demo.md)
for transport, runtime identity and security boundaries. Site readiness alone
is not tenant/workload acceptance or a verified Release.
