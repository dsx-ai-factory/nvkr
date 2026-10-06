#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Native preparation produces measurements, not publication approval.
set -euo pipefail

arch="${1:-}"
case "${arch}" in amd64|arm64) ;; *) echo 'usage: prepare-native-toolchains.sh amd64|arm64' >&2; exit 2 ;; esac
[[ "$(uname -s)" == Linux ]] || { echo 'native Linux is required' >&2; exit 1; }
case "$(uname -m)" in x86_64) host_arch=amd64 ;; aarch64|arm64) host_arch=arm64 ;; *) exit 1 ;; esac
[[ "${arch}" == "${host_arch}" ]] || { echo 'cross-architecture measurement is forbidden' >&2; exit 1; }
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"
[[ -z "$(git status --porcelain --untracked-files=normal)" ]] || { echo 'native preparation requires a clean source checkout' >&2; exit 1; }
build_tls=()
if [[ -n "${NKD_BUILD_CA_BUNDLE:-}" ]]; then
  [[ -f "${NKD_BUILD_CA_BUNDLE}" ]] || { echo 'build CA bundle is not a regular file' >&2; exit 1; }
  build_tls=(--secret "id=nkd_ca_bundle,src=${NKD_BUILD_CA_BUNDLE}")
fi
if [[ -n "${NKD_BUILD_ARCHIVES:-}" ]]; then
  [[ -d "${NKD_BUILD_ARCHIVES}" ]] || { echo 'archive context is not a directory' >&2; exit 1; }
  build_tls+=(--build-context "nkd_archives=${NKD_BUILD_ARCHIVES}" --build-arg NKD_ARCHIVE_MODE=local)
fi
export GOTOOLCHAIN=local
[[ "$(go env GOVERSION)" == "go$(cat .go-version)" ]] || { echo 'the exact pinned Go compiler is required' >&2; exit 1; }
for tool in docker jq sha256sum timeout gzip; do command -v "${tool}" >/dev/null; done
daemon_arch="$(docker info --format '{{.OSType}}/{{.Architecture}}')"
case "${arch}:${daemon_arch}" in amd64:linux/x86_64|amd64:linux/amd64|arm64:linux/aarch64|arm64:linux/arm64) ;; *) echo 'Docker daemon must match the native host' >&2; exit 1 ;; esac

output=".nkd/native-toolchains/${arch}"
mkdir -p "${output}" bin
CONTAINER_RUNTIME=docker timeout --signal=TERM --kill-after=30s 150m \
  toolchains/nkd-packer/build.sh --arch "${arch}" 2>&1 | tee "${output}/packer-build.log"
CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" timeout --signal=TERM --kill-after=30s 15m \
  go build -trimpath -buildvcs=false -o "bin/nkd-disk-inspect-${arch}" ./cmd/nkd-disk-inspect
mkdir -p "${output}/context/bin"
cp "bin/nkd-disk-inspect-${arch}" "${output}/context/bin/"
timeout --signal=TERM --kill-after=30s 150m docker buildx build \
  "${build_tls[@]}" \
  --platform "linux/${arch}" --build-arg "TARGETARCH=${arch}" --build-arg SOURCE_DATE_EPOCH=0 \
  --output 'type=docker,dest=-,rewrite-timestamp=true' \
  -f toolchains/nkd-disk-inspect/Dockerfile -t "localhost/nkd-disk-inspect:local-${arch}" "${output}/context" \
  2> >(tee "${output}/inspector-build.log" >&2) | timeout --signal=TERM --kill-after=30s 150m docker load

