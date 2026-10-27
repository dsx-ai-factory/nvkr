#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"

usage() {
  cat <<'EOF'
Usage: scripts/nvke-os-local.sh ATTEMPT [JOBS]

Build the locked local toolchain, freeze the exact authored recipe under
.nvkr/inputs/ATTEMPT, and execute one local non-FDE NVKE OS build on native Linux
with Docker and KVM. This is a development bootstrap route.

CONTAINER_RUNTIME defaults to docker; this launcher requires Docker.
Set NVKE_OS_ARCH=amd64 or NVKE_OS_ARCH=arm64. ARM64 is the compatibility default.
Set NVKE_OS_UBUNTU_VERSION=24.04 to select Ubuntu 24.04.5; 26.04 is the default.
Set NVKE_LOCAL_CLONE to a local NVKE Git checkout when replacing the pinned
repository fetch.
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
input_root=".nvkr/inputs/${attempt}"
runtime="${CONTAINER_RUNTIME:-docker}"
arch="${NVKE_OS_ARCH:-arm64}"
ubuntu_version="${NVKE_OS_UBUNTU_VERSION:-26.04}"
case "${runtime}" in
  docker) ;;
  *) echo "CONTAINER_RUNTIME must be docker" >&2; exit 2 ;;
esac
case "${arch}" in
  amd64|arm64) ;;
  *) echo "NVKE_OS_ARCH must be amd64 or arm64" >&2; exit 2 ;;
esac
case "${ubuntu_version}" in
  24.04|26.04) ;;
  *) echo "NVKE_OS_UBUNTU_VERSION must be 24.04 or 26.04" >&2; exit 2 ;;
esac
command -v "${runtime}" >/dev/null 2>&1 || {
  echo "container runtime not found: ${runtime}" >&2
  exit 1
}

host_os="$(uname -s)"
if [[ "${host_os}" != Linux ]]; then
  echo "the NVKE image build requires native Linux with Docker and KVM" >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *) echo "unsupported Linux host architecture: $(uname -m)" >&2; exit 1 ;;
esac
if [[ "${arch}" != "${host_arch}" ]]; then
  echo "the OS bootstrap requires a native ${arch} host, found: ${host_arch}" >&2
  exit 1
fi
if [[ -n "${NVKE_LOCAL_CLONE:-}" ]]; then
  if [[ ! -e "${NVKE_LOCAL_CLONE}/.git" ]]; then
    echo "NVKE_LOCAL_CLONE is not a Git checkout: ${NVKE_LOCAL_CLONE}" >&2
    exit 1
  fi
  nvke_local_clone="$(cd "${NVKE_LOCAL_CLONE}" && pwd -P)"
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.${nvke_local_clone}.insteadOf"
  # Use the catalog's canonical .git URL, not a prefix shared by nvke-* repos.
  export GIT_CONFIG_VALUE_0="https://github.com/dsx-ai-factory/nvke.git"
fi
command -v jq >/dev/null 2>&1 || {
  echo "jq is required to validate and freeze the toolchain identity" >&2
  exit 1
}
definition_source="inputs/definitions/diagnostics/os-${arch}.yaml"
environment_source="inputs/environments/local-linux-${arch}-kvm.yaml"
catalog_source="inputs/catalogs/node-images/diagnostic"
definition_catalog='../../catalogs/node-images/diagnostic'
definition_overlays='[]'
overlay_source=""
ubuntu_overlay_source=""
if [[ "${arch}" == arm64 ]]; then
  overlay_source="inputs/catalogs/node-images/variants/diagnostic-arm64-kvm.yaml"
  definition_overlays='["../../catalogs/node-images/variants/diagnostic-arm64-kvm.yaml"]'
fi
if [[ "${ubuntu_version}" == 24.04 ]]; then
  definition_source="inputs/definitions/diagnostics/ubuntu24/definition-${arch}.yaml"
  catalog_source="inputs/catalogs/node-images/release"
  overlay_source="inputs/catalogs/node-images/variants/native-toolchains.yaml"
  ubuntu_overlay_source="inputs/catalogs/node-images/variants/ubuntu24.json"
  definition_catalog='../../../catalogs/node-images/release'
  definition_overlays='["../../../catalogs/node-images/variants/native-toolchains.yaml","../../../catalogs/node-images/variants/ubuntu24.json"]'
fi
required_inputs=("${definition_source}" "${environment_source}"
  "${catalog_source}/registry.yaml" "${catalog_source}/pins.lock.yaml")
if [[ -n "${overlay_source}" ]]; then
  required_inputs+=("${overlay_source}")
fi
if [[ -n "${ubuntu_overlay_source}" ]]; then
  required_inputs+=("${ubuntu_overlay_source}")
fi
for required_input in "${required_inputs[@]}"; do
  if [[ ! -f "${required_input}" ]]; then
    echo "maintained ${arch} OS input is unavailable: ${required_input}" >&2
    exit 1
  fi
done

mkdir -p .nvkr/inputs
if ! mkdir "${input_root}"; then
  echo "frozen input already exists or cannot be reserved: ${input_root}" >&2
  exit 1
fi

