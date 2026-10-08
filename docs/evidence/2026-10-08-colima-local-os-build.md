# Colima Docker ARM64 OS build, October 8, 2026

## Scope and result

The first real Docker execution through the dedicated Colima `nkd-demo` profile
failed before Packer. It built and natively validated a fresh local Packer
toolchain, prepared Kubernetes/Calico dependencies, built the NKE agent package
and built the upstream OVN image. Creating the SPIRE private source snapshot then
failed with `NKD-E-BUILD-FAILED`: `too many open files in system`.

No QCOW2 was produced or accepted, and no guest boot, site install, tenant or
workload is established by this attempt. The source graph still retains the
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

The retry has been launched, not yet accepted as a complete disk Run. Its files
reside inside the VM, not under the Mac checkout's `.nkd/runs`. Record its terminal
status and exact exported QCOW2 digest/size separately after completion. Host free
capacity was approximately 103 GiB before this retry; guest free capacity does
not reserve physical space for the sparse VM disk.
