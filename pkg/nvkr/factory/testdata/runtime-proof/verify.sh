#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
disk=$1
seed=$2
logs=$3
[[ $(id -u) == 65534 && $(id -g) == 65534 ]]
[[ $(awk '/^CapEff:/ {print $2}' /proc/self/status) == 0000000000000000 ]]
[[ -z ${GITHUB_TOKEN:-}${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-}${NVKR_CREDENTIAL:-} ]]

# The guest itself must have written its boot sector at LBA 8. Merely copying
# or converting the seed cannot satisfy this check: that sector starts at zero.
qemu-img convert -f qcow2 -O raw "$disk" "$logs/disk.raw"
dd if="$logs/disk.raw" of="$logs/guest-sector.bin" bs=512 skip=8 count=1 status=none
cmp -n 512 "$seed" "$logs/guest-sector.bin"

# Exercise DNS, outbound HTTPS and the authenticated slirp4netns executable
# inside the production consumer boundary. This is an exact already-pinned
# public metadata file, not a new guest package or a claim about apt coverage.
curl --fail --silent --show-error --location --connect-timeout 10 --max-time 60 \
  'https://snapshot.ubuntu.com/ubuntu/20261109T000000Z/dists/resolute-security/InRelease' \
  -o "$logs/InRelease"
printf '%s  %s\n' 7f66b756f1b4eb0dee5076fe9cd08c7502510da510a86fa25f888449a131f7fe "$logs/InRelease" | sha256sum --check
{
  id
  qemu-system-x86_64 --version
  qemu-img info --output=json "$disk"
  sha256sum "$logs/guest-sector.bin" "$logs/disk.raw" "$logs/InRelease"
  echo 'BIOS guest write, ACPI shutdown, consumer UID/capabilities and outbound HTTPS checked'
} > "$logs/proof.txt"
cat "$logs/proof.txt"