bootstrap_root=".nvkr/bootstrap/${attempt}"
mkdir -p "${bootstrap_root}/go-build" "${bootstrap_root}/go-mod" "${bootstrap_root}/home"
# A containerized runner and its Docker daemon need not share /tmp. Keep
# host-side temporary bind sources in the already shared build workspace.
mkdir -m 0700 "${bootstrap_root}/tmp"
export TMPDIR="${repo_root}/${bootstrap_root}/tmp"

toolchain_image="$(CONTAINER_RUNTIME="${runtime}" toolchains/nvkr-packer/build.sh \
  --arch "${arch}" --print-validated-image-id)"
if [[ ! "${toolchain_image}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "the native Docker build did not return an exact validated image ID" >&2
  exit 1
fi

mkdir -p "${input_root}/catalog"
cp "${environment_source}" "${input_root}/environment-local.yaml"
cp "${catalog_source}/pins.lock.yaml" "${input_root}/catalog/pins.lock.yaml"
cp "${catalog_source}/registry.yaml" "${input_root}/catalog/registry.yaml"
if [[ -n "${overlay_source}" ]]; then
  cp "${overlay_source}" "${input_root}/$(basename "${overlay_source}")"
fi
if [[ -n "${ubuntu_overlay_source}" ]]; then
  cp "${ubuntu_overlay_source}" "${input_root}/ubuntu24.json"
fi

# Build the bootstrap runner from this checkout in the exact pinned native
# toolchain. A pre-existing bin/nvkr may belong to an older revision.
"${runtime}" run --rm --pull=never --network=bridge \
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
  "${toolchain_image}" \
  make -C /src build BIN=/out/nvkr VERSION=dev COMMIT=unknown
runner="${bootstrap_root}/nvkr"
if [[ ! -x "${runner}" ]]; then
  echo "the pinned toolchain did not produce ${runner}" >&2
  exit 1
fi

frozen_definition="${input_root}/definition.yaml"
# Keep the exact selected catalog stack and source patches local to this attempt.
# Reject authored drift instead of silently replacing an unexpected catalog.
"${runner}" definition inspect --definition "${definition_source}" |
  jq --arg catalog "${definition_catalog}" --argjson overlays "${definition_overlays}" '
    if .catalog.base != $catalog or any(.outputs[]; .catalog != null) or
      (.catalog.overlays // []) != $overlays then
      error("OS bootstrap requires the maintained catalog and ordered overlays")
    else . end |
    .catalog.base = "catalog" |
    if .catalog.overlays then .catalog.overlays |= map(split("/") | last) else . end' \
  >"${frozen_definition}"
# The native Docker build is an attempt input, not a maintained release pin.
# Freeze its observed exact ID through the existing catalog overlay contract
# before compiling the executable Plan. The Packer runtime rechecks the ID.
plan="$("${runner}" run plan --definition "${frozen_definition}" \
  --environment "${input_root}/environment-local.yaml" --json)"
root_component="$(jq -L scripts -er 'include "nvke-os-plan"; disk_root' <<<"${plan}")"
baseline="$(jq -er --arg root "${root_component}" --arg arch "${arch}" \
  '.builds[0].catalogSnapshot.Components[$root].toolchainImages[$arch]' <<<"${plan}")"
if [[ "${baseline}" != "${toolchain_image}" ]]; then
  catalog_digest="$(jq -er '.builds[0].catalogDigest' <<<"${plan}")"
  components="$(jq -c --arg arch "${arch}" --arg baseline "${baseline}" '
    .builds[0].components as $selected |
    [.builds[0].catalogSnapshot.Components | to_entries[] |
      select(.key as $name | $selected | index($name)) |
      select(.value.fromSource == true and .value.toolchainImages[$arch] == $baseline) | .key] | sort
  ' <<<"${plan}")"
  if [[ "$(jq 'length' <<<"${components}")" -eq 0 ]]; then
    echo "no catalog components use the selected ${arch} toolchain baseline" >&2
    exit 1
  fi
  jq -L scripts -n --arg arch "${arch}" --arg from "${baseline}" --arg to "${toolchain_image}" \
    --arg digest "${catalog_digest}" --argjson components "${components}" '
    include "nvke-os-plan";
    toolchain_overlay($arch; $from; $to; $digest; $components)
  ' >"${input_root}/toolchain-attempt.json"
  "${runner}" definition inspect --definition "${frozen_definition}" |
    jq '.catalog.overlays = ((.catalog.overlays // []) + ["toolchain-attempt.json"])' \
    >"${input_root}/definition-attempt.json"
  frozen_definition="${input_root}/definition-attempt.json"
fi
final_plan="$("${runner}" run plan --definition "${frozen_definition}" \
  --environment "${input_root}/environment-local.yaml" --json)"
if ! jq -e --arg root "${root_component}" --arg image "${toolchain_image}" '
  [.builds[0].executionGraph.nodes[] |
    select(.kind == "build" and .component == $root and .toolchain == $image)] | length == 1
' <<<"${final_plan}" >/dev/null; then
  echo "the frozen ${arch} Plan does not bind the observed Packer toolchain" >&2
  exit 1
fi

exec "${runner}" run local \
  --definition "${frozen_definition}" \
  --environment "${input_root}/environment-local.yaml" \
  --attempt "${attempt}" \
  --attempts-root .nvkr/runs \
  --runner-source . \
  --jobs "${jobs}" \
  --runtime "${runtime}" --ui=plain
