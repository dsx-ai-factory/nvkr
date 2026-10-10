#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# A real container build with local Git input and no publication.
set -euo pipefail
DEMO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$DEMO_DIR/.." && pwd)
NVKR=${NVKR_BIN:-$ROOT/bin/nvkr}
GO=${GO_BIN:-go}
OUT=
VERSION=1.0.0
INTERACTIVE=0
GREEN='' BOLD='' RESET=''
if [ -t 1 ] && [ -z "${NO_COLOR+x}" ] && [ "${TERM:-dumb}" != dumb ]; then
  GREEN=$'\033[38;5;112m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
fi

usage() {
  cat <<'EOF'
Usage: bash demos/demo-local.sh [--out NEW_DIR] [--version 1.0.0|1.1.0] [--pause]

Build and run a tiny greeting application through NVKR:
  pin source -> choose an output -> inspect the Plan -> execute -> use the artifact

Requires Docker, Git, Python 3, the Go version in .go-version and a built nvkr
(make build). NVKR_BIN and GO_BIN may select existing executables. The first
run may download the small pinned Alpine image and Go dependencies. No registry
credentials are needed. The source is an isolated local Git repository.

Default: run without pauses; retain inputs, artifacts and logs in a new directory
under .nvkr/. --pause waits between steps. --out must name a new directory whose
parent exists. --version 1.1.0 demonstrates a different source pin and artifact.

This is real, unqualified local execution. Nothing is signed or published.
EOF
}
die() { printf '\nDemo stopped: %s\n' "$*" >&2; exit 1; }
step() {
  printf '\n%s%s%s  %s\n' "$GREEN" "$1" "$RESET" "$2"
  if [ "$INTERACTIVE" -eq 1 ]; then read -r -p 'Press Enter to continue… ' _; fi
}
command_line() {
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
logged() {
  local log=$1
  shift
  if "$@" >"logs/$log.log" 2>&1; then return; else
    local status=$?
    tail -n 18 "logs/$log.log" >&2
    die "$log exited $status; full log: $OUT/logs/$log.log"
  fi
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out) [ "$#" -ge 2 ] && [ -n "$2" ] || die 'missing --out value'; OUT=$2; shift 2 ;;
    --version) [ "$#" -ge 2 ] || die 'missing --version value'; VERSION=$2; shift 2 ;;
    --pause) INTERACTIVE=1; shift ;;
    --non-interactive) INTERACTIVE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
case "$VERSION" in 1.0.0|1.1.0) ;; *) die '--version must be 1.0.0 or 1.1.0' ;; esac
[ -x "$NVKR" ] || die 'build nvkr first with make build, or set NVKR_BIN'
NVKR=$(cd "$(dirname "$NVKR")" && printf '%s/%s\n' "$PWD" "$(basename "$NVKR")")
for tool in git docker python3 "$GO"; do command -v "$tool" >/dev/null || die "missing command: $tool"; done
GO=$(command -v "$GO")
GO=$(cd "$(dirname "$GO")" && printf '%s/%s\n' "$PWD" "$(basename "$GO")")
required_go=$(tr -d '[:space:]' <"$ROOT/.go-version")
[ "$(GOTOOLCHAIN=local "$GO" env GOVERSION)" = "go$required_go" ] || die "use Go $required_go (set GO_BIN or PATH)"
export GOTOOLCHAIN=local
docker info >/dev/null 2>&1 || die 'start Docker before running this demo'
if [ "$INTERACTIVE" -eq 1 ] && [ ! -t 0 ]; then die '--pause requires a terminal'; fi
if [ -n "$OUT" ]; then
  [ ! -e "$OUT" ] && [ ! -L "$OUT" ] || die "output directory already exists: $OUT"
  mkdir -- "$OUT"
else
  mkdir -p "$ROOT/.nvkr"
  OUT=$(mktemp -d "$ROOT/.nvkr/demo-local.XXXXXX")
fi
OUT=$(cd "$OUT" && pwd -P)
cp -R "$DEMO_DIR/quickstart/catalog" "$DEMO_DIR/quickstart/source" "$OUT/"
cp "$DEMO_DIR/quickstart/definition.yaml" "$DEMO_DIR/quickstart/environment.yaml" "$OUT/"
cd "$OUT"
mkdir logs
started=$SECONDS

printf '\n%sNVKR | FROM SOURCE TO ARTIFACT%s\n' "$BOLD" "$RESET"
printf 'One catalog component. One requested output. One real container build.\n'
printf 'Working directory: %s\n' "$OUT"

