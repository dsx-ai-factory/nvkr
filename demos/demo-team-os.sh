#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Guided static OS-image material graph; intentionally does not start Packer.
set -euo pipefail
DEMO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DEMO_DIR/.." && pwd)
NVKR=${NVKR_BIN:-$ROOT/bin/nvkr}
NON_INTERACTIVE=0
ARCH=arm64
OUT=
usage() {
  cat <<'EOF'
Usage: bash demos/demo-team-os.sh [--arch arm64|amd64] [--out NEW_DIR] [--non-interactive]

Export the maintained native KVM OS-image Plan and native producer graph, then
inspect a dry-run. No Docker, KVM, Packer execution, registry access or OS image
build is required for this tour.
Prerequisites: Python 3 and a built nvkr from this checkout (make build, exact Go in .go-version),
or NVKR_BIN pointing to that binary. Output is retained in a new temporary
directory; --out requires a nonexistent directory whose parent already exists.
Full commands and dry-run output are saved with the exports. Set NO_COLOR to disable color.
EOF
}
die() { printf 'demo: %s\n' "$*" >&2; exit 2; }
pause() { if [ "$NON_INTERACTIVE" -eq 0 ]; then read -r -p 'Press Enter to continue... ' _; fi; }
heading() {
  if [ -t 1 ] && [ -z "${NO_COLOR+x}" ] && [ "${TERM:-dumb}" != dumb ]; then
    printf '\n\033[1;32m%s\033[0m\n' "$*"
  else
    printf '\n%s\n' "$*"
  fi
}
show_command() {
  local line='  $ nvkr' argument quoted
  for argument in "$@"; do
    printf -v quoted '%q' "$argument"
    if [ "$((${#line} + ${#quoted} + 1))" -gt 96 ]; then
      printf '%s \\\n' "$line"
      line="      $quoted"
    else
      line="$line $quoted"
    fi
  done
  printf '%s\n' "$line"
}
export_command() {
  local output=$1
  shift
  { printf '%q ' "$@"; printf '> %q 2> %q\n' "$output" "$output.stderr"; } >>"$OUT/commands.txt"
  if "$@" >"$output" 2>"$output.stderr"; then return; else
    local status=$?
    cat "$output.stderr" >&2
    die "command exited $status; see $output and $output.stderr"
  fi
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --arch) [ "$#" -ge 2 ] || die 'missing --arch value'; ARCH=$2; shift 2 ;;
    --out) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --out value'; OUT=$2; shift 2 ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
case "$ARCH" in arm64|amd64) ;; *) die '--arch must be arm64 or amd64' ;; esac
[ -x "$NVKR" ] || die "build nvkr first (make build), or set NVKR_BIN to an executable: $NVKR"
command -v python3 >/dev/null || die 'Python 3 is required for the readable summary'
NVKR=$(cd "$(dirname "$NVKR")" && printf '%s/%s\n' "$PWD" "$(basename "$NVKR")")
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-team-os.XXXXXX"); fi
cd "$ROOT"
printf 'cd %q\n' "$ROOT" >"$OUT/commands.txt"
DEFINITION="inputs/definitions/diagnostics/os-$ARCH.yaml"
ENVIRONMENT="inputs/environments/local-linux-$ARCH-kvm.yaml"

heading 'NVKR / Understand an OS image'
printf 'Follow components into a disk image, then preview the work.\n'
printf 'Exports and full commands: %s\n' "$OUT"

heading '1 / 3  Choose the disk output'
show_command definition inspect --definition "$DEFINITION"
export_command "$OUT/os-definition-$ARCH.json" "$NVKR" definition inspect --definition "$DEFINITION"
python3 - "$OUT/os-definition-$ARCH.json" <<'PY'
import json
import sys
definition = json.load(open(sys.argv[1]))
for output in definition['outputs']:
    print(f"  {output['name']} | {output['format']} | {output['platform']}")
    print(f"  {len(output['components'])} selected components, including the disk producer and all its materials.")
PY
printf 'Read the selection: %s\n' "$DEFINITION"
pause

heading '2 / 3  Match materials to the builder'
show_command run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json
export_command "$OUT/os-plan-$ARCH.json" "$NVKR" run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json
export_command "$OUT/os-plan-$ARCH.txt" "$NVKR" run plan --plan "$OUT/os-plan-$ARCH.json"
export_command "$OUT/os-producers-$ARCH.mermaid" "$NVKR" run graph --plan "$OUT/os-plan-$ARCH.json" --view producers --format mermaid
export_command "$OUT/os-graph-$ARCH.json" "$NVKR" run graph --plan "$OUT/os-plan-$ARCH.json" --view complete --format json
python3 - "$OUT/os-plan-$ARCH.json" <<'PY'
import json
import sys
plan = json.load(open(sys.argv[1]))
for build in plan['builds']:
    for node in build['executionGraph']['nodes']:
        if node.get('outputType') != 'disk-image' or 'packer' not in node:
            continue
        print(f"  {node['component']} uses Packer to produce {node['disk']['format']}.")
        print(f"  {'Material slot':<20} Supplied by")
        for material in node['materials']:
            print(f"  {material['binding']:<20} {material['component']}")
        resource = node['requirements']['resources']
        print(f"  Host requirements: {resource['cpuMillis'] / 1000:g} CPUs, {resource['memoryMiB'] / 1024:g} GiB memory, {resource['ephemeralStorageMiB'] / 1024:g} GiB workspace, KVM.")
PY
printf 'Slot names bind materials to producers; component list order is irrelevant.\n'
printf 'Load os-plan-%s.json in demos/demo-team-explorer.html.\n' "$ARCH"
printf 'Expand the material producers to see agents, networking, charts, and installer inputs.\n'
printf 'The Plan declares host requirements; real execution checks the actual host.\n'
pause

heading '3 / 3  Preview the Run'
show_command run local --definition "$DEFINITION" --environment "$ENVIRONMENT" --attempt team-os-preview --dry-run
export_command "$OUT/os-dry-run-$ARCH.txt" "$NVKR" run local --definition "$DEFINITION" --environment "$ENVIRONMENT" --attempt team-os-preview --dry-run
awk '/^(lane|status|observed outputs):/ { print "  " $0 }' "$OUT/os-dry-run-$ARCH.txt"
printf 'The preview creates no Run workspace or disk. Full operation output is saved.\n'
printf 'For a real KVM build, start with inputs/definitions/diagnostics/README.md.\n'
printf '\nNext: browse the hosted release to see an image that was actually published.\n'
