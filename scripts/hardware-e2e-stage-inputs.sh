#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Stage what one hardware E2E leg's node runs, from the NVKR-built artifacts the
# substituted plan names (ADR-18). It replaces the NVKE harness steps
# `controller-image` and `site-inputs`, which pull by tag from the registry NVKE publishes to.
#
#   hardware-e2e-stage-inputs.sh --arch ARCH --harness DIR --substitutions FILE \
#       --controller-version V --capi-version V --cli-version V \
#       --inputs DIR --staged FILE [--image-prefix localhost/NAME] [--step-env FILE]
#
# What it does, in order, before any machine exists:
#   - pulls the controller and capi-provisioned images by the digest the plan names and refuses
#     an image that does not report that digest or the leg's architecture;
#   - retags them under a localhost/ name and saves them as the archives the suite loads onto
#     the node. No registry answers to a localhost/ name, so a node that tried to pull instead of
#     using the archive would fail loudly rather than run NVKE's own image of the same tag;
#   - pulls nvkectl by its digest reference through the harness's hack/fetch-nvkectl.sh, which
#     also checks the version the binary reports when the host can run it;
#   - builds the site profiles fixture from the harness checkout. That fixture is NVKE's, not
#     NVKR's, and the staged record says so.
#
# DIR for --inputs and the --staged FILE must not exist. --staged is written last, only on success;
# a failure removes --inputs, so nothing a later suite run could mistake for staged inputs remains.
# --step-env receives the two harness variables the replaced steps would have emitted
# (SITE_IMAGES and SITE_NVKE_CONTROLLER_REPOSITORY) in the harness's NAME=value form.
#
# The controller version check compares the image's org.opencontainers.image.version label with
# --controller-version. An image with no such label is recorded as "not-determined", which says the
# check could not be made, not that it passed. The version the running controller reports is
# compared by the suite itself later.
#
# Exit codes: 0 ok, 2 usage, 13 unsupported input, 20 a pull failed, 21 the profiles build failed,
# 22 an image is not what the plan names, 29 missing tool, 41 the plan lacks an artifact,
# 47 an output exists, 49 an artifact is not pinned by digest.
set -euo pipefail
umask 077

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
usage() {
  fail NVKR-E-USAGE 'hardware-e2e-stage-inputs.sh --arch ARCH --harness DIR --substitutions FILE --controller-version V --capi-version V --cli-version V --inputs DIR --staged FILE [--image-prefix localhost/NAME] [--step-env FILE]' 2
}

readonly VERSION_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
readonly PINNED_RE='^oci://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64}$'
readonly PREFIX_RE='^localhost/[a-z0-9]([a-z0-9._/-]*[a-z0-9])?$'
readonly PULL_TIMEOUT=30m SAVE_TIMEOUT=15m BUILD_TIMEOUT=30m INSPECT_TIMEOUT=2m

arch='' harness='' subs='' controller_version='' capi_version='' cli_version='' inputs='' staged_out=''
prefix=localhost/nvke step_env=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail NVKR-E-USAGE "$1 needs a value" 2
  case $1 in
    --arch) arch=$2 ;;
    --harness) harness=$2 ;;
    --substitutions) subs=$2 ;;
    --controller-version) controller_version=$2 ;;
    --capi-version) capi_version=$2 ;;
    --cli-version) cli_version=$2 ;;
    --inputs) inputs=$2 ;;
    --staged) staged_out=$2 ;;
    --image-prefix) prefix=$2 ;;
    --step-env) step_env=$2 ;;
    *) fail NVKR-E-USAGE "unknown argument: $1" 2 ;;
  esac
  shift 2
done
[[ -n $arch && -n $harness && -n $subs && -n $controller_version && -n $capi_version && -n $cli_version && -n $inputs && -n $staged_out ]] || usage

# Admit everything that can be refused before any pull, build or write.
[[ $arch == amd64 || $arch == arm64 ]] || fail NVKR-E-UNSUPPORTED "unsupported architecture: $arch" 13
for version in "$controller_version" "$capi_version" "$cli_version"; do
  [[ $version =~ $VERSION_RE ]] || fail NVKR-E-UNSUPPORTED "not a release version: $version" 13
