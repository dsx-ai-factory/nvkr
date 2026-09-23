#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"

usage() {
  cat <<'EOF'
Usage: scripts/nke-os-arm64-local.sh ATTEMPT [JOBS]

Build the locked local ARM64 toolchain, freeze the exact authored recipe under
.nkd/inputs/ATTEMPT, and execute one local ARM64 non-FDE NKE OS build with QEMU
TCG. On macOS the command delegates to the existing ARM64 Podman machine. This
is a development bootstrap route, not a production runner profile.

Set CONTAINER_RUNTIME=podman or CONTAINER_RUNTIME=docker. Podman is the default.

NKE_OVN_SUPPLIER=source (the default) builds OVN from its pinned upstream
source. Set NKE_OVN_SUPPLIER=prebuilt to select the explicit digest-pinned
upstream-image overlay instead.
EOF
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage >&2
  exit 2
fi
attempt="$1"
jobs="${2:-1}"
if [[ ! "${attempt}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
  echo "invalid attempt name: ${attempt}" >&2
  exit 2
fi
if [[ ! "${jobs}" =~ ^[1-9][0-9]*$ ]]; then
  echo "JOBS must be a positive integer" >&2
  exit 2
fi
input_root=".nkd/inputs/${attempt}"
if [[ -e "${input_root}" || -L "${input_root}" ]]; then
  echo "frozen input already exists: ${input_root}" >&2
  exit 1
fi
runtime="${CONTAINER_RUNTIME:-podman}"
case "${runtime}" in
  podman|docker) ;;
  *) echo "CONTAINER_RUNTIME must be podman or docker" >&2; exit 2 ;;
esac
ovn_supplier="${NKE_OVN_SUPPLIER:-source}"
case "${ovn_supplier}" in
  source|prebuilt) ;;
  *) echo "NKE_OVN_SUPPLIER must be source or prebuilt" >&2; exit 2 ;;
esac
command -v "${runtime}" >/dev/null 2>&1 || {
  echo "container runtime not found: ${runtime}" >&2
  exit 1
}

host_os="$(uname -s)"
if [[ "${host_os}" == Darwin ]]; then
  if [[ "${runtime}" != podman ]]; then
    echo "the macOS bootstrap requires Podman; Docker is supported on Linux ARM64" >&2
    exit 1
  fi
  # podman machine ssh concatenates COMMAND arguments before the remote shell
  # evaluates them. Pass one shell-escaped command so paths and values retain
  # their argument boundaries inside the Linux VM.
  printf -v remote_command \
    'cd %q && exec env CONTAINER_RUNTIME=podman NKE_OVN_SUPPLIER=%q ./scripts/nke-os-arm64-local.sh %q %q' \
    "${repo_root}" "${ovn_supplier}" "${attempt}" "${jobs}"
  exec podman machine ssh "${remote_command}"
fi
if [[ "${host_os}" != Linux ]]; then
  echo "the NKE image build requires Linux ARM64 or macOS with an ARM64 Podman machine" >&2
  exit 1
fi
case "$(uname -m)" in
  aarch64|arm64) ;;
  *)
    echo "the ARM64 bootstrap requires a Linux ARM64 host, found: $(uname -m)" >&2
    exit 1
    ;;
esac
command -v jq >/dev/null 2>&1 || {
  echo "jq is required to read the locked local toolchain identity" >&2
  exit 1
}
toolchain_image_arm64="$(jq -r '.localImages.arm64 // empty' toolchains/nkd-packer/toolchain.lock.json)"
if [[ ! "${toolchain_image_arm64}" =~ (^|@)sha256:[0-9a-f]{64}$ ]]; then
  echo "the Packer toolchain lock has no exact ARM64 local image ID" >&2
  exit 1
fi
CONTAINER_RUNTIME="${runtime}" toolchains/nkd-packer/build.sh --arch arm64

mkdir -p "${input_root}/catalog"
cp definitions/nke-os-arm64/definition.yaml "${input_root}/definition.yaml"
cp definitions/nke-os-arm64/definition-ovn-prebuilt.yaml \
  "${input_root}/definition-ovn-prebuilt.yaml"
environment_source="definitions/nke-os-arm64/environment-local-macos-apple-m4-pro-podman.yaml"
if [[ "${runtime}" == docker ]]; then
  environment_source="definitions/nke-os-arm64/environment-local-linux-arm64-docker.yaml"
fi
cp "${environment_source}" "${input_root}/environment-local.yaml"
cp definitions/nke-os-arm64/catalog/pins.lock.yaml "${input_root}/catalog/pins.lock.yaml"
cp definitions/nke-os-arm64/catalog/registry.yaml "${input_root}/catalog/registry.yaml"

# Build the bootstrap runner from this checkout in the exact pinned ARM64
# toolchain. The Podman machine intentionally has no ambient Go or Make
# installation, and a pre-existing bin/nkdctl may belong to an older revision.
bootstrap_root=".nkd/bootstrap/${attempt}"
mkdir -p "${bootstrap_root}/go-build" "${bootstrap_root}/go-mod" "${bootstrap_root}/home"
bootstrap_network=default
if [[ "${runtime}" == docker ]]; then
  bootstrap_network=bridge
fi
"${runtime}" run --rm --pull=never --network="${bootstrap_network}" \
  --read-only --cap-drop=all \
  --security-opt no-new-privileges --security-opt label=disable \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,nosuid,nodev \
  --volume "${repo_root}:/src:ro" \
  --volume "${repo_root}/${bootstrap_root}:/out:rw" \
  --env CGO_ENABLED=0 \
  --env GOCACHE=/out/go-build \
  --env GOMODCACHE=/out/go-mod \
  --env GOTOOLCHAIN=local \
  --env HOME=/out/home \
  "${toolchain_image_arm64}" \
  make -C /src build BIN=/out/nkdctl VERSION=dev COMMIT=unknown
runner="${bootstrap_root}/nkdctl"
if [[ ! -x "${runner}" ]]; then
  echo "the pinned toolchain did not produce ${runner}" >&2
  exit 1
fi

cp definitions/nke-os-arm64/ovn-prebuilt-overlay.yaml \
  "${input_root}/ovn-prebuilt-overlay.yaml"

definition_path="${input_root}/definition.yaml"
if [[ "${ovn_supplier}" == prebuilt ]]; then
  definition_path="${input_root}/definition-ovn-prebuilt.yaml"
fi

exec "${runner}" run local \
  --definition "${definition_path}" \
  --environment "${input_root}/environment-local.yaml" \
  --attempt "${attempt}" \
  --attempts-root .nkd/runs \
  --runner-source . \
  --jobs "${jobs}" \
  --runtime "${runtime}"
