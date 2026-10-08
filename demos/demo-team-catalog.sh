#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

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

Prerequisites: Python 3 and make build with the Go patch in .go-version. NKDCTL_BIN
may name an already built binary. No build, pin mutation, CI dispatch, signing or publication
is performed. Generated files and exports are retained in a new temporary directory
unless --out is supplied; its parent must exist and the directory must not exist.
Full commands and native output are saved with the exports. Set NO_COLOR to disable color.
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
  local line='  $ nkdctl' argument quoted
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
    --deploy) DEPLOY=1; shift ;;
    --out) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --out value'; OUT=$2; shift 2 ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[ -x "$NKD" ] || die "build nkdctl first (make build), or set NKDCTL_BIN to an executable: $NKD"
command -v python3 >/dev/null || die 'Python 3 is required for the readable summary'
NKD=$(cd "$(dirname "$NKD")" && printf '%s/%s\n' "$PWD" "$(basename "$NKD")")
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ]; then die 'use --non-interactive when stdin is not a terminal'; fi
if [ -n "$OUT" ]; then mkdir -- "$OUT"; OUT=$(cd "$OUT" && pwd); else OUT=$(mktemp -d "${TMPDIR:-/tmp}/nkd-team-catalog.XXXXXX"); fi
cd "$ROOT"
printf 'cd %q\n' "$ROOT" >"$OUT/commands.txt"
DEFINITION=definitions/nke-v1/definition.yaml
ENVIRONMENT=definitions/nke-v1/environment-github.yaml
CATALOG=definitions/nke-v1/catalog
if [ "$DEPLOY" -eq 1 ]; then
  DEFINITION=definitions/nke-deploy/definition.yaml
  ENVIRONMENT=definitions/nke-deploy/environment-github.yaml
  CATALOG=definitions/nke-controller/catalog
fi

heading 'NVKE Recipes / From catalog to pipeline'
printf 'Choose outputs once. Inspect the work. Generate the workflow.\n'
printf 'Exports and full commands: %s\n' "$OUT"

heading '1 / 4  Meet the components'
printf 'The catalog describes how to produce each component; the lock fixes its inputs.\n'
show_command catalog list --data "$CATALOG"
export_command "$OUT/catalog-validation.txt" "$NKD" catalog validate --data "$CATALOG"
export_command "$OUT/catalog-list.txt" "$NKD" catalog list --data "$CATALOG"
cat "$OUT/catalog-list.txt"
printf 'Read the declarations: %s/registry.yaml\n' "$CATALOG"
printf 'Read the exact pins:   %s/pins.lock.yaml\n' "$CATALOG"
pause

heading '2 / 4  Choose the outputs'
show_command definition inspect --definition "$DEFINITION"
export_command "$OUT/definition-validation.txt" "$NKD" definition validate --definition "$DEFINITION"
export_command "$OUT/definition.json" "$NKD" definition inspect --definition "$DEFINITION"
python3 - "$OUT/definition.json" <<'PY'
import json
import sys
definition = json.load(open(sys.argv[1]))
print(f"  {'Output':<24} {'Format':<12} Platform")
for output in definition['outputs']:
    print(f"  {output['name']:<24} {output['format']:<12} {output['platform']}")
    print(f"    components: {', '.join(output['components'])}")
PY
printf 'Definition:  %s\n' "$DEFINITION"
printf 'Environment: %s\n' "$ENVIRONMENT"
printf 'The Environment supplies runners, publication destinations, and signing policy.\n'
pause

heading '3 / 4  See the work before running it'
show_command run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json
export_command "$OUT/plan.json" "$NKD" run plan --definition "$DEFINITION" --environment "$ENVIRONMENT" --json
export_command "$OUT/plan.txt" "$NKD" run plan --plan "$OUT/plan.json"
export_command "$OUT/plan.pretty.json" "$NKD" run plan --plan "$OUT/plan.json" --pretty
for format in mermaid dot json; do
  export_command "$OUT/plan-graph.$format" "$NKD" run graph --plan "$OUT/plan.json" --format "$format"
done
python3 - "$OUT/plan.json" <<'PY'
import json
import sys
plan = json.load(open(sys.argv[1]))
print(f"  {len(plan['builds'])} outputs | {len(plan['graph']['nodes'])} operations | {len(plan['graph']['artifacts'])} artifact records")
print(f"  Requested execution lane: {plan['qualification']}")
PY
printf 'Load plan.json in demos/demo-team-explorer.html.\n'
printf 'Follow a component from source to build, package, publication, and evidence.\n'
printf 'Native text and graph exports are saved beside the Plan. No build has run.\n'
pause

heading '4 / 4  Generate the GitHub workflow'
mkdir "$OUT/generated"
show_command run generate --definition "$DEFINITION" --environment "$ENVIRONMENT" --engine github --root "$OUT/generated"
export_command "$OUT/generate.txt" "$NKD" run generate --definition "$DEFINITION" --environment "$ENVIRONMENT" --engine github --root "$OUT/generated"
export_command "$OUT/generate-check.txt" "$NKD" run generate --definition "$DEFINITION" --environment "$ENVIRONMENT" --engine github --root "$OUT/generated" --check
export_command "$OUT/plan-diff.txt" "$NKD" run diff --before "$OUT/plan.json" --after "$OUT/generated/.github/nkd-run/plan.json" --exit-code
printf '  Generated bundle matches on regeneration; the Plans have no differences.\n'
printf '  Workflow: %s/generated/.github/workflows/nkd-run.yml\n' "$OUT"
printf '  Full commands and check output: %s\n' "$OUT"
printf '\nThe workflow is ready to inspect. No hosted job was dispatched.\n'
printf 'Next: bash demos/demo-local.sh --pause\n'
