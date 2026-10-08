# Native NICoSim QCOW2 guest, October 8, 2026

## Result and scope

The real ARM64 libvirt guest probe passed using existing NKD outputs, without
Make, Go, Packer or application rebuilding. The simulator created a QCOW2-backed
VM, reached its TCP-port Ready state, then passed key-only SSH, cloud-init done,
software TPM, installed agent and staged Kubernetes-package checks. The owned
VM was deleted, the simulator stopped/joined with exit 0, and the probe exited 0.

This is an **unqualified local diagnostic**, not a Release verification, complete
OS smoke, attestation, NKE management-plane install, tenant or workload acceptance.
The full source-owned image smoke remains failed on SPIRE/DSX; that assertion
was not removed or weakened. The older SPIRE package lacks the required DSX IMDS
input. A staged Kubernetes package is not a running Kubernetes node.

The [runnable probe](../local-qcow2-guest-probe.md) consumes completed local Run
descriptions, verifies/extracts the simulator through NKD's delivery owner and
independently hashes the disk. Local descriptions are not signed Release authority.

## Exact inputs and execution

Both inputs reside in the native source export
`/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-55fe7478247762bf4136a5bfc0b70a1b4a302c34`:

| Input | Completed attempt / exact identity |
| --- | --- |
| Simulator bundle | `laptop-colima-nicosim-assets-arm64-20261008-01`, manifest `sha256:be819c08681c9a848e138be5d4b34892ec6935110cf973b0696d73e4f30a0138` |
| Simulator source | NKE `6ffc2e5cae6739215b5cccf71f2692dcfa8da1c4` |
| OS QCOW2 | `laptop-colima-os-arm64-20261008-04`, 2,851,733,504 bytes, SHA-256 `38da250fa91eb5582067dcbf200a156f0fc71a085ce6a2b30c408a65059e129d` |
| OS source | NKE `ff21ba8c07cafff74b68e238a8876af11e5beded`, with the explicitly older SPIRE material |

Exact extracted members rehashed to:

- `nico-sim`: `2f07d4fc6d7936b761ccc86cfd2d9a8e9f1f46f38169594664816b837d93f4e0`
- `nico-simctl`: `da75ef4ecb568d19f9aae5e3feba36cd737ef63d8a61f5903edaab095a6e7042`
- `nico-sim-runtime-assets`: `dcdcf029cb3f2b72e06b9de987ab32c2ecb2e83ae73b92e1eca64235d21af5c6`

Final probe output:
`/mnt/lima-colima-nkd-demo/nkd-local/native-vm-probe-20261008-07`.
`inputs.json` records identities; `instance.json`, `domain-before-cleanup.xml`,
`startup.log`, `guest-check.log` and bounded console tails retain private runtime
observations. The directory also contains an ephemeral SSH private key and an
exact QCOW2 copy. It is not committed or suitable for public log upload.
The successfully exercised script bytes had SHA-256
`948a5ad3dcc53d4e0e8e55b4b1e7c3724323ce7b91500993c63e0df6a13f90dc`.
A subsequent review fix retains partial timeout diagnostics; that failure path
is contract-tested, not part of this successful guest's observed execution.

The dedicated Colima `nkd-demo` host is Linux ARM64, eight CPUs/24 GiB memory,
Docker 29.5.2, with native libvirt/QEMU/software-TPM packages from the
[startup preparation](2026-10-08-native-nicosim-libvirt-startup.md). This guest
route uses `qemu:///session`, TCG/cortex-a57, two vCPUs, 4 GiB RAM and a 30 GiB
instance disk. Its standalone base QCOW2 virtual size is 12 GiB. The libvirt XML
records ARM64 UEFI firmware; the source startup log's `uefi:false` flag is not
the authoritative final domain firmware description.

An operator-created `nkdvm0` bridge used `172.30.0.1/24`, with guest
`172.30.0.2`. The API was loopback-only at `127.0.0.1:18089`. BGP, containers,
diagnostic/console SSH and automatic orphan deletion were disabled. The
gateway metadata listener bound; the absent link-local address did not. DHCP,
NAT, BGP and complete tenant metadata/networking were not established.

The API-created instance was `1ae37d05-6de6-44ad-9d7a-e21758eeaba7`; libvirt's
domain UUID was `63d05bc1-a7c0-4017-b74b-6125a2eca59d`. Source logs show server
startup at 05:24:37 EDT, provisioning at 05:24:42, TCP readiness at 05:26:52,
and deletion/server stop at 05:27:17. The authenticated guest log records:

