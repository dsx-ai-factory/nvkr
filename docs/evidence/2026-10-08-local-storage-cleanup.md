# Local storage recovery and OS preflight, October 8, 2026

## Result

The user requested removal of old test-build artifacts. Eleven September 23
attempts whose `lifecycle.json` reported `failed` were permanently removed.
They occupied approximately 109.6 GiB according to `du -x -k` before deletion.
Host free storage increased from approximately 31 GiB to 141 GiB according to
`df`. Recovery of the removed generated outputs requires rebuilding; the removed
attempt diagnostics are not retained.

The two successful September 23 QCOW2 attempts and three attempts without a
terminal lifecycle record were retained. The safety review rejected removal of
the successful evidence; that rejected operation did not execute. Source
repositories, worktree Git metadata, active PR #113/#118 worktrees, Go caches,
container images and VM backing disks were not removed.

## Exact removal scope

All removed directories were children of:

```text
/Users/ikislenko/Workspace/github/nke-recipes/.git/checkouts/codex/nke-os-integration/.nkd/runs
```

Removed attempt names:

- `20260923-arm64-e2e-01`
- `20260923-arm64-e2e-02`
- `20260923-arm64-e2e-03`
- `20260923-arm64-e2e-04`
- `20260923-arm64-e2e-05`
- `20260923-arm64-e2e-06`
- `20260923-arm64-e2e-07`
- `20260923-arm64-e2e-08`
- `20260923-arm64-e2e-09`
- `20260923-arm64-e2e-10`
- `20260923-arm64-e2e-13`

Before removal, an elevated process inspection showed no running Packer, QEMU or
NKD build process, and `lsof` found no open files under the old runs directory.
The removal checked every target was a directory, not a symlink, had the exact
expected canonical parent and reported `failed`. Afterwards all eleven targets
were verified absent. No broad workspace or worktree deletion was performed.

## Preserved old attempts

| Attempt | Recorded status | Approximate allocated size |
| --- | --- | --- |
| `20260923-arm64-e2e-17` | Complete, one observed disk | 19.8 GiB |
| `20260923-arm64-source-owned-02` | Complete, one observed disk | 19.8 GiB |
| `20260923-arm64-e2e-11` | No lifecycle record | 30.5 GiB |
| `20260923-arm64-e2e-12` | No lifecycle record | 30.5 GiB |
| `20260923-arm64-source-owned-01` | No lifecycle record | 0.8 GiB |

The original old run tree accounted for approximately 211 GiB. Of that, 109.6 GiB
was confirmed failed, 61.7 GiB had no lifecycle record and 39.6 GiB was successful.
An absent terminal record was not treated as proof of failure or safe disposal.

## Real Colima preflight after cleanup

The existing `nkd-demo` profile was running with ARM64, Docker, eight CPUs,
16 GiB configured memory and a 150 GiB configured VM disk. The workspace is a
virtiofs mount of the Mac filesystem; the configured VM disk is not additional
physical host capacity.

From the PR #118 implementation worktree at
`0d5e6d3ad3424f6d1790609938db672dd7a84caf`:

```sh
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke \
  bash scripts/nke-full-arm64-local.sh --check os
```

The real check returned exit zero:

```text
OS advisory preflight: CPU 8/8; available memory 15489/10240 MiB; workspace 144492/65536 MiB. Not reserved.
Prerequisite diagnostics passed for os/docker. Git authorization and cgroup/resource/toolchain admission remain execution gates.
```

The fresh attempt `laptop-colima-os-arm64-20261008-01` was then launched using the
documented launcher. It later failed during SPIRE source snapshot preparation
before Packer; the [separate execution record](2026-10-08-colima-local-os-build.md)
tracks that failure and native-checkout retry. This record establishes recovered storage and prerequisite
admission, not a successful toolchain build, QCOW2 bake, boot or tenant lifecycle.
The runtime must recheck actual resources at execution admission. The successful
old images are not substituted for that fresh build.
