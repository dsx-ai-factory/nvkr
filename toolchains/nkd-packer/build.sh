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

# This image also compiles the NKD bootstrap, whose Makefile requires the
# repository's exact Go patch. Reject drift before building either architecture.
bootstrap_go_version="$(cat "${here}/../../.go-version")"
if [[ ! "${bootstrap_go_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "invalid bootstrap Go pin in .go-version" >&2
  exit 1
fi
if ! jq -e --arg version "${bootstrap_go_version}" '.go.version == $version' "${lock}" >/dev/null; then
  echo "Packer toolchain Go version must match .go-version (${bootstrap_go_version}); update the locked version and both archive digests" >&2
  exit 1
fi

jq -e '
  .schemaVersion == "nkd.dev/packer-toolchain/v1" and
  (.ubuntuSnapshot | type == "string" and test("^[0-9]{8}T[0-9]{6}Z$")) and
  (.baseImage | type == "string" and test("^ubuntu@sha256:[0-9a-f]{64}$")) and
  (.downloadImage | type == "string" and test("^curlimages/curl@sha256:[0-9a-f]{64}$")) and
  .validatedNKE.commit == "726f76944404e1106626b8333d3576bdf67b575b" and
  .validatedNKE.goVersion == "1.27.1" and
  .validatedNKE.packerProject == "os-image/packer" and
  .validatedNKE.requiredBuild == "nke-node-non-fde.qemu.nke-non-fde" and
  .packer.version == "1.15.4" and
  .packer.license == "BUSL-1.1" and
  .qemuPlugin.source == "github.com/hashicorp/qemu" and
  .qemuPlugin.version == "1.1.6" and
  .qemuPlugin.license == "MPL-2.0" and
  .go.license == "BSD-3-Clause" and
  .crane.version == "0.21.9" and
  .crane.license == "Apache-2.0" and
  .nfpm.version == "2.47.0" and
  .nfpm.license == "MIT" and
  .publicationGates.packerBUSLRedistributionApproval == "not-recorded" and
  .publicationGates.vulnerabilityReviewApproval == "not-recorded" and
  ([.packer.archives, .packer.binaries, .qemuPlugin.archives, .qemuPlugin.binaries,
    .go.archives, .crane.archives, .crane.binaries, .nfpm.archives, .nfpm.binaries] | all(
    (keys | sort) == ["amd64", "arm64"] and
    all(.[]; test("^sha256:[0-9a-f]{64}$"))
  )) and
  (.localPodmanImages | keys | sort) == ["amd64", "arm64"] and
  all(.localPodmanImages[]; . == null or test("^sha256:[0-9a-f]{64}$")) and
  (.localDockerImages | keys | sort) == ["amd64", "arm64"] and
  all(.localDockerImages[]; . == null or test("^sha256:[0-9a-f]{64}$")) and
  (.images | keys | sort) == ["amd64", "arm64"] and
  all(.images[]; . == null or test("^.+@sha256:[0-9a-f]{64}$"))
' "${lock}" >/dev/null

if [[ "${check_only}" -eq 1 ]]; then
  echo "Packer toolchain source lock is valid"
  exit 0
fi

command -v mktemp >/dev/null 2>&1 || { echo "mktemp is required" >&2; exit 1; }

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

build_inputs=()
if [[ -n "${NKD_BUILD_ARCHIVES:-}" || -n "${NKD_BUILD_CA_BUNDLE:-}" ]]; then
  [[ "${runtime##*/}" == docker ]] || { echo 'archive/CA inputs require Docker Buildx' >&2; exit 1; }
fi
if [[ -n "${NKD_BUILD_ARCHIVES:-}" ]]; then
  [[ -d "${NKD_BUILD_ARCHIVES}" ]] || { echo 'archive context is not a directory' >&2; exit 1; }
  build_inputs+=(--build-context "nkd_archives=${NKD_BUILD_ARCHIVES}" --build-arg NKD_ARCHIVE_MODE=local)
fi
if [[ -n "${NKD_BUILD_CA_BUNDLE:-}" ]]; then
  [[ -f "${NKD_BUILD_CA_BUNDLE}" ]] || { echo 'build CA bundle is not a regular file' >&2; exit 1; }
  build_inputs+=(--secret "id=nkd_ca_bundle,src=${NKD_BUILD_CA_BUNDLE}")
fi

uid="$(id -u)"
gid="$(id -g)"
identity_dir=""
cleanup_identity() {
  if [[ -n "${identity_dir}" ]]; then
    rm -f -- "${identity_dir}/passwd" "${identity_dir}/group"
    rmdir -- "${identity_dir}"
  fi
}
trap cleanup_identity EXIT
case "${runtime##*/}" in
  podman)
    identity_args=(
      --passwd-entry "nkd:x:${uid}:${gid}:NKD Builder:/nkd/work/home:/bin/sh"
      --group-entry "nkd:x:${gid}:"
    )
    ;;
  docker)
    identity_dir="$(mktemp -d "${TMPDIR:-/tmp}/nkd-packer-identity.XXXXXX")"
    printf 'nkd:x:%s:%s:NKD Builder:/nkd/work/home:/bin/sh\n' "${uid}" "${gid}" \
      >"${identity_dir}/passwd"
    printf 'nkd:x:%s:\n' "${gid}" >"${identity_dir}/group"
    chmod 0400 "${identity_dir}/passwd" "${identity_dir}/group"
    identity_args=(
      --mount "type=bind,src=${identity_dir}/passwd,dst=/etc/passwd,readonly"
      --mount "type=bind,src=${identity_dir}/group,dst=/etc/group,readonly"
    )
    ;;
  *)
    echo "unsupported container runtime: ${runtime}" >&2
    exit 1
    ;;
