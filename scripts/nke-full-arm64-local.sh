#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Developer bootstrap for an authored subset, not a full source-built distribution.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"
usage() {
  printf '%s\n' \
    'Usage: bash scripts/nke-full-arm64-local.sh ATTEMPT [JOBS] [all|components|os|providers|simulator|site|tools|profiles]' \
    '       bash scripts/nke-full-arm64-local.sh --check [all|components|os|providers|simulator|site|tools|profiles]' \
    'all: 23 outputs; components: 22; os: one disk; providers: two source-built images.' \
    'simulator: one executable bundle with the source-built Linux ARM64 NICoSim server and CLI.' \
    'simulator bootstraps the same pinned Go/GCC toolchain on Podman or Docker.' \
    'site: four source-aligned installer outputs (controller, CLI, controller chart and NKTP chart).' \
    'tools: one source-built Linux ARM64 bundle (Helm, kubectl, clusterctl, ORAS and Kind).' \
    'profiles: one diagnostic ARM64 plain Ubuntu 26.04 NICoSim profiles image, not fleet profiles.' \
    'This subset is NOT a full source-built NKE distribution.' \
    'No host Go/Make or installed nkdctl needed: bootstrap uses the pinned utility image.' \
    'CONTAINER_RUNTIME=podman (default) or docker (native Linux ARM64 / explicitly selected Colima).' \
    'On Apple Silicon: Podman uses podman-machine-default; Docker requires COLIMA_PROFILE.' \
    'No VM is created, started, reset or resized automatically; Docker Desktop is not admitted.' \
    '--check performs prerequisite diagnostics only: no pull, build or attempt creation.' \
    'NKE_LOCAL_CLONE supplies pinned Git objects, not uncommitted changes.' \
    'See docs/local-development.md for clean setup and acceptance limits.'
}
if [[ $# == 1 && ( "$1" == --help || "$1" == -h ) ]]; then usage; exit 0; fi
check_only=0
if [[ "${1:-}" == --check ]]; then
  [[ $# -le 2 ]] || { usage >&2; exit 2; }
  check_only=1
  attempt=prerequisites-only
  jobs=1
  lane="${2:-all}"
else
  [[ $# -ge 1 && $# -le 3 ]] || { usage >&2; exit 2; }
  attempt="$1"
  jobs="${2:-1}"
  lane="${3:-all}"
fi
if [[ ! "${attempt}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ || ! "${jobs}" =~ ^[1-9][0-9]*$ ]]; then
  echo 'Supply a fresh attempt name and positive job count.' >&2; exit 2
fi
case "${lane}" in all|components|os|providers|simulator|site|tools|profiles) ;; *) echo 'Select all, components, os, providers, simulator, site, tools or profiles.' >&2; exit 2 ;; esac
runtime="${CONTAINER_RUNTIME:-podman}"
case "${runtime}" in podman|docker) ;; *) echo 'CONTAINER_RUNTIME must be podman or docker.' >&2; exit 2 ;; esac
host_system="$(uname -s)"
# Colima supplies Docker inside Linux; its Mac SSH entrypoint needs no Mac
# Docker CLI. Native execution and the Podman Mac route still need their CLI.
if [[ "${host_system}" != Darwin || "${runtime}" != docker ]] && ! command -v "${runtime}" >/dev/null; then
  echo "Missing ${runtime}. Install a container runtime first; see docs/local-development.md#start-from-a-clean-machine." >&2
  exit 1
fi
if [[ "${host_system}" == Darwin ]]; then
  [[ "$(uname -m)" == arm64 ]] || { echo 'The macOS ARM64 launcher requires Apple Silicon.' >&2; exit 1; }
  if [[ "${runtime}" == docker ]]; then
    profile="${COLIMA_PROFILE:-}"
    if [[ -z "${profile}" ]]; then
      echo 'Docker Desktop on macOS is not an admitted OS/bootstrap route. For Colima, explicitly set COLIMA_PROFILE to a running ARM64 Docker profile; see docs/local-development.md.' >&2
      exit 1
    fi
    if [[ ! "${profile}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]]; then
      echo 'COLIMA_PROFILE must be a simple profile name (letters, digits, underscore or hyphen).' >&2; exit 2
    fi
    if ! command -v colima >/dev/null; then
      echo 'Missing Colima. Install and explicitly start the selected ARM64 Docker profile; no VM was created.' >&2; exit 1
    fi
    if ! colima status --profile "${profile}" >/dev/null 2>&1; then
      echo "Colima profile ${profile} is absent or stopped. Start that profile explicitly; no VM was created or started." >&2; exit 1
    fi
    # Execute beside the daemon: host-side Docker socket forwarding does not
    # give NKD access to the Linux workspace/kernel used by nested producers.
    # Do not inherit a Mac Docker context/endpoint into the selected VM.
    if [[ "${check_only}" == 1 ]]; then
      printf -v remote_command 'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh --check %q' \
        "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${lane}"
    else
      printf -v remote_command 'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh %q %q %q' \
        "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${attempt}" "${jobs}" "${lane}"
    fi
    exec colima ssh --profile "${profile}" -- bash -c "${remote_command}"
  fi
  if ! podman machine inspect podman-machine-default >/dev/null 2>&1; then
    echo 'Missing podman-machine-default. See the clean-machine guide for podman machine init; no VM was created.' >&2
    exit 1
  fi
  if [[ "$(podman machine inspect --format '{{.State}}' podman-machine-default)" != running ]]; then
    echo 'Podman VM is stopped. Run: podman machine start podman-machine-default' >&2; exit 1
  fi
  if [[ "${check_only}" == 1 ]]; then
    printf -v remote_command 'cd %q && exec env CONTAINER_RUNTIME=podman NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh --check %q' \
      "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${lane}"
  else
    printf -v remote_command 'cd %q && exec env CONTAINER_RUNTIME=podman NKE_LOCAL_CLONE=%q bash scripts/nke-full-arm64-local.sh %q %q %q' \
      "${repo_root}" "${NKE_LOCAL_CLONE:-}" "${attempt}" "${jobs}" "${lane}"
  fi
  exec podman machine ssh --username core podman-machine-default "${remote_command}"
fi
if [[ "$(uname -s)" != Linux || "$(uname -m)" != aarch64 ]]; then
  echo 'Execution requires native Linux ARM64 (or Apple Silicon with Podman). AMD64 emulation is not admitted.' >&2; exit 1
fi
if [[ "${runtime}" == podman && "$(id -u)" == 0 ]]; then
  echo 'Use a non-root user for the Podman route.' >&2; exit 1
fi
# Advisory shell diagnostics do not replace complete-Plan runtime admission.
missing=0
for tool in jq git timeout; do
  if ! command -v "${tool}" >/dev/null; then echo "Missing ${tool}: install it on this Linux host/VM (see guide)." >&2; missing=1; fi
done
[[ "${missing}" == 0 ]] || exit 1
if ! timeout --kill-after=5s 30s "${runtime}" version --format '{{.Server.Version}}' >/dev/null; then
  echo "${runtime} daemon is unreachable. Start it or fix access for this user; no build started." >&2; exit 1
fi
if [[ "${runtime}" == docker ]]; then
  daemon_platform="$(timeout --kill-after=5s 30s docker info --format '{{.OSType}}/{{.Architecture}}')"
  case "${daemon_platform}" in linux/aarch64|linux/arm64) ;; *) echo "Docker daemon must be native Linux ARM64, got ${daemon_platform}." >&2; exit 1 ;; esac
  if [[ -n "${DOCKER_CONTEXT:-}" ]]; then
    endpoint="$(docker context inspect --format '{{.Endpoints.docker.Host}}' "${DOCKER_CONTEXT}")"
  elif [[ -n "${DOCKER_HOST:-}" ]]; then
    endpoint="${DOCKER_HOST}"
  else
    endpoint="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
  fi
  case "${endpoint}" in unix://*) ;; *) echo 'Remote Docker endpoints are not supported by this local launcher; run on the Docker host.' >&2; exit 1 ;; esac
fi
if [[ -n "${NKE_LOCAL_CLONE:-}" ]]; then
  git -C "${NKE_LOCAL_CLONE}" rev-parse --git-dir >/dev/null || { echo 'NKE_LOCAL_CLONE must be an accessible Git checkout.' >&2; exit 1; }
  nke_clone="$(cd "${NKE_LOCAL_CLONE}" && pwd -P)"
  export GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0="url.${nke_clone}.insteadOf" GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/nke.git
  export GIT_CONFIG_KEY_1="url.${nke_clone}.insteadOf" GIT_CONFIG_VALUE_1=https://github.com/dsx-ai-factory/nke
fi
environment="definitions/nke-laptop-arm64/environment-local.json"
if [[ "${runtime}" == docker ]]; then environment="definitions/nke-laptop-arm64/environment-local-docker.json"; fi
# This is one narrow developer bootstrap, not an Environment validator. Admit
# its exact reviewed bootstrap image/platform before creating storage or running
# caller-edited inputs. The compiled runner owns complete document validation.
utility='golang@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9'
if ! jq -e --arg image "${utility}" --arg runtime "${runtime}" '
  .utilityContainer == $image and .utilityPlatform == "linux/arm64" and
  .runtime.runtime == $runtime
' "${environment}" >/dev/null; then
  echo 'Environment does not match the admitted bootstrap image/platform/runtime. No downloads or attempt creation; use the maintained local Environment.' >&2
  exit 1
fi
if [[ "${lane}" == all || "${lane}" == os ]]; then
  cpu="$(getconf _NPROCESSORS_ONLN)"
  memory="$(awk '/^MemAvailable:/ {printf "%d", $2 / 1024}' /proc/meminfo)"
  free="$(df -Pm . | awk 'NR==2 {print $4}')"
  printf 'OS advisory preflight: CPU %s/8; available memory %s/10240 MiB; workspace %s/65536 MiB. Not reserved.\n' "${cpu}" "${memory}" "${free}"
  if [[ "${cpu}" -lt 8 || "${memory}" -lt 10240 || "${free}" -lt 65536 ]]; then
    echo 'Insufficient OS resources. Provide 8 CPUs, 10 GiB available RAM and 64 GiB free AFTER material preparation; allow about 90 GiB before a fresh build. No downloads started.' >&2; exit 1
  fi
  if [[ "${free}" -lt 92160 ]]; then echo 'Warning: less than 90 GiB free; material preparation may exhaust the later 64 GiB gate.' >&2; fi
  if [[ "${runtime}" == docker ]]; then docker buildx version >/dev/null || { echo 'Install the Docker Buildx plugin for OS toolchain bootstrap.' >&2; exit 1; }; fi
fi
if [[ "${lane}" == simulator || "${lane}" == site ]]; then
  if [[ "${runtime}" == docker ]] && ! docker buildx version >/dev/null; then
    echo 'Install the Docker Buildx plugin for local toolchain bootstrap.' >&2; exit 1
  fi
fi
echo "Prerequisite diagnostics passed for ${lane}/${runtime}. Git authorization and cgroup/resource/toolchain admission remain execution gates."
if [[ "${check_only}" == 1 ]]; then exit 0; fi
bootstrap=".nkd/laptop-bootstrap/${attempt}"
mkdir -p .nkd/laptop-bootstrap
mkdir "${bootstrap}" || { echo 'Bootstrap already exists; use a fresh attempt name.' >&2; exit 1; }
mkdir "${bootstrap}/go-build" "${bootstrap}/go-mod" "${bootstrap}/home" "${bootstrap}/tmp"
export TMPDIR="${repo_root}/${bootstrap}/tmp"
go_version="$(jq -er '.go.version' toolchains/nkd-packer/toolchain.lock.json)"
go_digest="$(jq -er '.go.archives.arm64' toolchains/nkd-packer/toolchain.lock.json)"
network=default
if [[ "${runtime}" == docker ]]; then network=bridge; fi
"${runtime}" pull --platform linux/arm64 "${utility}"
"${runtime}" run --rm --pull=never --platform linux/arm64 --network="${network}" \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges --security-opt=label=disable \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,nosuid,nodev \
  --volume "${repo_root}:/src:ro" --volume "${repo_root}/${bootstrap}:/out:rw" \
  --env CGO_ENABLED=0 --env GOTOOLCHAIN=local --env GOCACHE=/out/go-build --env GOMODCACHE=/out/go-mod --env HOME=/out/home \
  --env "NKD_BOOTSTRAP_GO_VERSION=${go_version}" --env "NKD_BOOTSTRAP_GO_SHA256=${go_digest#sha256:}" \
  "${utility}" bash -euc '
    archive="/out/go${NKD_BOOTSTRAP_GO_VERSION}.linux-arm64.tar.gz"
    curl --fail --location --max-time 300 --retry 2 \
      "https://go.dev/dl/go${NKD_BOOTSTRAP_GO_VERSION}.linux-arm64.tar.gz" --output "$archive"
    printf "%s  %s\n" "$NKD_BOOTSTRAP_GO_SHA256" "$archive" | sha256sum --check --status
    tar --extract --gzip --file "$archive" --directory /out
    export PATH="/out/go/bin:$PATH"
    make -C /src build BIN=/out/nkdctl
  '
runner="${bootstrap}/nkdctl"
[[ -f "${runner}" && -x "${runner}" && ! -L "${runner}" ]] || { echo 'Bootstrap did not produce a regular executable.' >&2; exit 1; }
definition="definitions/nke-laptop-arm64/definition.json"
if [[ "${lane}" != all ]]; then definition="definitions/nke-laptop-arm64/definition-${lane}.json"; fi
if [[ "${lane}" == all || "${lane}" == os || "${lane}" == simulator || "${lane}" == site ]]; then
  # Admit the compiled selection before building/loading/probing a replacement
  # toolchain. The narrow utility bootstrap above supplies this Plan owner.
  "${runner}" run plan --definition "${definition}" --environment "${environment}" --json >"${bootstrap}/baseline-plan.json"
  if [[ "${lane}" == site ]]; then
    baseline="$(jq -L scripts -er 'include "nke-os-plan"; site_helm_toolchain_scope.pin' "${bootstrap}/baseline-plan.json")"
  elif [[ "${lane}" == simulator ]]; then
    baseline="$(jq -L scripts -er 'include "nke-os-plan"; simulator_toolchain_scope.pin' "${bootstrap}/baseline-plan.json")"
  else
    baseline="$(jq -er '[.builds[] | select(any(.executionGraph.nodes[]; .builderType == "vm-image"))] |
    if length != 1 then error("expected one disk build") else .[0] end |
    .catalogSnapshot.Components[([.executionGraph.nodes[] | select(.kind == "build" and .builderType == "vm-image") | .component] |
      if length != 1 then error("expected one disk root") else .[0] end)].toolchainImages.arm64' "${bootstrap}/baseline-plan.json")"
  fi
  if [[ "${lane}" == site ]]; then
    toolchain_tag="localhost/nkd-local-helm:laptop-${attempt}"
    if [[ "${runtime}" == docker ]]; then
      docker buildx build --load --platform linux/arm64 --network=none --provenance=false --sbom=false --build-arg SOURCE_DATE_EPOCH=0 \
        --tag "${toolchain_tag}" toolchains/nkd-local-helm
    else
      podman build --platform linux/arm64 --network=none --timestamp=0 \
        --tag "${toolchain_tag}" toolchains/nkd-local-helm
    fi
    image="$("${runtime}" image inspect --format '{{.Id}}' "${toolchain_tag}")"
    image="sha256:${image#sha256:}"
    "${runtime}" run --rm --pull=never --platform linux/arm64 --network=none --read-only \
      --cap-drop=ALL --security-opt=no-new-privileges "${image}" sh -euc \
      'test "$(helm version --template "{{.Version}}")" = v4.3.0; command -v sha256sum; command -v sh'
  else
    image="$(jq -er '.localPodmanImages.arm64' toolchains/nkd-packer/toolchain.lock.json)"
    if [[ "${runtime}" != podman ]] || ! podman image inspect "${image}" >/dev/null 2>&1; then
      toolchain_tag="localhost/nkd-packer-toolchain:laptop-${attempt}"
      CONTAINER_RUNTIME="${runtime}" bash toolchains/nkd-packer/build.sh --arch arm64 --diagnostic-local --tag "${toolchain_tag}"
      image="$("${runtime}" image inspect --format '{{.Id}}' "${toolchain_tag}")"
      image="sha256:${image#sha256:}"
    fi
  fi
  # Freeze fresh local bytes through the existing overlay contract, not a tag
  # or a hand-edited maintained lock. Each output retains its own catalog scope.
  if [[ "${baseline}" != "${image}" ]]; then
  jq -e --arg root "${repo_root}" --arg out "${repo_root}/${bootstrap}" --arg lane "${lane}" '
    def absolute: if startswith("/") then . else $root + "/definitions/nke-laptop-arm64/" + . end;
    def locate: .base |= absolute | if .overlays then .overlays |= map(absolute) else . end;
    .catalog |= locate |
    .outputs |= map(if .catalog then .catalog |= locate else . end) |
    if $lane == "simulator" or $lane == "site" then
      .catalog.overlays = ((.catalog.overlays // []) + [$out + "/toolchain-attempt.json"])
    else .outputs |= map(if .format == "disk-image" then
      .catalog.overlays += [$out + "/toolchain-attempt.json"] else . end) end
  ' "${definition}" >"${bootstrap}/definition.json"
  if [[ "${lane}" == simulator || "${lane}" == site ]]; then
    jq -L scripts -e --arg to "${image}" --arg lane "${lane}" '
      include "nke-os-plan";
      (if $lane == "site" then site_helm_toolchain_scope else simulator_toolchain_scope end) |
      toolchain_overlay("arm64"; .pin; $to; .catalogDigest; .components)
    ' "${bootstrap}/baseline-plan.json" >"${bootstrap}/toolchain-attempt.json"
  else
    jq -L scripts -n --arg to "${image}" --slurpfile plan "${bootstrap}/baseline-plan.json" '
    include "nke-os-plan";
    [$plan[0].builds[] | select(any(.executionGraph.nodes[]; .builderType == "vm-image"))] |
    if length != 1 then error("expected one disk build") else .[0] end |
    . as $disk | [.executionGraph.nodes[] | select(.kind == "build" and .builderType == "vm-image") | .component] |
    if length != 1 then error("expected one disk root") else .[0] end |
    $disk.catalogSnapshot.Components[.].toolchainImages.arm64 as $from |
    [$disk.catalogSnapshot.Components | to_entries[] |
      select(.key as $name | $disk.components | index($name)) |
      select(.value.fromSource == true and .value.toolchainImages.arm64 == $from) | .key] | sort |
    toolchain_overlay("arm64"; $from; $to; $disk.catalogDigest; .)
    ' >"${bootstrap}/toolchain-attempt.json"
  fi
  definition="${bootstrap}/definition.json"
  fi
fi
"${runner}" run local --definition "${definition}" --environment "${environment}" --attempt "${attempt}-preview" --dry-run
exec "${runner}" run local --definition "${definition}" --environment "${environment}" \
  --attempt "${attempt}" --attempts-root .nkd/runs --runner-source . --jobs "${jobs}" --runtime "${runtime}"
