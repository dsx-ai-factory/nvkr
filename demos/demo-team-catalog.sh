#!/usr/bin/env bash
# Guided, non-executing catalog -> Definition -> Plan -> GitHub bundle tour.
set -euo pipefail
DEMO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DEMO_DIR/.." && pwd)
NKD=${NKDCTL_BIN:-$ROOT/bin/nkdctl}
NON_INTERACTIVE=0
DEPLOY=0
OUT=

usage() {
  cat <<'EOF'
Usage: bash demos/demo-team-catalog.sh [--deploy] [--out NEW_DIR] [--non-interactive]

Inspect catalog inputs, export a real Plan and native linked graphs, then generate
and check a GitHub bundle in a new directory. Default: three-output nke-v1;
no Docker or source checkout needed.
--deploy selects the six-output nke-deploy inputs and requires private pinned Git
source access because that catalog revalidates an upstream pin set.

Prerequisite: make build with the Go patch in .go-version. NKDCTL_BIN may name an
already built binary. No build, pin mutation, CI dispatch, signing or publication
is performed. Generated files and exports are retained in a new temporary directory
unless --out is supplied; its parent must exist and the directory must not exist.
EOF
}
die() { printf 'demo: %s\n' "$*" >&2; exit 2; }
pause() { if [ "$NON_INTERACTIVE" -eq 0 ]; then read -r -p 'Press Enter to continue... ' _; fi; }
run() { printf '\n$'; printf ' %q' "$@"; printf '\n'; "$@"; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --deploy) DEPLOY=1; shift ;;
    --out) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --out value'; OUT=$2; shift 2 ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[ -x "$NKD" ] || die "build nkdctl first (make build), or set NKDCTL_BIN to an executable: $NKD"
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nkd-team-catalog.XXXXXX"); fi
printf 'Exports: %s\n' "$OUT"
cd "$ROOT"
DEFINITION=definitions/nke-v1/definition.yaml
ENVIRONMENT=definitions/nke-v1/environment-github.yaml
CATALOG=definitions/nke-v1/catalog
if [ "$DEPLOY" -eq 1 ]; then
  DEFINITION=definitions/nke-deploy/definition.yaml
  ENVIRONMENT=definitions/nke-deploy/environment-github.yaml
  CATALOG=definitions/nke-controller/catalog
fi

printf '\n1 / 4  Meet the ingredients (3 minutes)\n'
printf 'Open %s/registry.yaml and %s/pins.lock.yaml.\n' "$CATALOG" "$CATALOG"
printf 'The catalog describes producers; the lock records exact resolved inputs.\n'
run "$NKD" catalog validate --data "$CATALOG"
run "$NKD" catalog list --data "$CATALOG"
printf 'Checkpoint: locate one component, its source declaration, and its exact locked commit.\n'
pause

printf '\n2 / 4  Choose outputs and execution settings (2 minutes)\n'
run cat "$DEFINITION"
run "$NKD" definition validate --definition "$DEFINITION"
printf 'Open %s: find the publication namespace and signing identity.\n' "$ENVIRONMENT"
printf 'Checkpoint: the Definition selects artifacts; the Environment supplies execution and publication settings.\n'
pause

printf '\n3 / 4  Explore the generated plan (4 minutes)\n'
printf '\nExporting the complete Plan to %s/plan.json\n' "$OUT"
"$NKD" run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json > "$OUT/plan.json"
"$NKD" run plan --plan "$OUT/plan.json" > "$OUT/plan.txt"
"$NKD" run plan --plan "$OUT/plan.json" --pretty > "$OUT/plan.pretty.json"
printf '\nNative graph exports (complete producer + artifact relationships):\n'
for format in mermaid dot json; do
  printf '$'; printf ' %q' "$NKD" run graph --plan "$OUT/plan.json" --format "$format"; printf '\n'
  "$NKD" run graph --plan "$OUT/plan.json" --format "$format" > "$OUT/plan-graph.$format"
done
printf 'Read plan.txt or plan.pretty.json; render plan-graph.mermaid / plan-graph.dot with a compatible viewer.\n'
printf 'Open %s/demo-team-explorer.html and load plan.json.\n' "$DEMO_DIR"
printf 'Trace source -> build -> SBOM/package -> publish/sign/verify. Select a node to inspect its real inputs.\n'
printf 'Checkpoint: the Plan requests a lane; its graph contains no evidence that those operations ran.\n'
pause

printf '\n4 / 4  Generate the pipeline (3 minutes)\n'
mkdir "$OUT/generated"
run "$NKD" run generate --definition "$DEFINITION" --environment "$ENVIRONMENT" --engine github --root "$OUT/generated"
run "$NKD" run generate --definition "$DEFINITION" --environment "$ENVIRONMENT" --engine github --root "$OUT/generated" --check
run "$NKD" run diff --before "$OUT/plan.json" --after "$OUT/generated/.github/nkd-run/plan.json"
printf '\nOpen %s/generated/.github/workflows/nkd-run.yml\n' "$OUT"
printf 'Also inspect .github/nkd-run/manifest.json beneath that generated root.\n'
printf 'Checkpoint: identical inputs and generator produced identical bundle bytes; no hosted job ran.\n'
printf 'run diff explains Plan changes; run generate --check checks all generated bundle bytes.\n'
printf '\nNext: bash demos/demo-team-os.sh, then bash demos/demo-team-release.sh --help\n'