done
[[ $prefix =~ $PREFIX_RE ]] ||
  fail NVKR-E-UNSUPPORTED "the image prefix must be a localhost/ name, so that the node cannot pull what it should load from the archive: $prefix" 13
for tool in jq docker oras timeout; do
  command -v "$tool" >/dev/null || fail NVKR-E-ENGINE-CAPABILITY "missing $tool" 29
done
sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1
  else fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29; fi
}
command -v sha256sum >/dev/null || command -v shasum >/dev/null || fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29

[[ -f $subs && ! -L $subs ]] || fail NVKR-E-USAGE "not a regular file: $subs" 2
fetcher=$harness/controller/test/e2e/hack/fetch-nvkectl.sh
fixtures=$harness/controller/test/fixtures
[[ -d $harness && ! -L $harness && -f $fetcher && ! -L $fetcher && -x $fetcher && -d $fixtures ]] ||
  fail NVKR-E-USAGE "--harness is not an NVKE checkout with hack/fetch-nvkectl.sh and the profiles fixture: $harness" 2
[[ ! -e $inputs && ! -L $inputs ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $inputs" 47
[[ ! -e $staged_out && ! -L $staged_out ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $staged_out" 47
[[ -z $step_env || -f $step_env ]] || fail NVKR-E-USAGE "--step-env is not a file: $step_env" 2

jq -e '.schema == "nvkr.dev/hardware-e2e-substitutions/v1"' "$subs" >/dev/null ||
  fail NVKR-E-UNSUPPORTED 'substitution record has an unsupported schema' 13
artifact_ref() { # artifact_ref SLOT: the reference the plan carries for it, pinned by digest
  local slot=$1 ref
  ref=$(jq -r --arg slot "$slot" '[.replaced[] | select(.slot == $slot) | .to] | if length == 1 then .[0] else "" end' "$subs")
  [[ -n $ref ]] || fail NVKR-E-RELEASE-INCOMPLETE "the substitution record carries no single $slot artifact" 41
  [[ $ref =~ $PINNED_RE ]] || fail NVKR-E-UNPINNED-INPUT "$slot is not an oci:// reference pinned by digest: $ref" 49
  printf '%s' "$ref"
}
controller_ref=$(artifact_ref controller)
capi_ref=$(artifact_ref capi-provisioned)
cli_ref=$(artifact_ref "cli-$arch")

mkdir -m 700 "$inputs"
cleanup() {
  local status=$?
  [[ $status -eq 0 ]] || rm -rf -- "$inputs"
}
trap cleanup EXIT

stage_image() { # stage_image SLOT REFERENCE NAME VERSION ARCHIVE
  local slot=$1 pinned=${2#oci://} name=$3 version=$4 archive=$5 digests platform tagged
  timeout --kill-after=30s "$PULL_TIMEOUT" docker pull --quiet --platform "linux/$arch" "$pinned" >/dev/null ||
    fail NVKR-E-FETCH-FAILED "could not pull $slot $pinned" 20
  digests=$(timeout "$INSPECT_TIMEOUT" docker image inspect --format '{{json .RepoDigests}}' "$pinned") ||
    fail NVKR-E-VERIFY-FAILED "could not inspect $slot $pinned" 22
  jq -e --arg want "$pinned" 'type == "array" and any(.[]; . == $want)' <<<"$digests" >/dev/null ||
    fail NVKR-E-VERIFY-FAILED "$slot was pulled as $digests, which does not include the pinned $pinned" 22
  platform=$(timeout "$INSPECT_TIMEOUT" docker image inspect --format '{{.Os}}/{{.Architecture}}' "$pinned") ||
    fail NVKR-E-VERIFY-FAILED "could not inspect the platform of $slot $pinned" 22
  [[ $platform == "linux/$arch" ]] || fail NVKR-E-VERIFY-FAILED "$slot is $platform, not linux/$arch" 22
  tagged=$prefix/$name:$version
  timeout "$INSPECT_TIMEOUT" docker tag "$pinned" "$tagged" || fail NVKR-E-VERIFY-FAILED "could not tag $slot as $tagged" 22
  timeout --kill-after=30s "$SAVE_TIMEOUT" docker save --platform "linux/$arch" -o "$archive" "$tagged" ||
    fail NVKR-E-FETCH-FAILED "could not save $tagged" 20
  [[ -s $archive ]] || fail NVKR-E-VERIFY-FAILED "saving $tagged wrote nothing to $archive" 22
}

stage_image controller "$controller_ref" nvke-controller "$controller_version" "$inputs/controller.tar"
stage_image capi-provisioned "$capi_ref" cluster-api-provider-provisioned "$capi_version" "$inputs/capi-provisioned.tar"

# The controller is a server the suite deploys and then asks for its version, so a label that
# disagrees with the plan is worth refusing now, forty minutes sooner than the suite would.
label=$(timeout "$INSPECT_TIMEOUT" docker image inspect \
  --format '{{if .Config.Labels}}{{index .Config.Labels "org.opencontainers.image.version"}}{{end}}' "${controller_ref#oci://}") ||
  fail NVKR-E-VERIFY-FAILED 'could not read the controller image labels' 22
case $label in
  '') controller_check=not-determined ;;
  "$controller_version" | "${controller_version#v}") controller_check=matched ;;
  *) fail NVKR-E-VERIFY-FAILED "the controller image reports version $label, but the plan names $controller_version" 22 ;;
esac

# The fixture the profiles archive holds is NVKE's: nothing NVKR builds stands behind it.
timeout --kill-after=30s "$BUILD_TIMEOUT" docker buildx build --platform "linux/$arch" --load -t nke-e2e-profiles:local "$fixtures" >&2 ||
  fail NVKR-E-BUILD-FAILED "could not build the site profiles fixture from $fixtures" 21
timeout --kill-after=30s "$SAVE_TIMEOUT" docker save --platform "linux/$arch" -o "$inputs/profiles.tar" nke-e2e-profiles:local ||
  fail NVKR-E-FETCH-FAILED 'could not save the site profiles image' 20
[[ -s $inputs/profiles.tar ]] || fail NVKR-E-VERIFY-FAILED 'saving the site profiles image wrote nothing' 22

# The harness's own fetch, handed the digest reference: unlike a tag, that cannot be republished.
SITE_CLI_REF=${cli_ref#oci://} timeout --kill-after=30s "$PULL_TIMEOUT" "$fetcher" "$cli_version" "$arch" "$inputs/nvkectl" >&2 ||
  fail NVKR-E-FETCH-FAILED "could not stage nvkectl from $cli_ref" 20
nvkectl=$inputs/nvkectl/nvkectl-linux-$arch
[[ -f $nvkectl && ! -L $nvkectl ]] || fail NVKR-E-VERIFY-FAILED "staging nvkectl left no regular file at $nvkectl" 22

record=$(jq -n -S --indent 2 --arg arch "$arch" --arg check "$controller_check" \
  --arg controller "$controller_ref" --arg capi "$capi_ref" --arg cli "$cli_ref" --arg prefix "$prefix" \
  --arg controller_sha "$(sha256_of "$inputs/controller.tar")" --arg capi_sha "$(sha256_of "$inputs/capi-provisioned.tar")" \
  --arg profiles_sha "$(sha256_of "$inputs/profiles.tar")" --arg nvkectl_sha "$(sha256_of "$nvkectl")" '
  {
    schema: "nvkr.dev/hardware-e2e-staged/v1",
    arch: $arch,
    source: "nvkr-outputs",
    builtFromCommit: null,
    controllerVersionCheck: $check,
    files: {"controller.tar": $controller_sha, "capi-provisioned.tar": $capi_sha, "profiles.tar": $profiles_sha,
            ("nvkectl-linux-" + $arch): $nvkectl_sha},
    imagePrefix: $prefix,
    pulledByDigest: {controller: $controller, "capi-provisioned": $capi, ("cli-" + $arch): $cli},
    notNvkrBuilt: ["deploy-path", "imager-efi", "site-profiles"]
  }')

if [[ -n $step_env ]]; then
  printf 'SITE_IMAGES=%s\nSITE_NVKE_CONTROLLER_REPOSITORY=nvke-controller\n' "$prefix" >>"$step_env"
fi
(set -o noclobber; printf '%s\n' "$record" >"$staged_out") || fail NVKR-E-IMMUTABLE-VIOLATION "could not create $staged_out" 47
printf 'nvkr: staged %s inputs in %s (controller version check: %s)\n' "$arch" "$inputs" "$controller_check"
