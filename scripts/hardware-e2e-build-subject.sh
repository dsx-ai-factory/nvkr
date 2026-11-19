#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build what one hardware E2E leg's node runs from the subject commit itself
# (ADR-18), for artifact source "build-subject". It replaces the NVKE harness
# steps `controller-image` and `site-inputs`, and it is the other half of
# hardware-e2e-stage-inputs.sh, which stages NVKR-built artifacts instead.
#
#   hardware-e2e-build-subject.sh --arch ARCH --harness DIR --plan FILE \
#       --controller-version V --capi-version V --inputs DIR --staged FILE \
#       [--image-prefix localhost/NAME] [--step-env FILE]
#
# Nothing this script produces is NVKR-built. It runs NVKE's own `make site-inputs`, which builds
# the controller, capi-provisioned and nvkectl from the checkout in DIR, so the run shows that the
# workflow can drive NVKE's suite at the subject commit on hardware and says nothing about artifacts NVKR
# builds. The staged record says so, and the run summary repeats it.
#
# The subject commit is read from the plan, not from a caller's word for it, and the checkout must be
# exactly that commit with no uncommitted change to a tracked file, before and after the build. NVKE's
# build bakes the version it is given into the binaries it builds, so a build from any other tree
# would carry the plan's version name on other sources.
#
# The images are named under a localhost/ prefix, so the node can only use the archives the suite
# loads onto it. --step-env receives the harness variables the replaced steps would have emitted, in
# the harness's NAME=value form, including the build flags and the nvkectl path the harness itself sets
# when it builds from a checkout (release/hack/qualify-e2e.sh, resolve_nvkectl).
#
# DIR for --inputs and the --staged FILE must not exist. --staged is written last, only on success;
# a failure removes --inputs.
#
# Exit codes: 0 ok, 2 usage, 13 unsupported input, 21 the build failed, 22 the checkout is not the
# subject commit or the build did not produce what it should, 29 missing tool, 47 an output exists.
set -euo pipefail
umask 077

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
usage() {
  fail NVKR-E-USAGE 'hardware-e2e-build-subject.sh --arch ARCH --harness DIR --plan FILE --controller-version V --capi-version V --inputs DIR --staged FILE [--image-prefix localhost/NAME] [--step-env FILE]' 2
}

readonly VERSION_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
readonly PREFIX_RE='^localhost/[a-z0-9]([a-z0-9._/-]*[a-z0-9])?$'
readonly BUILD_TIMEOUT=120m

arch='' harness='' plan='' controller_version='' capi_version='' inputs='' staged_out=''
prefix=localhost/nvke step_env=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail NVKR-E-USAGE "$1 needs a value" 2
  case $1 in
    --arch) arch=$2 ;;
    --harness) harness=$2 ;;
    --plan) plan=$2 ;;
    --controller-version) controller_version=$2 ;;
    --capi-version) capi_version=$2 ;;
    --inputs) inputs=$2 ;;
    --staged) staged_out=$2 ;;
    --image-prefix) prefix=$2 ;;
    --step-env) step_env=$2 ;;
    *) fail NVKR-E-USAGE "unknown argument: $1" 2 ;;
  esac
  shift 2
done
[[ -n $arch && -n $harness && -n $plan && -n $controller_version && -n $capi_version && -n $inputs && -n $staged_out ]] || usage

# Admit everything that can be refused before any build or write.
[[ $arch == amd64 || $arch == arm64 ]] || fail NVKR-E-UNSUPPORTED "unsupported architecture: $arch" 13
for version in "$controller_version" "$capi_version"; do
  [[ $version =~ $VERSION_RE ]] || fail NVKR-E-UNSUPPORTED "not a release version: $version" 13
done
[[ $prefix =~ $PREFIX_RE ]] ||
  fail NVKR-E-UNSUPPORTED "the image prefix must be a localhost/ name, so that the node cannot pull what it should load from the archive: $prefix" 13
for tool in jq docker git make timeout; do
  command -v "$tool" >/dev/null || fail NVKR-E-ENGINE-CAPABILITY "missing $tool" 29
done
sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1
  else fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29; fi
}
command -v sha256sum >/dev/null || command -v shasum >/dev/null || fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29

[[ -f $plan && ! -L $plan ]] || fail NVKR-E-USAGE "not a regular file: $plan" 2
makefile=$harness/controller/test/e2e/Makefile
[[ -d $harness && ! -L $harness && -f $makefile && ! -L $makefile ]] ||
  fail NVKR-E-USAGE "--harness is not an NVKE checkout with controller/test/e2e/Makefile: $harness" 2
