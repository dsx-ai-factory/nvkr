#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
lock="${here}/toolchain.lock.json"

usage() {
  cat <<'EOF'
Usage: build.sh [--check] [--cross-build-only] [--arch amd64|arm64] [--tag IMAGE]

  --check             Validate the source lock without building an image.
  --cross-build-only  Build a non-native image without claiming runtime validation.
  --arch              Build one host architecture. Defaults to the normalized host arch.
  --tag               Local image name. Defaults to localhost/nkd-packer-toolchain:dev-ARCH.

This command builds and validates a local image. It never pushes, signs, or
writes an image digest into the lock.
EOF
}

check_only=0
cross_build_only=0
arch=""
tag=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --check)
      check_only=1
      shift
      ;;
    --cross-build-only)
      cross_build_only=1
      shift
      ;;
    --arch)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      arch="$2"
      shift 2
      ;;
    --tag)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      tag="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }

jq -e '
  .schemaVersion == "nkd.dev/packer-toolchain/v1" and
  (.ubuntuSnapshot | type == "string" and test("^[0-9]{8}T[0-9]{6}Z$")) and
  (.baseImage | type == "string" and test("^ubuntu@sha256:[0-9a-f]{64}$")) and
  (.downloadImage | type == "string" and test("^curlimages/curl@sha256:[0-9a-f]{64}$")) and
  .packer.version == "1.14.2" and
  .packer.license == "BUSL-1.1" and
  .qemuPlugin.source == "github.com/hashicorp/qemu" and
  .qemuPlugin.version == "1.1.6" and
  .qemuPlugin.license == "MPL-2.0" and
  ([.packer.archives, .packer.binaries, .qemuPlugin.archives, .qemuPlugin.binaries] | all(
    (keys | sort) == ["amd64", "arm64"] and
    all(.[]; test("^sha256:[0-9a-f]{64}$"))
  )) and
  (.images | keys | sort) == ["amd64", "arm64"] and
  all(.images[]; . == null or test("^.+@sha256:[0-9a-f]{64}$"))
' "${lock}" >/dev/null

if [[ "${check_only}" -eq 1 ]]; then
  echo "Packer toolchain source lock is valid"
  exit 0
fi

case "$(uname -m)" in
  x86_64) native_arch=amd64 ;;
  aarch64|arm64) native_arch=arm64 ;;
  *) echo "unsupported host architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [[ -z "${arch}" ]]; then
  arch="${native_arch}"
fi
case "${arch}" in
  amd64|arm64) ;;
  *) echo "unsupported architecture: ${arch}" >&2; exit 2 ;;
esac

if [[ "${arch}" != "${native_arch}" && "${cross_build_only}" -ne 1 ]]; then
  echo "refusing to validate ${arch} toolchain on ${native_arch}; use --cross-build-only or run on a native ${arch} host" >&2
  exit 1
fi

runtime="${CONTAINER_RUNTIME:-}"
if [[ -z "${runtime}" ]]; then
  runtime="$(command -v podman || command -v docker || true)"
fi
[[ -n "${runtime}" ]] || { echo "podman or docker is required" >&2; exit 1; }

base_image="$(jq -r '.baseImage' "${lock}")"
download_image="$(jq -r '.downloadImage' "${lock}")"
snapshot="$(jq -r '.ubuntuSnapshot' "${lock}")"
packer_version="$(jq -r '.packer.version' "${lock}")"
packer_digest="$(jq -r --arg arch "${arch}" '.packer.archives[$arch]' "${lock}")"
packer_binary_digest="$(jq -r --arg arch "${arch}" '.packer.binaries[$arch]' "${lock}")"
plugin_version="$(jq -r '.qemuPlugin.version' "${lock}")"
plugin_digest="$(jq -r --arg arch "${arch}" '.qemuPlugin.archives[$arch]' "${lock}")"
plugin_binary_digest="$(jq -r --arg arch "${arch}" '.qemuPlugin.binaries[$arch]' "${lock}")"
tag="${tag:-localhost/nkd-packer-toolchain:dev-${arch}}"