esac

base_image="$(jq -r '.baseImage' "${lock}")"
download_image="$(jq -r '.downloadImage' "${lock}")"
snapshot="$(jq -r '.ubuntuSnapshot' "${lock}")"
packer_version="$(jq -r '.packer.version' "${lock}")"
packer_digest="$(jq -r --arg arch "${arch}" '.packer.archives[$arch]' "${lock}")"
packer_binary_digest="$(jq -r --arg arch "${arch}" '.packer.binaries[$arch]' "${lock}")"
plugin_version="$(jq -r '.qemuPlugin.version' "${lock}")"
plugin_digest="$(jq -r --arg arch "${arch}" '.qemuPlugin.archives[$arch]' "${lock}")"
plugin_binary_digest="$(jq -r --arg arch "${arch}" '.qemuPlugin.binaries[$arch]' "${lock}")"
go_version="$(jq -r '.go.version' "${lock}")"
go_digest="$(jq -r --arg arch "${arch}" '.go.archives[$arch]' "${lock}")"
crane_version="$(jq -r '.crane.version' "${lock}")"
crane_digest="$(jq -r --arg arch "${arch}" '.crane.archives[$arch]' "${lock}")"
crane_binary_digest="$(jq -r --arg arch "${arch}" '.crane.binaries[$arch]' "${lock}")"
nfpm_version="$(jq -r '.nfpm.version' "${lock}")"
nfpm_digest="$(jq -r --arg arch "${arch}" '.nfpm.archives[$arch]' "${lock}")"
nfpm_binary_digest="$(jq -r --arg arch "${arch}" '.nfpm.binaries[$arch]' "${lock}")"
tag="${tag:-localhost/nkd-packer-toolchain:dev-${arch}}"

build_command=("${runtime}" build)
if [[ "${runtime##*/}" == docker ]]; then
  # Buildx passes the fixed epoch into BuildKit's image config and history,
  # then rewrites image-layer file timestamps to the same epoch on export.
  # Explicit stdout export avoids the Docker driver's automatic image-store
  # unpack, which cannot be combined with timestamp rewriting. Load the
  # archive separately below; pipefail preserves failures from either stage.
  # Runtime-specific recorded IDs remain independent and are checked below;
  # changes to the image sources require fresh measurements.
  "${runtime}" buildx version
  "${runtime}" version --format 'Docker server {{.Server.Version}}'
  build_command=("${runtime}" buildx build
    --output "type=docker,dest=-,rewrite-timestamp=true"
    --build-arg SOURCE_DATE_EPOCH=0)
fi

# Bash 3.2 treats an empty array expansion as unset under nounset. Optional
# archive/CA inputs must add no arguments, without weakening failure handling.
if [[ "${#build_inputs[@]}" -gt 0 ]]; then
  build_command+=("${build_inputs[@]}")
fi
build_command+=(
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
  --build-arg "GO_VERSION=${go_version}" \
  --build-arg "GO_ARCHIVE_SHA256=${go_digest#sha256:}" \
  --build-arg "CRANE_VERSION=${crane_version}" \
  --build-arg "CRANE_ARCHIVE_SHA256=${crane_digest#sha256:}" \
  --build-arg "CRANE_BINARY_SHA256=${crane_binary_digest#sha256:}" \
  --build-arg "NFPM_VERSION=${nfpm_version}" \
  --build-arg "NFPM_ARCHIVE_SHA256=${nfpm_digest#sha256:}" \
  --build-arg "NFPM_BINARY_SHA256=${nfpm_binary_digest#sha256:}" \
  --file "${here}/Dockerfile" \
  --tag "${tag}" \
  "${here}")

