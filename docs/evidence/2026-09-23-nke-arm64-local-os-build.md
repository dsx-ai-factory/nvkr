# NKE ARM64 local OS-image build

**Date:** 2026-09-23
**Lane:** `unqualified-local-execution`
**Attempt:** `20260923-arm64-e2e-17`
**Repository base:** `05c20375f9f6ee3ba7bf8b871fee2058536b2998`, with the uncommitted change under review

## Scope

This record establishes one local, network-enabled ARM64 non-FDE build through the NKD public lifecycle. NKD built
the selected component closure, invoked the pinned NKE Packer project, collected exactly one QCOW2, recorded its
digest and size, and completed the immutable local Run. A subsequent boot smoke exercised the produced disk.

This record does not establish a GitHub-hosted run, published or signed toolchain, qualified reproduction, Docker
execution, FDE support, SBOM publication or release acceptance.

## Environment

| Item | Value |
|---|---|
| Container runtime | Podman client 6.1.2, Linux server 5.6.2 |
| Physical host | macOS Apple M4 Pro |
| VM host | Linux ARM64 Podman machine, 8 CPUs, 16,719,290,368 bytes memory |
| Acceleration | QEMU TCG, no KVM |
| Network | Producer-declared default bridge |
| NKE source | `de0e46bb14cae2f892677bf9e21b91a7add2f9d9` |
| Packer | 1.15.4 |
| QEMU plugin | 1.1.6 |

The source tree and exact material graph were frozen under the attempt before execution. The toolchain was a local,
content-addressed image built from `toolchains/nkd-packer/toolchain.lock.json`; no published toolchain identity is
claimed.

## Build result

The lifecycle record completed with:

```text
status: complete
lane: unqualified-local-execution
planDigest: sha256:1b34f04fb24f8c422e8af61340cbbacaced517e1c167cc0c71a9fe0fdda435c1
definitionDigest: sha256:d755c7325e53b9da2de2e282a96f06e7b7d4a96714f9365e3666dd851c2549ab
environmentDigest: sha256:cfd490721b979a25b2268e45d8a6adf4429e49a62c3cdbbb73a5b86a2856305b
```

The observed disk was:

```text
name: nke-node-image-arm64
format: qcow2
encryption: none
size: 2851076096
sha256: da7e44bf7192b113747de07f5eff189a1375eb7ee792971b9b3597947f67a8b7
```

The completed closure contained the NKE agent, Calico, OVN image, OVN chart, OVN CNI package, kubeadm
distributions, SPIRE package, patched Ubuntu installer ISO, aggregate image-input archive and final QCOW2. The
kubeadm package set contained the three source-owned versions selected by the pinned NKE source.

## Boot smoke

The NKE source-owned `os-image/packer/scripts/boot-smoke.sh` completed successfully against the collected QCOW2.
It observed UEFI and GRUB boot, ARM64 Linux startup, cloud-init completion, SSH access, root-filesystem growth, NKE
agent and updater version execution, installed NKE agent, SPIRE and Calico packages, and the local kubeadm and Calico
APT repositories. Its terminal result was:

```text
boot-smoke: image contract and root-filesystem growth are satisfied
```

The smoke run used `cortex-a57` for TCG. The pinned Packer recipe already selects that CPU for this build, but the
upstream smoke helper defaults to `max,pauth-impdef=on`; the test used an attempt-local copy to apply the same CPU.
No upstream byte was changed. A maintained smoke wrapper or an upstream override remains required before this boot
test is a one-command repository check.

## Acceptance boundary

This execution proves that the local NKD component graph and direct-Packer path can produce and boot one ARM64 NKE
QCOW2. GitHub generation still rejects requirement-bearing VM nodes, the lock contains no published toolchain image
digest, and the current generated NKE workflow does not contain this OS Definition. Those paths remain fail-closed.
After this run, the local Environment metadata was renamed to identify the macOS Apple M4 Pro and Podman runtime
explicitly. Its execution requirements did not change, but the rename changes Environment and Plan identity. The
renamed Podman and Docker inputs have static Plan validation only; this record retains the identities actually run.
