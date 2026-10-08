#!/usr/bin/env bash
set -euo pipefail
cd /tmp/nkd-arc-native-amd64
image=$(docker image inspect --format '{{.Id}}' localhost/nkd-disk-inspect:local-amd64)
timeout --signal=TERM --kill-after=15s 300 docker run --rm --pull=never --platform linux/amd64 \
  --network none --read-only --cap-drop ALL --security-opt no-new-privileges --user 65534:65534 \
  --cpus 2 --memory 4g --memory-swap 4g --pids-limit 512 \
  --tmpfs /tmp:rw,nosuid,nodev,size=2147483648,mode=1777 \
  --env HOME=/tmp/probe-work --env TMPDIR=/tmp/probe-work --env LIBGUESTFS_CACHEDIR=/tmp/probe-work --env LIBGUESTFS_TMPDIR=/tmp/probe-work \
  --env LIBGUESTFS_BACKEND=direct --env LIBGUESTFS_BACKEND_SETTINGS=force_tcg \
  --entrypoint /bin/sh "$image" -ec '
    mkdir -m 0700 /tmp/probe-work
    qemu-img create -f qcow2 /tmp/probe.qcow2 64M
    result=$(guestfish --rw -a /tmp/probe.qcow2 run : part-disk /dev/sda mbr : mkfs ext4 /dev/sda1 : mount /dev/sda1 / : write /probe native-amd64 : cat /probe)
    test "$result" = native-amd64
    printf "Native AMD64 libguestfs TCG appliance created, mounted, wrote and read ext4 successfully\n"
  ' > measurements/appliance-probe.txt 2>&1
cat measurements/appliance-probe.txt
