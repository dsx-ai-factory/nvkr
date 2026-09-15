#!/usr/bin/env bash
# Regenerate THIRD_PARTY_NOTICES.md from the modules linked into the nkdctl
# binary. Tools invoked as separate processes are deliberately excluded: they
# are not linked, so Apache-2.0 section 4(d) does not reach them.
set -euo pipefail
cd "$(dirname "$0")/.."

# A private temporary file, not a predictable path: the redirect below follows
# symlinks, so a fixed name in a world-writable directory is clobberable by
# another user on a shared machine.
linked=$(mktemp)
trap 'rm -f "$linked"' EXIT

# The module excludes itself: `go list -deps` reports the main module, and its
# own licence is LICENSE, not a third-party notice. The path stays
# github.com/NVIDIA/nke-recipes because Go module paths do not follow a GitHub
# organization rename.
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/nkdctl \
  | sort -u | grep -v '^$' | grep -v '^github.com/NVIDIA/nke-recipes' > "$linked"
python3 scripts/gen_notices.py "$linked" > THIRD_PARTY_NOTICES.md
echo "wrote THIRD_PARTY_NOTICES.md ($(wc -l < THIRD_PARTY_NOTICES.md | tr -d ' ') lines)"
