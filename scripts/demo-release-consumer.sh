#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Verify one signed lifecycle publication through the public Release consumer.
set -euo pipefail
CALLER_DIR=$(pwd -P)
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

REFERENCE=${REFERENCE:-}
IDENTITY=${IDENTITY:-}
ISSUER=${ISSUER:-}
COSIGN=${COSIGN:-}
COSIGN_DIGEST=${COSIGN_DIGEST:-}
DEFINITION=
ENVIRONMENT=
PLAN=
DEFINITION_SET=0
ENVIRONMENT_SET=0
PLAN_SET=0
WORK=${WORK:-.nkd/demo-release-consumer}
PLAIN_HTTP=0
ANONYMOUS=0
EXECUTION_RUNTIME=auto
REGISTRY_CONFIG=
# The maintained NKE CLI toolchain provides Linux amd64, CA certificates and
# no additional authority: NKD still snapshots and authenticates cosign bytes.
LINUX_IMAGE=golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6

usage() {
  cat <<'EOF'
Usage: scripts/demo-release-consumer.sh [options]

Verify one digest-pinned lifecycle publication under an explicit
pipeline-attested/artifact-publication Verification Policy. The consumer checks
the exact signature, provenance attestation subject, signer identity and issuer,
publication closure, named Artifacts, and exact evidence membership before returning a
Release capability scoped to artifact-publication. This does not establish
qualification verdicts, a release manifest, revocation status, or deployment acceptance.

Required:
  --reference REF        registry/repository@sha256:...
  --identity REGEXP      trusted certificate identity policy
  --issuer ISSUER        trusted OIDC issuer
  --cosign PATH          pinned static cosign executable
  --cosign-digest SHA256 exact executable digest

Options:
  --definition PATH      exact authored Definition to require with the next two options
  --environment PATH     exact selected Environment to require with the other two options
  --plan PATH            exact compiled Plan to require with the previous two options
  --work DIR             verification workspace below .nkd
  --plain-http           use a development HTTP registry
  --anonymous            use anonymous registry access
  --runtime MODE         auto (default), native, or docker; auto uses Docker on macOS
  --registry-config DIR  Docker auth directory for container verification (inline auth only)
  -h, --help             show help

This command contacts the configured registry and cosign verification services.
Relative exact-input paths are resolved from the caller's working directory.
Use a static Linux amd64 cosign executable for Docker verification. On macOS,
the script builds a Linux NKD helper and verifies inside the pinned container;
native Linux uses the host CLI. Docker mode needs the repository's pinned Go
toolchain and Docker. The executable pin, signer policy and release checks remain
mandatory. Host credential helpers are not available inside the container; use
--anonymous for public releases or --registry-config with explicit inline auth.
It needs a real signed publication
produced by the supported GitHub lane; local dry-run output cannot pass this policy.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --reference) [ "$#" -ge 2 ] || demo_die "missing --reference value"; REFERENCE=$2; shift 2 ;;
    --identity) [ "$#" -ge 2 ] || demo_die "missing --identity value"; IDENTITY=$2; shift 2 ;;
    --issuer) [ "$#" -ge 2 ] || demo_die "missing --issuer value"; ISSUER=$2; shift 2 ;;
    --cosign) [ "$#" -ge 2 ] || demo_die "missing --cosign value"; COSIGN=$2; shift 2 ;;
    --cosign-digest) [ "$#" -ge 2 ] || demo_die "missing --cosign-digest value"; COSIGN_DIGEST=$2; shift 2 ;;
    --definition) [ "$#" -ge 2 ] || demo_die "missing --definition value"; DEFINITION=$2; DEFINITION_SET=1; shift 2 ;;
    --environment) [ "$#" -ge 2 ] || demo_die "missing --environment value"; ENVIRONMENT=$2; ENVIRONMENT_SET=1; shift 2 ;;
    --plan) [ "$#" -ge 2 ] || demo_die "missing --plan value"; PLAN=$2; PLAN_SET=1; shift 2 ;;
    --work) [ "$#" -ge 2 ] || demo_die "missing --work value"; WORK=$2; shift 2 ;;
    --plain-http) PLAIN_HTTP=1; shift ;;
    --anonymous) ANONYMOUS=1; shift ;;
    --runtime) [ "$#" -ge 2 ] || demo_die "missing --runtime value"; EXECUTION_RUNTIME=$2; shift 2 ;;
    --registry-config) [ "$#" -ge 2 ] || demo_die "missing --registry-config value"; REGISTRY_CONFIG=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) demo_die "unknown option: $1" ;;
  esac
done

[ -n "$REFERENCE" ] || demo_die "--reference is required"
[ -n "$IDENTITY" ] || demo_die "--identity is required"
[ -n "$ISSUER" ] || demo_die "--issuer is required"
[ -n "$COSIGN" ] || demo_die "--cosign is required"
case "$COSIGN_DIGEST" in sha256:????????????????????????????????????????????????????????????????) ;; *) demo_die "--cosign-digest must be sha256:<64 lowercase hex>" ;; esac
case "${COSIGN_DIGEST#sha256:}" in *[!0-9a-f]*) demo_die "--cosign-digest must be sha256:<64 lowercase hex>" ;; esac
EXACT_INPUTS=$((DEFINITION_SET + ENVIRONMENT_SET + PLAN_SET))
if [ "$EXACT_INPUTS" -ne 0 ] && [ "$EXACT_INPUTS" -ne 3 ]; then
  demo_die "--definition, --environment, and --plan must be supplied together"
