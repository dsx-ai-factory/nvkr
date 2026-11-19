#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Read-only navigation of one user-selected immutable release publication.
set -euo pipefail
DEMO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DEMO_DIR/.." && pwd)
NVKR=${NVKR_BIN:-$ROOT/bin/nvkr}
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

Prerequisites: a compatible nvkr (or NVKR_BIN), jq,
and registry access/credentials unless using --layout. New --out must not exist;
its parent must exist. Registry browsing downloads bounded metadata and evidence
documents, never artifact payload layers. --output and --mermaid each repeat the
metadata read; use --layout with a saved metadata carrier for repeated offline
views. --layout cannot use --anonymous.
Current NVKR requires publication-root v4. Historical v3 roots require their
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
  printf '$ nvkr release browse --reference "$RELEASE_REF"'
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
[ -x "$NVKR" ] || die "build nvkr from this checkout (make build), or set NVKR_BIN: $NVKR"
command -v jq >/dev/null || die 'jq is required for the readable summary'
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-team-release.XXXXXX"); fi
heading 'NVKR / Explore a release'
printf 'Follow one immutable release from outputs to supporting evidence.\n'
printf 'Exports: %s\n' "$OUT"
ARGS=(release browse --reference "$REFERENCE" --timeout "$TIMEOUT")
if [ -n "$LAYOUT" ]; then ARGS+=(--layout "$LAYOUT"); fi
if [ "$ANONYMOUS" -eq 1 ]; then ARGS+=(--anonymous); fi

heading '1 / 3  What is in this release?'
printf 'Reading metadata and evidence; payload layers are not read.\n'
show_command --format json
"$NVKR" "${ARGS[@]}" --format json > "$OUT/release-view.json"
jq -L "$DEMO_DIR" -r 'include "demo-json";
  "  Outputs: \(.totalOutputs) | Evidence records: \((.evidence // []) | length)",
  (.outputs | group_by(.kind)[] | (length | tostring) as $count |
    "  \(" " * ([3 - ($count | length), 0] | max))\($count)  \(.[0].kind | safe)"),
  "  Root: \(.rootDigest | safe)",
  (.metadata.invocationId // empty | select(length > 0) | "  Run:  \(. | safe)")' "$OUT/release-view.json"
printf 'The root digest identifies this exact collection of artifacts and evidence.\n'
pause

heading '2 / 3  Follow one output'
if [ -z "$OUTPUT" ]; then
  OUTPUT=$(jq -er '[.outputs[].name] | if index("nvke-node-image-arm64") then "nvke-node-image-arm64" else .[0] end' "$OUT/release-view.json")
fi
show_command --output "$OUTPUT"
"$NVKR" "${ARGS[@]}" --output "$OUTPUT" > "$OUT/selected-output.txt"
jq -L "$DEMO_DIR" -r --arg output "$OUTPUT" '
  include "demo-json";
  def pad($n): . + (" " * ([$n - length, 0] | max));
  def gib: (. / 1073741824 * 100 | round) as $n |
    "\($n / 100 | floor)." + ("0" + ($n % 100 | tostring))[-2:];
  . as $view | ([.outputs[] | select(.name == $output)][0] // error("output not found")) |
  . as $selected |
  [$view.evidence[]? | select((.binding.outputs // []) | index($output))] as $evidence |
  ([.os, .arch] | map(select(. != null and . != "")) | join("/")) as $platform |
  "  \(.name | safe) | \(.kind | safe)" + (if $platform == "" then "" else " | \($platform | safe)" end),
  "  Contributing components: \(.components | length) | Evidence records: \($evidence | length)",
  "  Manifest: \(.digest | safe)",
  (.disk // empty | "  Payload:  \(.payloadDigest | safe)",
    "  Disk: \(.format | safe) | \(.size | gib) GiB",
    "  Disk evidence: \(if .evidenceMode == "build-provenance-v1" then "build provenance (no full disk filesystem inventory)" else (.evidenceMode // "inspected" | safe) end)"),
  "    \("Component" | pad(34)) Evidence records",
  ($selected.components[] | . as $component |
    "    \(. | safe | pad(34)) \([$evidence[] | select(.binding.component == $component)] | length)")
' "$OUT/release-view.json"
printf 'Components identify contributors; evidence records bind their recorded subjects to this output.\n'
printf '\nOpen demos/demo-team-explorer.html and load:\n  %s/release-view.json\n' "$OUT"
if [ "$MERMAID" -eq 1 ]; then
  "$NVKR" "${ARGS[@]}" --format mermaid > "$OUT/release-graph.mmd"
  printf 'Native Mermaid export: %s/release-graph.mmd\n' "$OUT"
fi
printf 'Native text with full details: %s/selected-output.txt\n' "$OUT"
pause

heading '3 / 3  What has been checked?'
printf 'Browse checked metadata integrity and evidence membership.\n'
printf 'Release verify checks payloads, signatures and your accepted workflow identity.\n'
printf 'Signed attestations bind claims to exact subjects; they do not claim ownership.\n'
printf 'Verification walkthrough: demos/team-guide.md\n'
