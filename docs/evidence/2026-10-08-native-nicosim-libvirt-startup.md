# Native NICoSim libvirt startup, October 8, 2026

## Result and scope

The dedicated Colima `nkd-demo` Linux ARM64 VM now has native libvirt, QEMU,
UEFI, cloud-image and software-TPM packages. The exact previously NKD-built and
delivery-verified simulator bundle started natively as UID 502, selecting
`libvirt` / `qemu:///session`, not the mock or container backend. It could list
the empty hypervisor; SQLite/seed initialization passed, the metadata gateway
bound, `/healthz` returned `{"status":"ok"}`, and the UI response matched
packaged index bytes. The probe sent SIGTERM and joined the server; it exited 0.

This is real-backend startup, **not VM creation, guest/attestation acceptance,
a site install or tenant/workload success**. Source health reports server
startup, not guest provisioning. No Make, Go compiler, Packer, Docker build or
application rebuild ran in the probe.

## Installed host prerequisites

After a bounded APT simulation, installation selected the inspected exact
versions with `--no-install-recommends`. Ubuntu host repository acquisition is
not a hermetic NKD artifact build or approved toolchain closure.

| Package | Installed version |
| --- | --- |
| libvirt-daemon-system / libvirt-clients | `10.0.0-2ubuntu8.19` |
| qemu-system-arm / qemu-utils | `1:8.2.2+ds-0ubuntu1.18` |
| qemu-efi-aarch64 | `2024.02-2ubuntu0.10` |
| cloud-image-utils | `0.33-1` |
| swtpm / swtpm-tools | `0.7.3-0ubuntu5.24.04.1` |

APT installed 64 packages and upgraded nine dependencies, including systemd to
`255.4-1ubuntu8.17`, requiring about 507 MB additional disk. Its package scripts
enabled libvirtd and the default `virbr0` NAT network. `dpkg --audit` was empty;
libvirtd was active, session/system domain lists empty, and native tool version
commands passed. Docker still returned 29.5.2, native ARM64/eight CPUs; its socket
remained `0660 root:docker`. No Mac software, default Docker context or Podman VM
was changed. Packages/network remain installed for subsequent work.

NKE's broader `controller/nico-sim/scripts/setup-linux.sh` was inspected, not
executed: it also replaces developer tools, can open Docker socket permissions,
and configures global development networking. The probe granted no setuid
bridge helper, group changes, AppArmor overrides or artifact file capabilities.
Those deployment capabilities still require admission before guest creation.

## Exact artifact-only probe

All three simulator members independently rehashed to the identities in the
[completed assets Run](2026-10-08-colima-nicosim-assets.md). They came from its
existing delivery-verified `consumer-bundle`, not a source checkout or rebuild.
GNU tar extracted the authenticated seed/UI archive into a new private directory.

`NICO_VMS_BASE_IMAGE` named the accepted [Run 04 QCOW2](2026-10-08-colima-local-os-build.md).
Independent pre-start hash/size checks matched SHA-256
`38da250fa91eb5582067dcbf200a156f0fc71a085ce6a2b30c408a65059e129d`,
2,851,733,504 bytes. No guest was created: configuration acceptance is not proof
that the simulator can provision or boot this disk.

Private probe directory:
`/mnt/lima-colima-nkd-demo/nkd-local/native-libvirt-probe-20261008-01`.
API listener `127.0.0.1:18089`; backend/URI `libvirt`, `qemu:///session`;
bridge `virbr0`, `192.168.122.0/24`. This uses bridge mode, not the source's
`NICO_VMS_LIBVIRT_NETWORK` mode. BGP, containers, diagnostic/console SSH and
orphan deletion were disabled. Database/files/cache stayed private; no NKE
source was mounted. Outer timeout 120 seconds, startup budget 30 seconds,
stop/join 40 seconds then a bounded kill/join fallback.

Source startup warned that `169.254.169.254:7777` could not bind: that address
was not configured. `192.168.122.1:7777` bound successfully, satisfying the
source's at-least-one-listener rule. This does not establish complete Forge
NoCloud/DHCP/DNS routing. This default bridge overlaps the source's default
Calico pod range; **do not reuse this startup-only subnet for the tenant demo**.

## Shutdown and remaining acceptance

After the probe, session/system domain lists remained empty; no `nico-sim`
process or listeners at 18089/7777 remained. Owned database/assets/log were
retained; the server was not left running. Guest free capacity was about 90 GiB,
physical Mac free capacity 43 GiB: measurements, not reservations/bake admission.

Still required: owned non-overlapping VM/Kind networks, admitted bridge-helper
and TPM permissions, BGP/DHCP/metadata transport, actual QCOW2 VM creation,
readiness/deletion, correct plain 26.04.1 profiles and accessible pinned DSX
material for the complete image contract. Then exact registry/chart transport,
staged NKE install, tenant/workload acceptance and owned cleanup. The image's
full smoke remains failed; this startup probe does not supersede it or complete
the requested demo.
