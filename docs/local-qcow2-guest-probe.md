# Local QCOW2 guest validation

The standalone guest experiment has been consolidated into the Go local demo.
Follow [Build and run NKE on Ubuntu ARM64](local-nke-demo.md) to consume the
completed disk and simulator Runs and test a real two-node tenant plus workload.

The maintained flow owns the native libvirt/NICoSim lifetime, exact disk serving,
temporary host controls and cleanup. It does not rebuild the disk at deployment
time or substitute containers for tenant VMs. Separate guest boot success is
not the complete OS smoke contract or a signed Release; the guide keeps those
validation limits explicit.
