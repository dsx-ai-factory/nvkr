#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Shared by committed-pin verification and candidate refresh verification.
set -euo pipefail
case "$NATIVE_ARCH:$(uname -s)/$(uname -m)" in
  amd64:Linux/x86_64|arm64:Linux/aarch64) ;;
  *) echo 'native Linux architecture mismatch' >&2; exit 1 ;;
esac
daemon=$(docker info --format '{{.OSType}}/{{.Architecture}}')
case "$NATIVE_ARCH:$daemon" in
  amd64:linux/x86_64|amd64:linux/amd64) ;;
  arm64:linux/aarch64|arm64:linux/arm64) ;;
  *) echo 'native Docker architecture mismatch' >&2; exit 1 ;;
esac
if [[ "${1:-}" == --check-host ]]; then exit 0; fi
for image in "$PACKER_IMAGE" "$INSPECTOR_IMAGE"; do
  platform=$(docker image inspect \
    --format '{{.Os}}/{{.Architecture}}' "$image")
  test "$platform" = "linux/$NATIVE_ARCH"
  docker image inspect --format '{{json .RepoDigests}}' "$image" |
    jq -e --arg image "$image" 'index($image) != null'
done
run=(timeout --signal=TERM --kill-after=10s 60s docker run --rm
  --pull=never --platform "linux/$NATIVE_ARCH" --network none
  --read-only --cap-drop ALL --security-opt no-new-privileges
  --user 65534:65534 --env HOME=/nvkr/work/home
  --env TMPDIR=/nvkr/work
  --env CHECKPOINT_DISABLE=1
  --tmpfs '/nvkr/work:rw,nosuid,nodev,size=16777216,mode=1777')
"${run[@]}" --entrypoint /bin/sh "$PACKER_IMAGE" -ec '
  mkdir -p /nvkr/work/home
  test -d /nvkr/toolchain/packer/plugins
  export PACKER_PLUGIN_PATH=/nvkr/toolchain/packer/plugins
  /usr/local/bin/packer version
  /usr/local/bin/packer plugins installed
'
"${run[@]}" --entrypoint /bin/sh "$INSPECTOR_IMAGE" -ec '
  virt-inspector --version
  qemu-img --version
  guestfish --version
  syft version
  dpkg-deb --version
'
helper_status=0
"${run[@]}" --entrypoint /usr/local/bin/nvkr-disk-inspect \
  "$INSPECTOR_IMAGE" > "$RUNNER_TEMP/nvkr-helper.stdout" \
  2> "$RUNNER_TEMP/nvkr-helper.stderr" || helper_status=$?
test "$helper_status" = 2
test ! -s "$RUNNER_TEMP/nvkr-helper.stdout"
grep -F '[NVKR-E-USAGE]' "$RUNNER_TEMP/nvkr-helper.stderr"
printf 'Verified pull and native entrypoints: %s\n%s\n%s\n' \
  "linux/$NATIVE_ARCH" "$PACKER_IMAGE" "$INSPECTOR_IMAGE" \
  >> "$GITHUB_STEP_SUMMARY"

if [[ -n "${EXPECTED_EXECUTABLES:-}" ]]; then
  "${run[@]}" --entrypoint sha256sum "$INSPECTOR_IMAGE" \
    /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish \
    /usr/local/bin/syft /usr/bin/dpkg-deb /usr/local/bin/nvkr-disk-inspect \
    > "$RUNNER_TEMP/inspector-actual.sha256"
  jq -er '
    {inspectorDigest:"/usr/bin/virt-inspector", codecDigest:"/usr/bin/qemu-img",
     guestfishDigest:"/usr/bin/guestfish", scannerDigest:"/usr/local/bin/syft",
     packageReaderDigest:"/usr/bin/dpkg-deb", helperDigest:"/usr/local/bin/nvkr-disk-inspect"}
    as $paths | to_entries[] | .key as $key |
    ($paths[$key] // error("unknown executable")) as $path |
    (.value | ltrimstr("sha256:")) + "  " + $path
  ' "$EXPECTED_EXECUTABLES" | sort > "$RUNNER_TEMP/inspector-expected.sorted"
  sort "$RUNNER_TEMP/inspector-actual.sha256" > "$RUNNER_TEMP/inspector-actual.sorted"
  diff -u "$RUNNER_TEMP/inspector-expected.sorted" "$RUNNER_TEMP/inspector-actual.sorted"
fi
