#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Optional offline diagnostics; no release or merge authority.
set -euo pipefail
zizmor_version=1.32.0
zizmor_error() { printf '%s\n' "$*" >&2; return 1; }
zizmor_timer() {
  if command -v timeout >/dev/null; then printf timeout
  elif command -v gtimeout >/dev/null; then printf gtimeout
  else zizmor_error 'timeout or gtimeout is required'; fi
}
zizmor_collect() {
  local root=$1 work=$2 inputs=$3 name
  "$timer" --kill-after=5s 15 git -C "$root" ls-files -z --cached --others --exclude-standard -- .github >"$work/list" || return 1
  # jq replaces malformed UTF-8. Reject any such replacement before filtering.
  jq -jRs . "$work/list" >"$work/list-utf8" || return 1
  cmp -s "$work/list" "$work/list-utf8" || { zizmor_error 'input names are not UTF-8'; return 1; }
  jq -Rs '
    split("\u0000") | map(select(length > 0) | select(
      (split("/") | length == 3 and .[0] == ".github" and .[1] == "workflows" and (.[2] | test("\\.ya?ml$"))) or
      (split("/") | last | . == "action.yml" or . == "action.yaml"))) | sort | unique
  ' "$work/list" >"$inputs" || return 1
  jq -e 'length > 0' "$inputs" >/dev/null || { zizmor_error 'no workflow/action inputs collected'; return 1; }
  jq -j '.[] | ., "\u0000"' "$inputs" >"$work/inputs" || return 1
  while IFS= read -r -d '' name; do
    if [ -L "$root/$name" ] || [ ! -f "$root/$name" ]; then zizmor_error "missing or non-regular input: $name"; return 1; fi
    cat "$root/$name" >/dev/null || return 1
  done <"$work/inputs"
}
zizmor_pin() {
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) printf '%s %s\n' x86_64-unknown-linux-gnu bb110cc765eca9cd7efb2e284a956be5974c60027b0970f16397d86208a9957b ;;
    Darwin/arm64) printf '%s %s\n' aarch64-apple-darwin 6efed18c4539a663fd3c851a2be9533fd8456c4e646d9416206da059154a0d3c ;;
    *) zizmor_error 'unsupported zizmor platform'; return 1 ;;
  esac
}
zizmor_install() {
  local work=$1 target checksum archive listing pin
  pin=$(zizmor_pin) || return 1
  read -r target checksum <<<"$pin"
  archive=$work/zizmor.tar.gz
  "$timer" --kill-after=5s 70 curl --fail --silent --show-error --location --connect-timeout 10 --max-time 60 \
    "https://github.com/zizmorcore/zizmor/releases/download/v$zizmor_version/zizmor-$target.tar.gz" --output "$archive" >"$work/download.txt" 2>&1 ||
    { zizmor_error 'zizmor download failed; see download.txt'; return 1; }
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c - >"$work/checksum.txt" 2>&1 ||
    { zizmor_error 'zizmor archive checksum mismatch; executable not extracted'; return 1; }
  listing=$(tar -tvzf "$archive" zizmor) || return 1
  # Extract only one regular member to stdout, never archive-controlled paths.
  case "$listing" in -*) ;; *) zizmor_error 'zizmor archive member is not a regular file'; return 1 ;; esac
  [ "$(printf '%s\n' "$listing" | wc -l | tr -d ' ')" = 1 ] || { zizmor_error 'ambiguous zizmor archive member'; return 1; }
  tar -xOzf "$archive" zizmor >"$work/zizmor" || return 1
  chmod 700 "$work/zizmor" || return 1
  printf '%s\n' "$work/zizmor"
}
zizmor_summarize() {
  local raw=$1 output=$2
  jq -jRs . "$raw" | cmp -s "$raw" - || { zizmor_error 'findings are not UTF-8'; return 1; }
  jq -er -s '
    def finding:
      type == "object" and (.ident | type == "string") and (.desc | type == "string") and
      (.determinations.severity | type == "string") and (.ignored | type == "boolean") and
      (.locations | type == "array") and all(.locations[];
        (.symbolic.key.Local.verbatim_path | type == "string") and
        (.symbolic.annotation | type == "string") and
        (.concrete.location.start_point.row | type == "number"));
    if length == 1 and (.[0] | type == "array" and all(.[]; finding)) then .[0]
    else error("invalid zizmor JSON v1 findings array") end |
    .[] | (.determinations.severity + " " + .ident + ": " + .desc + (if .ignored then " [ignored]" else "" end)),
      (.locations[] | "  \(.symbolic.key.Local.verbatim_path):\(.concrete.location.start_point.row + 1): \(.symbolic.annotation)")
  ' "$raw" >"$output" && return 0
  # An empty findings array emits no rows; jq -e distinguishes that from a
  # parse/schema error with exit status 4. Validate it explicitly.
  jq -e -s 'length == 1 and .[0] == []' "$raw" >/dev/null 2>&1 || return 1
  printf '\n' >"$output"
}
zizmor_collect_report() {
  local root=$1 work=$2 attempt=$3 binary status count input_count name
  local args=()
  timer=$(zizmor_timer) || return 1
  zizmor_collect "$root" "$work" "$attempt/inputs.json" || return 1
  binary=$(zizmor_install "$work") || return 1
  while IFS= read -r -d '' name; do args+=("$name"); done <"$work/inputs"
  if (cd "$root" && RUST_LOG=info "$timer" --kill-after=5s 120 "$binary" --offline --strict-collection --no-progress --color never --format json-v1 --no-exit-codes "${args[@]}") >"$attempt/findings.json" 2>"$attempt/stderr.txt"; then :
  else status=$?; zizmor_error "zizmor tool/collection failure (exit $status)"; return 1; fi
  zizmor_summarize "$attempt/findings.json" "$attempt/findings.txt" || return 1
  jq -jRs . "$attempt/stderr.txt" | cmp -s "$attempt/stderr.txt" - || { zizmor_error 'audit diagnostics are not UTF-8'; return 1; }
  if LC_ALL=C grep -Eq '^[[:blank:]]*(WARN|ERROR)[[:blank:]]+' "$attempt/stderr.txt"; then
    zizmor_error 'zizmor emitted audit warnings/errors; see stderr.txt and partial findings'; return 1
  fi
  count=$(jq length "$attempt/findings.json") || return 1
  input_count=$(jq length "$attempt/inputs.json") || return 1
  verdict=FINDINGS
  [ "$count" != 0 ] || verdict='NO FINDINGS'
  detail="$count findings (including ignored entries); $input_count inputs"
}
zizmor_report() {
  local root=$1 output=$2 attempt work verdict=INCOMPLETE detail='collection did not finish' timer=''
  mkdir -p "$output" || return 1
  output=$(cd "$output" && pwd -P) || return 1
  attempt=$(mktemp -d "$output/attempt-XXXXXX") || return 1
  work=$(mktemp -d "${TMPDIR:-/tmp}/nvkr-zizmor-XXXXXX") || return 1
  printf 'zizmor %s: INCOMPLETE\n' "$zizmor_version" >"$attempt/summary.txt" || return 1
  if zizmor_collect_report "$root" "$work" "$attempt" 2>"$work/error.txt"; then :
  else detail=$(cat "$work/error.txt"); fi
  if [ -f "$work/download.txt" ]; then cp "$work/download.txt" "$attempt/download.txt" || return 1; fi
  printf 'zizmor %s: %s\n%s\nReport-only; offline audits only. Online checks omitted.\nTool/download scratch: %s\n' "$zizmor_version" "$verdict" "$detail" "$work" >"$attempt/summary.txt" || return 1
  printf 'zizmor %s: %s; diagnostics: %s\n' "$zizmor_version" "$verdict" "$attempt"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf 'zizmor %s: **%s** (report-only, offline). See zizmor-report artifact.\n' "$zizmor_version" "$verdict" >>"$GITHUB_STEP_SUMMARY" || return 1
  fi
  [ "$verdict" != INCOMPLETE ]
}
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
  zizmor_report "$root" "${ZIZMOR_REPORT_DIR:-$root/.nvkr/zizmor-report}"
fi