"${runtime}" build \
  --platform "linux/${arch}" \
  --build-arg "TARGETARCH=${arch}" \
  --build-arg "BASE_IMAGE=${base_image}" \
  --build-arg "DOWNLOAD_IMAGE=${download_image}" \
  --build-arg "UBUNTU_SNAPSHOT=${snapshot}" \
  --build-arg "PACKER_VERSION=${packer_version}" \
  --build-arg "PACKER_ARCHIVE_SHA256=${packer_digest#sha256:}" \
  --build-arg "PACKER_BINARY_SHA256=${packer_binary_digest#sha256:}" \
  --build-arg "QEMU_PLUGIN_VERSION=${plugin_version}" \
  --build-arg "QEMU_PLUGIN_ARCHIVE_SHA256=${plugin_digest#sha256:}" \
  --build-arg "QEMU_PLUGIN_BINARY_SHA256=${plugin_binary_digest#sha256:}" \
  --file "${here}/Dockerfile" \
  --tag "${tag}" \
  "${here}"

inspection="$("${runtime}" image inspect "${tag}")"
if ! jq -e --arg arch "${arch}" '
  length == 1 and
  .[0].Os == "linux" and
  .[0].Architecture == $arch and
  (.[0].Config.Env // []) == ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"] and
  ((.[0].Config.Volumes // {}) | length == 0)
' <<<"${inspection}" >/dev/null; then
  echo "toolchain image declares ambient environment or writable volumes" >&2
  exit 1
fi

image_id="$("${runtime}" image inspect --format '{{.Id}}' "${tag}")"
if [[ "${arch}" != "${native_arch}" ]]; then
  printf 'built structurally checked cross-architecture toolchain %s (%s)\n' "${tag}" "${image_id}"
  echo "run this build on a native ${arch} host before publication"
  exit 0
fi

"${runtime}" run --rm \
  --platform "linux/${arch}" \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --hostname nkd-packer \
  --tmpfs /nkd/work:rw,nosuid,nodev,mode=1777 \
  --env CHECKPOINT_DISABLE=1 \
  --env HOME=/nkd/work/home \
  --env PACKER_PLUGIN_PATH=/nkd/toolchain/packer/plugins \
  --env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  --env TMPDIR=/nkd/work/tmp \
  --entrypoint sh \
  "${tag}" -ec '
  mkdir -p "${HOME}" "${TMPDIR}"
  test "$(CHECKPOINT_DISABLE=1 packer version)" = "Packer v1.14.2"
  plugin=/nkd/toolchain/packer/plugins/github.com/hashicorp/qemu/packer-plugin-qemu_v1.1.6_x5.0_linux_'"${arch}"'
  test -x "${plugin}"
  test "$(cat "${plugin}_SHA256SUM")" = "$(sha256sum "${plugin}" | cut -d" " -f1)"
  test "$(packer plugins installed)" = "${plugin}"
  command -v qemu-img >/dev/null
  command -v qemu-system-aarch64 >/dev/null
  command -v qemu-system-x86_64 >/dev/null
  command -v ssh-keygen >/dev/null
  command -v bash >/dev/null
  command -v sh >/dev/null
  command -v install >/dev/null
  command -v mktemp >/dev/null
  command -v stat >/dev/null
  command -v mv >/dev/null
  command -v rm >/dev/null
  command -v chmod >/dev/null
  command -v chown >/dev/null
  command -v dirname >/dev/null
  command -v python3 >/dev/null
  command -v tar >/dev/null
  case "$(qemu-system-x86_64 -help)" in *"-vnc "*) ;; *) exit 1 ;; esac
  qemu-system-aarch64 -device virtio-gpu-pci,help >/dev/null
  qemu-system-aarch64 -device qemu-xhci,help >/dev/null
  qemu-system-aarch64 -device usb-kbd,help >/dev/null
  case "$(qemu-system-aarch64 -help)" in *"-vnc "*) ;; *) exit 1 ;; esac
  test -f /usr/share/OVMF/OVMF_CODE_4M.fd
  test -f /usr/share/OVMF/OVMF_VARS_4M.fd
  test -f /usr/share/AAVMF/AAVMF_CODE.fd
  test -f /usr/share/AAVMF/AAVMF_VARS.fd
'

printf 'validated local toolchain %s (%s)\n' "${tag}" "${image_id}"
echo "publish through an approved registry workflow, then record the immutable repository digest before catalog use"
