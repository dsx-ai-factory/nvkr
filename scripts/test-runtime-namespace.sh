#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Explicit native-kernel diagnostic. No container engine or production Plan runs.
set -euo pipefail

if [[ $# != 1 || $1 != /* || $(uname -s)/$(uname -m) != Linux/x86_64 ]]; then
  echo 'usage: test-runtime-namespace.sh /absolute/evidence-directory (native Linux amd64)' >&2
  exit 2
fi
repo=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
evidence=$1
mkdir -m 0700 "$evidence"
binary="$evidence/runtimeexec.test"
fixture=''
mounted=false
cleanup() {
  if [[ $mounted == true ]]; then
    sudo -n umount -- "$fixture"
  fi
  if [[ -n $fixture ]]; then
    sudo -n rmdir -- "$fixture"
  fi
}
trap cleanup EXIT

export GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=2 CGO_ENABLED=0
go -C "$repo" test -c -o "$binary" ./pkg/nvkr/internal/runtimeexec
{
  git -C "$repo" rev-parse HEAD
  go version
  uname -a
  sha256sum "$binary"
  findmnt -R /proc -o TARGET,FSTYPE,OPTIONS
} > "$evidence/inputs.txt"

# This is a new, owned mount. It does not clear NOEXEC on /tmp or on any
# inherited filesystem. Its public traversal permits the mapped assembly ID
# to reach that test's own 0700 child directory; it grants no host-root access.
fixture=$(sudo -n mktemp -d /tmp/nvkr-runtime-namespace.XXXXXXXX)
sudo -n mount -t tmpfs -o rw,exec,nosuid,nodev,size=128m,mode=0755 tmpfs "$fixture"
mounted=true
findmnt -T "$fixture" -o TARGET,FSTYPE,OPTIONS >> "$evidence/inputs.txt"

# The service owns and joins the whole test process tree. Privilege is supplied
# only for trusted setup; the launcher gives assembly its separate user mapping
# and strips every capability from consumer payloads. No extra child capability
# is added here. The parent has no external network and a read-only host root.
sudo -n systemd-run --quiet --wait --pipe --collect \
  --property=PrivateNetwork=yes --property=ProtectSystem=strict \
  --property="ReadWritePaths=$fixture" --property=NoNewPrivileges=yes \
  --property=CPUQuota=100% --property=MemoryMax=256M --property=TasksMax=96 \
  --property=RuntimeMaxSec=60 --property=KillMode=control-group \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C GOMAXPROCS=2 \
    NVKR_TEST_NATIVE_RUNTIME=1 "NVKR_TEST_RUNTIME_SCRATCH=$fixture" \
    "$binary" '-test.run=TestNative(Runtime|Assembly|OutboundRuntime)Namespace' -test.v -test.timeout=45s \
  2>&1 | tee "$evidence/namespaces.log"
