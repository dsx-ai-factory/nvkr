#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"

usage() {
  cat <<'EOF'
Usage: scripts/nke-os-local.sh ATTEMPT [JOBS]

Build the locked local toolchain, freeze the exact authored recipe under
.nkd/inputs/ATTEMPT, and execute one local non-FDE NKE OS build. On macOS the
command delegates to the existing ARM64 Podman machine and uses QEMU TCG. This
is a development bootstrap route, not a production runner profile.

Set CONTAINER_RUNTIME=podman or CONTAINER_RUNTIME=docker. Podman is the default.
Set NKE_OS_ARCH=amd64 or NKE_OS_ARCH=arm64. ARM64 is the compatibility default.
Set NKE_LOCAL_CLONE to a local NKE Git checkout when replacing the pinned
repository fetch. On macOS, that path must also be visible inside the Podman
machine; the script checks it there before building.
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
runtime="${CONTAINER_RUNTIME:-podman}"
arch="${NKE_OS_ARCH:-arm64}"
case "${runtime}" in
  podman|docker) ;;
  *) echo "CONTAINER_RUNTIME must be podman or docker" >&2; exit 2 ;;
esac
case "${arch}" in
  amd64|arm64) ;;
  *) echo "NKE_OS_ARCH must be amd64 or arm64" >&2; exit 2 ;;
esac
command -v "${runtime}" >/dev/null 2>&1 || {
  echo "container runtime not found: ${runtime}" >&2
  exit 1
}

host_os="$(uname -s)"
if [[ "${host_os}" == Darwin ]]; then
  if [[ "${arch}" != arm64 ]]; then
    echo "the macOS bootstrap supports only the ARM64 TCG compatibility path" >&2
    exit 1
  fi
  if [[ "${runtime}" != podman ]]; then
    echo "the macOS bootstrap requires Podman; Docker is supported on native Linux hosts" >&2
    exit 1
  fi
  # podman machine ssh concatenates COMMAND arguments before the remote shell
  # evaluates them. Pass one shell-escaped command so paths and values retain
  # their argument boundaries inside the Linux VM.
  printf -v remote_command \
    'cd %q && exec env CONTAINER_RUNTIME=podman NKE_OS_ARCH=arm64 NKE_LOCAL_CLONE=%q ./scripts/nke-os-local.sh %q %q' \
    "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${attempt}" "${jobs}"
  exec podman machine ssh "${remote_command}"
fi
if [[ "${host_os}" != Linux ]]; then
  echo "the NKE image build requires native Linux or macOS with an ARM64 Podman machine" >&2
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
if [[ -n "${NKE_LOCAL_CLONE:-}" ]]; then
  if [[ ! -e "${NKE_LOCAL_CLONE}/.git" ]]; then
    echo "NKE_LOCAL_CLONE is not a Git checkout: ${NKE_LOCAL_CLONE}" >&2
    exit 1
  fi
  nke_local_clone="$(cd "${NKE_LOCAL_CLONE}" && pwd -P)"
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.${nke_local_clone}.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/dsx-ai-factory/nke"
fi
command -v jq >/dev/null 2>&1 || {
  echo "jq is required to validate and freeze the toolchain identity" >&2
  exit 1
}
toolchain_image=""
if [[ "${runtime}" == podman ]]; then
  toolchain_image="$(jq -r --arg arch "${arch}" '.localPodmanImages[$arch] // empty' toolchains/nkd-packer/toolchain.lock.json)"
  if [[ ! "${toolchain_image}" =~ (^|@)sha256:[0-9a-f]{64}$ ]]; then
    echo "the Packer toolchain lock has no exact ${arch} Podman image ID" >&2
    exit 1
  fi
fi
definition_source="definitions/nke-os/definition-${arch}-kvm.yaml"
environment_source="definitions/nke-os/environment-local-macos-apple-m4-pro-podman.yaml"
overlay_source=""
if [[ "${runtime}" == docker ]]; then
  environment_source="definitions/nke-os/environment-local-linux-${arch}-docker.yaml"
  # Only maintained Definitions that declare an architecture overlay need one.
  # A missing declared overlay still fails when NKD loads the frozen Definition.
  if [[ -f "definitions/nke-os/${arch}-kvm.yaml" ]]; then
    overlay_source="definitions/nke-os/${arch}-kvm.yaml"
  fi
