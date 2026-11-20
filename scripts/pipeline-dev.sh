#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Dispatch the committed development workflow from a clean, pushed branch.
set -euo pipefail
fail() { printf '%s: %s\n' "$1" "$3" >&2; exit "$2"; }
usage() { printf 'Usage: pipeline-dev.sh --repo OWNER/REPO [--check]\nRun make pipeline-dev-check before committing and pushing the generated bundle.\n'; }
repo='' check=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo=*) repo=${1#*=}; shift ;;
    --repo) [ "$#" -ge 2 ] || fail NVKR-E-USAGE 2 '--repo requires OWNER/REPO'; repo=$2; shift 2 ;;
    --check) check=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail NVKR-E-USAGE 2 "unexpected argument: $1" ;;
  esac
done
if [[ ! $repo =~ ^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$ ]] || [ "${repo#*/}" = . ] || [ "${repo#*/}" = .. ]; then
  fail NVKR-E-USAGE 2 '--repo must be an explicit OWNER/REPO on github.com'
fi
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
command -v jq >/dev/null || fail NVKR-E-USAGE 2 'Cannot execute jq; install it and retry.'
if command -v timeout >/dev/null; then timer=timeout
elif command -v gtimeout >/dev/null; then timer=gtimeout
else fail NVKR-E-USAGE 2 'Cannot execute timeout or gtimeout; install coreutils and retry.'; fi
export GH_HOST=github.com GH_PROMPT_DISABLED=1 GIT_TERMINAL_PROMPT=0
result=''
command_result() {
  local failure=$1 mutates=$2 status detail
  shift 2
  command -v "$1" >/dev/null || fail NVKR-E-USAGE 2 "Cannot execute $1; install it and retry."
  if result=$(cd "$root" && "$timer" --kill-after=5s 30 "$@" </dev/null 2>/dev/null); then return; else status=$?; fi
  case "$status" in
    124|137)
      detail='No workflow was dispatched.'
      [ "$mutates" = false ] || detail='Dispatch outcome is unknown; inspect the listed runs before retrying.'
      fail NVKR-E-TIMEOUT 31 "$failure timed out. $detail" ;;
    *) fail NVKR-E-FETCH-FAILED 20 "$failure" ;;
  esac
}
local_branch() {
  command_result 'Cannot inspect the Git working tree.' false git status --porcelain=v1 --untracked-files=all
  [ -z "$result" ] || fail NVKR-E-USAGE 2 'Commit or set aside all tracked and untracked changes before dispatch.'
  command_result 'A named branch is required; check out a branch instead of detached HEAD.' false git symbolic-ref --quiet --short HEAD
  branch=$result
  if [ -z "$branch" ] || LC_ALL=C printf '%s' "$branch" | LC_ALL=C grep -q '[[:cntrl:][:space:]]'; then
    fail NVKR-E-USAGE 2 'Git returned an invalid branch name.'
  fi
  command_result 'Cannot resolve the local commit.' false git rev-parse --verify 'HEAD^{commit}'
  commit=$result
  [[ $commit =~ ^[0-9a-f]{40}$ ]] || fail NVKR-E-USAGE 2 'HEAD must resolve to one exact GitHub commit SHA.'
}
api() {
  command_result "$2" false gh api --hostname github.com "$1"
  jq -se 'length == 1' <<<"$result" >/dev/null 2>&1 || fail NVKR-E-FETCH-FAILED 20 'GitHub returned malformed JSON; no dispatch was requested.'
  jq -e 'type == "object"' <<<"$result" >/dev/null 2>&1 || fail NVKR-E-FETCH-FAILED 20 'GitHub returned an ambiguous response; no dispatch was requested.'
}
local_branch
initial_branch=$branch initial_commit=$commit
workflow=nvkr-dev.yml
api "repos/$repo/actions/workflows/$workflow" "Cannot find $workflow in $repo. Land it on the default branch first, then check gh auth status and repository access."
jq -e --arg path ".github/workflows/$workflow" '.path == $path and .state == "active"' <<<"$result" >/dev/null ||
  fail NVKR-E-USAGE 2 "$workflow must be registered and active on $repo's default branch."
encoded_branch=$(printf '%s' "$branch" | jq -sRr @uri)
api "repos/$repo/git/ref/heads/$encoded_branch" "Cannot read branch $branch in $repo. Push the branch to this repository and check gh auth status."
jq -e --arg ref "refs/heads/$branch" --arg sha "$commit" '.ref == $ref and (.object | type == "object") and .object.type == "commit" and .object.sha == $sha' <<<"$result" >/dev/null 2>&1 ||
  fail NVKR-E-USAGE 2 "Remote branch $branch does not match local HEAD $commit; push or reconcile it first."
local_branch
[ "$branch" = "$initial_branch" ] && [ "$commit" = "$initial_commit" ] ||
  fail NVKR-E-USAGE 2 'The local branch changed during preflight; inspect it and retry.'
printf 'Ready: %s branch %q at %s\n' "$repo" "$branch" "$commit"
if [ "$check" -eq 1 ]; then printf 'Preflight passed; no workflow dispatched.\n'; exit 0; fi
printf 'Inspect matching runs:'
printf ' %q' gh run list --workflow "$workflow" --branch "$branch" --commit "$commit" --repo "$repo" --limit 5 --json databaseId,headSha,status,conclusion,url
printf '\n'
command_result 'Workflow dispatch failed; check Actions permissions and inspect matching runs before retrying.' true gh workflow run "$workflow" --ref "$branch" --repo "$repo"
printf "Dispatch request accepted. Verify the run's headSha and completion in Actions; branch refs can move after preflight.\n"
