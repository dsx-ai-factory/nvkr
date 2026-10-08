# Mac-to-Colima local build preflight, October 8, 2026

## Result and scope

The developer launcher entered a real, explicitly selected Colima Linux ARM64
Docker VM. Its provider prerequisite check passed. Its OS prerequisite check
reached resource measurement and failed before bootstrap downloads or attempt
creation because the Mac-backed workspace lacked the authored free capacity.

This is **launcher/preflight evidence only**: no Docker producer, fresh Packer
toolchain, QCOW2 bake/boot, site install or tenant lifecycle was exercised through
Colima. The requested tenant uses the existing NKD QCOW2 output; a tenant-node OCI
image is not a substitute.

## Exercised inputs

- NKE Recipes base: `91a8f7e348d82bc3ac6e1bdec61747d342d9328e`, with uncommitted
  launcher/tests/docs and simulator changes. This is not a committed-head replay.
- Exercised launcher SHA-256:
  `377d11b0f9d7eb79c50384a89e416421d536f9209eb97019946b31d51b26ef08`.
- Colima: `0.10.3`, commit `00f6c297e92a82c04a4ab507db0a61435650d7e8`.
- Profile: `nkd-demo`, native `aarch64`, Docker runtime, macOS
  Virtualization.Framework, virtiofs mounts, 8 CPUs, 16 GiB configured memory,
  150 GiB configured data disk.
- Docker server: `29.5.2`; Buildx: `v0.34.1`.
- The Linux VM already supplied Git, Bash, coreutils and Docker. `jq` was absent
  and was explicitly installed inside this dedicated profile using Ubuntu APT.
  These host setup packages are not a claimed qualified/pinned runtime closure.
- NKE local Git transport: the existing Mac checkout mounted at its same
  absolute path. Preflight checked access, not a complete source fetch/build.

## Setup and checks

From the implementation worktree:

```sh
colima start --profile nkd-demo --arch aarch64 --runtime docker --vm-type vz \
  --cpus 8 --memory 16 --disk 150 --activate=false --ssh-config=false \
  --mount "$(pwd -P):w" --mount /absolute/path/to/nke
colima ssh --profile nkd-demo -- bash -c \
  'sudo timeout --kill-after=10s 180s apt-get update && sudo timeout --kill-after=10s 180s apt-get install -y jq'
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh --check os
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh --check providers
```

The setup commands create/configure a VM explicitly. The launcher does not
perform them automatically. Existing Podman state was preserved. The Mac Docker
context was `desktop-linux` before and after setup; profile creation did not
activate the new context.

The OS check returned exit one:

```text
OS advisory preflight: CPU 8/8; available memory 15372/10240 MiB; workspace 32531/65536 MiB. Not reserved.
Insufficient OS resources. Provide 8 CPUs, 10 GiB available RAM and 64 GiB free AFTER material preparation; allow about 90 GiB before a fresh build. No downloads started.
```

The provider check returned exit zero:

```text
Prerequisite diagnostics passed for providers/docker. Git authorization and cgroup/resource/toolchain admission remain execution gates.
```

An earlier non-escalated invocation could not inspect Colima's host state in the
Codex filesystem sandbox and reported the profile absent/stopped. The permitted
real check above was rerun after startup and prerequisite installation. Neither
that sandbox failure nor a passing mocked test establishes VM availability.

## Storage boundary

The checkout is a virtiofs mount of the Mac's filesystem. A larger configured VM
disk cannot create physical Mac storage. The existing Podman VM separately
reported 229 GiB free internally, but its backing raw file lives on the same
nearly full Mac. Moving to that sparse disk without physical headroom is not an
accepted workaround. No admission requirement was reduced and no image, cache,
attempt or user file was deleted.

## Remaining acceptance

Provide adequate physical workspace and VM storage, build the existing
`nke-node-image-arm64` output through the chosen Linux route, retain its digest
and size, then boot that exact QCOW2 through real NICoSim VM provisioning. The
complete site/tenant/workload demo and its exact-disk handoff remain unfinished.
