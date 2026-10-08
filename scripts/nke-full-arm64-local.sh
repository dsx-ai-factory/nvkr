#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Diagnostic launcher for the authored 21-output laptop inputs, not publication.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"
if [[ $# == 1 && "$1" == --help ]]; then
  printf '%s\n' \
    'Usage: bash scripts/nke-full-arm64-local.sh ATTEMPT [JOBS] [all|components|os]' \
    'all: one combined 21-output ARM64 Run; components: 20 non-disk outputs; os: one disk.' \
    'Requires the repository-pinned host Go version and native rootless Podman.' \
    'On Apple Silicon, executes inside the existing podman-machine-default VM.' \
    'NKE_LOCAL_CLONE supplies pinned Git objects, not uncommitted source changes.' \
    'See docs/local-development.md for setup, outputs and OS prerequisites.'
  exit 0
fi
if [[ $# -lt 1 || $# -gt 3 ]]; then
  echo 'Usage: bash scripts/nke-full-arm64-local.sh ATTEMPT [JOBS] [all|components|os]' >&2
  exit 2
fi
attempt="$1"
jobs="${2:-1}"
lane="${3:-all}"
if [[ ! "${attempt}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ || ! "${jobs}" =~ ^[1-9][0-9]*$ ]]; then
  echo 'Supply a fresh attempt name and positive job count.' >&2
  exit 2
fi
case "${lane}" in
  all|components|os) ;;
  *) echo 'Select all, components or os.' >&2; exit 2 ;;
esac
bootstrap=".nkd/laptop-bootstrap/${attempt}"
build_runner() {
  mkdir -p .nkd/laptop-bootstrap
  mkdir "${bootstrap}"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOTOOLCHAIN=local \
    make build "BIN=${repo_root}/${bootstrap}/nkdctl"
}
if [[ "$(uname -s)" == Darwin ]]; then
  if [[ "$(uname -m)" != arm64 ]]; then
    echo 'The macOS launcher requires Apple Silicon.' >&2
    exit 1
  fi
  build_runner
  printf -v remote_command \
    'cd %q && exec env NKD_LAPTOP_GUEST=1 NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh %q %q %q' \
    "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${attempt}" "${jobs}" "${lane}"
  exec podman machine ssh --username core podman-machine-default "${remote_command}"
fi
if [[ "$(uname -s)" != Linux || "$(uname -m)" != aarch64 || "$(id -u)" == 0 ]]; then
  echo 'This launcher requires the native Linux ARM64 rootless Podman user.' >&2
  exit 1
fi
for tool in podman jq git; do
  command -v "${tool}" >/dev/null || { echo "Missing tool: ${tool}" >&2; exit 1; }
done
if [[ -n "${NKE_LOCAL_CLONE:-}" ]]; then
  git -C "${NKE_LOCAL_CLONE}" rev-parse --git-dir >/dev/null
  nke_clone="$(cd "${NKE_LOCAL_CLONE}" && pwd -P)"
  export GIT_CONFIG_COUNT=2
  export GIT_CONFIG_KEY_0="url.${nke_clone}.insteadOf"
  export GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke.git
  export GIT_CONFIG_KEY_1="url.${nke_clone}.insteadOf"
  export GIT_CONFIG_VALUE_1=https://github.com/dsx-ai-factory/nke
fi

environment=definitions/nke-laptop-arm64/environment-local.json
# Disk work retains the exact maintained Packer subject, never a mutable tag.
if [[ "${lane}" != components ]]; then
  image="$(jq -er '.localPodmanImages.arm64' toolchains/nkd-packer/toolchain.lock.json)"
  actual="$(podman image inspect --format '{{.Id}}' "${image}")"
  actual="sha256:${actual#sha256:}"
  if [[ "${actual}" != "${image}" ]]; then
    echo 'The rootless store does not contain the exact maintained toolchain.' >&2
    exit 1
  fi
fi
test -f definitions/nke-laptop-arm64/definition-components.json
test -f definitions/nke-laptop-arm64/definition-os.json
test -f "${environment}"
test -f definitions/nke-laptop-arm64/operator-catalog/pins.lock.yaml
if [[ "${NKD_LAPTOP_GUEST:-}" != 1 ]]; then
  build_runner
fi
runner="${bootstrap}/nkdctl"
if [[ ! -f "${runner}" || ! -x "${runner}" || -L "${runner}" ]]; then
  echo 'The attempt-owned Linux runner is missing or not a regular executable.' >&2
  exit 1
fi
definition="definitions/nke-laptop-arm64/definition.json"
if [[ "${lane}" != all ]]; then
  definition="definitions/nke-laptop-arm64/definition-${lane}.json"
fi
"${runner}" run local --definition "${definition}" --environment "${environment}" \
  --attempt "${attempt}-preview" --dry-run
"${runner}" run local --definition "${definition}" --environment "${environment}" \
  --attempt "${attempt}" --attempts-root .nkd/runs --runner-source . --jobs "${jobs}" --runtime podman
