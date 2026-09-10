#!/usr/bin/env bash
# CUJ: execute the AICR Definition on this laptop and optionally publish
# its complete lifecycle envelope to an owned loopback registry.

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

ATTEMPTS_ROOT=${ATTEMPTS_ROOT:-.nkd/demo-ai-factory-local}
ATTEMPT=${ATTEMPT:-$(demo_default_attempt aicr)}
ENVIRONMENT=${ENVIRONMENT:-definitions/aicr/environment-local.yaml}
DEFINITION=${DEFINITION:-definitions/aicr/definition.yaml}
JOBS=${JOBS:-1}
PUBLISH=0
PLAN=0
REGISTRY_CONTAINER=${REGISTRY_CONTAINER:-nkd-aicr-demo-registry}
REGISTRY_WORK=${REGISTRY_WORK:-.nkd/demo-aicr-registry}

usage() {
  cat <<'EOF_USAGE'
Usage: scripts/demo-ai-factory-local.sh [options]

Execute one immutable local Run:
  pinned AICR Recipe -> exact render observation -> Helm OCI output
    -> complete lifecycle publication envelope

Options:
  --definition PATH    Definition YAML (default: maintained AICR Definition)
  --environment PATH   Environment YAML (default: loopback registry on port 5001)
  --attempts-root DIR  immutable Run storage root under .nkd
  --attempt NAME       new attempt name (default: timestamp plus process ID)
  --jobs N             parallel recipe slots (default: 1)
  --publish             start or reuse the owned localhost:5001 registry and publish
  --plan                validate and print the Run plan; perform no execution
  -h, --help            show this help

The execution needs Docker and network access to acquire the exact pinned AICR
archive. Rendering runs in the pinned Linux utility container with no network.
Planning is static-unqualified. Executed local Runs record unqualified-local-execution
and cannot mint a verified Release capability.
EOF_USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --definition) [ "$#" -ge 2 ] || demo_die "missing --definition value"; DEFINITION=$2; shift 2 ;;
    --environment) [ "$#" -ge 2 ] || demo_die "missing --environment value"; ENVIRONMENT=$2; shift 2 ;;
    --attempts-root) [ "$#" -ge 2 ] || demo_die "missing --attempts-root value"; ATTEMPTS_ROOT=$2; shift 2 ;;
    --attempt) [ "$#" -ge 2 ] || demo_die "missing --attempt value"; ATTEMPT=$2; shift 2 ;;
    --jobs) [ "$#" -ge 2 ] || demo_die "missing --jobs value"; JOBS=$2; shift 2 ;;
    --publish) PUBLISH=1; shift ;;
    --plan) PLAN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) demo_die "unknown flag: $1 (run --help)" ;;
  esac
done

case "$JOBS" in ''|*[!0-9]*) demo_die "--jobs must be a positive integer" ;; esac
[ "$JOBS" -gt 0 ] || demo_die "--jobs must be a positive integer"
case "$ATTEMPTS_ROOT" in .nkd/?*) ;; *) demo_die "--attempts-root must be a repository-relative child of .nkd" ;; esac
case "$ATTEMPT" in ""|*/*|*..*) demo_die "--attempt must be one clean name" ;; esac

WORK=$ATTEMPTS_ROOT/$ATTEMPT

demo_prepare_nkdctl

demo_section "1/4  Validate the Definition and inspect its exact Run plan"
printf 'Definition: %s\n' "$DEFINITION"
"$NKD" definition validate --definition "$DEFINITION"
"$NKD" run plan --definition "$DEFINITION" --environment "$ENVIRONMENT"
if [ "$PLAN" -eq 1 ]; then
  printf '\nPLAN ONLY — no attempt, container, source, artifact, or registry work was performed.\n'
  exit 0
fi

demo_need_command docker
docker info >/dev/null 2>&1 || demo_die "Docker daemon is not reachable"

if [ "$PUBLISH" -eq 1 ]; then
  demo_section "2/4  Start or reuse the owned localhost OCI registry"
  demo_need_command curl
  if docker container inspect "$REGISTRY_CONTAINER" >/dev/null 2>&1; then
    owner=$(docker container inspect --format '{{index .Config.Labels "dev.nkd.demo.owner"}}' "$REGISTRY_CONTAINER")
    [ "$owner" = "aicr-local" ] || demo_die "container $REGISTRY_CONTAINER exists but is not owned by this demo"
    [ "$(docker container inspect --format '{{.State.Running}}' "$REGISTRY_CONTAINER")" = true ] || docker start "$REGISTRY_CONTAINER" >/dev/null
  else
    REGISTRY_WORK=$(demo_absolute_path "$REGISTRY_WORK")
    demo_prepare_work "$REGISTRY_WORK" "aicr-local-registry" "$(demo_absolute_path .nkd/demo-aicr-registry)"
    mkdir -p "$REGISTRY_WORK/data"
    docker run -d --name "$REGISTRY_CONTAINER" --label dev.nkd.demo.owner=aicr-local \
      -p 127.0.0.1:5001:5000 -v "$REGISTRY_WORK/data:/var/lib/registry" registry:3 >/dev/null
  fi
  ready=0
  for _ in $(seq 1 30); do
    if curl --fail --silent --show-error http://127.0.0.1:5001/v2/ >/dev/null 2>&1; then ready=1; break; fi
    sleep 1
  done
  [ "$ready" -eq 1 ] || demo_die "local registry did not become ready"
else
  demo_section "2/4  Keep publication local"
  printf '%s\n' 'The complete OCI envelope is built locally; the registry route is skipped.'
fi

demo_section "3/4  Execute one immutable local Run"
args=(run local --definition "$DEFINITION" --environment "$ENVIRONMENT" --attempts-root "$ATTEMPTS_ROOT" --attempt "$ATTEMPT" --jobs "$JOBS")
[ "$PUBLISH" -eq 0 ] || args+=(--publish)
"$NKD" "${args[@]}"

ROOT="$WORK/packages/gpu-runtime-release"
[ -d "$ROOT" ] || demo_die "Run completed without the planned publication envelope: $ROOT"
"$NKD" publish inspect --layout "$ROOT" --json > "$WORK/publication-inspection.json"

demo_section "4/4  Show Run and output locations"
cat <<EOF_RESULT
LOCAL AICR RUN COMPLETE — UNQUALIFIED-LOCAL-EXECUTION
  Run record:           $WORK/lifecycle.json
  AICR render tree:     $WORK/h100-runtime/aicr-bundle
  Helm OCI payload:     $WORK/packages/gpu-runtime
  publication envelope: $ROOT
  inspection:           $WORK/publication-inspection.json
EOF_RESULT
if [ "$PUBLISH" -eq 1 ]; then
  printf '  published reference: %s\n' "$(tr -d '\n' < "$WORK/publications/gpu-runtime.txt")"
fi
cat <<'EOF_NOTE'

This local Run records the exact AICR and Helm output observations. Its lane is
unqualified-local-execution; it does not authorize release consumption or deployment.
EOF_NOTE
