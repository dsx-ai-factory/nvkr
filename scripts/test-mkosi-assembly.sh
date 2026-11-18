#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Explicit VM diagnostic of production assembly; no consumer or publication run.
set -euo pipefail
if [[ $# != 1 || $1 != /* || $(uname -s)/$(uname -m) != Linux/x86_64 ]]; then
  echo 'usage: test-mkosi-assembly.sh /absolute/evidence-directory (native Linux amd64)' >&2
  exit 2
fi
repo=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
evidence=$1
mkdir -m 0700 "$evidence"
binary="$evidence/pipeline.test"
fixture=''
mounted=false
cleanup() {
  if [[ $mounted == true ]]; then
    sudo -n umount -- "$fixture/root"
  fi
  # The temporary backing file remains with the evidence until runner teardown.
  if [[ -n $fixture ]]; then
    sudo -n rmdir -- "$fixture/root" "$fixture"
  fi
}
trap cleanup EXIT

export GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=2 CGO_ENABLED=0
go -C "$repo" test -c -o "$binary" ./pkg/nvkr/pipeline
# These are recorded bootstrap utilities, not snapshot-pinned runtime inputs.
sudo -n timeout --kill-after=10s 180s apt-get update -q
sudo -n timeout --kill-after=10s 300s apt-get install -y --no-install-recommends apt-utils acl zstd
{
  git -C "$repo" rev-parse HEAD
  go version
  uname -a
  sha256sum "$binary"
  dpkg-query -W python3 apt apt-utils dpkg systemd coreutils tar util-linux e2fsprogs xz-utils zstd acl
} > "$evidence/inputs.txt"

# A fresh ext4 filesystem bounds disk use and supplies explicit executable
# staging. No inherited mount flags or host-wide namespace policy are changed.
fixture=$(sudo -n mktemp -d /tmp/nvkr-mkosi-proof.XXXXXXXX)
sudo -n chmod 0755 "$fixture"
sudo -n mkdir -m 0755 "$fixture/root"
truncate -s 20G "$evidence/scratch.ext4"
mkfs.ext4 -q -F "$evidence/scratch.ext4"
sudo -n mount -o loop,rw,exec,nosuid,nodev "$evidence/scratch.ext4" "$fixture/root"
mounted=true
# Keep mkosi's admitted scratch empty of filesystem maintenance directories.
sudo -n mkdir -m 0755 "$fixture/root/assembly"
findmnt -T "$fixture/root/assembly" -o TARGET,FSTYPE,OPTIONS >> "$evidence/inputs.txt"

# Acquisition has bounded HTTPS access. Assembly itself always enters the
# launcher's separate user, network, mount and PID namespaces without host IDs.
sudo -n systemd-run --quiet --wait --pipe --collect \
  --property=ProtectSystem=strict --property="ReadWritePaths=$fixture/root" \
  --property=NoNewPrivileges=yes --property=CPUQuota=200% \
  --property=MemoryMax=5G --property=TasksMax=512 \
  --property=RuntimeMaxSec=2700 --property=KillMode=control-group \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C GOMAXPROCS=2 \
    NVKR_TEST_NATIVE_MKOSI=1 "NVKR_TEST_MKOSI_SCRATCH=$fixture/root/assembly" \
    "NVKR_TEST_MKOSI_CATALOG=$repo/toolchains/nvkr-vm-runtime" \
    "$binary" -test.run='^TestNativeMkosiAssembly$' -test.v -test.timeout=42m \
  2>&1 | tee "$evidence/assembly.log"
