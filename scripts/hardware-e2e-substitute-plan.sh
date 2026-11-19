#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Substitute NVKR-built references into the committed NVKE plan fixture for the
# hardware E2E workflow (ADR-18). The plan and the outputs manifest are
# descriptions: this script authenticates only the fixture's blob integrity against
# its provenance record and refuses any reference that is not content-addressed
# (OCI) or not plain HTTPS (node image). It issues no authority and the result
# carries qualificationAuthority "none" through the lane.
#
#   hardware-e2e-substitute-plan.sh [--allow-node-image-version-skew] PLAN PROVENANCE OUTPUTS OUT
#   hardware-e2e-substitute-plan.sh --build-subject PLAN PROVENANCE OUT
#
# OUT is created fresh and receives plan.json and substitutions.json.
#
# The first form is artifact source "nvkr-outputs": the plan names NVKR-built artifacts.
# --build-subject is artifact source "build-subject": the plan is kept byte for byte, because
# the job builds the controller, capi-provisioned and nvkectl from the subject commit itself
# (hardware-e2e-build-subject.sh). Nothing in that run is NVKR-built, and the substitution record
# says so: replaced is empty, producerRunId is null, and everything the plan names is retained
# from NVKE. The fixture checks below apply to both forms.
#
# Each node image in the outputs manifest names its source: where it came from and the NVKE
# version it was built at. A node image built at a different version than the plan's subject
# is refused unless --allow-node-image-version-skew is given; the substitution record then says
# the combination is skewed, and the run summary repeats that. The flag permits a combination
# nobody has run before. It does not make it a tested one.
#
# Each node image also names how its bytes are checked before a machine is taken, as a
# verification: {method: http-get} has the lane fetch the URL from a hosted runner, and
# {method: s3-readback, run, bytes, qcow2Version, virtualSizeBytes} cites the publication run that
# read the object back from the object store. The second exists because the HTTPS front end can sit
# behind an address allowlist that a hosted runner is not on. This script checks the shape only; it
# does not authenticate the cited run, and the summary says this run did not observe it.
#
# Exit codes: 0 ok, 2 usage, 13 unsupported input, 22 fixture differs from its provenance,
# 29 unsupported node image location, 41 a required slot is missing, 47 output exists,
# 49 unpinned input or a node image with no source or no verification.
set -euo pipefail
umask 077

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
usage() {
  fail NVKR-E-USAGE 'hardware-e2e-substitute-plan.sh {[--allow-node-image-version-skew] PLAN PROVENANCE OUTPUTS OUT | --build-subject PLAN PROVENANCE OUT}' 2
}
allow_skew=false
mode=nvkr-outputs
case ${1:-} in
  --allow-node-image-version-skew) allow_skew=true; shift ;;
  --build-subject) mode=build-subject; shift ;;
