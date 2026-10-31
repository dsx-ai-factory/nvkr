#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Sample the whole host and Docker alongside process timing. This records one
# experimental allocation; it does not derive minimum resource requirements.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo 'usage: ci-measure-workload.sh COMMAND [ARG...]' >&2
  exit 2
fi
[[ $(uname -s) == Linux && -x /usr/bin/time ]] || {
  echo 'measurement requires Linux and /usr/bin/time' >&2
  exit 1
}

snapshot() {
  printf '\n--- resource sample %s ---\n' "$(date -u +%FT%TZ)"
  free -m
  df -Pm .
  if [[ -r /sys/fs/cgroup/memory.peak ]]; then
    printf 'host cgroup memory.peak bytes: '
    cat /sys/fs/cgroup/memory.peak
  fi
  if [[ -r /sys/fs/cgroup/memory.events ]]; then
    printf 'host cgroup memory.events:\n'
    cat /sys/fs/cgroup/memory.events
  fi
  timeout --foreground --kill-after=2s 10s docker stats --no-stream \
    --format 'container={{.Name}} cpu={{.CPUPerc}} memory={{.MemUsage}} block_io={{.BlockIO}}' || \
    echo 'Docker usage sample unavailable'
}

kernel_warnings() {
  # Kernel warnings can explain an OOM; never print process arguments or env.
  timeout --kill-after=2s 5s sudo -n dmesg --level=err,warn | tail -n 12 || \
    echo 'Kernel warning sample unavailable'
}

sampler=''
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$sampler" ]]; then
    # The exact-child fallback covers exit before setsid creates the group.
    kill -- "-$sampler" "$sampler" 2>/dev/null || true
    if join_sampler 8s; then
      wait "$sampler" 2>/dev/null || true
    else
      kill -KILL -- "-$sampler" "$sampler" 2>/dev/null || true
      echo 'Resource sampler did not stop within its join budget' >&2
      if join_sampler 3s; then
        wait "$sampler" 2>/dev/null || true
      else
        echo 'Resource sampler join remains unconfirmed' >&2
      fi
      [[ "$status" -ne 0 ]] || status=1
    fi
  fi
  snapshot || true
  kernel_warnings
  printf '\nMeasured invocation exit status: %s\n' "$status"
  echo 'Process maximum RSS excludes Docker. Host/container samples are observations, not proven minima.'
  exit "$status"
}

join_sampler() {
  # The inner shell receives the owned PID as its positional argument.
  # shellcheck disable=SC2016
  timeout --foreground --kill-after=2s "$1" bash -c '
    while kill -0 "$1" 2>/dev/null || kill -0 -- "-$1" 2>/dev/null; do
      sleep 0.1
    done
  ' _ "$sampler"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

snapshot
kernel_warnings
export -f snapshot
setsid bash -c 'while sleep 15; do snapshot || true; done' &
sampler=$!
/usr/bin/time -f 'Workload timing: elapsed=%E user=%U system=%S max-rss-KiB=%M exit=%x' "$@"
