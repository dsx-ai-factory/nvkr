#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build the hardware E2E stream matrix by calling the NVKE harness's own
# `matrix` step, so the harness decides what a stream is, then admit it for this workflow:
# streams of the harness's `forge` provider only (no DSX credentials exist here) and amd64 before arm64.
#
#   hardware-e2e-matrix.sh HARNESS_SCRIPT PLAN
#
# HARNESS_SCRIPT is release/hack/qualify-e2e.sh from the harness checkout; PLAN is the
# substituted plan. Prints {"include":[...]} on stdout, compact, in run order. The
# workflow runs the legs with max-parallel 1 in that order, which is GitHub's matrix
# expansion order and not a scheduling guarantee; the run summary records the order
# the legs actually started in.
set -euo pipefail

fail() { printf 'nvkr: [%s] %s\n' "$1" "$2" >&2; exit "$3"; }
[[ $# == 2 ]] || fail NVKR-E-USAGE 'hardware-e2e-matrix.sh HARNESS_SCRIPT PLAN' 2
harness=$1 plan=$2
command -v jq >/dev/null || fail NVKR-E-ENGINE-CAPABILITY 'missing jq' 29
[[ -f $harness && -x $harness && ! -L $harness ]] ||
  fail NVKR-E-USAGE "harness script is not an executable regular file: $harness" 2
[[ -f $plan && ! -L $plan ]] || fail NVKR-E-USAGE "plan is not a regular file: $plan" 2
# The harness changes into its own checkout before it reads the plan, so a relative
# path the caller meant against the current directory would name some other file.
absolute() { printf '%s/%s' "$(cd "$(dirname "$1")" && pwd -P)" "$(basename "$1")"; }
harness=$(absolute "$harness")
plan=$(absolute "$plan")

# The harness reads STEP_ENV for the steps that emit values; `matrix` emits none.
matrix=$(STEP_ENV=/dev/null "$harness" matrix "$plan") ||
  fail NVKR-E-PREFLIGHT-FAILED 'the harness matrix step failed for this plan' 17
jq -e '(.include | type == "array") and (.include | length > 0)' <<<"$matrix" >/dev/null ||
  fail NVKR-E-UNSUPPORTED 'the harness matrix named no streams' 13

# Integration boundary: "forge" is the pinned harness's own name for this provider, not NVKR's.
# Each entry is named by an interpolation that is never empty, so an entry with no stream name is still
# listed: joining the bare names would print a null as nothing and let that entry through.
others=$(jq -r '[.include[] | select(.provider != "forge") | "\(.combination)/\(.stream)=\(.provider)"] | join(", ")' <<<"$matrix")
[[ -z $others ]] || fail NVKR-E-UNSUPPORTED "this workflow carries the hardware site's credentials only; the plan also names: $others" 13
# A stream is named by its combination and its stream, and the run summary joins evidence on that pair.
unnamed=$(jq -r '[.include | to_entries[] | select((.value.combination | type) != "string" or .value.combination == "" or (.value.stream | type) != "string" or .value.stream == "") | .key] | join(", ")' <<<"$matrix")
[[ -z $unnamed ]] || fail NVKR-E-UNSUPPORTED "the matrix has entries without a combination and stream name, at positions: $unnamed" 13
unknown=$(jq -r '[.include[] | select(.arch != "amd64" and .arch != "arm64") | "\(.stream)=\(.arch)"] | join(", ")' <<<"$matrix")
[[ -z $unknown ]] || fail NVKR-E-UNSUPPORTED "streams with an architecture other than amd64 or arm64: $unknown" 13
duplicate=$(jq -r '[.include | group_by([.combination, .stream])[] | select(length > 1) | .[0].stream] | join(", ")' <<<"$matrix")
[[ -z $duplicate ]] || fail NVKR-E-UNSUPPORTED "the matrix names a stream more than once: $duplicate" 13

# amd64 before arm64. sort_by is stable, so each architecture keeps the plan's order.
jq -c '{include: (.include | sort_by(if .arch == "amd64" then 0 else 1 end))}' <<<"$matrix"