```text
architecture
cloud-init
...status: done
TPM
agent
NKE Agent Version: v0.2.1-81-gff21ba8
staged kubeadm
7.0.0-1018-nvidia-64k
```

The staged package check uses
`apt-cache search --names-only '^nke-kubeadm-distro-[0-9]'`, as in the exact
[source image smoke](https://github.com/dsx-ai-factory/nke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/scripts/boot-smoke.sh).
The image stores local APT packages for the agent to install during cluster
creation; it does not preinstall `kubeadm` into the executable search path.

## Failed trials and fixes

These trials were not promoted to passed image or demo acceptance:

| Trial | Observed result / response |
| --- | --- |
| `01` | Initial host bridge preparation was incomplete; server could not create a bridge without privilege. No guest created. |
| `02` | Real Create failed because QEMU lacked `efi-virtio.rom`. The API deleted its record but left a shut-off libvirt domain; exact UUID/state-checked removal cleaned it. Installed the Ubuntu-owned `ipxe-qemu` package. |
| `03` | Prototype passed real VM boot/TCP readiness/deletion. No authenticated SSH or full image assertions were established. |
| `04` | SSH subprocess failed; the prototype did not retain stdout/stderr. Its precise guest assertion failure is unknown, not inferred. Cleanup passed. |
| `05` | Retained SSH diagnostics show banner timeout with a five-second ConnectTimeout. Increased that transport budget to 30 seconds and bounded only exit-255 retries. Cleanup passed. |
| `06` | SSH, cloud-init, TPM and agent path passed; the new probe incorrectly expected installed `kubeadm`. Corrected this diagnostic to the source-owned staged-package contract. Cleanup passed. |
| `07` | Reusable command passed all its declared diagnostic checks and owned cleanup, exit 0. Complete source image smoke remains independently failed. |

The source warns that software-TPM state could not be mirrored into system
`/var/lib/libvirt/swtpm`. The session VM nevertheless booted and exposed
`/dev/tpm0`; this does not prove the downstream attestation flow.

## Host restoration and remaining demo

Additional APT prerequisites were `gnutls-bin` `3.8.3-1.1ubuntu3.6` (plus three
dependencies) and `ipxe-qemu` `1.21.1+git-20220113.fbbdc3926-0ubuntu2`. These are
operator-managed Ubuntu host packages, not a pinned NKD toolchain closure.
The actual EFI ROM is `/usr/lib/ipxe/qemu/efi-virtio.rom`.

The temporary helper policy allowed only `nkdvm0`; its package-owned helper
was temporarily setuid inside the dedicated VM. After probe exit and empty
session/process checks, its mode was restored from `4755 root:root` to
`0755 root:root`. The bridge was removed. The exact policy was recoverably moved
to `native-vm-probe-20261008-07/bridge.conf.disabled`; the newly created empty
`/etc/qemu` directory was removed. No accepted Run output was deleted.

Read-back showed no session domains, simulator/QEMU/TPM processes or probe
listeners at ports 18089/7777. Docker remained native ARM64/29.5.2 and its socket
remained `0660 root:docker`; `dpkg --audit` was empty. The accepted disk hash
remained unchanged. Measured guest free space was about 81 GiB, physical Mac
free space 38 GiB, not a fresh-bake reservation or capacity acceptance.

The subsequent guarded guide block was exercised separately in the old native
source export, which intentionally lacks the newly added probe script. Python
exited 2 before guest creation. The EXIT trap retained that failure, restored the
helper/bridge and archived the exact policy to `/tmp/nkd-vm-policy.fr4NEvHE`.
This checks the real nonzero-exit restoration path, not another guest success;
the Colima CLI reports its child failure as outer exit 1. Shell syntax/static
checks and Python bytes/text timeout-diagnostic fixtures cover the review fixes.

Repository validation passed after the code fixes: `make check`, uncached
`go test ./... -count=1`, whole default-suite `go test -race ./... -count=1`,
all twelve Python methods, Bash syntax/shellcheck for the guarded guide block,
formatting and `git diff --check`. Independent review's cleanup, inventory and
timeout-diagnostic findings were fixed; its final pass had no actionable findings.
These gates do not add operational acceptance beyond the recorded probes.

Still required: accessible pinned DSX input and complete image smoke, the correct
plain Ubuntu 26.04.1 profile, exact reachable registry/chart handoff, integration
of the separate built site/tools/simulator selections, admitted tenant networking
and trust setup, staged NKE install, tenant creation/workload and owned cleanup.
This probe does not complete PR118's requested full local demo.
