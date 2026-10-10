#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Local combined NVKE walkthrough. The historical M1 name predates the addition
# of CWE and Helm: the current Definition builds and publishes all three outputs.
# The CLI build is offline; CWE wraps the upstream network-enabled Dockerfile.
# This script inspects the CLI SBOM and dry-run signing records, pulls its
# publication by digest, and runs the recovered binary. It does not independently
# smoke-test the image or render the charts.
#
# Plans are static-unqualified; executed Runs are unqualified-local-execution.
# Dry-run signing executes no cosign and produces no authenticated signature.
# M1 acceptance requires the separate hosted exercise and its review requirements.

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

ATTEMPTS_ROOT=${ATTEMPTS_ROOT:-.nvkr/m1-local}
ATTEMPT=${ATTEMPT:-$(demo_default_attempt m1)}
WORKSPACE=$ATTEMPTS_ROOT/$ATTEMPT
CONSUME=${CONSUME:-.nvkr/m1-local-consume}
DEFINITIONS=${DEFINITIONS:-definitions/nvke-v1}
ROUTE=${ROUTE:-release}
BUNDLE=${BUNDLE:-nvkectl}
BUILD_TARGET=${BUILD_TARGET:-nvkectl}
JOBS=${JOBS:-1}
REGISTRY_CONTAINER=${REGISTRY_CONTAINER:-nvkr-m1-registry}
REGISTRY_IMAGE=${REGISTRY_IMAGE:-registry:3}
REGISTRY_HOSTPORT=${REGISTRY_HOSTPORT:-127.0.0.1:5000}
RUNTIME=${RUNTIME:-docker}
KEEP_REGISTRY=1

# The same pinned toolchain image the catalog builds nvkectl with, reused here as the
# amd64 execution environment for the recovered binary. Reusing it keeps this script from
# introducing another execution image. The loopback registry service uses registry:3.
RUN_IMAGE=golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6
# The version string upstream's own cli/Makefile stamps for tag cli/v0.1.0-rc.2.
EXPECT_VERSION=${EXPECT_VERSION:-v0.1.0-rc.2}

usage() {
  cat <<'EOF'
Usage: scripts/demo-m1-local.sh [options]

Run the combined NVKE local walkthrough (M1 requires the CLI slice only):
  pinned NVKE sources -> CLI + CWE image + Helm bundle -> per-artifact SBOMs
    -> one OCI release index -> loopback registry -> dry-run signing records
    -> pull the release by digest -> check SBOM digest -> run recovered nvkectl

The CLI build is offline; CWE uses network-enabled rootless BuildKit. All three
outputs must complete, but pull-back and version checks cover the CLI only.
Planning is static-unqualified; executed Runs use unqualified-local-execution.
Nothing is signed, and this walkthrough does not complete M1 acceptance.

Options:
  --attempts-root DIR
                    root for immutable Run attempts (default: .nvkr/m1-local)
  --attempt NAME    new immutable attempt name (default: timestamp plus process ID)
  --consume DIR     scratch directory for the pull/extract half of the demo
                    (default: .nvkr/m1-local-consume)
  --stop-registry   remove the demo registry container after successful completion
  -h, --help        show this help

Environment:
  NVKE_LOCAL_CLONE   path to a local NVKE clone containing the exact pinned commits. When set, git is
                    redirected to it for this process only, via GIT_CONFIG_COUNT.
                    The catalog keeps its https://github.com/dsx-ai-factory/nvke identity and
                    the user's own git configuration is never modified.

Requires docker with rootless BuildKit/overlayfs support, git, curl, a Go toolchain,
and network access for tools, images and CWE build dependencies. Re-running is safe: the registry
container is reused; every execution gets a new immutable attempt, and the consume directory is rebuilt.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --attempts-root) [ "$#" -ge 2 ] || demo_die "missing --attempts-root value"; ATTEMPTS_ROOT=$2; shift 2 ;;
    --attempt) [ "$#" -ge 2 ] || demo_die "missing --attempt value"; ATTEMPT=$2; shift 2 ;;
    --consume) [ "$#" -ge 2 ] || demo_die "missing --consume value"; CONSUME=$2; shift 2 ;;
    --stop-registry) KEEP_REGISTRY=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) demo_die "unknown flag: $1 (run --help)" ;;
  esac
