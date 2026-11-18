#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Bounded native component integration. No provider or publication authority.
set -euo pipefail
if [[ $# != 1 || $1 != /* || $(uname -s)/$(uname -m) != Linux/x86_64 ]]; then
  echo 'usage: test-runtime-consumer.sh /absolute/evidence-directory (Linux amd64)' >&2
  exit 2
fi
repo=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
evidence=$1
mkdir -m 0700 "$evidence"
binary="$evidence/factory.test"
fixture=''
mounted=false
cleanup() {
  if [[ $mounted == true ]]; then
    sudo -n umount -- "$fixture/root"
  fi
  if [[ -n $fixture ]]; then
    sudo -n rmdir -- "$fixture/root" "$fixture"
  fi
}
trap cleanup EXIT

export GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=2 CGO_ENABLED=0
go -C "$repo" test -c -o "$binary" ./pkg/nvkr/factory
sudo -n timeout --kill-after=10s 180s apt-get update -q
sudo -n timeout --kill-after=10s 300s apt-get install -y --no-install-recommends apt-utils acl zstd
{
  git -C "$repo" rev-parse HEAD
  go version
  uname -a
  sha256sum "$binary"
  stat -Lc 'resolver owner=%u mode=%a size=%s type=%F' /etc/resolv.conf
  dpkg-query -W python3 apt apt-utils dpkg systemd coreutils tar util-linux e2fsprogs xz-utils zstd acl
} > "$evidence/inputs.txt"

fixture=$(sudo -n mktemp -d /tmp/nvkr-consumer-proof.XXXXXXXX)
sudo -n chmod 0755 "$fixture"
sudo -n mkdir -m 0755 "$fixture/root"
truncate -s 20G "$evidence/scratch.ext4"
mkfs.ext4 -q -F "$evidence/scratch.ext4"
sudo -n mount -o loop,rw,exec,nosuid,nodev "$evidence/scratch.ext4" "$fixture/root"
mounted=true
sudo -n mkdir -m 0755 "$fixture/root/workspace" "$fixture/root/native"
sudo -n chown "$(id -u):$(id -g)" "$fixture/root/workspace"
findmnt -T "$fixture/root" -o TARGET,FSTYPE,OPTIONS >> "$evidence/inputs.txt"

# The fetch operation scrubs its own Git environment and authenticates exact
# commit/tree source closures. No credential is copied into this workspace.
env -i PATH=/usr/bin:/bin LANG=C GOMAXPROCS=2 \
  NVKR_TEST_RUNTIME_PHASE=prepare "NVKR_TEST_RUNTIME_WORKSPACE=$fixture/root/workspace" \
  "NVKR_TEST_RUNTIME_CATALOG=$repo/toolchains/nvkr-vm-runtime" \
  "NVKR_TEST_RUNTIME_COMMIT=$(git -C "$repo" rev-parse HEAD)" \
  "NVKR_TEST_RUNTIME_TREE=$(git -C "$repo" rev-parse 'HEAD^{tree}')" \
  "$binary" -test.run='^TestNativeRuntimePrepare$' -test.v -test.timeout=16m \
  2>&1 | tee "$evidence/prepare.log"
sudo -n chown -R 0:0 "$fixture/root/workspace"

# Bind the bounded fixture onto /opt only in this service's private mount
# namespace. The production scratch owner still admits the fixed /opt parent.
# The service gets no GitHub, registry, source, signing or inherited environment.
sudo -n systemd-run --quiet --wait --pipe --collect \
  --property=ProtectSystem=strict --property="ReadWritePaths=$fixture/root" \
  --property="BindPaths=$fixture/root/native:/opt" \
  --property=NoNewPrivileges=yes --property=CPUQuota=200% \
  --property=MemoryMax=5G --property=TasksMax=512 \
  --property=RuntimeMaxSec=2700 --property=KillMode=control-group \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C GOMAXPROCS=2 "TMPDIR=$fixture/root/native" \
    NVKR_TEST_RUNTIME_PHASE=consume "NVKR_TEST_RUNTIME_WORKSPACE=$fixture/root/workspace" \
    "$binary" -test.run='^TestNativeRuntimeConsumer$' -test.v -test.timeout=42m \
  2>&1 | tee "$evidence/consumer.log"

# Only public descriptions are retained; the disk and temporary bootstrap
# material remain on the disposable filesystem until runner teardown.
sudo -n install -m 0644 "$fixture/root/workspace/disk-result.json" "$evidence/disk-result.json"
sudo -n install -m 0644 "$fixture/root/workspace/disk-observation.json" "$evidence/disk-observation.json"
python3 - "$fixture/root/workspace" "$evidence" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

workspace, evidence = map(Path, sys.argv[1:])
plan = json.loads((workspace / "plan.json").read_bytes())
result = json.loads((evidence / "disk-result.json").read_bytes())
tool, = plan["internalTools"]
artifacts = {item["id"]: item for item in plan["graph"]["artifacts"]}
for field, name, digest in (
    ("sbomArtifactId", "runtime-sbom.json", result["runtimeUse"]["sbom"]),
    ("observationArtifactId", "runtime-observation.json", result["runtimeUse"]["observation"]),
):
    path = (workspace / artifacts[tool[field]]["path"]).resolve()
    path.relative_to(workspace.resolve())
    raw = path.read_bytes()
    assert "sha256:" + hashlib.sha256(raw).hexdigest() == digest
    (evidence / name).write_bytes(raw)
PY
