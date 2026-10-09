# Local NKE management cluster

The standalone management experiment has been consolidated into the Go local
demo. Follow [Build and run NKE on Ubuntu ARM64](local-nke-demo.md) for the
maintained setup and command.

Kind hosts management services only. Tenant nodes are real ARM64 libvirt VMs
using the completed NKD QCOW2 through CAPNICo/NICoSim, not CAPD containers.
The Go consumer verifies and extracts the completed installer-tools Run, calls
NKE's pinned management installer, and owns cleanup through the full tenant test.
It does not rebuild artifacts during deployment or establish Release authority.