# Scoped to this script and its children: no user Git configuration is changed.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE \
  GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_PREFIX
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$OUT/source.insteadOf"
export GIT_CONFIG_VALUE_0=https://demo.example.invalid/hello
export GIT_AUTHOR_NAME='NVKR Demo' GIT_AUTHOR_EMAIL=demo@example.invalid
export GIT_COMMITTER_NAME='NVKR Demo' GIT_COMMITTER_EMAIL=demo@example.invalid
export GIT_AUTHOR_DATE=2026-01-01T00:00:00Z GIT_COMMITTER_DATE=2026-01-01T00:00:00Z
fixture_git() { git -C source -c commit.gpgsign=false -c tag.gpgSign=false -c core.autocrlf=false "$@"; }
fixture_git init -q --template= --object-format=sha1 --initial-branch=main
fixture_git add hello.sh
fixture_git commit -q -m 'hello v1.0.0'
fixture_git tag v1.0.0
python3 - <<'PY'
from pathlib import Path
p = Path('source/hello.sh')
p.write_text(p.read_text().replace('Hello, %s!', 'Welcome, %s!'))
PY
fixture_git add hello.sh
fixture_git commit -q -m 'hello v1.1.0'
fixture_git tag v1.1.0

step '1 / 4' 'Pin the source'
printf 'The catalog names the source and the build command. NVKR locks the exact commit.\n'
command_line catalog pin --dir catalog --tag "v$VERSION" hello
logged pin "$NVKR" catalog pin --dir catalog --tag "v$VERSION" hello
printf '  v%s -> %s\n' "$VERSION" "$(fixture_git rev-parse --short=12 "v$VERSION")"
printf '  Build command: substitute the pinned version into hello.sh, then make it executable.\n'

step '2 / 4' 'Choose the output and see the plan'
sed '/^#/d; /^$/d' definition.yaml
command_line run plan --definition definition.yaml --environment environment.yaml --json
if ! "$NVKR" run plan --definition definition.yaml --environment environment.yaml --json >plan.json 2>logs/plan.log; then
  cat logs/plan.log >&2; die 'Plan compilation failed'
fi
if ! "$NVKR" run graph --plan plan.json --format mermaid >plan.mermaid 2>logs/graph.log; then
  cat logs/graph.log >&2; die 'graph export failed'
fi
python3 - <<'PY'
import json
p = json.load(open('plan.json'))
print('\n  Source -> build hello -> observe bytes')
print('  From those bytes: executable package + composition SBOM')
print('  Build digest: ' + p['builds'][0]['buildDigest'])
print('  Full Plan and graph: plan.json | plan.mermaid')
PY

step '3 / 4' 'Run it'
command_line run local --definition definition.yaml --environment environment.yaml --attempt demo --runner-source "$ROOT" --go "$GO"
printf '  Building in the pinned Alpine container… full output goes to logs/run.log\n'
run_started=$SECONDS
logged run "$NVKR" run local --definition definition.yaml --environment environment.yaml \
  --attempt demo --runner-source "$ROOT" --go "$GO"
run_seconds=$((SECONDS - run_started))

step '4 / 4' 'Check the result and use it'
artifact=.nvkr/runs/demo/hello/out/hello/hello
python3 - "$artifact" <<'PY'
import hashlib, json, sys
from pathlib import Path
r = json.load(open('.nvkr/runs/demo/lifecycle.json'))
def require(condition, message):
    if not condition:
        raise SystemExit(message)
require(r['status'] == 'complete', 'Run did not complete')
require(r['lane'] == 'unqualified-local-execution', 'unexpected execution lane')
require(len(r['observed']) == 1 and r['observed'][0]['name'] == 'hello', 'expected exactly hello')
artifact = Path(sys.argv[1])
digest = 'sha256:' + hashlib.sha256(artifact.read_bytes()).hexdigest()
index = json.load(open('.nvkr/runs/demo/hello/sbom/index.json'))
require(len(index['entries']) == 1 and index['entries'][0]['artifactSha256'] == digest, 'artifact/SBOM subject mismatch')
print('  Run: complete | output: hello | lane: unqualified-local-execution')
print('  Artifact bytes match the recorded SBOM subject.')
print('  Artifact digest: ' + digest)
PY
printf '\n  $ %s team\n' "$artifact"
"$artifact" team
printf '\n%sComplete%s  Run: %ss | total: %ss\n' "$GREEN" "$RESET" "$run_seconds" "$((SECONDS - started))"
printf 'Real local build; no signing, publication, or qualified reproduction claim.\n'
printf 'Inspect the inputs, Plan, Run record and artifact in:\n  %s\n' "$OUT"
if [ "$VERSION" = 1.0.0 ]; then
  printf 'Try --version 1.1.0 next: a new source pin produces a new Build digest and greeting.\n'
fi
