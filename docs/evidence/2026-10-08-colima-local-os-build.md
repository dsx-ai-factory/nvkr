# Colima Docker ARM64 OS build, October 8, 2026

## Scope and result

The first real Docker execution through the dedicated Colima `nkd-demo` profile
failed before Packer. It built and natively validated a fresh local Packer
toolchain, prepared Kubernetes/Calico dependencies, built the NKE agent package
and built the upstream OVN image. Creating the SPIRE private source snapshot then
failed with `NKD-E-BUILD-FAILED`: `too many open files in system`.

No QCOW2 was produced or accepted, and no guest boot, site install, tenant or
workload is established by that first attempt. The later fresh native Run 04
completed and exported one accepted disk, as recorded below. Boot smoke and the
complete site/tenant/workload remain separate acceptance gates. The source graph still retains the
older plugin-free SPIRE and acquired-dependency exceptions; it is not the PR #113
direct-source graph or a complete source-built distribution.

## First attempt

- NKE Recipes implementation base: `0d5e6d3ad3424f6d1790609938db672dd7a84caf`.
  Documentation edits were present; the launcher/code matched that commit.
- Attempt: `laptop-colima-os-arm64-20261008-01`, one disk expected, zero observed,
  terminal `failed`, recorded lane `static-unqualified`.
- Definition: `definitions/nke-laptop-arm64/definition-os.json`, with the fresh
  toolchain substitution frozen under `.nkd/laptop-bootstrap/ATTEMPT/`.
- Environment: `definitions/nke-laptop-arm64/environment-local-docker.json`.
- Runtime: Colima 0.10.3, Linux ARM64, Docker 29.5.2, Buildx 0.34.1, eight CPUs,
  16 GiB configured memory, 150 GiB configured data disk. TCG, not KVM, selected.
- NKE objects came from the existing local checkout through the Git transport
  override; it does not select dirty files or its current checked-out HEAD.
- First Run storage: `.nkd/runs` in the virtiofs-mounted Mac checkout.
- Fresh Docker toolchain ID:
  `sha256:c28b7415c160e694a71ed637c406546ea3d50ef1f9d6acd901f7058954dfbebe`.
  Native tool probes passed. This is a local diagnostic pin, not a published,
  reviewed or qualified toolchain approval.
- The OVN producer emitted native manifest
  `sha256:dc9606423c3743bcf03a3aca83fe07c1f29861df4cbbd694bc69b705ce4cdef2`.
  A producer result within a failed Run does not establish final artifact acceptance.

The retained `lifecycle.json` records:

```text
factory node nke-node-image-arm64/build-nke-spire-agent:
copying private source snapshot for nke-spire-agent:
creating snapshot file vendor/github.com/gravitational/teleport/api/utils/bool.go:
too many open files in system
```

## File-handle investigation

After the failure, Linux reported only 832 allocated file handles against its
effectively unlimited system ceiling. macOS reported 225,968 handles, with
`kern.maxfiles=368640` and `kern.maxfilesperproc=184320`. The Colima host VM
process held 48,737 descriptors; the separate Podman host VM held 159,749.
These are post-failure measurements, not proof of the peak or which limit fired.
The inspected source-copy loop closes each file; no code leak was established.

With no build process or Docker container running in the dedicated Colima VM,
an explicit `sync` followed by `sudo sysctl -w vm.drop_caches=2` reduced its host
VM process to 33 descriptors and total macOS usage to 177,397. Podman was not
stopped or changed. This supports a shared-filesystem cache-pressure explanation,
but does not prove an exact failure threshold. No Mac limit was raised.