[[ ! -e $inputs && ! -L $inputs ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $inputs" 47
[[ ! -e $staged_out && ! -L $staged_out ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $staged_out" 47
[[ -z $step_env || -f $step_env ]] || fail NVKR-E-USAGE "--step-env is not a file: $step_env" 2

commit=$(jq -er '.commit | select(type == "string" and test("^[0-9a-f]{40}$"))' "$plan") ||
  fail NVKR-E-UNSUPPORTED 'the plan names no 40-hex subject commit' 13

# The checkout is the subject commit with no tracked change, or nothing here may be built from it.
checkout_is_subject() { # checkout_is_subject WHEN
  local head dirty
  head=$(timeout 2m git -C "$harness" rev-parse HEAD) ||
    fail NVKR-E-VERIFY-FAILED "$1: could not read the checkout's HEAD in $harness" 22
  [[ $head == "$commit" ]] ||
    fail NVKR-E-VERIFY-FAILED "$1: the checkout is at $head, but the plan's subject commit is $commit; a build here would carry the plan's version on other sources" 22
  dirty=$(timeout 2m git -C "$harness" status --porcelain --untracked-files=no) ||
    fail NVKR-E-VERIFY-FAILED "$1: could not read the checkout's status in $harness" 22
  [[ -z $dirty ]] || fail NVKR-E-VERIFY-FAILED "$1: the checkout has uncommitted changes to tracked files, so what is built is not the subject commit" 22
}
checkout_is_subject 'before the build'

mkdir -m 700 "$inputs"
cleanup() {
  local status=$?
  [[ $status -eq 0 ]] || rm -rf -- "$inputs"
}
trap cleanup EXIT

short=${commit:0:12}
timeout --kill-after=60s "$BUILD_TIMEOUT" make -C "$harness/controller/test/e2e" site-inputs \
  SITE_ARCH="$arch" SITE_INPUTS="$inputs" SITE_IMAGE_REGISTRY="$prefix" \
  SITE_NVKE_CONTROLLER_VERSION="$controller_version" SITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller \
  SITE_NVKE_CONTROLLER_COMMIT="$short" SITE_NVKE_CONTROLLER_BUILD=1 \
  SITE_PROVISIONED_VERSION="$capi_version" SITE_PROVISIONED_BUILD=1 \
  SITE_CLI_BUILD=1 >&2 ||
  fail NVKR-E-BUILD-FAILED "could not build the site inputs from $commit" 21

checkout_is_subject 'after the build'

for archive in controller.tar capi-provisioned.tar profiles.tar; do
  [[ -f $inputs/$archive && ! -L $inputs/$archive && -s $inputs/$archive ]] ||
    fail NVKR-E-VERIFY-FAILED "the build left no $archive in $inputs" 22
done
nvkectl=$harness/cli/dist/nvkectl-linux-$arch
[[ -f $nvkectl && ! -L $nvkectl && -s $nvkectl ]] || fail NVKR-E-VERIFY-FAILED "the build left no nvkectl at $nvkectl" 22

record=$(jq -n -S --indent 2 --arg arch "$arch" --arg commit "$commit" --arg prefix "$prefix" \
  --arg controller_sha "$(sha256_of "$inputs/controller.tar")" --arg capi_sha "$(sha256_of "$inputs/capi-provisioned.tar")" \
  --arg profiles_sha "$(sha256_of "$inputs/profiles.tar")" --arg nvkectl_sha "$(sha256_of "$nvkectl")" '
  {
    schema: "nvkr.dev/hardware-e2e-staged/v1",
    arch: $arch,
    source: "build-subject",
    builtFromCommit: $commit,
    controllerVersionCheck: "not-determined",
    files: {"controller.tar": $controller_sha, "capi-provisioned.tar": $capi_sha, "profiles.tar": $profiles_sha,
            ("nvkectl-linux-" + $arch): $nvkectl_sha},
    imagePrefix: $prefix,
    notNvkrBuilt: ["capi-provisioned", "cli", "controller", "deploy-path", "imager-efi", "node-image", "site-profiles"]
  }')

if [[ -n $step_env ]]; then
  # What resolve_nvkectl emits when the harness builds nvkectl from its own checkout.
  printf 'SITE_IMAGES=%s\nSITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller\nSITE_NVKE_CONTROLLER_BUILD=1\nSITE_PROVISIONED_BUILD=1\nSITE_CLI_BUILD=1\nCLI_VERSION=%s\nSITE_NVKECTL=%s\n' \
    "$prefix" "$short" "$nvkectl" >>"$step_env"
fi
(set -o noclobber; printf '%s\n' "$record" >"$staged_out") || fail NVKR-E-IMMUTABLE-VIOLATION "could not create $staged_out" 47
printf 'nvkr: built %s inputs from %s in %s (not NVKR-built)\n' "$arch" "$commit" "$inputs"