done

case "$ATTEMPTS_ROOT" in .nvkr/?*) ;; *) demo_die "--attempts-root must be a repository-relative child of .nvkr" ;; esac
case "$ATTEMPT" in ""|*/*|*..*) demo_die "--attempt must be one clean name" ;; esac
WORKSPACE=$ATTEMPTS_ROOT/$ATTEMPT
case "$CONSUME" in .nvkr/?*) ;; *) demo_die "--consume must be a repository-relative child of .nvkr" ;; esac
[ "$WORKSPACE" != "$CONSUME" ] || demo_die "the Run attempt and --consume must differ"

demo_need_command docker
demo_need_command git
demo_need_command curl
docker info >/dev/null 2>&1 || demo_die "Docker daemon is not reachable"

# Pretty-print a small canonical JSON record. python3 is a convenience for reading, never
# a correctness dependency: without it the raw canonical bytes are shown instead.
demo_show_json() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -m json.tool "$1"
  else
    cat "$1"
    printf '\n'
  fi
}

demo_section "0/8  Build nvkr from this checkout"
demo_prepare_nvkr
printf 'nvkr: %s\n' "$NVKR"

# The catalog names the real upstream repository, because that is the component's
# identity. Redirecting the TRANSPORT for a local run is environment configuration, so it
# is expressed as git configuration scoped to this process — not as a catalog edit and
# not as a mutation of the user's ~/.gitconfig.
if [ -n "${NVKE_LOCAL_CLONE:-}" ]; then
  [ -d "$NVKE_LOCAL_CLONE" ] || demo_die "NVKE_LOCAL_CLONE is not a directory: $NVKE_LOCAL_CLONE"
  NVKE_LOCAL_CLONE=$(demo_absolute_path "$NVKE_LOCAL_CLONE")
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.${NVKE_LOCAL_CLONE}.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/dsx-ai-factory/nvke"
  printf 'git transport redirected for this process only: %s -> %s\n' \
    "https://github.com/dsx-ai-factory/nvke" "$NVKE_LOCAL_CLONE"
fi

demo_section "1/8  Start or reuse the loopback OCI registry"
registry_owner_label=dev.nvkr.demo.owner
registry_owner=m1-local
if docker container inspect "$REGISTRY_CONTAINER" >/dev/null 2>&1; then
  owner=$(docker container inspect --format "{{index .Config.Labels \"$registry_owner_label\"}}" "$REGISTRY_CONTAINER")
  [ "$owner" = "$registry_owner" ] ||
    demo_die "container $REGISTRY_CONTAINER exists but is not owned by this demo (owner=${owner:-none})"
  if [ "$(docker container inspect --format '{{.State.Running}}' "$REGISTRY_CONTAINER")" = true ]; then
    printf 'reusing running registry container %s on %s\n' "$REGISTRY_CONTAINER" "$REGISTRY_HOSTPORT"
  else
    printf 'restarting existing registry container %s\n' "$REGISTRY_CONTAINER"
    docker start "$REGISTRY_CONTAINER" >/dev/null
  fi
else
  printf 'creating registry container %s (%s) on %s\n' "$REGISTRY_CONTAINER" "$REGISTRY_IMAGE" "$REGISTRY_HOSTPORT"
  docker run -d --name "$REGISTRY_CONTAINER" --label "$registry_owner_label=$registry_owner" \
    -p "$REGISTRY_HOSTPORT:5000" "$REGISTRY_IMAGE" >/dev/null
fi

registry_ready=0
for _ in $(seq 1 30); do
  if curl --fail --silent --show-error "http://$REGISTRY_HOSTPORT/v2/" >/dev/null 2>&1; then
    registry_ready=1
    break
  fi
  sleep 1
done
[ "$registry_ready" -eq 1 ] || demo_die "registry $REGISTRY_HOSTPORT did not become ready"
printf 'registry ready: http://%s/v2/\n' "$REGISTRY_HOSTPORT"

demo_section "2/8  Validate the pinned component catalog"
"$NVKR" catalog validate --data "$DEFINITIONS/catalog"

demo_section "3/8  Validate the Definition and generate its hosted Run in scratch space"
"$NVKR" definition validate --definition "$DEFINITIONS/definition.yaml"
"$NVKR" run plan --definition "$DEFINITIONS/definition.yaml" \
  --environment "$DEFINITIONS/environment-local.yaml"
# The committed workflow selects M2, while this demo selects the three-output v1
# Definition. Generate and check that example with the same binary in a fresh root.
HOSTED_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-m1-hosted.XXXXXX")
"$NVKR" run generate --definition "$DEFINITIONS/definition.yaml" \
  --environment "$DEFINITIONS/environment-github.yaml" --engine github --root "$HOSTED_ROOT"
"$NVKR" run generate --definition "$DEFINITIONS/definition.yaml" \
  --environment "$DEFINITIONS/environment-github.yaml" --engine github --root "$HOSTED_ROOT" --check
printf 'scratch hosted bundle: %s\n' "$HOSTED_ROOT"

demo_section "4/8  Build and publish all three outputs in one immutable Run"
run_started=$SECONDS
"$NVKR" run local --definition "$DEFINITIONS/definition.yaml" \
  --environment "$DEFINITIONS/environment-local.yaml" --attempts-root "$ATTEMPTS_ROOT" --attempt "$ATTEMPT" \
  --runtime "$RUNTIME" --jobs "$JOBS" --publish
run_seconds=$((SECONDS - run_started))

reference_file="$WORKSPACE/publications/$ROUTE.txt"
[ -f "$reference_file" ] || demo_die "Run completed without a published reference: $reference_file"
REFERENCE=$(tr -d '\n' < "$reference_file")
case "$REFERENCE" in
  *@sha256:*) ;;
  *) demo_die "published reference is not digest-pinned: $REFERENCE" ;;
esac
DIGEST=${REFERENCE#*@}
printf '\npublished reference: %s\n' "$REFERENCE"

demo_section "5/8  Inspect the CLI SBOM and its observed subject"
# The Run's SBOM node ran during
# step 4 and wrote a deterministic CycloneDX SBOM per built component plus an aggregate
# index, each bound to the component's runner-observed artifact digest. This is
# unqualified composition evidence — it asserts what went in, never a signature.
sbom_dir="$WORKSPACE/$BUILD_TARGET/sbom"
sbom_index="$sbom_dir/index.json"
[ -f "$sbom_index" ] || demo_die "Run did not emit an SBOM index: $sbom_index"
# Fail closed rather than narrate: an index that binds no exact subject digest would make
# the SBOM's central claim — "this bill of materials is for THAT artifact" — uncheckable.
grep -q '"artifactSha256":"sha256:' "$sbom_index" || demo_die "SBOM index binds no exact artifact digest"
printf 'SBOM index: %s\n' "$sbom_index"
demo_show_json "$sbom_index"
sbom_count=0
for cdx in "$sbom_dir"/*.cdx.json; do
  [ -f "$cdx" ] || continue
  printf '\nper-artifact SBOM: %s\n' "$cdx"
  demo_show_json "$cdx"
  sbom_count=$((sbom_count + 1))
done
[ "$sbom_count" -ge 1 ] || demo_die "SBOM index exists but no per-artifact CycloneDX document was written"

demo_section "6/8  Show the dry-run signing and verification records"
signature_record="$WORKSPACE/signatures/$ROUTE.json"
predicate_record="$WORKSPACE/predicates/$ROUTE.json"
verification_record="$WORKSPACE/verifications/$ROUTE.json"
for record in "$signature_record" "$predicate_record" "$verification_record"; do
  [ -f "$record" ] || demo_die "signing lane did not produce $record"
done

# Fail closed rather than narrate: a record that does not name the digest that was
# actually published would make every downstream claim meaningless.
grep -q "$DIGEST" "$signature_record" || demo_die "signing record does not name the published digest"
grep -q "$DIGEST" "$verification_record" || demo_die "verification record does not name the published digest"
grep -q '"dryRun":true' "$signature_record" || demo_die "signing record is not marked dry-run"
grep -q '"dryRun":true' "$verification_record" || demo_die "verification record is not marked dry-run"

# The record's predicateSha256 must hash the predicate file EXACTLY as it sits on disk —
# those are the bytes a real `cosign attest --predicate <file>` would attest. Checking it
# here is the only thing that makes the record's claim about the predicate falsifiable.
PREDICATE_SHA256="sha256:$(demo_sha256 "$predicate_record")"
grep -q "\"predicateSha256\":\"$PREDICATE_SHA256\"" "$signature_record" ||
  demo_die "signing record's predicateSha256 does not hash the predicate file on disk"

printf 'signing record:      %s\n' "$signature_record"
demo_show_json "$signature_record"
printf '\nSLSA v1 predicate:   %s\n' "$predicate_record"
demo_show_json "$predicate_record"
printf '\nverification record: %s\n' "$verification_record"
demo_show_json "$verification_record"

demo_section "7/8  Pull the publication back BY DIGEST and extract the binary bundle"
CONSUME_ABS=$(demo_absolute_path "$CONSUME")
demo_prepare_work "$CONSUME_ABS" "m1-local-consume"
pull_started=$SECONDS
"$NVKR" publish pull --ref "$REFERENCE" --out "$CONSUME_ABS/root" --plain-http --anonymous
pull_seconds=$((SECONDS - pull_started))
"$NVKR" publish inspect --layout "$CONSUME_ABS/root"
"$NVKR" publish extract --layout "$CONSUME_ABS/root" --bundle "$BUNDLE" --out "$CONSUME_ABS/extract"

NVKECTL="$CONSUME_ABS/extract/nvkectl"
[ -f "$NVKECTL" ] || demo_die "the extracted bundle does not contain nvkectl: $NVKECTL"
NVKECTL_SHA256=$(demo_sha256 "$NVKECTL")

# Close the loop on the SBOM from step 5: the digest it billed for the built binary must
# equal the sha256 of the binary we just recovered by digest from the registry. If they
# disagree, the SBOM describes something other than what shipped, so fail closed.
grep -q "$NVKECTL_SHA256" "$sbom_dir"/*.cdx.json ||
  demo_die "no per-artifact SBOM bills the recovered binary digest sha256:$NVKECTL_SHA256"

demo_section "8/8  Run the recovered nvkectl under the pinned amd64 image"
# The definition targets linux/amd64; on an arm64 host this runs under emulation.
VERSION_OUTPUT=$(docker run --rm --platform linux/amd64 -v "$CONSUME_ABS/extract:/w" \
  "$RUN_IMAGE" /w/nvkectl version 2>&1)
printf '%s\n' "$VERSION_OUTPUT"
case "$VERSION_OUTPUT" in
  *"$EXPECT_VERSION"*) ;;
  *) demo_die "recovered nvkectl reported '$VERSION_OUTPUT', which does not contain $EXPECT_VERSION" ;;
esac

if [ "$KEEP_REGISTRY" -eq 0 ]; then
  docker rm -f "$REGISTRY_CONTAINER" >/dev/null
  printf 'removed registry container %s\n' "$REGISTRY_CONTAINER"
fi

cat <<EOF

LOCAL NVKE WALKTHROUGH COMPLETE — UNQUALIFIED-LOCAL-EXECUTION, NO SIGNATURES
  definition:          $DEFINITIONS/definition.yaml
  produced outputs:    nvkectl, cwe, nvke-helm (default Definition)
  pull-back check:     nvkectl only
  registry:            http://$REGISTRY_HOSTPORT/v2/ (container $REGISTRY_CONTAINER)
  published reference: $REFERENCE
  index digest:        $DIGEST
  nvkectl sha256:       $NVKECTL_SHA256
  nvkectl version:      $VERSION_OUTPUT
  run record:          $WORKSPACE/lifecycle.json
  hosted bundle:       $HOSTED_ROOT/.github/nvkr-run/manifest.json
  SBOM index:          $sbom_index ($sbom_count CLI CycloneDX doc(s), unqualified composition evidence)
  signing record:      $signature_record
  SLSA v1 predicate:   $predicate_record
  predicate sha256:    $PREDICATE_SHA256
  verification record: $verification_record
  extracted binary:    $NVKECTL
  timings:             Run ${run_seconds}s, pull ${pull_seconds}s

Nothing was signed: dry-run mode executed no cosign and used no signing identity.
The local Run records observations and dry-run signing/predicate records, not
signature authority. Selecting cosign mode alone would not prove hosted execution,
verification, or milestone completion. M1 acceptance also requires the hosted and
independent-consumer evidence plus the review and governance requirements in the
acceptance specification. This local run does not establish them.
EOF
