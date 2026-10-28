#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Own shell options in this child process, never the caller's SSH shell.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"
install_go=0
if [[ "${1:-}" == --install-go && $# == 1 ]]; then
  install_go=1
elif [[ $# != 0 ]]; then
  echo 'Usage: bash scripts/nvkr-quickstart.sh [--install-go]' >&2
  exit 1
fi
# Require explicit installation consent from an interactive terminal.
confirm() {
  local answer=""
  if [[ ! -t 0 ]]; then
    echo 'Installation needs confirmation in an interactive terminal.' >&2
    return 1
  fi
  printf '%s [y/N] ' "$1" >&2
  read -r answer || return 1
  [[ "${answer}" == y || "${answer}" == Y || "${answer}" == yes ]]
}
# Show the supported Docker installation action and run it only after consent.
install_docker() {
  local package=docker.io
  local -a privilege=()
  if command -v apt-get >/dev/null; then
    if [[ "$(id -u)" != 0 ]]; then
      command -v sudo >/dev/null || { echo 'sudo is required to install system packages.' >&2; return 1; }
      privilege=(sudo)
    fi
    printf 'Proposed install: %sapt-get update; %sapt-get install -y %s\n' "${privilege[*]:+sudo }" "${privilege[*]:+sudo }" "${package}" >&2
    confirm "Install ${package}?" || return 1
    "${privilege[@]}" apt-get update
    "${privilege[@]}" apt-get install -y "${package}"
  elif command -v brew >/dev/null; then
    echo 'Proposed install: brew install --cask docker' >&2
    confirm 'Install Docker Desktop?' || return 1
    brew install --cask docker
  else
    echo 'Install the missing tool with your system package manager, then rerun.' >&2
    return 1
  fi
}
case "$(uname -s)" in
  Linux) host_os=linux ;;
  Darwin) host_os=darwin ;;
  *) echo 'The quickstart supports macOS and Linux.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'The quickstart supports AMD64 and ARM64 processors.' >&2; exit 1 ;;
esac
command -v git >/dev/null || { echo 'Install Git 2.38 or later before running the quickstart.' >&2; exit 1; }
git ls-files --format='%(path)' -- . >/dev/null || {
  echo 'The quickstart requires Git 2.38 or later in an NVKR checkout.' >&2
  exit 1
}
if ! command -v docker >/dev/null; then install_docker; fi
if ! docker info >/dev/null; then
  echo 'Docker is not ready. Start Docker and grant your user daemon access, then rerun.' >&2
  exit 1
fi
go_version="$(cat .go-version)"
sdk="${repo_root}/.nvkr/quickstart-sdk/go${go_version}.${host_os}-${arch}"
if [[ -x "${sdk}/go/bin/go" ]]; then
  export PATH="${sdk}/go/bin:$PATH"
fi
# Never let a version probe or build install a compiler before consent.
export GOTOOLCHAIN=local
need_sdk=1
if command -v go >/dev/null; then
  if actual="$(go env GOVERSION)" && [[ "${actual}" == "go${go_version}" ]]; then need_sdk=0; fi
fi
if [[ "${need_sdk}" == 1 ]]; then
  printf 'Proposed Go installation: Go %s inside %s (no sudo or shell-profile changes).\n' "${go_version}" "${sdk}" >&2
  if [[ "${install_go}" != 1 ]]; then confirm 'Download, verify and install this Go SDK?' || exit 1; fi
  archive="go${go_version}.${host_os}-${arch}.tar.gz"
  checksum="${repo_root}/toolchains/quickstart/${archive}.sha256"
  [[ -f "${checksum}" ]] || { echo 'This checkout has no SDK checksum for your platform.' >&2; exit 1; }
  [[ ! -e "${sdk}" ]] || { echo 'An incomplete local SDK exists; retain it and use a fresh checkout.' >&2; exit 1; }
  mkdir -p "${repo_root}/.nvkr/quickstart-sdk"
  stage="$(mktemp -d "${repo_root}/.nvkr/quickstart-sdk/install.XXXXXX")"
  utility='golang@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9'
  docker run --rm --init --platform "linux/${arch}" \
    --read-only --cap-drop=ALL --security-opt=no-new-privileges \
    --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,nosuid,nodev \
    --volume "${stage}:/download:rw" --volume "${checksum}:/checksum:ro" \
    --workdir /download "${utility}" sh -euc '
      curl --fail --location --silent --show-error --proto "=https" --proto-redir "=https" \
        --connect-timeout 30 --max-time 300 --max-filesize 1073741824 \
        --output "$1" "https://go.dev/dl/$1"
      sha256sum --check /checksum
    ' quickstart-download "${archive}"
  tar -xzf "${stage}/${archive}" -C "${stage}"
  mv "${stage}" "${sdk}"
  export PATH="${sdk}/go/bin:$PATH"
fi
actual="$(go env GOVERSION)"
[[ "${actual}" == "go${go_version}" ]] || { printf 'Go %s does not match pinned go%s.\n' "${actual}" "${go_version}" >&2; exit 1; }
attempt="quickstart-$(date -u +%Y%m%d-%H%M%S)-$$"
outputs="${repo_root}/.nvkr/runs/${attempt}"
printf 'Build directory: %s\n' "${outputs}"
version="$(git describe --tags --always --dirty)"
commit="$(git rev-parse --short HEAD)"
go build -ldflags "-X main.version=${version} -X main.commit=${commit}" -o bin/nvkr ./cmd/nvkr
bin/nvkr --version
bin/nvkr run local \
  --definition "docs/examples/nvke-tutorial/definition-${arch}.yaml" \
  --environment "docs/examples/nvke-tutorial/environment-local-${arch}.yaml" \
  --attempt "${attempt}" --ui plain
ls -lh "${outputs}/nvkectl/out/nvkectl/"* \
  "${outputs}/nvke-controller/out/nvke-controller/"* \
  "${outputs}/nvke-controller-chart/out/nvke-controller-chart/"*
printf '\nBuild complete. Artifacts and SBOMs: %s\n' "${outputs}"
