# Fresh upstream OVN local diagnostic, 2026-10-11

## Scope

A fresh continuous E2E completed naturally on source revision
`71b356f5899efb759571f0ea51781ead3f7c466a`. It selected explicit
`--network upstream-ovn` for both the Kind management site and the real QCOW2
tenant. The Linux ARM64 execution used QEMU TCG, four build jobs and a previously
absent source-only checkout. No previous Run outputs or QCOW2 were supplied;
shared Docker caches were retained. This was not an empty-cache build.

This establishes one unqualified local diagnostic at that exact revision. It
does not validate NVIDIA OVN, later source revisions, a supported NKE release
combination, a complete source-built distribution, M2 or qualification. NVIDIA
OVN remains the default; there was no implicit Calico fallback. See the
[execution guide](../local-nvke-demo.md#explicit-upstream-ovn-alternative).

## Observed acceptance

| Boundary | Retained owning evidence | Result |
|---|---|---|
| Artifact Run | `lifecycle.json`, admitted inputs and producer/Plan/readback bindings | Complete: exactly 29 expected and observed output names, with matching kinds, components and build bindings |
| Matched networking | `site/site-inputs.json`, `site/site-ovn-inputs.json` and source-owned site checks | Selected upstream image/chart, profiles and disk; site networking passed before Cluster API |
| Real tenant | `site/tenant-result.json`, node observations and actual disk/domain evidence | Two distinct Ready ARM64 Kubernetes v1.34.2 nodes, exactly one control-plane role, two distinct real guest domains |
| Workload and DNS | Actual Job, pod status/image ID, rollout checks and retained workload log | One pod succeeded with exit zero and zero restarts; `backoffLimit: 0`, `restartPolicy: Never`; functional cluster DNS resolved and `NKE_LOCAL_WORKLOAD_PASS` was printed |
| Natural tenant deletion | `site/tenant-cleanup-observations.json` | Delete command completed with exit zero; owning deletion wait observed final tenant inventory `[]` without forced removal |
| Joined owned cleanup | Actual resource/process absence plus `network-state.json` | No attempt-owned libvirt domains, demo containers or producers remained; journal finished and bridge/BGP/helper-grant flags cleared |
| Terminal result | Post-cleanup owning PASS, E2E wrapper and original recorder | All completed naturally with exit zero |

An independent read-only audit also rehashed the accepted QCOW2, served copy and
guest cache. All were regular, non-symlink files with the same size and digest.
No force removal, workload retry, gate bypass or newer code injection was used
to manufacture acceptance. Logs and JSON are descriptive observations, not
standalone authenticated receipts; artifact admission remains at its owning
boundary. Original private records remain retained and are not copied into this
sanitized report.

## Exact selected bindings

The upstream selection replaced four names while retaining 25 unchanged names:
`controller-profiles-upstream-ovn`, `nke-node-image-upstream-ovn-arm64`,
`upstream-ovn-chart-arm64` and `upstream-ovn-image-arm64`.

The observed disk payload was 2,840,723,968 bytes:

```text
payload: sha256:1af29c66b7de88532ee98b6cc6f99a975cb5caef554aaaa6a35ff3cc077e9a61
build:   sha256:5cfcb21fbaa89b2fa827e8e0a33a0a87cacd63b85463c46185dd45afccfdeef1
Plan:    sha256:ef90eba8eefa1a4f2582f15b92e9323f19e3f1edef39bd6407edfe6abf5cd6fe
```

The selected standalone site chart was version 1.4.0 with manifest
`sha256:ce33eb57d2361a3db0686fc592a4757adeeaeb85da8c3afb98b19744fe007a3a`.
The successful smoke pod's actual image ID matched its pinned acquisition:
`docker.io/library/alpine@sha256:260479a1cfaf304c4c20da7f8405d3ce313513dcd534bb743257bdd2fe0f3e2d`.
Its command checked `aarch64` and resolved
`kubernetes.default.svc.cluster.local` before printing the pass marker. Both
CoreDNS and node-local DNS rollout admission completed before smoke submission.

## Recording integrity

The original output-only 160-by-42 terminal cast retained the entire continuous
session, including preparation, build, deployment, workload and cleanup. Its
last event was `[10851.861198, "x", "0"]`.

```text
bytes: 786909137
SHA256: 6ea71cb2d9eac9b5f34c2a0dfe65a103d249b298f0659fffae401d9e441aadd6
```

The validated 1920-by-1080 H.264 presentation lasts 3,291.5 seconds (54m51.5s),
at 10 frames per second. It uses this unchanged cast, with build at 4x and other
stages at 2x. There are no omitted idle intervals or substituted attempts.
Captions identify the upstream-only diagnostic and do not imply NVIDIA-fork or
release acceptance. The measured duration matches the adjacent playback ranges
within one frame. Sampled terminal/caption frames were readable through the
natural zero-exit ending. The full encoded stream decoded without error.

```text
video bytes: 103731701
video SHA256: 7c9043d0dc553f28b77c9ea2f8c94dd33464365dbbc55d8b2a5b625b81a6f87c
```

The original cast and rendered video are retained local artifacts, not hosted
release assets. This report does not publish private execution-host details.