if [[ "${runtime##*/}" == docker ]]; then
  "${build_command[@]}" | "${runtime}" image load
else
  "${build_command[@]}"
fi

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
if [[ "${image_id}" =~ ^[0-9a-f]{64}$ ]]; then
  image_id="sha256:${image_id}"
fi
case "${runtime##*/}" in
  podman) pin_family=localPodmanImages ;;
  docker) pin_family=localDockerImages ;;
esac
expected_local_image="$(jq -r --arg arch "${arch}" --arg family "${pin_family}" '.[$family][$arch] // empty' "${lock}")"
if [[ -n "${expected_local_image}" && "${image_id}" != "${expected_local_image}" ]]; then
  echo "local toolchain image ID ${image_id} does not match locked ${arch} ID ${expected_local_image}" >&2
  exit 1
fi
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
  --user "${uid}:${gid}" \
  "${identity_args[@]}" \
  --hostname nkd-packer \
  --tmpfs /nkd/work:rw,nosuid,nodev,mode=1777 \
  --env CHECKPOINT_DISABLE=1 \
  --env HOME=/nkd/work/home \
  --env "EXPECTED_GO_VERSION=${go_version}" \
  --env PACKER_PLUGIN_PATH=/nkd/toolchain/packer/plugins \
  --env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  --env TMPDIR=/nkd/work/tmp \
  --entrypoint sh \
  "${tag}" -ec '
  mkdir -p "${HOME}" "${TMPDIR}"
  test "$(CHECKPOINT_DISABLE=1 packer version)" = "Packer v1.15.4"
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
  command -v curl >/dev/null
  command -v cloud-localds >/dev/null
  command -v dpkg-scanpackages >/dev/null
  command -v file >/dev/null
  command -v gcc >/dev/null
  command -v git >/dev/null
  command -v go >/dev/null
  command -v gpg >/dev/null
  command -v gzip >/dev/null
  command -v make >/dev/null
  command -v crane >/dev/null
  command -v nfpm >/dev/null
  command -v 7z >/dev/null
  command -v unzip >/dev/null
  command -v xorriso >/dev/null
  test "$(GOTOOLCHAIN=local go version)" = "go version go${EXPECTED_GO_VERSION} linux/'"${arch}"'"
  test "$(crane version)" = "0.21.9"
  nfpm --version | grep -Eq "^GitVersion:[[:space:]]+2.47.0$"
  dpkg-scanpackages --version >/dev/null
  7z i >/dev/null
  xorriso -version >/dev/null 2>&1
  case "$(qemu-system-x86_64 -help)" in *"-vnc "*) ;; *) exit 1 ;; esac
  qemu-system-aarch64 -device virtio-gpu-pci,help >/dev/null
  qemu-system-aarch64 -device qemu-xhci,help >/dev/null
  qemu-system-aarch64 -device usb-kbd,help >/dev/null
  case "$(qemu-system-aarch64 -help)" in *"-vnc "*) ;; *) exit 1 ;; esac
  test -f /usr/share/OVMF/OVMF_CODE_4M.fd
  test -f /usr/share/OVMF/OVMF_VARS_4M.fd
  test -f /usr/share/AAVMF/AAVMF_CODE.fd
  test -f /usr/share/AAVMF/AAVMF_VARS.fd
  key="${TMPDIR}/id_ed25519"
  ssh-keygen -q -t ed25519 -N "" -f "${key}"
  test "$(ssh-keygen -y -f "${key}" | cut -d" " -f1-2)" = "$(cut -d" " -f1-2 "${key}.pub")"
  mkdir -p "${TMPDIR}/packer-probe"
  cat >"${TMPDIR}/packer-probe/probe.pkr.hcl" <<"EOF"
packer {
  required_version = "= 1.15.4"
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "= 1.1.6"
    }
  }
}
source "qemu" "probe" {
  iso_url          = "https://example.invalid/installer.iso"
  iso_checksum     = "none"
  ssh_username     = "probe"
  accelerator      = "none"
  disk_size        = "1G"
  format           = "qcow2"
  headless         = true
  shutdown_command = "echo ready"
  output_directory = "/nkd/work/packer-probe-output"
}
build {
  sources = ["source.qemu.probe"]
}
EOF
  packer validate "${TMPDIR}/packer-probe/probe.pkr.hcl" >"${TMPDIR}/packer-validate.out"
  grep -Fx "The configuration is valid." "${TMPDIR}/packer-validate.out" >/dev/null
'

printf 'validated local toolchain %s (%s)\n' "${tag}" "${image_id}"
echo "publish through an approved registry workflow, then record the immutable repository digest before catalog use"
