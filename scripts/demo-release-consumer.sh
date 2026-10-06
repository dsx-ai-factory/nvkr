#!/usr/bin/env bash
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
  -h, --help             show help

This command contacts the configured registry and cosign verification services.
Relative exact-input paths are resolved from the caller's working directory.
Run it on Linux with a static Linux cosign executable; macOS cannot execute the
held-descriptor verification capability. It needs a real signed publication
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
demo_prepare_nkdctl
demo_prepare_work "$WORK" "release-consumer"

args=(release verify --reference "$REFERENCE" --lane pipeline-attested --scope artifact-publication
  --identity "$IDENTITY" --issuer "$ISSUER" --cosign "$COSIGN" --cosign-digest "$COSIGN_DIGEST" --workspace "$WORK")
[ "$PLAIN_HTTP" -eq 0 ] || args+=(--plain-http)
[ "$ANONYMOUS" -eq 0 ] || args+=(--anonymous)
[ "$EXACT_INPUTS" -eq 0 ] || args+=(--definition "$DEFINITION" --environment "$ENVIRONMENT" --plan "$PLAN")
"$NKD" "${args[@]}"

printf '\nARTIFACT PUBLICATION VERIFIED\n  reference: %s\n  policy: pipeline-attested/artifact-publication\n' "$REFERENCE"
if [ "$EXACT_INPUTS" -eq 3 ]; then
  printf '  exact inputs: Definition, Environment, and Plan receipt emitted\n'
fi
