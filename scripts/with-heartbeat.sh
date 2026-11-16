#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Keep a quiet foreground command visible; stop and join the owned timer.
set -euo pipefail
label=$1
shift
heartbeat() {
  local timer=
  trap 'if [[ -n "$timer" ]]; then kill "$timer" 2>/dev/null || true; wait "$timer" 2>/dev/null || true; fi; exit 0' TERM INT
  while true; do
    sleep 10 & timer=$!
    wait "$timer"
    timer=
    printf '[working] %s…\n' "$label" >&2
  done
}
printf '[start] %s\n' "$label" >&2
heartbeat & heartbeat_pid=$!
cleanup() {
  kill "$heartbeat_pid" 2>/dev/null || true
  wait "$heartbeat_pid" 2>/dev/null || true
}
trap cleanup EXIT
"$@"
