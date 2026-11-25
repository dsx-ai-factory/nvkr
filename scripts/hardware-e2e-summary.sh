#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Record what a hardware E2E run covered, and refuse a summary that claims more.
# The summary is a description of the run, not a verdict on it: it always says
# qualificationAuthority "none", and `assert` rejects any summary that adds a field
# or changes that value (ADR-18).
#
#   hardware-e2e-summary.sh leg DIR STREAM COMBINATION ARCH
#       Stamp when a leg started into DIR/leg.json. Set HARDWARE_E2E_NOW to an RFC 3339
#       UTC time to fix the clock in tests.
#   hardware-e2e-summary.sh write OUT --artifact-source SOURCE --stage STAGE --harness-commit SHA \
#       --orchestrator-commit SHA --orchestrator-run ID --plan PLAN \
#       --substitutions FILE --matrix FILE [--node-user USER] \
#       [--node-image-check FILE ...] --leg DIR [--leg DIR ...]
#       Build OUT (created fresh) from the plan, the substitution record, the matrix the run
#       was dispatched with, one `hwe2e check-node-image` record per node image and one
#       directory per leg. SOURCE is the artifact source the run used and must be the one the
#       substitution record names:
#         nvkr-outputs   the plan names NVKR-built artifacts. Each node image record must name
#                        the exact URL and sha256 the substituted plan carries and the method the
#                        plan names for it. The summary says how the image was checked, whether
#                        this run observed that (http-get did; s3-readback repeats what a
#                        publication run reported and this run did not see), what the check saw,
#                        where the image came from and whether its NVKE version differs from the
#                        plan's, computed here and rechecked by `assert`. At least one node image
#                        record is required.
#         build-subject  the controller, capi-provisioned and nvkectl were built from the subject
#                        commit in the job, by NVKE's own build. Nothing was NVKR-built, the plan is
#                        recorded unmodified, there is no producer, and node image records are
#                        refused: the node images are the ones the plan names, which NVKR did not
#                        check.
#       A leg directory holds leg.json, coverage.json (`hwe2e check-junit`) and staged.json
#       (hardware-e2e-stage-inputs.sh or hardware-e2e-build-subject.sh). Every dispatched stream must be
#       one the plan names, on the architecture the plan gives it, so a stream the plan lacks can never
#       stand in for one it has; a run that dispatched fewer streams than the plan names is incomplete.
#       For NVKR outputs each leg's staged record must name the controller, capi-provisioned and CLI
#       references the substitution record names, and each node image's URL must be the plan's own.
#       The summary must pass `assert` before it is written. --node-user is the login the suite was given for the
#       node image (empty is the harness's default for the OSS image); an image
#       whose baked user differs fails as a publickey rejection late in the run.
#   hardware-e2e-summary.sh assert FILE
#       Check FILE against the summary contract; the workflow runs this before the
#       run is allowed to pass.
#
# Exit codes: 0 ok, 2 usage, 13 malformed input, 22 contract violation, a dispatched stream the plan
# does not name, staged evidence for other artifacts than the substitution record, or a node image
# record that names other bytes, or another verification, than the plan, 29 missing tool,
# 41 a dispatched leg or a node image has no evidence, 47 output exists.
set -euo pipefail
umask 077

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
usage() {
  fail NVKR-E-USAGE 'hardware-e2e-summary.sh {leg DIR STREAM COMBINATION ARCH | write OUT --artifact-source {nvkr-outputs|build-subject} --stage S --harness-commit SHA --orchestrator-commit SHA --orchestrator-run ID --plan PLAN --substitutions FILE --matrix FILE [--node-user USER] [--node-image-check FILE...] --leg DIR... | assert FILE}' 2
}
command -v jq >/dev/null || fail NVKR-E-ENGINE-CAPABILITY 'missing jq' 29

readonly STAGES='["provision","bootstrap","deployment","full"]'
readonly NAME_RE='^[A-Za-z0-9._-]+$'
readonly TIME_RE='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'

sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null; then shasum -a 256 "$1" | cut -d' ' -f1
  else fail NVKR-E-ENGINE-CAPABILITY 'missing sha256sum or shasum' 29; fi
}

regular() { [[ -f $1 && ! -L $1 ]] || fail NVKR-E-USAGE "not a regular file: $1" 2; }

