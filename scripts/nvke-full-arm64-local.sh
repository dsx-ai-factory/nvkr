#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Developer bootstrap for an authored subset, not a full source-built distribution.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${repo_root}"
print_nvkr_command() {
  printf 'nvkr command:'
  printf ' %q' "$@"
  printf '\n'
} >&2
usage() {
  printf '%s\n' \
    'Usage: bash scripts/nvke-full-arm64-local.sh ATTEMPT [JOBS] [all|components|os|providers|simulator|site|tools|profiles]' \
    '       bash scripts/nvke-full-arm64-local.sh --check [all|components|os|providers|simulator|site|tools|profiles]' \
    '       bash scripts/nvke-full-arm64-local.sh --prepare-only ATTEMPT [JOBS] [all|components|os|providers|simulator|site|tools|profiles]' \
    'Explicit upstream alternative: add --network upstream-ovn before ATTEMPT (all selection only).' \
    'Linux image builds require KVM by default. Explicit slow diagnostic: --build-accelerator tcg.' \
    'NVIDIA OVN remains the default. This alternative does not validate the NVIDIA fork.' \
    'Add --conformance for 38 outputs in one Run, including matching Kubernetes conformance tools (native Linux KVM all only).' \
    'all: one Run with 32 outputs; components: 22; os: one disk; providers: two source-built images.' \
    'simulator: one executable bundle with the source-built Linux ARM64 NICoSim server and CLI.' \
    'simulator bootstraps the same pinned Go/GCC Docker toolchain.' \
    'site: five source-aligned installer outputs (controller, profiles image, CLI, controller chart and NKTP chart).' \
    'tools: one source-built Linux ARM64 bundle (Helm, kubectl, clusterctl, ORAS and Kind).' \
    'profiles: one native OCI image containing diagnostic ARM64 plain Ubuntu 26.04 NICoSim profiles, not fleet profiles.' \
    'This subset is NOT a full source-built NVKE distribution.' \
    'No host Go/Make or installed nvkr needed: bootstrap uses the pinned utility image.' \
    'Docker is required (native Linux ARM64 / explicitly selected Colima).' \
    'On Apple Silicon: set COLIMA_PROFILE for an existing ARM64 Docker VM.' \
    'Colima os/all: export committed source to native ext4 and run inside Linux; Mac forwarding is rejected.' \
    'No VM is created, started, reset or resized automatically; Docker Desktop is not admitted.' \
    '--check performs prerequisite diagnostics only: no pull, build or attempt creation.' \
    '--prepare-only freezes the bootstrap and selected Plan without running producers (native Linux only).' \
    'For local qualification, author the exact policy after preparation, then use nvke-demo build/e2e --prepared.' \
    'NVKE_LOCAL_CLONE supplies pinned Git objects, not uncommitted changes.' \
    'The Go demo builds all selections in one Run; subset selectors remain available for diagnostics.' \
    'See docs/local-development.md for clean setup and acceptance limits.'
}
if [[ $# == 1 && ( "$1" == --help || "$1" == -h ) ]]; then usage; exit 0; fi
check_only=0
prepare_only=0
network=ovn
build_accelerator=kvm
conformance=0
# Go-demo preparation handoff: no activity descriptor is inherited by this shell.
# It prepares the same selected inputs, but the Go process launches the final
# unchanged run-local vector directly so only that child receives telemetry.
if [[ "${1:-}" == --prepare-only ]]; then
  prepare_only=1
  shift
fi
while [[ "${1:-}" == --network || "${1:-}" == --build-accelerator || "${1:-}" == --conformance ]]; do
  if [[ "$1" == --conformance ]]; then conformance=1; shift; continue; fi
  [[ $# -ge 2 ]] || { usage >&2; exit 2; }
  case "$1" in
    --network)
      network="$2"
      case "${network}" in ovn|upstream-ovn) ;; *) echo 'Build network must be ovn or upstream-ovn.' >&2; exit 2 ;; esac
      ;;
    --build-accelerator)
      build_accelerator="$2"
      case "${build_accelerator}" in kvm|tcg) ;; *) echo 'Build accelerator must be kvm or tcg.' >&2; exit 2 ;; esac
      ;;
  esac
  shift 2