fi
if [ "$EXACT_INPUTS" -eq 3 ]; then
  [ -n "$DEFINITION" ] || demo_die "--definition requires a non-empty path"
  [ -n "$ENVIRONMENT" ] || demo_die "--environment requires a non-empty path"
  [ -n "$PLAN" ] || demo_die "--plan requires a non-empty path"
  case "$DEFINITION" in /*) ;; *) DEFINITION="$CALLER_DIR/$DEFINITION" ;; esac
  case "$ENVIRONMENT" in /*) ;; *) ENVIRONMENT="$CALLER_DIR/$ENVIRONMENT" ;; esac
  case "$PLAN" in /*) ;; *) PLAN="$CALLER_DIR/$PLAN" ;; esac
fi
demo_require_nkd_child_path "$WORK"
case "$EXECUTION_RUNTIME" in
  auto) if [ "$(uname -s)" = Darwin ]; then EXECUTION_RUNTIME=docker; else EXECUTION_RUNTIME=native; fi ;;
  native|docker) ;;
  *) demo_die "--runtime must be auto, native, or docker" ;;
esac
if [ "$EXECUTION_RUNTIME" = docker ]; then
  demo_need_command docker
  demo_need_command go
  docker info >/dev/null 2>&1 || demo_die "Docker daemon is not reachable"
  make go-version-check
  case "$COSIGN" in /*) ;; *) COSIGN="$CALLER_DIR/$COSIGN" ;; esac
  [ -f "$COSIGN" ] && [ ! -L "$COSIGN" ] || demo_die "--cosign must name a regular Linux amd64 executable"
  if [ -n "$REGISTRY_CONFIG" ]; then
    [ "$ANONYMOUS" -eq 0 ] || demo_die "--registry-config cannot be combined with --anonymous"
    case "$REGISTRY_CONFIG" in /*) ;; *) REGISTRY_CONFIG="$CALLER_DIR/$REGISTRY_CONFIG" ;; esac
    [ -f "$REGISTRY_CONFIG/config.json" ] && [ ! -L "$REGISTRY_CONFIG/config.json" ] ||
      demo_die "--registry-config requires a regular config.json file"
    demo_need_command python3
    python3 - "$REGISTRY_CONFIG/config.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    config = json.load(f)
if config.get("credsStore") or config.get("credHelpers"):
    sys.exit("demo: container registry config requires inline auth, not host credential helpers")
PY
  fi
else
  [ -z "$REGISTRY_CONFIG" ] || demo_die "--registry-config requires Docker mode; native verification uses DOCKER_CONFIG"
  demo_prepare_nkdctl
fi
demo_prepare_work "$WORK" "release-consumer"

args=(release verify --reference "$REFERENCE" --lane pipeline-attested --scope artifact-publication
  --identity "$IDENTITY" --issuer "$ISSUER" --cosign "$COSIGN" --cosign-digest "$COSIGN_DIGEST" --workspace "$WORK")
[ "$PLAIN_HTTP" -eq 0 ] || args+=(--plain-http)
[ "$ANONYMOUS" -eq 0 ] || args+=(--anonymous)
[ "$EXACT_INPUTS" -eq 0 ] || args+=(--definition "$DEFINITION" --environment "$ENVIRONMENT" --plan "$PLAN")
if [ "$EXECUTION_RUNTIME" = docker ]; then
  printf 'Build Linux amd64 NKD; verify using Docker (%s)\n' "$LINUX_IMAGE"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local go build -trimpath -o "$WORK/nkdctl-linux-amd64" ./cmd/nkdctl
  docker_args=(run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" -e HOME=/tmp
    -v "$PWD/$WORK:/workspace/$WORK" -w /workspace
    -v "$PWD/$WORK/nkdctl-linux-amd64:/nkdctl:ro" -v "$COSIGN:/cosign:ro")
  if [ -n "$REGISTRY_CONFIG" ]; then
    # Cosign scrubs DOCKER_CONFIG overrides; mount the explicitly selected auth
    # at the private container HOME's standard path, shared with the OCI reader.
    docker_args+=(-v "$REGISTRY_CONFIG/config.json:/tmp/.docker/config.json:ro")
  fi
  # Inputs are explicit read-only mounts, including paths outside the checkout.
  args=(release verify --reference "$REFERENCE" --lane pipeline-attested --scope artifact-publication
    --identity "$IDENTITY" --issuer "$ISSUER" --cosign /cosign --cosign-digest "$COSIGN_DIGEST" --workspace "$WORK")
  [ "$PLAIN_HTTP" -eq 0 ] || args+=(--plain-http)
  [ "$ANONYMOUS" -eq 0 ] || args+=(--anonymous)
  if [ "$EXACT_INPUTS" -eq 3 ]; then
    for input_path in "$DEFINITION" "$ENVIRONMENT" "$PLAN"; do
      [ -f "$input_path" ] && [ ! -L "$input_path" ] || demo_die "exact inputs must be regular files"
    done
    docker_args+=(-v "$DEFINITION:/inputs/definition.yaml:ro" -v "$ENVIRONMENT:/inputs/environment.yaml:ro" -v "$PLAN:/inputs/plan.json:ro")
    args+=(--definition /inputs/definition.yaml --environment /inputs/environment.yaml --plan /inputs/plan.json)
  fi
  docker "${docker_args[@]}" "$LINUX_IMAGE" /nkdctl "${args[@]}"
else
  "$NKD" "${args[@]}"
fi

printf '\nARTIFACT PUBLICATION VERIFIED\n  reference: %s\n  policy: pipeline-attested/artifact-publication\n' "$REFERENCE"
if [ "$EXACT_INPUTS" -eq 3 ]; then
  printf '  exact inputs: Definition, Environment, and Plan matched (description emitted)\n'
fi