packer_id="$(docker image inspect --format '{{.Id}}' "localhost/nkd-packer-toolchain:dev-${arch}")"
inspector_id="$(docker image inspect --format '{{.Id}}' "localhost/nkd-disk-inspect:local-${arch}")"
for image_id in "${packer_id}" "${inspector_id}"; do
  [[ "${image_id}" =~ ^sha256:[0-9a-f]{64}$ ]]
  [[ "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${image_id}")" == "linux/${arch}" ]]
done
run=(timeout --signal=TERM --kill-after=10s 5m docker run --rm --pull=never
  --platform "linux/${arch}" --network none --read-only --cap-drop ALL
  --security-opt no-new-privileges --user 65534:65534)
"${run[@]}" --entrypoint sha256sum "${inspector_id}" \
  /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish \
  /usr/local/bin/syft /usr/bin/dpkg-deb /usr/local/bin/nkd-disk-inspect \
  >"${output}/executables.sha256"
"${run[@]}" --entrypoint cat "${inspector_id}" /usr/share/nkd-disk-inspect/packages.tsv \
  >"${output}/packages.tsv"
[[ "$(sha256sum "bin/nkd-disk-inspect-${arch}" | cut -d' ' -f1)" == "$(awk '$2 == "/usr/local/bin/nkd-disk-inspect" {print $1}' "${output}/executables.sha256")" ]]
"${run[@]}" --entrypoint /bin/sh "${inspector_id}" -ec \
  'virt-inspector --version; qemu-img --version; guestfish --version; syft version; dpkg-deb --version' \
  >"${output}/inspector-versions.log"
helper_status=0
"${run[@]}" --entrypoint /usr/local/bin/nkd-disk-inspect "${inspector_id}" \
  >"${output}/helper.stdout" 2>"${output}/helper-admission.log" || helper_status=$?
[[ "${helper_status}" == 2 && ! -s "${output}/helper.stdout" ]]
grep -F '[NKD-E-USAGE]' "${output}/helper-admission.log" >/dev/null
# Match the owner's private scratch layout. QEMU redirects exactly /tmp to
# /var/tmp for snapshot overlays, which is read-only in this container.
"${run[@]}" --cpus 2 --memory 4g --memory-swap 4g --pids-limit 512 \
  --tmpfs /tmp:rw,nosuid,nodev,size=2147483648,mode=1777 \
  --env HOME=/tmp/probe-work --env TMPDIR=/tmp/probe-work \
  --env LIBGUESTFS_CACHEDIR=/tmp/probe-work --env LIBGUESTFS_TMPDIR=/tmp/probe-work \
  --env LIBGUESTFS_BACKEND=direct --env LIBGUESTFS_BACKEND_SETTINGS=force_tcg \
  --entrypoint /bin/sh "${inspector_id}" -ec '
    mkdir -m 0700 /tmp/probe-work
    qemu-img create -f qcow2 /tmp/probe.qcow2 64M
    result=$(guestfish --rw -a /tmp/probe.qcow2 run : part-disk /dev/sda mbr : mkfs ext4 /dev/sda1 : mount /dev/sda1 / : write /probe native-inspector : cat /probe)
    test "$result" = native-inspector
    printf "Native libguestfs TCG appliance filesystem round trip passed\n"
  ' >"${output}/appliance-probe.log" 2>&1
timeout --signal=TERM --kill-after=30s 20m docker save "${packer_id}" "${inspector_id}" \
  | timeout --signal=TERM --kill-after=30s 20m gzip -n >"${output}/images.tar.gz"
jq -n --arg arch "${arch}" --arg source "$(git rev-parse HEAD)" \
  --arg packer "${packer_id}" --arg inspector "${inspector_id}" \
  --arg archive "sha256:$(sha256sum "${output}/images.tar.gz" | cut -d' ' -f1)" \
  --rawfile hashes "${output}/executables.sha256" \
  '{schemaVersion:"nkd.dev/native-toolchain-measurement/v1", scope:"unqualified-diagnostic",
    sourceCommit:$source, platform:("linux/"+$arch), packerImageID:$packer,
    inspectorImageID:$inspector, imageArchiveDigest:$archive, executableMeasurements:$hashes,
    publicationApproved:false}' >"${output}/measurement.json"
printf 'Native measurements: %s/measurement.json\n' "${output}"
