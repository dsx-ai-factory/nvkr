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
SETUP_ARGS=(--build-only)
GREEN='' BOLD='' RESET=''
if [ -t 1 ] && [ -z "${NO_COLOR+x}" ] && [ "${TERM:-dumb}" != dumb ]; then
  GREEN=$'\033[38;5;112m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
fi

usage() {
  cat <<'EOF'
Usage: bash demos/demo-local.sh [--out NEW_DIR] [--version 1.0.0|1.1.0] [--pause] [--install-go]

Build and run a tiny greeting application through NVKR:
  pin source -> choose an output -> inspect the Plan -> execute -> use the artifact

Requires Docker and Git. The launcher builds NVKR and offers the pinned Go SDK
(--install-go consents without a prompt). NVKR_BIN and GO_BIN may select existing executables. The first
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
    --install-go) SETUP_ARGS+=(--install-go); shift ;;
    --non-interactive) INTERACTIVE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
case "$VERSION" in 1.0.0|1.1.0) ;; *) die '--version must be 1.0.0 or 1.1.0' ;; esac
case "$(uname -s)" in
  Darwin) host_os=darwin ;; Linux) host_os=linux ;; *) die 'requires macOS or Linux' ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) die 'requires AMD64 or ARM64' ;;
esac
if [ -z "${NVKR_BIN:-}" ]; then
  GO_BIN="${GO_BIN:-}" bash "$ROOT/scripts/nvkr-quickstart.sh" "${SETUP_ARGS[@]}"
fi
[ -x "$NVKR" ] || die 'NVKR_BIN must select an executable'
required_go=$(tr -d '[:space:]' <"$ROOT/.go-version")
sdk="$ROOT/.nvkr/quickstart-sdk/go${required_go}.${host_os}-${arch}/go/bin/go"
if [ -z "${GO_BIN:-}" ] && [ -x "$sdk" ]; then GO=$sdk; fi
NVKR=$(cd "$(dirname "$NVKR")" && printf '%s/%s\n' "$PWD" "$(basename "$NVKR")")
for tool in git docker "$GO"; do command -v "$tool" >/dev/null || die "missing command: $tool"; done
GO=$(command -v "$GO")
GO=$(cd "$(dirname "$GO")" && printf '%s/%s\n' "$PWD" "$(basename "$GO")")
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
cp -R "$DEMO_DIR/hello/catalog" "$DEMO_DIR/hello/source" "$OUT/"
cp "$DEMO_DIR/hello/definition.yaml" "$DEMO_DIR/hello/environment.yaml" "$OUT/"
for input in definition.yaml environment.yaml catalog/registry.yaml; do
  sed "s@linux/amd64@linux/$arch@g" "$OUT/$input" >"$OUT/$input.native"
  mv "$OUT/$input.native" "$OUT/$input"
done
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
sed 's/Hello, %s!/Welcome, %s!/' source/hello.sh >source/hello.next
cat source/hello.next >source/hello.sh
# Retain the editing scratch outside the source closure.
mv source/hello.next logs/hello-v1.1.0.sh

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
"$NVKR" run plan --definition definition.yaml --environment environment.yaml
printf '  Full Plan and graph: plan.json | plan.mermaid\n'

step '3 / 4' 'Run it'
command_line run local --definition definition.yaml --environment environment.yaml --attempt demo --runner-source "$ROOT" --go "$GO"
printf '  Building in the pinned Alpine container… output is retained in logs/run.log\n'
run_started=$SECONDS
if ! "$NVKR" run local --definition definition.yaml --environment environment.yaml \
  --attempt demo --runner-source "$ROOT" --go "$GO" --ui plain 2>&1 | tee logs/run.log; then
  die "Run failed; full log: $OUT/logs/run.log"
fi
run_seconds=$((SECONDS - run_started))

step '4 / 4' 'Check the result and use it'
artifact=.nvkr/runs/demo/hello/out/hello/hello
"$NVKR" run inspect --record .nvkr/runs/demo/lifecycle.json
"$GO" run "$DEMO_DIR/hello/verify.go" "$artifact"
printf '\n  $ %s team\n' "$artifact"
greeting=$("$artifact" team)
expected="Hello, team! This is hello v1.0.0, built by NVKR."
if [ "$VERSION" = 1.1.0 ]; then expected="Welcome, team! This is hello v1.1.0, built by NVKR."; fi
[ "$greeting" = "$expected" ] || die "unexpected greeting: $greeting"
printf '%s\n' "$greeting"
printf '\n%sComplete%s  Run: %ss | total: %ss\n' "$GREEN" "$RESET" "$run_seconds" "$((SECONDS - started))"
printf 'Real local build; no signing, publication, or qualified reproduction claim.\n'
printf 'Inspect the inputs, Plan, Run record and artifact in:\n  %s\n' "$OUT"
if [ "$VERSION" = 1.0.0 ]; then
  printf 'Try --version 1.1.0 next: a new source pin produces a new Build digest and greeting.\n'
fi
