#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Dispatch one maintained producer and retain its exact GitHub execution facts.
# These records locate artifacts; release verify remains the authentication owner.
set -euo pipefail
umask 077

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
[[ $# == 3 ]] || fail NVKR-E-USAGE 'nightly-build.sh PROFILES PROFILE OUT' 2
profiles=$1 profile=$2 out=$3
for command in gh jq timeout; do
  command -v "$command" >/dev/null || fail NVKR-E-ENGINE-CAPABILITY "missing $command" 29
done
[[ ${GITHUB_SERVER_URL:-} == https://github.com && ${GITHUB_REF:-} == refs/heads/main ]] ||
  fail NVKR-E-ENGINE-CAPABILITY 'nightly production requires the GitHub main workflow' 29
: "${GITHUB_REPOSITORY:?}" "${GITHUB_REPOSITORY_ID:?}" "${GITHUB_SHA:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_RUN_ATTEMPT:?}"
[[ ${NVKE_COMMIT:-} =~ ^[0-9a-f]{40}$ ]] || fail NVKR-E-UNPINNED-INPUT 'nightly requires the cycle-resolved exact NVKE main revision' 49
selection=$(jq -ce --arg name "$profile" '[.[] | select(.name == $name)] | if length == 1 then .[0] else error("unknown or ambiguous profile") end' "$profiles") ||
  fail NVKR-E-USAGE 'select one maintained nightly profile' 2
workflow=$(jq -er '.build_workflow' <<<"$selection")
[[ $workflow == "nvkr-run-$(jq -er '.bundle' <<<"$selection").yml" ]] ||
  fail NVKR-E-PIPELINE-DRIFT 'profile workflow does not match its generated namespace' 51
[[ ! -e $out && ! -L $out ]] || fail NVKR-E-IMMUTABLE-VIOLATION 'nightly build output must be fresh' 47
mkdir -m 700 "$out"
run_id='' terminal=false active_pid=''
# GNU timeout creates the owned command's process group. Waiting with Bash's
# builtin lets INT/TERM traps run promptly instead of waiting for foreground I/O.
run_owned() {
  local status=0
  "$@" &
  active_pid=$!
  wait "$active_pid" || status=$?
  active_pid=''
  return "$status"
}
api() { run_owned timeout --signal=TERM --kill-after=1s 60s gh api --hostname github.com -H 'X-GitHub-Api-Version: 2026-03-10' "$@"; }
# Invoked through the EXIT trap, including error and cancellation paths.
# shellcheck disable=SC2329
cleanup() {
  local status=$?
  trap - EXIT
  # Finish this bounded cleanup even if the runner sends its second signal.
  trap '' INT TERM
  if [[ -n $active_pid ]]; then
    # Stop only our current GNU-timeout group, then reap its direct child. The
    # PID fallback also covers cancellation before timeout creates that group;
    # the second group signal covers group creation racing the first signal.
    kill -KILL -- "-$active_pid" 2>/dev/null || true
    kill -KILL "$active_pid" 2>/dev/null || true
    kill -KILL -- "-$active_pid" 2>/dev/null || true
    wait "$active_pid" 2>/dev/null || true
    active_pid=''
  fi
  if [[ -n $run_id && $terminal != true ]]; then
    # Cancel only the ID returned by this dispatch. Never guess a run by time.
    if run_owned timeout --signal=TERM --kill-after=1s 5s gh api --hostname github.com \
      -H 'X-GitHub-Api-Version: 2026-03-10' --method POST \
      "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/cancel" >"$out/cancel.json" 2>"$out/cancel-error.txt"; then
      printf '%s\n' 'Owned producer cancellation requested; cancellation is not a cleanup verdict.' >&2
    else
      printf '%s\n' 'Owned producer cancellation was not acknowledged; inspect its exact run ID.' >&2
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 31' INT TERM
api "repos/${GITHUB_REPOSITORY}" >"$out/repository.json"
jq -e --arg repo "$GITHUB_REPOSITORY" --arg id "$GITHUB_REPOSITORY_ID" \
  '.full_name == $repo and (.id|tostring) == $id and .default_branch == "main"' "$out/repository.json" >/dev/null ||
  fail NVKR-E-UNPINNED-INPUT 'repository identity/default branch mismatch' 49
api "repos/${GITHUB_REPOSITORY}/actions/workflows/${workflow}" >"$out/workflow.json"
jq -e --arg path ".github/workflows/$workflow" '.path == $path and .state == "active"' "$out/workflow.json" >/dev/null ||
  fail NVKR-E-ENGINE-CAPABILITY 'maintained producer workflow is unavailable' 29
jq -n --arg source "$NVKE_COMMIT" '{ref:"main",inputs:{source_commit:$source}}' >"$out/request.json"
# A failed/ambiguous POST is never retried: it may already have created a run.
api --method POST "repos/${GITHUB_REPOSITORY}/actions/workflows/${workflow}/dispatches" \
  --input "$out/request.json" >"$out/dispatch.json" ||
  fail NVKR-E-UNAVAILABLE 'dispatch outcome is ambiguous; do not retry or guess the producing run' 30
run_id=$(jq -er '.workflow_run_id | select(type == "number" and . > 0 and . <= 9007199254740991 and floor == .) | tostring' "$out/dispatch.json") ||
  fail NVKR-E-UNAVAILABLE 'dispatch returned no exact run ID; no latest-run fallback is allowed' 30
# Reserve thirty minutes below the 360-minute job for admission and cleanup.
deadline=$((SECONDS + 19800))
while (( SECONDS < deadline )); do
  api "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}" >"$out/producer-run.json"
  jq -e --arg id "$run_id" --arg sha "$GITHUB_SHA" --arg repo "$GITHUB_REPOSITORY" \
    --arg path ".github/workflows/$workflow" \
    '(.id|tostring) == $id and .head_sha == $sha and .head_branch == "main" and
     .event == "workflow_dispatch" and .path == $path and .repository.full_name == $repo and .run_attempt == 1' \
    "$out/producer-run.json" >/dev/null ||
    fail NVKR-E-UNPINNED-INPUT 'producer identity, commit, workflow or attempt differs from the nightly cycle' 49
  if [[ $(jq -er '.status' "$out/producer-run.json") == completed ]]; then
    terminal=true
    [[ $(jq -er '.conclusion' "$out/producer-run.json") == success ]] ||
      fail NVKR-E-BUILD-FAILED 'exact nightly producer did not succeed' 21
    jq -n --argjson profile "$selection" --argjson producer "$(cat "$out/producer-run.json")" \
      --arg coordinator "$GITHUB_RUN_ID" --arg attempt "$GITHUB_RUN_ATTEMPT" \
      --arg nvke "$NVKE_COMMIT" \
      '{profile:$profile,coordinatorRun:$coordinator,coordinatorAttempt:$attempt,
        producerRun:$producer.id,producerAttempt:$producer.run_attempt,sourceCommit:$producer.head_sha,nvkeCommit:$nvke,
        producerURL:$producer.html_url,trust:"github-run-facts; publication-not-yet-verified"}' >"$out/build.json"
    exit 0
  fi
  run_owned timeout --signal=TERM --kill-after=1s 20s sleep 15
done
fail NVKR-E-TIMEOUT 'nightly producer exceeded its bounded wait' 31