# The summary contract. Every rule is evaluated even when an earlier one fails, and a
# malformed value fails its rule instead of aborting the check.
read -r -d '' contract <<'JQ' || true
def hex($n): type == "string" and test("^[0-9a-f]{" + ($n | tostring) + "}$");
def digits: type == "string" and test("^[1-9][0-9]{0,19}$");
def ts: type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$");
def keys_are($want): type == "object" and (keys == ($want | sort));
def rule($message; test): try (if test then empty else $message end) catch $message;
. as $s
| [
    rule("not a JSON object"; type == "object"),
    rule("unexpected or missing top-level fields"; keys_are(["armStartedAfterAmd","artifactSource","complete","harness","legs","nodeImageVersionSkew","nodeImages","nodeUser","orchestrator","plan","plannedStreams","producer","qualificationAuthority","retainedFromNvke","schema","stage","subject"])),
    rule("artifactSource must be nvkr-outputs or build-subject"; .artifactSource == "nvkr-outputs" or .artifactSource == "build-subject"),
    rule("nodeUser must be empty or a plain account name"; .nodeUser | type == "string" and test("^([a-z_][a-z0-9_-]{0,31})?$")),
    rule("schema is not nvkr.dev/hardware-e2e-summary/v1"; .schema == "nvkr.dev/hardware-e2e-summary/v1"),
    rule("qualificationAuthority is not none"; .qualificationAuthority == "none"),
    rule("stage is not one of the harness stages"; .stage | IN($stages[])),
    rule("harness must name exactly a 40-hex commit"; .harness | keys_are(["commit"]) and (.commit | hex(40))),
    rule("subject must name a 40-hex commit and a version"; .subject | keys_are(["commit","version"]) and (.commit | hex(40)) and (.version | type == "string" and length > 0)),
    rule("orchestrator must name a 40-hex commit and a run id"; .orchestrator | keys_are(["commit","runId"]) and (.commit | hex(40)) and (.runId | digits)),
    rule("producer must name a run id for NVKR outputs and be null for a build from the subject commit"; if .artifactSource == "build-subject" then .producer == null else (.producer | keys_are(["runId"]) and (.runId | digits)) end),
    rule("plan must carry two sha256 digests, the plan as generated and the plan the legs ran"; .plan | keys_are(["generatedSha256","sha256"]) and (.generatedSha256 | hex(64)) and (.sha256 | hex(64))),
    rule("a build from the subject commit must run the plan as generated, and a run of NVKR outputs must have changed it"; if .artifactSource == "build-subject" then .plan.sha256 == .plan.generatedSha256 else .plan.sha256 != .plan.generatedSha256 end),
    rule("nodeImages must be empty for a build from the subject commit, and nonempty https locations with a sha256 for NVKR outputs"; if .artifactSource == "build-subject" then .nodeImages == [] else (.nodeImages | type == "array" and length > 0 and all(keys_are(["checked","sha256","slot","source","url"]) and (.sha256 | hex(64)) and (.slot | startswith("os-image-")) and (.url | test("^https://[^/@?#]+/[^?#]+$")))) end),
    rule("each node image slot must appear once"; ([.nodeImages[].slot] | length) == ([.nodeImages[].slot] | unique | length)),
    rule("every leg architecture must have its node image"; if .artifactSource == "build-subject" then true else ([.legs[].arch] | unique | all(. as $a | $s.nodeImages | any(.slot == "os-image-\($a)"))) end),
    rule("each node image must name its source: a release digest or a CI run, and an NVKE version"; .nodeImages | all(.source | keys_are(["kind","reference","version"]) and (.version | test("^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$")) and (if .kind == "nvkr-release" then (.reference | test("^oci://[^[:space:]@]+@sha256:[0-9a-f]{64}$")) elif .kind == "ci-run" then (.reference | test("^https://[^[:space:]@]+$")) else false end))),
    rule("each node image must record how it was checked, whether this run observed that, and what the check saw"; .nodeImages | all(.checked | type == "object" and (.bytes | type == "number" and . >= 1 and . == floor) and (.qcow2Version == 2 or .qcow2Version == 3) and (.virtualSizeBytes | type == "number" and . >= 1 and . == floor) and (if .method == "http-get" then (keys_are(["bytes","method","observedByThisRun","qcow2Version","rangeSupported","virtualSizeBytes"]) and .observedByThisRun == true and (.rangeSupported | type == "boolean")) elif .method == "s3-readback" then (keys_are(["bytes","method","observedByThisRun","qcow2Version","run","virtualSizeBytes"]) and .observedByThisRun == false and (.run | type == "string" and test("^https://github\\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[1-9][0-9]{0,19}(/attempts/[1-9][0-9]{0,5})?$"))) else false end))),
    rule("nodeImageVersionSkew must say whether any node image version differs from the subject version"; (.nodeImageVersionSkew | type == "boolean") and .nodeImageVersionSkew == ([.nodeImages[].source.version] | any(. != $s.subject.version))),
    rule("retainedFromNvke must be a list of names"; .retainedFromNvke | type == "array" and all(type == "string")),
    rule("plannedStreams must be at least 1"; .plannedStreams | type == "number" and . >= 1),
    rule("legs must be nonempty with exactly the leg fields"; .legs | type == "array" and length > 0 and all(keys_are(["arch","combination","coverage","staged","startedAt","stream"]) and (.startedAt | ts) and (.arch == "amd64" or .arch == "arm64") and (.stream | type == "string" and length > 0) and (.combination | type == "string" and length > 0))),
    rule("legs must be ordered by start time"; [.legs[].startedAt] == ([.legs[].startedAt] | sort)),
    rule("a stream appears in more than one leg"; ([.legs[] | [.combination, .stream]] | length) == ([.legs[] | [.combination, .stream]] | unique | length)),
    rule("every leg coverage must match the summary stage and be complete exactly at the full stage"; .legs | all(.coverage | keys_are(["categories","complete","dryRunSha256","labelFilter","passed","reportSha256","schema","selected","specsInSuite","stage","tenantLabel"]) and .schema == "nvkr.dev/hardware-e2e-coverage/v1" and .stage == $s.stage and .complete == ($s.stage == "full") and .selected >= 1 and (if .complete then .passed == .selected else true end))),
    rule("every leg coverage must count its groups, the groups must add up to the totals, and each group the stage registers must be selected and, at the full stage, passed"; .legs | all(.coverage as $c | $c.categories | (type == "object" and keys == ["provisioning","site-checks","tenant"] and all(.[]; keys_are(["passed","selected"]) and (.selected | type == "number" and . >= 0 and . == floor) and (.passed | type == "number" and . >= 0 and . == floor) and .passed <= .selected) and ([.[].selected] | add) == $c.selected and ([.[].passed] | add) == $c.passed and (if $c.complete then all(.[]; .passed == .selected) else true end) and (({"provision":["provisioning"],"bootstrap":["provisioning"],"deployment":["provisioning","site-checks"],"full":["provisioning","site-checks","tenant"]}[$s.stage]) as $need | $need != null and ($need | all(. as $g | $c.categories[$g].selected >= 1)))))),
    rule("every leg must record what it staged and what is not NVKR-built"; .legs | all(.staged | keys_are(["builtFromCommit","controllerVersionCheck","files","notNvkrBuilt","source"]) and (.controllerVersionCheck == "matched" or .controllerVersionCheck == "not-determined") and (.files | type == "object" and length > 0 and all(.[]; hex(64))) and (.notNvkrBuilt | type == "array" and all(type == "string")))),
    rule("every leg must have staged from the artifact source the summary names"; .legs | all(.staged.source == $s.artifactSource)),
    rule("a build from the subject commit must name that commit as the harness commit and the plan's, and NVKR outputs must name none"; .legs | all(.staged | if .source == "build-subject" then (.builtFromCommit == $s.subject.commit and .builtFromCommit == $s.harness.commit) else .builtFromCommit == null end)),
    rule("every leg must say that the deploy path, the imager EFI and the site profiles are not NVKR-built"; .legs | all((["deploy-path","imager-efi","site-profiles"] - .staged.notNvkrBuilt) == [])),
    rule("a build from the subject commit must say that the controller, capi-provisioned, the CLI and the node image are not NVKR-built"; .legs | all(if .staged.source == "build-subject" then ((["capi-provisioned","cli","controller","node-image"] - .staged.notNvkrBuilt) == []) else true end)),
    rule("for NVKR outputs, a node image whose source is not an NVKR release must be named as not NVKR-built, and an NVKR release must not be"; if .artifactSource == "build-subject" then true else (.legs | all(. as $leg | ($s.nodeImages | map(select(.slot == "os-image-\($leg.arch)")) | first) as $img | (($img.source.kind == "nvkr-release") == (($leg.staged.notNvkrBuilt | any(. == "node-image")) | not)))) end),
    rule("armStartedAfterAmd must be true and match the leg start times"; (([.legs[] | select(.arch == "amd64") | .startedAt] | max) as $amd | ([.legs[] | select(.arch == "arm64") | .startedAt] | min) as $arm | (if $amd == null or $arm == null then true else $amd < $arm end) as $computed | .armStartedAfterAmd == true and $computed == true)),
    rule("complete must be true exactly for a full-stage run whose legs cover every planned stream"; .complete == (.stage == "full" and (.legs | length) == .plannedStreams and (.legs | all(.coverage.complete))))
  ] | .[]