esac
outputs=''
if [[ $mode == build-subject ]]; then
  [[ $# == 3 ]] || usage
  plan=$1 provenance=$2 out=$3
else
  [[ $# == 4 ]] || usage
  plan=$1 provenance=$2 outputs=$3 out=$4
fi
command -v jq >/dev/null || fail NVKR-E-ENGINE-CAPABILITY 'missing jq' 29
for input in "$plan" "$provenance" ${outputs:+"$outputs"}; do
  [[ -f $input && ! -L $input ]] || fail NVKR-E-USAGE "input is not a regular file: $input" 2
done

sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1
  else fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29; fi
}

# The NVKR-owned slots and the location strategy the plan must declare for each.
slots='{"controller":"ociArtifact","capi-provisioned":"ociArtifact","cli-amd64":"ociArtifact","cli-arm64":"ociArtifact","os-image-amd64":"osImage","os-image-arm64":"osImage"}'

# 1. Fixture bytes must be the bytes its provenance record names.
jq -e '.schema == "nvkr.dev/hardware-e2e-plan-provenance/v1"' "$provenance" >/dev/null ||
  fail NVKR-E-USAGE 'provenance record has an unsupported schema' 2
want=$(jq -er '.fixture.sha256 | select(test("^[0-9a-f]{64}$"))' "$provenance") ||
  fail NVKR-E-USAGE 'provenance record names no fixture sha256' 2
have=$(sha256_of "$plan")
[[ $have == "$want" ]] ||
  fail NVKR-E-VERIFY-FAILED "plan fixture bytes ($have) differ from its provenance record ($want)" 22

# 2. The plan must still have the shape this lane understands, at the recorded commit.
jq -e --slurpfile prov "$provenance" --argjson slots "$slots" '
  def entries: .combinations[] | ((.components[]), (.dependencies[]), (.streams[].dependencies[]));
  .subject == "nvke"
  and .commit == $prov[0].source.commit
  and .version == $prov[0].source.tag
  and .scope == $prov[0].source.scope
  and (.commit | test("^[0-9a-f]{40}$"))
  and (.combinations | length == 1)
  and (. as $plan | $slots | to_entries | all(
    .key as $name | .value as $strategy |
    [$plan | entries | select(.name == $name)] as $found |
    ($found | length >= 1)
    and ($found | all(.locationStrategy == $strategy and (.url | type == "string")))
    and ([$found[].url] | unique | length == 1)))' "$plan" >/dev/null ||
  fail NVKR-E-UNSUPPORTED 'plan fixture does not match the recorded subject or the slots this lane substitutes' 13

if [[ $mode == build-subject ]]; then
  record=$(jq -n --indent 2 --slurpfile p "$plan" --arg sha "$have" '
    def entries: $p[0].combinations[] | ((.components[]), (.dependencies[]), (.streams[].dependencies[]));
    {
      schema: "nvkr.dev/hardware-e2e-substitutions/v1",
      artifactSource: "build-subject",
      plan: {commit: $p[0].commit, version: $p[0].version, scope: $p[0].scope, fixtureSha256: $sha},
      producerRunId: null,
      nodeImageVersionSkew: false,
      replaced: [],
      retainedFromNvke: [entries | {name, version, url} | with_entries(select(.value != null))] | unique}')
  [[ ! -e $out && ! -L $out ]] || fail NVKR-E-IMMUTABLE-VIOLATION 'substituted plan output must be fresh' 47
  mkdir -m 700 "$out"
  cp -- "$plan" "$out/plan.json"
  printf '%s\n' "$record" >"$out/substitutions.json"
  printf 'nvkr: kept the plan unmodified in %s: the artifacts are built from the subject commit, not by NVKR\n' "$out/plan.json"
  exit 0
fi

# 3. The outputs manifest: exact slots, content-addressed OCI references, HTTPS images.
jq -e '.schema == "nvkr.dev/hardware-e2e-outputs/v1" and (.producerRunId | type == "string" and test("^[1-9][0-9]{0,19}$"))
  and (.outputs | type == "object")' "$outputs" >/dev/null ||
  fail NVKR-E-USAGE 'outputs manifest needs schema nvkr.dev/hardware-e2e-outputs/v1, a producerRunId and outputs' 2
unknown=$(jq -r --argjson slots "$slots" '.outputs | keys[] | select(in($slots) | not)' "$outputs")
[[ -z $unknown ]] || fail NVKR-E-UNSUPPORTED "outputs manifest names slots this lane does not substitute: ${unknown//$'\n'/ }" 13
missing=$(jq -n -r --argjson slots "$slots" --slurpfile o "$outputs" '$slots | keys[] | select(in($o[0].outputs) | not)')
[[ -z $missing ]] || fail NVKR-E-RELEASE-INCOMPLETE "outputs manifest lacks required slots: ${missing//$'\n'/ }" 41
jq -e --argjson slots "$slots" '
  .outputs | to_entries | all(.value | type == "object" and (.url | type == "string"))' "$outputs" >/dev/null ||
  fail NVKR-E-USAGE 'every output needs a url' 2
jq -e --argjson slots "$slots" '
  .outputs | to_entries | map(select($slots[.key] == "ociArtifact")) |
  all(.value.url | test("^oci://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64}$"))' "$outputs" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'OCI outputs must be oci:// references pinned by @sha256 digest, not tags' 49
jq -e --argjson slots "$slots" '
  .outputs | to_entries | map(select($slots[.key] == "osImage")) |
  all(.value.url | test("^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?/[A-Za-z0-9._~%+=/-]+\\.qcow2$"))' "$outputs" >/dev/null ||
  fail NVKR-E-ENGINE-CAPABILITY 'node images must be plain https URLs to a .qcow2: iPXE cannot pull an OCI artifact' 29
jq -e --argjson slots "$slots" '
  .outputs | to_entries | map(select($slots[.key] == "osImage")) |
  all(.value.sha256 | type == "string" and test("^[0-9a-f]{64}$"))' "$outputs" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'node image outputs must name the qcow2 sha256' 49
jq -e --argjson slots "$slots" '
  def source_ok: type == "object" and (keys == ["kind","reference","version"])
    and (.version | type == "string" and test("^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$"))
    and (if .kind == "nvkr-release" then (.reference | type == "string" and test("^oci://[^[:space:]@]+@sha256:[0-9a-f]{64}$"))
         elif .kind == "ci-run" then (.reference | type == "string" and test("^https://[^[:space:]@]+$"))
         else false end);
  .outputs | to_entries | map(select($slots[.key] == "osImage")) | all(.value.source | source_ok)' "$outputs" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'node image outputs must name their source: {kind: nvkr-release with an oci:// digest reference, or ci-run with an https reference; version: vX.Y.Z}' 49
jq -e --argjson slots "$slots" '
  def whole: type == "number" and . >= 1 and . == floor;
  def verification_ok: type == "object" and (
    (.method == "http-get" and (keys == ["method"]))
    or (.method == "s3-readback" and (keys == ["bytes","method","qcow2Version","run","virtualSizeBytes"])
        and (.run | type == "string" and test("^https://github\\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[1-9][0-9]{0,19}(/attempts/[1-9][0-9]{0,5})?$"))
        and (.bytes | whole) and (.virtualSizeBytes | whole)
        and (.qcow2Version == 2 or .qcow2Version == 3)));
  .outputs | to_entries | map(select($slots[.key] == "osImage")) | all(.value.verification | verification_ok)' "$outputs" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'node image outputs must name their verification: {method: http-get} or {method: s3-readback, run: the https URL of a GitHub Actions run, bytes, qcow2Version: 2|3, virtualSizeBytes}' 49

# A node image built at another NVKE version than the plan's subject is a combination no run has exercised.
skewed=$(jq -n -r --argjson slots "$slots" --slurpfile o "$outputs" --slurpfile p "$plan" '
  [$slots | to_entries[] | select(.value == "osImage") | .key
   | select($o[0].outputs[.].source.version != $p[0].version)
   | "\(.) is \($o[0].outputs[.].source.version)"] | join(", ")')
if [[ -n $skewed && $allow_skew != true ]]; then
  fail NVKR-E-UNSUPPORTED "node image version differs from the plan's $(jq -r .version "$plan") ($skewed); pass --allow-node-image-version-skew to run that combination anyway" 13
fi

# 4. Substitute, then prove no NVKE-owned NVCR artifact survives in the result.
result=$(jq --indent 2 --slurpfile o "$outputs" '
  def swap: if ($o[0].outputs[.name]) != null then .url = $o[0].outputs[.name].url else . end;
  .combinations |= map(
    .components |= map(swap)
    | .dependencies |= map(swap)
    | .streams |= map(.dependencies |= map(swap)))' "$plan")
jq -e '[.. | strings | select(test("nvcr\\.io"))] | length == 0' <<<"$result" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'substituted plan still references an nvcr.io artifact that NVKR did not build' 49

record=$(jq -n --indent 2 --argjson slots "$slots" --slurpfile o "$outputs" --slurpfile p "$plan" --arg sha "$have" --arg skewed "$skewed" '
  def entries: $p[0].combinations[] | ((.components[]), (.dependencies[]), (.streams[].dependencies[]));
  {
    schema: "nvkr.dev/hardware-e2e-substitutions/v1",
    artifactSource: "nvkr-outputs",
    plan: {commit: $p[0].commit, version: $p[0].version, scope: $p[0].scope, fixtureSha256: $sha},
    producerRunId: $o[0].producerRunId,
    nodeImageVersionSkew: ($skewed != ""),
    replaced: [$slots | keys[] as $name | {
      slot: $name,
      from: ([entries | select(.name == $name) | .url] | unique | .[0]),
      to: $o[0].outputs[$name].url,
      sha256: ($o[0].outputs[$name].sha256 // null),
      source: ($o[0].outputs[$name].source // null),
      verification: ($o[0].outputs[$name].verification // null)} | with_entries(select(.value != null))],
    retainedFromNvke: [entries | select(.name as $n | $slots | has($n) | not)
      | {name, version, url} | with_entries(select(.value != null))] | unique}')

[[ ! -e $out && ! -L $out ]] || fail NVKR-E-IMMUTABLE-VIOLATION 'substituted plan output must be fresh' 47
mkdir -m 700 "$out"
printf '%s\n' "$result" >"$out/plan.json"
printf '%s\n' "$record" >"$out/substitutions.json"
printf 'nvkr: substituted %s NVKR-built references into %s\n' "$(jq -r '.replaced | length' <<<"$record")" "$out/plan.json"
