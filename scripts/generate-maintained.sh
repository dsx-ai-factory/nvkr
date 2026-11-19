#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Admit the complete authored inventory before running any generator.
set -euo pipefail
fail() { printf 'NVKR-E-PIPELINE-DRIFT: maintained generation failed: %s\n' "$*" >&2; exit 1; }
usage() { printf 'Usage: generate-maintained.sh [--check] [--group main|nightly|development] [--projection-pilot in-process]\n'; }
check=0 group='' pilot=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check) check=1; shift ;;
    --group=*|--projection-pilot=*) set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
    --) shift; [ "$#" -eq 0 ] || { usage >&2; exit 2; } ;;
    --group|--projection-pilot)
      option=$1
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      case "$option:$2" in
        --group:main|--group:nightly|--group:development) group=$2 ;;
        --projection-pilot:in-process) pilot=$2 ;;
        *) usage >&2; exit 2 ;;
      esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
command -v jq >/dev/null || fail 'jq is required'
if command -v timeout >/dev/null; then timer=timeout
elif command -v gtimeout >/dev/null; then timer=gtimeout
else fail 'timeout or gtimeout is required'; fi
inventory=$(jq -sce '
  def path: type == "string" and length > 0 and
    (startswith("/") | not) and (split("/") | index("..") | not) and
    (contains("\u0000") | not);
  (if length == 1 then .[0] else error("expected one inventory document") end) |
  if type == "array" and length > 0 and
    all(.[]; type == "object" and
      ((keys - ["bundle","group","definition","environment","qualificationPolicy","runtime"]) | length == 0) and
      (.bundle | type == "string" and test("^nvkr-[a-z0-9-]*\\z")) and
      (.group | . == "main" or . == "nightly" or . == "development") and
      ((has("runtime") | not) or (.runtime | type == "boolean")) and
      (.definition | path) and (.environment | path) and
      ((has("qualificationPolicy") | not) or (.qualificationPolicy | path))) and
    (map(.bundle) == (map(.bundle) | sort | unique))
  then . else error("invalid maintained generation inventory") end
' "$root/inputs/policies/maintained-generation.json") || fail 'invalid maintained generation inventory'
while IFS= read -r -d '' path; do
  [ -f "$root/$path" ] || fail "invalid maintained input path: $path"
done < <(jq -j '.[] | (.definition, .environment, .qualificationPolicy // empty) | ., "\u0000"' <<<"$inventory")
# Read NUL-delimited strings so whitespace in authored paths stays literal.
while IFS= read -r entry; do
  IFS= read -r -d '' definition < <(jq -j '.definition, "\u0000"' <<<"$entry")
  IFS= read -r -d '' environment < <(jq -j '.environment, "\u0000"' <<<"$entry")
  IFS= read -r -d '' policy < <(jq -j '.qualificationPolicy // "", "\u0000"' <<<"$entry")
  args=(run generate --engine github --definition "$definition" --environment "$environment" --root .)
  [ -z "$policy" ] || args+=(--qualification-policy "$policy")
  runtime=$(jq -r '.runtime // false' <<<"$entry")
  if [ "$check" -eq 1 ]; then
    args+=(--check)
    [ "$runtime" = false ] || args+=(--runtime)
  fi
  if [ -n "$pilot" ] && [ "$runtime" = false ]; then args+=(--projection-pilot "$pilot"); fi
  (cd "$root" && "$timer" 900 "$root/bin/nvkr-exec" "${args[@]}") || fail 'generator failed or timed out'
done < <(jq -c --arg group "$group" '.[] | select($group == "" or .group == $group)' <<<"$inventory")
