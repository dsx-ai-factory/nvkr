#!/usr/bin/env bash
# Read-only navigation of one user-selected immutable release publication.
set -euo pipefail
DOCS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DOCS/.." && pwd)
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
Usage: bash docs/demo-team-release.sh --reference registry/repository@sha256:... [options]

Inspect one exact release root and export native browse JSON for the offline
graph companion. Browsing validates metadata and evidence membership, without
reading artifact payload layers or verifying signatures/consumer policy.
No publication, artifact execution, signing or installation occurs.

Options:
  --layout DIR        inspect a local full or metadata-only OCI layout (offline)
  --anonymous         public registry access without ambient credentials
  --output NAME       also show native text focused on one exact output
  --mermaid           also export the native Mermaid graph
  --timeout DURATION  deadline for each complete inspection (default: 20m)
  --out NEW_DIR       retain exports here (default: new temporary directory)
  --non-interactive   skip guided pauses
  -h, --help          show this help

Prerequisites: nkdctl built from this PR #108 checkout (or NKDCTL_BIN), Python 3,
and registry access/credentials unless using --layout. New --out must not exist;
its parent must exist. Registry browsing downloads bounded metadata and evidence
documents, never artifact payload layers. --output and --mermaid each repeat the
metadata read; use --layout with a saved metadata carrier for repeated offline
views. --layout cannot use --anonymous.
For Linux signature/policy verification, see docs/demo-team-guide.md.
EOF
}
die() { printf 'demo: %s\n' "$*" >&2; exit 2; }
pause() { if [ "$NON_INTERACTIVE" -eq 0 ]; then read -r -p 'Press Enter to continue... ' _; fi; }
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
printf 'Exports: %s\n' "$OUT"
ARGS=(release browse --reference "$REFERENCE" --timeout "$TIMEOUT")
if [ -n "$LAYOUT" ]; then ARGS+=(--layout "$LAYOUT"); fi
if [ "$ANONYMOUS" -eq 1 ]; then ARGS+=(--anonymous); fi

printf '\n1 / 3  Read the exact release metadata (2 minutes plus metadata fetch time)\n'
printf '$'; printf ' %q' "$NKD" "${ARGS[@]}" --format json; printf '\n'
"$NKD" "${ARGS[@]}" --format json > "$OUT/release-view.json"
python3 - "$OUT/release-view.json" <<'PY'
import json
import sys
from pathlib import Path
view = json.loads(Path(sys.argv[1]).read_text())
print("Inspection:", json.dumps(view["inspection"]))
print("Release:", json.dumps(view["reference"]))
print("Outputs:", view["totalOutputs"])
for output in view["outputs"]:
    print(" ", json.dumps(output["name"]), "|", json.dumps(output["kind"]), "|", json.dumps(output["digest"]))
print("Evidence entries:", len(view.get("evidence", [])))
PY
printf 'Checkpoint: use the actual output names above; a tag or a successful job label is not the release identity.\n'
pause

printf '\n2 / 3  Navigate outputs and evidence (4 minutes)\n'
printf 'Open %s/demo-team-explorer.html and load %s/release-view.json.\n' "$DOCS" "$OUT"
printf 'Select an output. Follow its component and evidence relationships, then inspect the full digest/subject details.\n'
printf 'If OS disk outputs are present, inspect config-declared platforms and distinguish OCI manifest from declared QCOW2 payload identity.\n'
if [ -n "$OUTPUT" ]; then
  "$NKD" "${ARGS[@]}" --output "$OUTPUT" > "$OUT/selected-output.txt"
  cat "$OUT/selected-output.txt"
fi
if [ "$MERMAID" -eq 1 ]; then
  "$NKD" "${ARGS[@]}" --format mermaid > "$OUT/release-graph.mmd"
  printf 'Native Mermaid export: %s/release-graph.mmd\n' "$OUT"
fi
printf 'Checkpoint: selecting one output changes the view after all metadata and evidence memberships are checked.\n'
pause

printf '\n3 / 3  Name the trust decision (1 minute)\n'
printf 'Browse checked metadata integrity and membership. Payload bytes, signatures and policy remain unverified.\n'
printf 'The declared qualification is descriptive; an exported JSON file is not a verification receipt.\n'
printf 'Use the separate Linux verification exercise in docs/demo-team-guide.md for pipeline-attested/artifact-publication.\n'