JQ

check_summary() { # check_summary JSON-TEXT
  local problems
  # An empty file or a second document would evaluate no rule, and so pass.
  jq -e -s 'length == 1' <<<"$1" >/dev/null 2>&1 ||
    fail NVKR-E-VERIFY-FAILED 'the summary is not exactly one JSON document' 22
  problems=$(jq -r --argjson stages "$STAGES" "$contract" <<<"$1" 2>&1) ||
    fail NVKR-E-VERIFY-FAILED "the summary could not be evaluated: $problems" 22
  [[ -z $problems ]] || fail NVKR-E-VERIFY-FAILED "summary contract violated: ${problems//$'\n'/; }" 22
}

timestamp() {
  local now=${HARDWARE_E2E_NOW:-}
  [[ -n $now ]] || now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  [[ $now =~ $TIME_RE ]] || fail NVKR-E-USAGE "HARDWARE_E2E_NOW is not an RFC 3339 UTC time: $now" 2
  printf '%s' "$now"
}

create() { # create PATH CONTENT: write a new file, never replacing one
  [[ ! -e $1 && ! -L $1 ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $1" 47
  (set -o noclobber; printf '%s\n' "$2" >"$1") || fail NVKR-E-IMMUTABLE-VIOLATION "could not create $1" 47
}

cmd_leg() {
  [[ $# == 4 ]] || usage
  local dir=$1 stream=$2 combination=$3 arch=$4
  [[ -d $dir && ! -L $dir ]] || fail NVKR-E-USAGE "leg directory does not exist: $dir" 2
  [[ $stream =~ $NAME_RE && $combination =~ $NAME_RE ]] ||
    fail NVKR-E-UNSUPPORTED 'stream and combination must be plain names' 13
  [[ $arch == amd64 || $arch == arm64 ]] || fail NVKR-E-UNSUPPORTED "unsupported architecture: $arch" 13
  local started
  started=$(timestamp)
  create "$dir/leg.json" "$(jq -n -S --indent 2 --arg s "$stream" --arg c "$combination" --arg a "$arch" --arg t "$started" \
    '{schema: "nvkr.dev/hardware-e2e-leg/v1", stream: $s, combination: $c, arch: $a, startedAt: $t}')"
}

cmd_write() {
  [[ $# -ge 1 ]] || usage
  local out=$1 artifact_source='' stage='' harness='' orch_commit='' orch_run='' plan='' subs='' matrix='' node_user='' legs=() node_checks=()
  shift
  while [[ $# -gt 0 ]]; do
    [[ $# -ge 2 ]] || fail NVKR-E-USAGE "$1 needs a value" 2
    case $1 in
      --artifact-source) artifact_source=$2 ;;
      --stage) stage=$2 ;;
      --harness-commit) harness=$2 ;;
      --orchestrator-commit) orch_commit=$2 ;;
      --orchestrator-run) orch_run=$2 ;;
      --plan) plan=$2 ;;
      --substitutions) subs=$2 ;;
      --matrix) matrix=$2 ;;
      --node-user) node_user=$2 ;;
      --node-image-check) node_checks+=("$2") ;;
      --leg) legs+=("$2") ;;
      *) fail NVKR-E-USAGE "unknown argument: $1" 2 ;;
    esac
    shift 2
  done
  [[ -n $artifact_source && -n $stage && -n $harness && -n $orch_commit && -n $orch_run && -n $plan && -n $subs && -n $matrix && ${#legs[@]} -gt 0 ]] || usage
  [[ $artifact_source == nvkr-outputs || $artifact_source == build-subject ]] ||
    fail NVKR-E-UNSUPPORTED "unsupported artifact source: $artifact_source" 13
  if [[ $artifact_source == nvkr-outputs ]]; then
    [[ ${#node_checks[@]} -gt 0 ]] || usage
  else
    [[ ${#node_checks[@]} -eq 0 ]] ||
      fail NVKR-E-USAGE 'node image checks are taken only for NVKR outputs: a build from the subject commit uses the plan'"'"'s own node images, which NVKR did not check' 2
  fi
  [[ ! -e $out && ! -L $out ]] || fail NVKR-E-IMMUTABLE-VIOLATION "refusing to replace $out" 47
  regular "$plan"; regular "$subs"; regular "$matrix"

  jq -e '.schema == "nvkr.dev/hardware-e2e-substitutions/v1"' "$subs" >/dev/null ||
    fail NVKR-E-UNSUPPORTED 'substitution record has an unsupported schema' 13
  jq -e --arg want "$artifact_source" '.artifactSource == $want' "$subs" >/dev/null ||
    fail NVKR-E-VERIFY-FAILED "the substitution record names artifact source $(jq -r '.artifactSource // "none"' "$subs"), not $artifact_source" 22
  jq -e '(.include | type == "array") and (.include | length > 0)' "$matrix" >/dev/null ||
    fail NVKR-E-UNSUPPORTED 'the matrix names no streams' 13

  # Every dispatched stream must be one the plan names, on the architecture the plan gives it. Without this
  # a matrix that names a stream the plan lacks would pass the checks below with equal counts, and the
  # stream the plan does name would have no evidence.
  local foreign
  foreign=$(jq -n -r --slurpfile p "$plan" --slurpfile m "$matrix" '
    [$p[0].combinations[] as $c | $c.streams[] | {combination: $c.id, stream: .name, arch: .capabilities.arch}] as $planned
    | [$m[0].include[] as $e
        | select([$planned[] | select(. == {combination: $e.combination, stream: $e.stream, arch: $e.arch})] | length != 1)
        | "\($e.combination)/\($e.stream)"] | join(", ")') ||
    fail NVKR-E-UNSUPPORTED 'the plan could not be read for its streams' 13
  [[ -z $foreign ]] || fail NVKR-E-VERIFY-FAILED "the matrix names streams the plan does not: $foreign" 22

  # One check record per node image, naming the exact URL and bytes the substituted plan carries.
  local node_images=() check node_array='[]' mismatch absent
  if [[ $artifact_source == nvkr-outputs ]]; then
    for check in "${node_checks[@]}"; do
      regular "$check"
      node_images+=("$(jq -n -e --slurpfile c "$check" --slurpfile s "$subs" '
        $c[0] as $chk
        | select($chk.schema == "nvkr.dev/hardware-e2e-node-image-check/v1")
        | select(($chk.method == "http-get" and $chk.observedByThisRun == true)
                 or ($chk.method == "s3-readback" and $chk.observedByThisRun == false))
        | [$s[0].replaced[] | select(.slot == $chk.slot and (.slot | startswith("os-image-")))] as $named
        | select($named | length == 1)
        | $named[0] as $n
        | {slot: $n.slot, url: $n.to, sha256: $n.sha256, source: $n.source, urlChecked: $chk.url, sha256Checked: $chk.sha256,
           agrees: ($n.verification.method == $chk.method
                    and (if $chk.method == "s3-readback"
                         then ($n.verification.run == $chk.run and $n.verification.bytes == $chk.bytes
                               and $n.verification.qcow2Version == $chk.qcow2.version
                               and $n.verification.virtualSizeBytes == $chk.qcow2.virtualSizeBytes)
                         else true end)),
           checked: (if $chk.method == "http-get" then
                       {method: "http-get", observedByThisRun: true, bytes: $chk.bytes, qcow2Version: $chk.qcow2.version,
                        rangeSupported: $chk.rangeSupported, virtualSizeBytes: $chk.qcow2.virtualSizeBytes}
                     elif $chk.method == "s3-readback" then
                       {method: "s3-readback", observedByThisRun: false, run: $chk.run, bytes: $chk.bytes,
                        qcow2Version: $chk.qcow2.version, virtualSizeBytes: $chk.qcow2.virtualSizeBytes}
                     else error("unknown node image check method") end)}')") ||
        fail NVKR-E-UNSUPPORTED "node image check $check has an unsupported schema or method, an observation flag that does not fit its method (http-get was observed by this run, s3-readback was not), or names a slot the plan did not substitute" 13
    done
    node_array=$(printf '%s\n' "${node_images[@]}" | jq -s -c 'sort_by(.slot)')
    mismatch=$(jq -n -r --argjson n "$node_array" '[$n[] | select(.url != .urlChecked or .sha256 != .sha256Checked) | .slot] | join(", ")')
    [[ -z $mismatch ]] || fail NVKR-E-VERIFY-FAILED "node image check names other bytes than the plan for: $mismatch" 22
    mismatch=$(jq -n -r --argjson n "$node_array" '[$n[] | select(.agrees | not) | .slot] | join(", ")')
    [[ -z $mismatch ]] || fail NVKR-E-VERIFY-FAILED "node image check disagrees with the verification the plan names for: $mismatch" 22
    # The check and the substitution record agree with each other; the plan this summary will digest must
    # name the same location, or its digest would sit beside another image's verification.
    mismatch=$(jq -n -r --argjson n "$node_array" --slurpfile p "$plan" '
      def entries: $p[0].combinations[] | ((.components[]), (.dependencies[]), (.streams[].dependencies[]));
      [$n[] | . as $i | select(([entries | select(.name == $i.slot) | .url] | unique) != [$i.url]) | .slot] | join(", ")') ||
      fail NVKR-E-UNSUPPORTED 'the plan could not be read for its node image locations' 13
    [[ -z $mismatch ]] || fail NVKR-E-VERIFY-FAILED "the plan names other node image locations than the checks and the substitution record for: $mismatch" 22
    absent=$(jq -n -r --argjson n "$node_array" --slurpfile s "$subs" '
      [$s[0].replaced[] | select(.slot | startswith("os-image-")) | .slot | select(. as $slot | [$n[] | select(.slot == $slot)] | length != 1)] | join(", ")')
    [[ -z $absent ]] || fail NVKR-E-RELEASE-INCOMPLETE "node image slots without exactly one check: $absent" 41
  fi

  local built=() dir stale
  for dir in "${legs[@]}"; do
    [[ -d $dir && ! -L $dir ]] || fail NVKR-E-USAGE "leg directory does not exist: $dir" 2
    for part in leg coverage staged; do regular "$dir/$part.json"; done
    built+=("$(jq -n -e --slurpfile l "$dir/leg.json" --slurpfile c "$dir/coverage.json" --slurpfile s "$dir/staged.json" '
      $l[0] as $leg | $c[0] as $cov | $s[0] as $st
      | select($leg.schema == "nvkr.dev/hardware-e2e-leg/v1"
          and $cov.schema == "nvkr.dev/hardware-e2e-coverage/v1"
          and $st.schema == "nvkr.dev/hardware-e2e-staged/v1"
          and $st.arch == $leg.arch)
      | {stream: $leg.stream, combination: $leg.combination, arch: $leg.arch, startedAt: $leg.startedAt,
         coverage: $cov, staged: ($st | {builtFromCommit, controllerVersionCheck, files, notNvkrBuilt, source})}')") ||
      fail NVKR-E-UNSUPPORTED "leg evidence in $dir has an unsupported schema or mixes architectures" 13
    # What was staged must be what this run's substitution record names: a staged record from another run
    # passes every other check, and its archive hashes would be reported as evidence for this one.
    if [[ $artifact_source == nvkr-outputs ]]; then
      stale=$(jq -n -r --slurpfile st "$dir/staged.json" --slurpfile s "$subs" '
        $st[0] as $staged
        | def named($slot): [$s[0].replaced[] | select(.slot == $slot) | .to] | if length == 1 then .[0] else null end;
          ["controller", "capi-provisioned", "cli-\($staged.arch)"] as $slots
        | [$slots[] | select($staged.pulledByDigest[.] == null or $staged.pulledByDigest[.] != named(.))] as $differ
        | (if ($staged.pulledByDigest | type) == "object" and ($staged.pulledByDigest | keys) != ($slots | sort)
           then ["the set of pulled references"] else [] end) + $differ | join(", ")') ||
        fail NVKR-E-UNSUPPORTED "staged evidence in $dir has no readable pulledByDigest" 13
      [[ -z $stale ]] || fail NVKR-E-VERIFY-FAILED "staged evidence in $dir names other artifacts than the substitution record for: $stale" 22
    fi
  done
  local leg_array
  leg_array=$(printf '%s\n' "${built[@]}" | jq -s -c 'sort_by(.startedAt, .arch, .stream)')

  # Every dispatched leg must have evidence, on the architecture it was dispatched for.
  local shortfall
  shortfall=$(jq -n -r --argjson legs "$leg_array" --slurpfile m "$matrix" '
    [$m[0].include[] as $e
      | select([$legs[] | select(.combination == $e.combination and .stream == $e.stream and .arch == $e.arch)] | length != 1)
      | "\($e.combination)/\($e.stream)"] | join(", ")')
  [[ -z $shortfall ]] || fail NVKR-E-RELEASE-INCOMPLETE "no evidence for dispatched legs: $shortfall" 41
  local extra
  extra=$(jq -n -r --argjson legs "$leg_array" --slurpfile m "$matrix" '
    [$legs[] | . as $l
      | select([$m[0].include[] | select(.combination == $l.combination and .stream == $l.stream)] | length == 0)
      | "\(.combination)/\(.stream)"] | join(", ")')
  [[ -z $extra ]] || fail NVKR-E-UNSUPPORTED "evidence for legs that were not dispatched: $extra" 13

  local plan_sha summary
  plan_sha=$(sha256_of "$plan")
  summary=$(jq -n -S --indent 2 --arg stage "$stage" --arg harness "$harness" --arg oc "$orch_commit" --arg orun "$orch_run" \
    --arg nodeuser "$node_user" --arg plansha "$plan_sha" --argjson legs "$leg_array" --argjson nodes "$node_array" \
    --slurpfile p "$plan" --slurpfile s "$subs" '
    $s[0] as $sub
    | {
        schema: "nvkr.dev/hardware-e2e-summary/v1",
        qualificationAuthority: "none",
        artifactSource: $sub.artifactSource,
        stage: $stage,
        nodeUser: $nodeuser,
        harness: {commit: $harness},
        subject: {commit: $sub.plan.commit, version: $sub.plan.version},
        orchestrator: {commit: $oc, runId: $orun},
        producer: (if $sub.producerRunId == null then null else {runId: $sub.producerRunId} end),
        plan: {sha256: $plansha, generatedSha256: $sub.plan.generatedSha256},
        plannedStreams: ([$p[0].combinations[].streams[]] | length),
        nodeImages: ([$nodes[] | {slot, url, sha256, source, checked}] | sort_by(.slot)),
        nodeImageVersionSkew: $sub.nodeImageVersionSkew,
        retainedFromNvke: ([$sub.retainedFromNvke[].name] | unique),
        legs: $legs,
        armStartedAfterAmd: (([$legs[] | select(.arch == "amd64") | .startedAt] | max) as $amd
          | ([$legs[] | select(.arch == "arm64") | .startedAt] | min) as $arm
          | if $amd == null or $arm == null then true else $amd < $arm end),
        complete: ($stage == "full" and ($legs | length) == ([$p[0].combinations[].streams[]] | length)
          and ($legs | all(.coverage.complete)))
      }') || fail NVKR-E-UNSUPPORTED 'the summary could not be assembled from its inputs' 13

  check_summary "$summary"
  create "$out" "$summary"
  printf 'nvkr: wrote %s (stage %s, %s legs, qualificationAuthority none)\n' "$out" "$stage" "${#legs[@]}"
}

cmd_assert() {
  [[ $# == 1 ]] || usage
  regular "$1"
  local text
  text=$(<"$1")
  check_summary "$text"
  printf 'nvkr: summary ok: qualificationAuthority none, stage %s, %s legs\n' \
    "$(jq -r .stage <<<"$text")" "$(jq -r '.legs | length' <<<"$text")"
}

[[ $# -ge 1 ]] || usage
command=$1
shift
case $command in
  leg) cmd_leg "$@" ;;
  write) cmd_write "$@" ;;
  assert) cmd_assert "$@" ;;
  *) usage ;;
esac