The [Linux kernel documentation](https://www.kernel.org/doc/html/latest/admin-guide/sysctl/vm.html#drop-caches)
defines this as a non-destructive clean inode/directory-cache debugging action;
it may cause extra I/O. It is not implemented as automatic launcher behavior.

## Rejected external-root probe

A temporary launcher prototype forwarded a native absolute `--attempts-root`
for `laptop-colima-os-arm64-20261008-02`. Its advisory capacity check and cached
toolchain validation passed, but the factory rejected the dry-run preview with:

```text
NKD-E-USAGE: factory workspace "/mnt/lima-colima-nkd-demo/nkd-local/runs/laptop-colima-os-arm64-20261008-02-preview" must be a clean repository-relative child of .nkd
```

No executable Run or source preparation started. The prototype and its tests
were removed. `pkg/nkd/factory/local.go` retains the containment rule unchanged.
An exported checkout on native storage, not a symlink or relaxed admission,
is the next approach.

## Native-checkout retry

The exact committed PR #118 source at the implementation commit above was
exported with `git archive` into a new private native ext4 source directory,
`/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes`. No ignored old attempts, caches
or dirty source changes were included. The existing Linux launcher is unchanged;
its bootstrap and Run now both use normal `.nkd` children on the native filesystem.
The first failed Run is retained, not resumed or overwritten.

The real native-checkout prerequisite check passed:

```text
OS advisory preflight: CPU 8/8; available memory 15306/10240 MiB; workspace 136639/65536 MiB. Not reserved.
Prerequisite diagnostics passed for os/docker. Git authorization and cgroup/resource/toolchain admission remain execution gates.
```

The fresh attempt uses the committed implementation, not the rejected prototype:

```sh
colima ssh --profile nkd-demo -- bash -c \
  'cd /mnt/lima-colima-nkd-demo/nkd-local/nke-recipes && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke bash scripts/nke-full-arm64-local.sh laptop-colima-os-arm64-20261008-03 1 os'
```

The retry passed SPIRE private snapshot preparation and the remaining material
builds, verified/acquired and patched the installer ISO, then passed `packer validate`.
The real QEMU ARM64 guest booted the ISO and reached Ubuntu autoinstall, APT
configuration and Curtin kernel installation. These were intermediate observations,
not a completed disk or boot-smoke result. Host free capacity was approximately
103 GiB before this retry; guest capacity did not reserve physical Mac storage.

## Native retry failed: guest-host global OOM

At `2026-10-08 05:48:44 UTC`, while a separate simulator build was active,
Colima's Linux kernel killed the QEMU process. The authoritative kernel record
reports a **global** OOM, not an authored container memory limit:

```text
oom-kill:constraint=CONSTRAINT_NONE ... global_oom ... task=qemu-system-aar,pid=78467,uid=502
Out of memory: Killed process 78467 (qemu-system-aar) total-vm:7142560kB, anon-rss:5396348kB
```

The Packer container remained running and waited for SSH after QEMU was gone.
Its Docker state reported `OOMKilled=true`; its cgroup `memory.events` recorded
`oom_kill 1`, with `memory.max=max` and a peak of `6132101120` bytes.
The installer log stopped at kernel installation; the private partial disk had
reached `7166951424` bytes. That partial file was never a lifecycle-accepted disk.
The record establishes aggregate host-memory exhaustion and its QEMU victim,
not an isolated peak allocation attributable to a single concurrent build.

After verifying the exact attempt-owned launcher, it received SIGTERM through
NKD's existing signal/context cancellation path. The terminal record is:

```text
attempt: laptop-colima-os-arm64-20261008-03
status: failed
lane: static-unqualified
required: 1
observed: 0
failure: NKD-E-TIMEOUT — local Run canceled: context canceled
```

This terminal code describes cancellation, not the kernel's original OOM cause.
There is no accepted output directory or QCOW2. The run-owned build container
was cleaned up, and held material memory was released. No retained installer
console log was located afterward; the bounded pre-cancellation observations and
kernel/cancellation evidence above must not be described as a complete retained
Packer log. The failed Run remains in its native source export.

## Scoped recovery and fresh attempt

With the dedicated Docker profile idle, `fstrim` on its data filesystem released
47.7 GiB of already-free blocks. Physical Mac free space increased from 44 to
89 GiB. No source, successful Run, artifact or worktree was deleted.
Only the existing `nkd-demo` profile was stopped/restarted, preserving its disk
and mounts, with eight CPUs, **24 GiB RAM** and its existing 150 GiB data disk.
Other Docker contexts/Podman VMs were not changed; default context remained
`desktop-linux`. Docker 29.5.2 responded after restart.

The fresh preflight reported CPU `8/8`, available memory `23502/10240 MiB` and
workspace `117119/65536 MiB`. Those are measurements, not reservations. The new
single-job attempt uses the committed asset-increment source export, without
concurrent simulator/component builds:

```sh
colima ssh --profile nkd-demo -- bash -c \
  'cd /mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-55fe7478247762bf4136a5bfc0b70a1b4a302c34 && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke bash scripts/nke-full-arm64-local.sh laptop-colima-os-arm64-20261008-04 1 os'
```

This is a fresh immutable attempt, not resume/reuse of failed Run 03. It completed
all material preparation, patched the ISO, passed Packer validation, installed
Ubuntu, connected over SSH, provisioned staged NKE packages, cleaned/trimmed the
guest and compressed the final disk. Packer reported a successful build after
1 hour 55 minutes. NKD's terminal observation then completed and the launcher
exited 0 with one expected and one observed output, no failure and lane
`unqualified-local-execution`.

| Record | Exact value |
| --- | --- |
| Attempt | `laptop-colima-os-arm64-20261008-04` |
| Plan identity | `sha256:c64a8befaa704f3da3eefd40cae1560c2eb2d9bf1e583a01540b763c25d2e4f9` |
| Build identity | `sha256:7f7a7d3da4d9385c3e8ab293e1d13e83d631d46b5209d84b3befd708e98b1d95` |
| Output | `nke-node-image-arm64`, `disk-image`, `qcow2`, encryption `none` |
| Producer outcome | `build-nke-node-image`, `succeeded` |
| Exact QCOW2 SHA-256 | `38da250fa91eb5582067dcbf200a156f0fc71a085ce6a2b30c408a65059e129d` |
| Exact bytes | `2851733504` |
| Retained output | `.nkd/runs/laptop-colima-os-arm64-20261008-04/outputs/nke-node-image-arm64.qcow2` inside the committed native export above |

Independent `sha256sum` and `stat` of the retained file matched the observation;
the file has mode `0444`. The guest installer console identified Ubuntu 26.04.1
LTS. This plain disk must not be described or selected as the example profiles'
Ubuntu 24.04 FDE image. The 24 GiB host configuration worked for this single-job
bake; it is not a qualified execution profile or a capacity guarantee.

The successful Run still uses the older plugin-free SPIRE package and acquired
inputs. It is not PR #113's direct-source graph, full-disk inspected evidence,
publication/signing, qualified reproduction, a complete combined 23-output Run
or a running tenant. The separate boot check below reached Linux and SSH but
failed the full image contract; its result must not be inferred from the bake.

After the producer containers exited, the sparse Colima disk still consumed
physical Mac storage: the guest reported about 95 GiB free while macOS reported
15 GiB free. An explicit `fstrim` of the idle dedicated data filesystem discarded
52.3 GiB of already-free blocks, increasing Mac free space to 66 GiB. No files,
successful Run or accepted artifact were deleted. This is an observed diagnostic
maintenance action, not automatic launcher behavior or a storage reservation.

## Independent guest boot: partial success, full contract failure

The exact source-owned `os-image/packer/scripts/boot-smoke.sh` and its companion
scripts came from NKE `ff21ba8c07cafff74b68e238a8876af11e5beded`. The test
converted the accepted read-only QCOW2 to an independent 20 GiB target, generated
private NoCloud/SSH inputs outside Git and used the measured Packer/QEMU image
above. The accepted disk's SHA-256 and size remained unchanged afterward.

Three diagnostic invocations separated harness failures from image checks:

1. The source harness's ARM64 TCG CPU, `max,pauth-impdef=on`, remained at UEFI
   without Linux/SSH. It was explicitly stopped; this was not a naturally expired
   complete timeout. It does not establish the exact cause of the stall.
2. A private QEMU argument adapter changed only that CPU to the produced Plan's
   `cortex-a57`. Linux booted, cloud-init finished and the root filesystem grew,
   but host OpenSSH exited with `No user exists for uid 502`: the non-root
   container's UID had no passwd entry. That invocation was stopped too.
3. The same CPU adapter plus a private read-only passwd projection for the
   observed UID allowed the unchanged source assertions to run. SSH connected,
   cloud-init reported `done`, `lvm-growth-firstboot.service` was active and the
   root-size assertion passed. NKE agent, SPIRE and configure units were enabled;
   agent/updater version probes returned `v0.2.1-81-gff21ba8`. The script then
   exited **1** in its SPIRE-content assertion section. It did not print its final
   image-contract success message or execute the later conntrack acceptance.

The container was native ARM64, non-root, networkless, capability-dropped, with
a read-only root filesystem, 6 GiB container memory and four allocated CPUs.
The guest used two CPUs and 2 GiB RAM; its boot budget was 900 seconds, with an
outer bounded process timeout. Only the independent target/private work was
writable. QEMU stopped and the owned container was removed; no assertions were
removed to obtain a pass. The CPU/passwd projections are diagnostic harness
adaptations, not changes to NKE source or the produced disk, and are not yet a
maintained copy-pastable demo boot adapter.

Inspection of the retained material
`spire-agent_1.14.1+tpm1.9.0-1_arm64.deb` with `dpkg-deb --contents` confirms it
contains `spire-agent`, `tpm_attestor_agent` and `get_tpm_pubhash`, but no
`dsx-imds-node-attestor-agent`. The full source contract requires that plugin
immediately after its SPIRE/TPM executable checks. The remote script did not
trace individual assertions, so the log alone does not identify the exact first
failed assertion; the missing required package member is independently verified.

NKE's selected source lock specifies the ARM64 DSX payload SHA-256
`88226ee3a848e09c620a99aad9c99c64e6c04e7a862836eb206d6e6f9fae2bf5` at
`https://nke-prod-assets.s3.us-west-2.amazonaws.com/dsx-imds-node-attestor-agent/linux-arm64/dsx-imds-node-attestor-agent`.
Unauthenticated bounded HEAD and ranged GET requests on October 8 returned HTTP 403. This
does not prove the object is absent or that authorized acquisition is impossible.
An organization-scoped GitHub search for `dsx-identity` was denied by SAML
conditional-access policy, so an accessible source/pin was not established.
Do not omit the plugin or weaken smoke checks and describe the image as complete.

Private console logs and SSH material remain outside Git in the dedicated VM's
`nkd-local/os-boot-smoke.GtecjK` probe directory. No site, NICoSim VM tenant,
server-backed attestation or workload has been accepted. The next gate is an
accessible pinned DSX input, a fresh correctly packaged image, and a passing
complete boot contract before the deploy-only journey.
