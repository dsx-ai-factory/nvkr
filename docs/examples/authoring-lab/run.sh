#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Exercise public authoring commands against an isolated, deterministic Git fixture.
set -euo pipefail

die() { printf 'authoring lab: %s\n' "$*" >&2; exit 1; }
usage() { printf 'Usage: bash run.sh --nvkr PATH [--work NEW_DIRECTORY]\n'; }

binary='' work='' have_work=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --nvkr) [[ $# -ge 2 ]] || die '--nvkr requires a path'; binary=$2; shift 2 ;;
    --work) [[ $# -ge 2 && -n $2 ]] || die '--work requires a path'; work=$2; have_work=1; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -n $binary && -f $binary && -x $binary ]] || die '--nvkr must be an executable file'
for tool in git jq; do
  command -v "$tool" >/dev/null 2>&1 || die "missing required command: $tool"
done
timeout_bin=$(command -v gtimeout || command -v timeout) || die 'GNU timeout (timeout or gtimeout) is required'
[[ $("$timeout_bin" --version) == *'GNU coreutils'* ]] || die 'GNU timeout is required'
if command -v sha256sum >/dev/null 2>&1; then
  hash_command=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  hash_command=(shasum -a 256)
else
  die 'sha256sum or shasum is required'
fi

# Bound each command, including its children, and allow five seconds to stop
# after the deadline before GNU timeout kills the process group.
bounded() { "$timeout_bin" --kill-after=5s 60s "$@"; }
here=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
binary=$(CDPATH='' cd -- "$(dirname -- "$binary")" && printf '%s/%s\n' "$PWD" "$(basename -- "$binary")")
if [[ $have_work == 1 ]]; then
  case "$work" in /*) ;; *) work="$PWD/$work" ;; esac
  [[ ! -e $work && ! -L $work ]] || die "workspace already exists: $work"
  bounded mkdir -p -- "$(dirname -- "$work")"
  bounded mkdir -- "$work" || die "cannot create new workspace: $work"
else
  work=$(bounded mktemp -d "${TMPDIR:-/tmp}/nvkr-authoring.XXXXXXXX")
fi
work=$(CDPATH='' cd -- "$work" && pwd -P)
bounded cp -pR -- "$here/catalog" "$here/upstream" "$work/"
bounded cp -p -- "$here/definition.yaml" "$here/environment.yaml" "$here/supplier-doctor.sh" "$work/"
bounded mkdir -- "$work/plans"
cd -- "$work"

# These settings affect only this process and its children, never global config.
# Keep inherited repository paths out of fixture Git and CLI subprocesses.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE \
  GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_PREFIX
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$work/upstream.insteadOf"
export GIT_CONFIG_VALUE_0=https://authoring.example.invalid/platform
export GIT_AUTHOR_NAME='NVKR Authoring Fixture' GIT_AUTHOR_EMAIL=fixture@example.invalid
export GIT_COMMITTER_NAME='NVKR Authoring Fixture' GIT_COMMITTER_EMAIL=fixture@example.invalid
export GIT_AUTHOR_DATE=2025-01-01T00:00:00Z GIT_COMMITTER_DATE=2025-01-01T00:00:00Z

# Keep stdout byte-for-byte; command substitution would strip Plan newlines.
run_checked() {
  local expected=$1 code=$2 output=$3 status=0
  shift 3
  bounded "$@" >"$output" 2>command.stderr || status=$?
  if [[ $status != "$expected" ]]; then
    printf 'authoring lab: exit %s, expected %s: ' "$status" "$expected" >&2
    printf '%q ' "$@" >&2
    printf '\n' >&2
    bounded cat -- "$output" command.stderr >&2
    return 1
  fi
  if [[ -n $code ]] && ! bounded grep -Fq -- "$code" command.stderr; then
    bounded cat -- command.stderr >&2
    die "missing expected error code: $code"
  fi
}
git_fixture() {
  run_checked 0 '' command.stdout git -C "$work/upstream" \
    -c commit.gpgsign=false -c tag.gpgSign=false -c core.autocrlf=false "$@"
}
cli() { run_checked 0 '' command.stdout "$binary" "$@"; }
cli_to() { local output=$1; shift; run_checked 0 '' "$output" "$binary" "$@"; }
compile_plan() {
  cli_to "plans/$1.json" run plan --definition "${2:-definition.yaml}" \
    --environment environment.yaml --json
}
unchanged_lock() {
  bounded cmp -s original-pins.lock.yaml catalog/pins.lock.yaml || die "$1 changed the lock"
}

git_fixture init -q --template= --object-format=sha1 --initial-branch=main
git_fixture add .
git_fixture commit -q -m 'fixture 1.0.0'
for tag in app/v1.0.0 tools/v1.0.0 platform/v1.0.0; do git_fixture tag "$tag"; done
cli catalog pin --dir catalog --tag 1.0.0 platform
bounded cp -- catalog/pins.lock.yaml original-pins.lock.yaml
cli catalog pin --dir catalog --tag 1.0.0 platform
unchanged_lock 'no-op pin'
run_checked 13 NVKR-E-UNSUPPORTED command.stdout "$binary" catalog pin --dir catalog --tag 1.0.0 app
unchanged_lock 'refused derived-member pin'

cli_to definition.json definition inspect --definition definition.yaml
compile_plan baseline
compile_plan baseline-repeat
bounded cmp -s plans/baseline.json plans/baseline-repeat.json || die 'plan regeneration drifted'
bounded jq -e '
  def require($condition; $message): if $condition then . else error($message) end;
  . as $plan | (.builds | map({key: .name, value: .}) | from_entries) as $builds
  | require(($builds | length) == 7; "baseline must retain all seven outputs")
  | require($builds["cli-amd64"].buildDigest != $builds["cli-arm64"].buildDigest;
            "target architecture did not change Build identity")
  | require(([$builds.tools.executionGraph.nodes[] | select(.kind == "fetch")] | length) == 1;
            "the grouped tools output must share one source fetch")
  | require(([$plan.graph.nodes[] | select(.id == "package/controller-chart")
              | .inputs[] | select(. == "package/controller")] | length) == 1;
            "chart package lost its exact image package dependency")
  | true
' plans/baseline.json >/dev/null

supplier_hash=$(bounded "${hash_command[@]}" <supplier-doctor.sh)
supplier_hash=${supplier_hash%% *}
[[ $supplier_hash =~ ^[0-9a-f]{64}$ ]] || die 'invalid supplier SHA-256 output'
cli_to catalog-digest.txt catalog digest --data catalog
catalog_digest=$(<catalog-digest.txt)
# These are complete authored inputs and reports, never canonical identities.
bounded jq -nS --arg digest "$catalog_digest" --arg supplier "sha256:$supplier_hash" '{
  apiVersion: "nvkr.dev/catalog-overlay/v3", kind: "CatalogOverlay", name: "supplier-doctor",
  requiresCatalogDigest: $digest,
  components: {doctor: {version: "1.0.0", sourceDateEpoch: 1735689600,
    prebuilt: {url: "https://authoring.example.invalid/supplier/doctor.sh",
      digests: {amd64: $supplier, arm64: $supplier}, evidence: {none: true}}}}
}' >supplier.json
bounded jq -S '.catalog.overlays = ["supplier.json"]' definition.json >whole-producer.json
bounded jq -S '(.outputs[] | select(.name == "tools")).catalog =
  {base: "./catalog", overlays: ["supplier.json"]}' definition.json >output-catalog.json
bounded jq -S '(.outputs[] | select(.name == "diagnostics")).components = ["doctor-alt"]' \
  definition.json >output-selection.json
for scenario in whole-producer output-catalog output-selection; do compile_plan "$scenario" "$scenario.json"; done
bounded jq -e '[.builds[] | select(.name == "tools") | .executionGraph.nodes[]
  | select(.component == "doctor") | .kind] == ["pull"]
  or error("whole replacement did not switch from build to acquisition")' plans/whole-producer.json >/dev/null

bounded cat >upstream/cli.sh <<'EOF'
#!/bin/sh
printf 'authoring lab CLI 1.0.1\n'
EOF
git_fixture add cli.sh
git_fixture commit -q -m 'fixture app 1.0.1'
git_fixture tag app/v1.0.1
bounded cat >upstream/versions.yaml <<'EOF'
dependencies:
- {name: app, version: 1.0.1}
- {name: tools, version: 1.0.0}
EOF
git_fixture add versions.yaml
git_fixture commit -q -m 'fixture platform 1.0.1'
git_fixture tag platform/v1.0.1
run_checked 3 NVKR-E-DIFF pin-preview.txt "$binary" catalog bump --dir catalog --check platform
unchanged_lock 'pin preview'
cli catalog bump --dir catalog platform
compile_plan source-pin
run_checked 43 NVKR-E-UNVERIFIED-REF command.stdout "$binary" run plan --definition whole-producer.json \
  --environment environment.yaml --json

bounded jq -nS --slurpfile baseline plans/baseline.json --slurpfile source plans/source-pin.json \
  --slurpfile producer plans/whole-producer.json --slurpfile catalog plans/output-catalog.json \
  --slurpfile selection plans/output-selection.json '
  def digests: .builds | map({key: .name, value: .buildDigest}) | from_entries;
  def scenario($before; $plan; $expected):
    ($plan | digests) as $after
    | if ($before | keys) != ($after | keys) then error("required output membership changed") else . end
    | [($before | keys[]) as $name | select($before[$name] != $after[$name]) | $name] as $changed
    | if $changed != ($expected | sort) then
        error("changed outputs \($changed), expected \($expected | sort)") else . end
    | {changedOutputs: $changed, outputs: ($before | to_entries
        | map(.key as $name | {key: $name, value: {before: .value, after: $after[$name]}}) | from_entries)};
  ($baseline[0] | digests) as $before
  | {scope: "authoring/compilation fixture; no artifact execution or release acceptance", scenarios: {
      "source-pin": scenario($before; $source[0]; ["cli-amd64", "cli-arm64", "controller", "controller-chart"]),
      "whole-producer": scenario($before; $producer[0]; ["diagnostics", "tools"]),
      "output-catalog": scenario($before; $catalog[0]; ["tools"]),
      "output-selection": scenario($before; $selection[0]; ["diagnostics"])
    }}
' >identity-report.json
for scenario in source-pin whole-producer output-catalog output-selection; do
  bounded jq -r --arg scenario "$scenario" \
    '"\($scenario): changed Build Digests: \(.scenarios[$scenario].changedOutputs | join(", "))"' identity-report.json
done
printf 'Authoring checks passed. Plans and identity report: %s\n' "$work"
printf 'UNQUALIFIED fixture: no container build, download, publication, or installation executed.\n'
