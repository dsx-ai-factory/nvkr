# Local NVKE management cluster

The standalone management experiment has been consolidated into the Go local
demo. Follow [Build and run NVKE on Ubuntu ARM64](local-nvke-demo.md) for the
maintained setup and command.

Kind hosts management services only. Tenant nodes are real ARM64 libvirt VMs
using the completed NVKR QCOW2 through CAPNICo/NICoSim, not CAPD containers.
The Go consumer verifies and extracts the completed installer-tools Run, calls
NVKE's pinned management installer, and owns cleanup through the full tenant test.
It does not rebuild artifacts during deployment or establish Release authority.
