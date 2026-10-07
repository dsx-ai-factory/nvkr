#!/usr/bin/env bash
# Guided static OS-image material graph; intentionally does not start Packer.
set -euo pipefail
DOCS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DOCS/.." && pwd)
NKD=${NKDCTL_BIN:-$ROOT/bin/nkdctl}
NON_INTERACTIVE=0
ARCH=arm64
OUT=
usage() {
  cat <<'EOF'
Usage: bash docs/demo-team-os.sh [--arch arm64|amd64] [--out NEW_DIR] [--non-interactive]

Export the maintained native KVM OS-image Plan and native producer graph, then
inspect a dry-run. No Docker,
KVM, Packer execution, registry access or OS image build is required for this tour.
Prerequisite: a built nkdctl from this checkout (make build, exact Go in .go-version),
or NKDCTL_BIN pointing to that binary. Output is retained in a new temporary
directory; --out requires a nonexistent directory whose parent already exists.
EOF
}
die() { printf 'demo: %s\n' "$*" >&2; exit 2; }
pause() { if [ "$NON_INTERACTIVE" -eq 0 ]; then read -r -p 'Press Enter to continue... ' _; fi; }
run() { printf '\n$'; printf ' %q' "$@"; printf '\n'; "$@"; }
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
[ -x "$NKD" ] || die "build nkdctl first (make build), or set NKDCTL_BIN to an executable: $NKD"
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nkd-team-os.XXXXXX"); fi
printf 'Exports: %s\n' "$OUT"
cd "$ROOT"
DEFINITION="definitions/nke-os/definition-$ARCH-kvm.yaml"
ENVIRONMENT="definitions/nke-os/environment-local-linux-$ARCH-docker.yaml"

printf '\n1 / 3  An OS image is a declared output (1 minute)\n'
run cat "$DEFINITION"
printf 'Find disk-image, the concrete guest platform, and the explicitly selected material producers.\n'
pause

printf '\n2 / 3  Follow materials into Packer (3 minutes)\n'
"$NKD" run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json > "$OUT/os-plan-$ARCH.json"
"$NKD" run plan --plan "$OUT/os-plan-$ARCH.json" > "$OUT/os-plan-$ARCH.txt"
printf '$'; printf ' %q' "$NKD" run graph --plan "$OUT/os-plan-$ARCH.json" --view producers --format mermaid; printf '\n'
"$NKD" run graph --plan "$OUT/os-plan-$ARCH.json" --view producers --format mermaid > "$OUT/os-producers-$ARCH.mermaid"
"$NKD" run graph --plan "$OUT/os-plan-$ARCH.json" --view complete --format json > "$OUT/os-graph-$ARCH.json"
printf 'Native graph: %s/os-producers-%s.mermaid; exact graph records: os-graph-%s.json\n' "$OUT" "$ARCH" "$ARCH"
printf '\nOpen %s/demo-team-explorer.html and load os-plan-%s.json.\n' "$DOCS" "$ARCH"
printf 'Find the disk root and follow its incoming material edges: agent, networking, installer ISO and packages.\n'
printf 'Open %s to inspect KVM, CPU, memory and workspace requirements.\n' "$ENVIRONMENT"
printf 'Checkpoint: declared capacity is planning input; execution must admit the actual host.\n'
pause

printf '\n3 / 3  Separate the plan from a built disk (1 minute)\n'
run "$NKD" run local --definition "$DEFINITION" --environment "$ENVIRONMENT" --attempt team-os-preview --dry-run
printf '\nExpected: pending, static-unqualified, zero observed outputs; no attempt workspace or QCOW2 is created.\n'
printf 'A successful real local Run retains .nkd/runs/<attempt>/outputs/nke-node-image-%s.qcow2.\n' "$ARCH"
printf 'Read definitions/nke-os/README.md before running the actual build wrapper.\n'
printf 'Historical native ARM64 and AMD64 bake/boot evidence: docs/evidence/2026-10-01-nke-arc-os-builds.md\n'
printf 'The linked presentation pipeline is a separate candidate; its exact artifacts and status belong to its recorded Run.\n'
printf 'This maintained-checkout walkthrough does not execute generated hosted disk publication or claim qualification.\n'