elif [[ "${arch}" == arm64 ]]; then
  definition_source="definitions/nke-os/definition-arm64-tcg.yaml"
fi
required_inputs=("${definition_source}" "${environment_source}")
if [[ -n "${overlay_source}" ]]; then
  required_inputs+=("${overlay_source}")
fi
for required_input in "${required_inputs[@]}"; do
  if [[ ! -f "${required_input}" ]]; then
    echo "maintained ${arch} OS input is unavailable: ${required_input}" >&2
    exit 1
  fi
done

mkdir -p .nkd/inputs
if ! mkdir "${input_root}"; then
  echo "frozen input already exists or cannot be reserved: ${input_root}" >&2
  exit 1
fi

bootstrap_root=".nkd/bootstrap/${attempt}"
mkdir -p "${bootstrap_root}/go-build" "${bootstrap_root}/go-mod" "${bootstrap_root}/home"
if [[ "${runtime}" == docker ]]; then
  # A containerized runner and its Docker daemon need not share /tmp. Keep
  # host-side temporary bind sources in the already shared build workspace.
  # Reserve a private directory per immutable attempt; do not reuse a caller's
  # existing temporary tree or change the Podman compatibility path.
  mkdir -m 0700 "${bootstrap_root}/tmp"
  export TMPDIR="${repo_root}/${bootstrap_root}/tmp"
fi

CONTAINER_RUNTIME="${runtime}" toolchains/nkd-packer/build.sh --arch "${arch}"
if [[ "${runtime}" == docker ]]; then
  toolchain_image="$(docker image inspect --format '{{.Id}}' "localhost/nkd-packer-toolchain:dev-${arch}")"
  if [[ ! "${toolchain_image}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "the native Docker build did not produce an exact local image ID" >&2
    exit 1
  fi
fi

mkdir -p "${input_root}/catalog"
cp "${definition_source}" "${input_root}/definition.yaml"
cp "${environment_source}" "${input_root}/environment-local.yaml"
cp definitions/nke-os/catalog/pins.lock.yaml "${input_root}/catalog/pins.lock.yaml"
cp definitions/nke-os/catalog/registry.yaml "${input_root}/catalog/registry.yaml"
if [[ -n "${overlay_source}" ]]; then
  cp "${overlay_source}" "${input_root}/${arch}-kvm.yaml"
fi

# Build the bootstrap runner from this checkout in the exact pinned native
# toolchain. The Podman machine intentionally has no ambient Go or Make
# installation, and a pre-existing bin/nkdctl may belong to an older revision.
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
  "${toolchain_image}" \
  make -C /src build BIN=/out/nkdctl VERSION=dev COMMIT=unknown
runner="${bootstrap_root}/nkdctl"
if [[ ! -x "${runner}" ]]; then
  echo "the pinned toolchain did not produce ${runner}" >&2
  exit 1
fi

frozen_definition="${input_root}/definition.yaml"
if [[ "${runtime}" == docker ]]; then
  # The native Docker build is an attempt input, not a maintained release pin.
  # Freeze its observed exact ID through the existing catalog overlay contract
  # before compiling the executable Plan. The Packer runtime rechecks the ID.
  root_component=nke-node-image
  if [[ "${arch}" == amd64 ]]; then
    root_component=nke-node-image-amd64
  fi
  plan="$("${runner}" run plan --definition "${frozen_definition}" \
    --environment "${input_root}/environment-local.yaml" --json)"
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
    jq -n --arg arch "${arch}" --arg from "${baseline}" --arg to "${toolchain_image}" \
      --arg digest "${catalog_digest}" --argjson components "${components}" '
      {apiVersion:"nkd.dev/catalog-overlay/v3", kind:"CatalogOverlay",
       name:("nke-os-local-toolchain-" + $arch), requiresCatalogDigest:$digest,
       toolchainSubstitutions:[{arch:$arch, from:$from, to:$to, components:$components}]}
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
fi

exec "${runner}" run local \
  --definition "${frozen_definition}" \
  --environment "${input_root}/environment-local.yaml" \
  --attempt "${attempt}" \
  --attempts-root .nkd/runs \
  --runner-source . \
  --jobs "${jobs}" \
  --runtime "${runtime}"
