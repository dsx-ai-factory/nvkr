#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"

usage() {
  cat <<'EOF'
Usage: scripts/nke-os-arm64-local.sh ATTEMPT [JOBS]

Build the exact local ARM64 toolchain when its image variable is unset, freeze
the rendered recipe under .nkd/inputs/ATTEMPT, and execute one local ARM64
non-FDE NKE OS build with QEMU TCG. This is a development bootstrap route, not
a production runner profile.

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
if [[ "$(uname -s)" != Linux ]]; then
  echo "the NKE image build requires a Linux host" >&2
  exit 1
fi
case "$(uname -m)" in
  aarch64|arm64) ;;
  *)
    echo "the ARM64 bootstrap requires a Linux ARM64 host, found: $(uname -m)" >&2
    exit 1
    ;;
esac

runtime="${CONTAINER_RUNTIME:-podman}"
case "${runtime}" in
  podman|docker) ;;
  *) echo "CONTAINER_RUNTIME must be podman or docker" >&2; exit 2 ;;
esac
command -v "${runtime}" >/dev/null 2>&1 || {
  echo "container runtime not found: ${runtime}" >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  echo "python3 is required to freeze the exact catalog input" >&2
  exit 1
}
ovn_supplier="${NKE_OVN_SUPPLIER:-source}"
case "${ovn_supplier}" in
  source|prebuilt) ;;
  *) echo "NKE_OVN_SUPPLIER must be source or prebuilt" >&2; exit 2 ;;
esac

toolchain_image_arm64="${NKE_OS_TOOLCHAIN_IMAGE_ARM64:-}"
if [[ -z "${toolchain_image_arm64}" ]]; then
  toolchain_tag="localhost/nkd-nke-os-toolchain:${attempt}-arm64"
  CONTAINER_RUNTIME="${runtime}" toolchains/nkd-packer/build.sh \
    --arch arm64 --tag "${toolchain_tag}"
  toolchain_image_arm64="$(${runtime} image inspect --format '{{.Id}}' "${toolchain_tag}")"
  if [[ "${toolchain_image_arm64}" =~ ^[0-9a-f]{64}$ ]]; then
    toolchain_image_arm64="sha256:${toolchain_image_arm64}"
  fi
fi
if [[ ! "${toolchain_image_arm64}" =~ (^|@)sha256:[0-9a-f]{64}$ ]]; then
  echo "NKE_OS_TOOLCHAIN_IMAGE_ARM64 must be a repository digest or local sha256 image ID" >&2
  exit 1
fi

input_root=".nkd/inputs/${attempt}"
if [[ -e "${input_root}" || -L "${input_root}" ]]; then
  echo "frozen input already exists: ${input_root}" >&2
  exit 1
fi
mkdir -p "${input_root}/catalog"
cp definitions/nke-os-arm64/definition.yaml "${input_root}/definition.yaml"
cp definitions/nke-os-arm64/definition-ovn-prebuilt.yaml \
  "${input_root}/definition-ovn-prebuilt.yaml"
environment_source="definitions/nke-os-arm64/environment-local-macos-apple-m4-pro-podman.yaml"
if [[ "${runtime}" == docker ]]; then
  environment_source="definitions/nke-os-arm64/environment-local-macos-apple-m4-pro-docker.yaml"
fi
cp "${environment_source}" "${input_root}/environment-local.yaml"
cp definitions/nke-os-arm64/catalog/pins.lock.yaml "${input_root}/catalog/pins.lock.yaml"
cp -R definitions/nke-os-arm64/catalog/patches "${input_root}/catalog/patches"
cp definitions/nke-os-arm64/catalog/registry.yaml.in "${input_root}/catalog/registry.yaml"

NKE_OS_RENDER_IMAGE_ARM64="${toolchain_image_arm64}" \
  python3 - "${input_root}/catalog/registry.yaml" <<'PY'
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
content = path.read_text(encoding="utf-8")
tokens = {
    "${NKE_OS_TOOLCHAIN_IMAGE_ARM64}": (5, os.environ["NKE_OS_RENDER_IMAGE_ARM64"]),
}
for token, (expected, value) in tokens.items():
    if content.count(token) != expected:
        raise SystemExit(f"expected {expected} {token} tokens in {path}")
    content = content.replace(token, value)
path.write_text(content, encoding="utf-8")
PY

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

base_catalog_digest="$("${runner}" catalog digest --data "${input_root}/catalog")"
cp definitions/nke-os-arm64/ovn-prebuilt-overlay.yaml.in \
  "${input_root}/ovn-prebuilt-overlay.yaml"
NKE_OS_BASE_CATALOG_DIGEST="${base_catalog_digest}" python3 - \
  "${input_root}/ovn-prebuilt-overlay.yaml" <<'PY'
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
content = path.read_text(encoding="utf-8")
token = "${NKE_OS_BASE_CATALOG_DIGEST}"
if content.count(token) != 1:
    raise SystemExit(f"expected one {token} token in {path}")
path.write_text(content.replace(token, os.environ["NKE_OS_BASE_CATALOG_DIGEST"]), encoding="utf-8")
PY

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