done
if [[ "${1:-}" == --check ]]; then
  [[ "${prepare_only}" == 0 ]] || { usage >&2; exit 2; }
  [[ $# -le 2 ]] || { usage >&2; exit 2; }
  check_only=1
  attempt=prerequisites-only
  jobs=1
  lane="${2:-components}"
else
  [[ $# -ge 1 && $# -le 3 ]] || { usage >&2; exit 2; }
  attempt="$1"
  jobs="${2:-1}"
  lane="${3:-components}"
fi
if [[ ! "${attempt}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ || ! "${jobs}" =~ ^[1-9][0-9]*$ ]]; then
  echo 'Supply a fresh attempt name and positive job count.' >&2; exit 2
fi
case "${lane}" in all|components|os|providers|simulator|site|tools|profiles) ;; *) echo 'Select all, components, os, providers, simulator, site, tools or profiles.' >&2; exit 2 ;; esac
if [[ "${network}" == upstream-ovn && "${lane}" != all ]]; then
  echo 'Upstream OVN requires the complete all selection so chart, image and QCOW2 inputs stay aligned.' >&2; exit 2
fi
if [[ "${conformance}" == 1 && ( "${lane}" != all || "${build_accelerator}" != kvm || "$(uname -s)" != Linux ) ]]; then
  echo "Conformance selection requires native Linux KVM and the complete all selection." >&2; exit 2
fi
# Keep invocation-local redirects when a second local repository is selected.
# Bound the scalar before arithmetic, host diagnostics or any producer work.
git_config_count="${GIT_CONFIG_COUNT-0}"
if [[ ! "${git_config_count}" =~ ^(0|[1-9]|1[0-6])$ ]]; then
  echo 'GIT_CONFIG_COUNT must be a decimal integer from 0 through 16.' >&2; exit 2
fi
if [[ -n "${NVKE_LOCAL_CLONE:-}" && "${git_config_count}" -gt 14 ]]; then
  echo 'NVKE_LOCAL_CLONE requires two free Git configuration entries (total limit 16).' >&2; exit 2
fi
runtime="${CONTAINER_RUNTIME:-docker}"
[[ "${runtime}" == docker ]] || { echo 'CONTAINER_RUNTIME must be docker for this local demo.' >&2; exit 2; }
host_system="$(uname -s)"
# Colima supplies Docker inside Linux; its Mac SSH entrypoint needs no Mac
# Docker CLI. Native Linux execution needs its own Docker CLI.
if [[ "${host_system}" != Darwin ]] && ! command -v docker >/dev/null; then
  echo 'Missing Docker. Install it first; see docs/local-development.md#start-from-a-clean-machine.' >&2
  exit 1
fi
if [[ "${host_system}" == Darwin ]]; then
  [[ "${prepare_only}" == 0 ]] || { echo 'Preparation requires native Linux execution.' >&2; exit 1; }
  [[ "$(uname -m)" == arm64 ]] || { echo 'The macOS ARM64 launcher requires Apple Silicon.' >&2; exit 1; }
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
  if [[ "${lane}" == os || "${lane}" == all ]]; then
    echo 'Colima os/all cannot run from the Mac shared mount. Export committed source to native ext4 and run inside Linux; see docs/local-development.md#apple-silicon-mac-with-colima. No downloads or attempt creation.' >&2
    exit 1
  fi
  # Execute beside the daemon: host-side Docker socket forwarding does not
  # give NVKR access to the Linux workspace/kernel used by nested producers.
  # Do not inherit a Mac Docker context/endpoint into the selected VM.
  # SSH does not forward the invocation environment. Preserve only this bounded
  # redirect vector; the Linux sourcegit owner still validates its keys/values.
  printf -v remote_redirects ' %q' "GIT_CONFIG_COUNT=${git_config_count}"
  for ((index = 0; index < git_config_count; index++)); do
    key="GIT_CONFIG_KEY_${index}"
    value="GIT_CONFIG_VALUE_${index}"
    if [[ -z "${!key-}" || -z "${!value-}" ]]; then
      echo 'Each invocation-local Git entry needs a nonempty key and value.' >&2
      exit 2
    fi
    printf -v remote_entry ' %q %q' "${key}=${!key}" "${value}=${!value}"
    remote_redirects+="${remote_entry}"
  done
  if [[ "${check_only}" == 1 ]]; then
    printf -v remote_command 'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NVKE_LOCAL_CLONE=%q%s bash scripts/nvke-full-arm64-local.sh --check %q' \
      "${repo_root}" "${NVKE_LOCAL_CLONE:-}" "${remote_redirects}" "${lane}"
  else
    printf -v remote_command 'cd %q && exec env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker NVKE_LOCAL_CLONE=%q%s bash scripts/nvke-full-arm64-local.sh %q %q %q' \
      "${repo_root}" "${NVKE_LOCAL_CLONE:-}" "${remote_redirects}" "${attempt}" "${jobs}" "${lane}"
  fi
  exec colima ssh --profile "${profile}" -- bash -c "${remote_command}"
fi
if [[ "$(uname -s)" != Linux || "$(uname -m)" != aarch64 ]]; then
  echo 'Execution requires native Linux ARM64 (including a Colima Linux VM). AMD64 emulation is not admitted.' >&2; exit 1
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
if [[ -n "${NVKE_LOCAL_CLONE:-}" ]]; then
  [[ "${NVKE_LOCAL_CLONE}" == /* ]] || { echo 'NVKE_LOCAL_CLONE must be an absolute path.' >&2; exit 2; }
  git -C "${NVKE_LOCAL_CLONE}" rev-parse --git-dir >/dev/null || { echo 'NVKE_LOCAL_CLONE must be an accessible Git checkout.' >&2; exit 1; }
  nvke_clone="$(cd "${NVKE_LOCAL_CLONE}" && pwd -P)"
  export "GIT_CONFIG_KEY_${git_config_count}=url.${nvke_clone}.insteadOf" "GIT_CONFIG_VALUE_${git_config_count}=https://github.com/dsx-ai-factory/nvke.git"
  git_config_count=$((git_config_count + 1))
  export "GIT_CONFIG_KEY_${git_config_count}=url.${nvke_clone}.insteadOf" "GIT_CONFIG_VALUE_${git_config_count}=https://github.com/dsx-ai-factory/nvke"
  export GIT_CONFIG_COUNT=$((git_config_count + 1))
  # Git insteadOf is prefix-based; pinned commit/tree verification still owns
  # source identity. The bare NVKE form also prefix-matches sibling repository
  # names, so use this override only for this maintained NVKE selection.
fi
environment="inputs/environments/local-arm64-kvm.json"
if [[ "${build_accelerator}" == tcg ]]; then environment="inputs/environments/local-arm64-tcg.json"; fi
# This is one narrow developer bootstrap, not an Environment validator. Admit
# its exact reviewed bootstrap image/platform before creating storage or running
# caller-edited inputs. The compiled runner owns complete document validation.
utility='golang@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc'
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
fi
if [[ "${lane}" == all || "${lane}" == os || "${lane}" == simulator || "${lane}" == site ]]; then
  if ! docker buildx version >/dev/null 2>&1; then
    printf '%s\n' 'Docker Buildx plugin is missing or unusable; local toolchain bootstrap requires it.' \
      'Ubuntu docker.io: sudo apt-get install docker-buildx' \
      "Docker CE from Docker's apt repository: sudo apt-get install docker-buildx-plugin" \
      'Then verify: docker buildx version. No downloads or attempt creation.' >&2
    exit 1
  fi
fi
echo "Prerequisite diagnostics passed for ${lane}/${runtime}. Git authorization and cgroup/resource/toolchain admission remain execution gates."
if [[ "${check_only}" == 1 ]]; then exit 0; fi
bootstrap=".nvkr/laptop-bootstrap/${attempt}"
shared_bootstrap="${NVKR_LOCAL_BOOTSTRAP:-}"
if [[ -n "${shared_bootstrap}" ]]; then
  [[ "${shared_bootstrap}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || { echo 'Invalid shared bootstrap name.' >&2; exit 2; }
  tools=".nvkr/laptop-bootstrap/${shared_bootstrap}"
  [[ -d "${tools}" && ! -L "${tools}" ]] || { echo 'Shared bootstrap is missing or not a real directory.' >&2; exit 1; }
else
  tools="${bootstrap}"
fi
mkdir -p .nvkr/laptop-bootstrap
mkdir "${bootstrap}" || { echo 'Bootstrap already exists; use a fresh attempt name.' >&2; exit 1; }
mkdir "${bootstrap}/tmp"
export TMPDIR="${repo_root}/${bootstrap}/tmp"
if [[ -z "${shared_bootstrap}" ]]; then
  mkdir "${bootstrap}/go-build" "${bootstrap}/go-mod" "${bootstrap}/home"
  "${runtime}" pull --platform linux/arm64 "${utility}"
  "${runtime}" run --rm --pull=never --platform linux/arm64 --network=bridge \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges --security-opt=label=disable \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,nosuid,nodev \
  --volume "${repo_root}:/src:ro" --volume "${repo_root}/${bootstrap}:/out:rw" \
  --env CGO_ENABLED=0 --env GOTOOLCHAIN=local --env GOCACHE=/out/go-build --env GOMODCACHE=/out/go-mod --env HOME=/out/home \
  "${utility}" bash -euc '
    # make checks the container SDK against .go-version before compiling.
    make -C /src build BIN=/out/nvkr
    make -C /src demo DEMO_BIN=/out/nvke-demo
  '
fi
runner="${tools}/nvkr"
[[ -f "${runner}" && -x "${runner}" && ! -L "${runner}" ]] || { echo 'Bootstrap did not produce a regular executable.' >&2; exit 1; }
definition="inputs/definitions/demo/arm64-nvidia-local-kvm.json"
if [[ "${network}" == upstream-ovn ]]; then definition="inputs/definitions/demo/arm64-upstream-local-kvm.json"; fi
if [[ "${build_accelerator}" == tcg ]]; then
  definition="inputs/definitions/demo/arm64-nvidia-tcg.json"
  if [[ "${network}" == upstream-ovn ]]; then definition="inputs/definitions/demo/arm64-upstream-tcg.json"; fi
fi
if [[ "${lane}" != all ]]; then definition="inputs/definitions/diagnostics/demo-arm64/${lane}.json"; fi
if [[ "${lane}" == os && "${build_accelerator}" == kvm ]]; then definition="inputs/definitions/diagnostics/demo-arm64/os-kvm.json"; fi
if [[ "${conformance}" == 1 ]]; then definition="${definition%.json}-conformance.json"; fi
if [[ "${lane}" == all || "${lane}" == os || "${lane}" == simulator || "${lane}" == site ]]; then
  # Admit the compiled selection before building/loading/probing a replacement
  # toolchain. The narrow utility bootstrap above supplies this Plan owner.
  plan_command=("${runner}" run plan --definition "${definition}" --environment "${environment}" --json)
  print_nvkr_command "${plan_command[@]}"
  "${plan_command[@]}" >"${bootstrap}/baseline-plan.json"
  graph_command=("${runner}" run graph --plan "${bootstrap}/baseline-plan.json" --view complete --format mermaid)
  print_nvkr_command "${graph_command[@]}"
  {
    printf '# Planned build graph\n\nBaseline selection before local toolchain substitution; description only, not execution evidence.\n\n```mermaid\n'
    "${graph_command[@]}"
    printf '```\n'
  } >"${bootstrap}/build-graph.md"
  printf 'Build graph: %s/%s/build-graph.md\n' "${repo_root}" "${bootstrap}" >&2
  jq -r '
    "Build graph: planned outputs: \(.lifecycleOutputs | length); producer steps: \([.builds[].executionGraph.nodes[]?] | length); producer dependency edges: \([.builds[].executionGraph.nodes[]?.needs[]?] | length)",
    (.lifecycleOutputs[]? | "  output: \(.name) [\(.kind)]: \(.components | join(", "))")
  ' "${bootstrap}/baseline-plan.json" >&2
  jq -L scripts -e --arg lane "${lane}" 'include "nvke-os-plan"; local_toolchain_scopes($lane)' \
    "${bootstrap}/baseline-plan.json" >"${bootstrap}/toolchain-scopes.json"
  if [[ -n "${shared_bootstrap}" ]]; then
    # This is an explicit invocation-local tool selection, not a serialized
    # receipt. The next Plan still admits the exact image and substitution.
    [[ -f "${tools}/toolchain-image" && ! -L "${tools}/toolchain-image" ]] || { echo 'Shared bootstrap lacks its measured toolchain image.' >&2; exit 1; }
    image="$(<"${tools}/toolchain-image")"
    [[ "${image}" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'Shared bootstrap image must be an immutable native image ID.' >&2; exit 1; }
    actual="$(timeout --kill-after=5s 30s "${runtime}" image inspect --format '{{.Id}}' "${image}")"
    [[ "sha256:${actual#sha256:}" == "${image}" ]] || { echo 'Shared toolchain bytes are unavailable in this runtime.' >&2; exit 1; }
  else
    toolchain_tag="localhost/nvkr-packer-toolchain:laptop-${attempt}"
    image="$(CONTAINER_RUNTIME="${runtime}" bash toolchains/nvkr-packer/build.sh \
      --arch arm64 --diagnostic-local --print-validated-image-id --tag "${toolchain_tag}")"
    [[ "${image}" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'Toolchain build did not return one native-validated immutable image ID.' >&2; exit 1; }
    printf '%s\n' "${image}" >"${tools}/toolchain-image"
  fi
  # Freeze fresh local bytes through the existing overlay contract, not a tag
  # or a hand-edited maintained lock. Each output retains its own catalog scope.
  jq -L scripts --arg image "${image}" 'include "nvke-os-plan"; local_toolchain_substitutions($image)' "${bootstrap}/toolchain-scopes.json" >"${bootstrap}/substitutions.json"
  if [[ "$(jq length "${bootstrap}/substitutions.json")" != 0 ]]; then
  while IFS= read -r index; do
    file=toolchain-attempt.json
    if [[ "${index}" != 0 ]]; then file="toolchain-attempt-${index}.json"; fi
    jq -L scripts -e --argjson index "${index}" --arg to "${image}" 'include "nvke-os-plan"; .[$index] |
      toolchain_overlay("arm64"; .pin; $to; .catalogDigest; .components)' \
      "${bootstrap}/substitutions.json" >"${bootstrap}/${file}"
  done < <(jq -r 'keys[]' "${bootstrap}/substitutions.json")
  # The generated Definition lives three directories below the repository.
  # Keep its references relocatable so compilation can capture and replay the
  # original input closure without the producer checkout's absolute paths.
  definition_dir="${definition%/*}"
  jq -L scripts -e --arg definition_dir "${definition_dir}" \
    --slurpfile scopes "${bootstrap}/substitutions.json" \
    'include "nvke-os-plan"; local_toolchain_definition($definition_dir; "../../../"; $scopes[0])' \
    "${definition}" >"${bootstrap}/definition.json"
  definition="${bootstrap}/definition.json"
  fi
fi
selected_plan_command=("${runner}" run plan --definition "${definition}" --environment "${environment}" --json)
print_nvkr_command "${selected_plan_command[@]}"
"${selected_plan_command[@]}" >"${bootstrap}/selected-plan.json"
preview_command=("${runner}" run local --definition "${definition}" --environment "${environment}" --attempt "${attempt}-preview" --capture-inputs-root=. --dry-run)
print_nvkr_command "${preview_command[@]}"
"${preview_command[@]}"
build_command=("${runner}" run local --definition "${definition}" --environment "${environment}" \
  --attempt "${attempt}" --attempts-root .nvkr/runs --runner-source . --jobs "${jobs}" --runtime "${runtime}" --capture-inputs-root=. --ui=plain)
if [[ "${prepare_only}" == 1 ]]; then
  # This is a private description, not executable authority or source evidence.
  # Preserve the exact final argv selected above; never pass it through eval.
  (umask 077; set -o noclobber; jq -cn --args '$ARGS.positional' -- "${build_command[@]}" >"${bootstrap}/build-invocation.json")
  exit 0
fi
print_nvkr_command "${build_command[@]}"
exec "${build_command[@]}"
