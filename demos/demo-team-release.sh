#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Read-only navigation of one user-selected immutable release publication.
set -euo pipefail
DEMO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DEMO_DIR/.." && pwd)
NKD=${NKDCTL_BIN:-$ROOT/bin/nkdctl}
REFERENCE=
LAYOUT=
OUTPUT=
TIMEOUT=20m
OUT=
NON_INTERACTIVE=0
ANONYMOUS=0
MERMAID=0
usage() {
  cat <<'EOF'
Usage: bash demos/demo-team-release.sh --reference registry/repository@sha256:... [options]

Inspect one exact release root and export native browse JSON for the offline
graph companion. Browsing validates metadata and evidence membership, without
reading artifact payload layers or verifying signatures/consumer policy.
No publication, artifact execution, signing or installation occurs.

Options:
  --layout DIR        inspect a local full or metadata-only OCI layout (offline)
  --anonymous         public registry access without ambient credentials
  --output NAME       focus an exact output (default: ARM64 disk, then first output)
  --mermaid           also export the native Mermaid graph
  --timeout DURATION  deadline for each complete inspection (default: 20m)
  --out NEW_DIR       retain exports here (default: new temporary directory)
  --non-interactive   skip guided pauses
  -h, --help          show this help

Prerequisites: a compatible nkdctl (or NKDCTL_BIN), Python 3,
and registry access/credentials unless using --layout. New --out must not exist;
its parent must exist. Registry browsing downloads bounded metadata and evidence
documents, never artifact payload layers. --output and --mermaid each repeat the
metadata read; use --layout with a saved metadata carrier for repeated offline
views. --layout cannot use --anonymous.
Current NVKE Recipes requires publication-root v4. Historical v3 roots require their
matching historical CLI; changing a metadata file cannot upgrade a release.
For signature and policy verification, see demos/team-guide.md.
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
  # Display short variable names; ARGS below always uses the complete values.
  # shellcheck disable=SC2016
  printf '$ nkdctl release browse --reference "$RELEASE_REF"'
  # shellcheck disable=SC2016
  if [ -n "$LAYOUT" ]; then printf ' --layout "$LAYOUT"'; fi
  if [ "$ANONYMOUS" -eq 1 ]; then printf ' --anonymous'; fi
  printf ' %q' "$@"
  printf '\n'
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --reference) [ "$#" -ge 2 ] || die 'missing --reference value'; REFERENCE=$2; shift 2 ;;
    --layout) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --layout value'; LAYOUT=$2; shift 2 ;;
    --output) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --output value'; OUTPUT=$2; shift 2 ;;
    --timeout) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --timeout value'; TIMEOUT=$2; shift 2 ;;
    --out) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --out value'; OUT=$2; shift 2 ;;
    --anonymous) ANONYMOUS=1; shift ;;
    --mermaid) MERMAID=1; shift ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$REFERENCE" =~ ^[^[:space:]@]+/[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || die 'supply the exact registry/repository@sha256:<64 lowercase hex> release root from the Run'
if [ -n "$LAYOUT" ] && [ "$ANONYMOUS" -eq 1 ]; then die '--layout cannot be combined with --anonymous'; fi
[ -x "$NKD" ] || die "build nkdctl from this checkout (make build), or set NKDCTL_BIN: $NKD"
command -v python3 >/dev/null || die 'Python 3 is required for the readable summary'
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nkd-team-release.XXXXXX"); fi
heading 'NVKE Recipes / Explore a release'
printf 'Follow one immutable release from outputs to supporting evidence.\n'
printf 'Exports: %s\n' "$OUT"
ARGS=(release browse --reference "$REFERENCE" --timeout "$TIMEOUT")
if [ -n "$LAYOUT" ]; then ARGS+=(--layout "$LAYOUT"); fi
if [ "$ANONYMOUS" -eq 1 ]; then ARGS+=(--anonymous); fi

heading '1 / 3  What is in this release?'
printf 'Reading metadata and evidence; payload layers are not read.\n'
show_command --format json
"$NKD" "${ARGS[@]}" --format json > "$OUT/release-view.json"
python3 - "$OUT/release-view.json" <<'PY'
from collections import Counter
import json
import sys
from pathlib import Path
view = json.loads(Path(sys.argv[1]).read_text())
safe = lambda value: json.dumps(value, ensure_ascii=True)[1:-1]
print(f"  Outputs: {view['totalOutputs']} | Evidence records: {len(view.get('evidence', []))}")
for kind, count in sorted(Counter(o['kind'] for o in view['outputs']).items()):
    print(f"  {count:>3}  {safe(kind)}")
print(f"  Root: {safe(view['rootDigest'])}")
run = view.get('metadata', {}).get('invocationId')
if run:
    print(f"  Run:  {safe(run)}")
PY
printf 'The root digest identifies this exact collection of artifacts and evidence.\n'
pause

heading '2 / 3  Follow one output'
if [ -z "$OUTPUT" ]; then
  OUTPUT=$(python3 - "$OUT/release-view.json" <<'PY'
import json
import sys
view = json.load(open(sys.argv[1]))
names = [o['name'] for o in view['outputs']]
print('nke-node-image-arm64' if 'nke-node-image-arm64' in names else names[0])
PY
  )
fi
show_command --output "$OUTPUT"
"$NKD" "${ARGS[@]}" --output "$OUTPUT" > "$OUT/selected-output.txt"
python3 - "$OUT/release-view.json" "$OUTPUT" <<'PY'
import json
import sys
view = json.load(open(sys.argv[1]))
output = next(o for o in view['outputs'] if o['name'] == sys.argv[2])
safe = lambda value: json.dumps(value, ensure_ascii=True)[1:-1]
evidence = [e for e in view.get('evidence', [])
            if output['name'] in e.get('binding', {}).get('outputs', [])]
platform = '/'.join(output[k] for k in ('os', 'arch') if output.get(k))
print(f"  {safe(output['name'])} | {safe(output['kind'])}" + (f" | {safe(platform)}" if platform else ''))
print(f"  Contributing components: {len(output['components'])} | Evidence records: {len(evidence)}")
print(f"  Manifest: {safe(output['digest'])}")
disk = output.get('disk')
if disk:
    print(f"  Payload:  {safe(disk['payloadDigest'])}")
    print(f"  Disk: {safe(disk['format'])} | {disk['size'] / 1024**3:.2f} GiB")
    mode = disk.get('evidenceMode') or 'inspected'
    label = 'build provenance (no full disk filesystem inventory)' if mode == 'build-provenance-v1' else safe(mode)
    print(f"  Disk evidence: {label}")
print(f"    {'Component':<34} Evidence records")
for component in output['components']:
    count = sum(e.get('binding', {}).get('component') == component for e in evidence)
    print(f"    {safe(component):<34} {count}")
PY
printf 'Components identify contributors; evidence records bind their recorded subjects to this output.\n'
printf '\nOpen demos/demo-team-explorer.html and load:\n  %s/release-view.json\n' "$OUT"
if [ "$MERMAID" -eq 1 ]; then
  "$NKD" "${ARGS[@]}" --format mermaid > "$OUT/release-graph.mmd"
  printf 'Native Mermaid export: %s/release-graph.mmd\n' "$OUT"
fi
printf 'Native text with full details: %s/selected-output.txt\n' "$OUT"
pause

heading '3 / 3  What has been checked?'
printf 'Browse checked metadata integrity and evidence membership.\n'
printf 'Release verify checks payloads, signatures and your accepted workflow identity.\n'
printf 'Signed attestations bind claims to exact subjects; they do not claim ownership.\n'
printf 'Verification walkthrough: demos/team-guide.md\n'
