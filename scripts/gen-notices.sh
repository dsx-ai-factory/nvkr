#!/usr/bin/env bash
# Regenerate THIRD_PARTY_NOTICES.md from the modules linked into the nkdctl
# binary. Tools invoked as separate processes are deliberately excluded: they
# are not linked, so Apache-2.0 section 4(d) does not reach them.
#
# The linked-module set is the union over GOOS in {linux, darwin} at
# GOARCH=amd64, so the output does not depend on the host platform. The repo
# has Linux-gated authority code; a module imported only from a `//go:build
# linux` file must appear whether notices are regenerated on Linux CI or on the
# documented macOS dev machine.
#
# Usage:
#   scripts/gen-notices.sh            regenerate THIRD_PARTY_NOTICES.md in place
#   scripts/gen-notices.sh --check    verify the committed file is current (no write)
set -euo pipefail
cd "$(dirname "$0")/.."

command -v go >/dev/null 2>&1 || { echo "gen-notices: go not found on PATH" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "gen-notices: python3 not found on PATH" >&2; exit 1; }

check=false
case "${1:-}" in
  --check) check=true ;;
  "") ;;
  *) echo "gen-notices: unknown argument: $1 (expected --check or none)" >&2; exit 2 ;;
esac

out=THIRD_PARTY_NOTICES.md

# Private temporary files, not predictable paths: the redirect below follows
# symlinks, so a fixed name in a world-writable directory is clobberable by
# another user on a shared machine. Both are removed on exit.
linked=$(mktemp)
generated=$(mktemp)
trap 'rm -f "$linked" "$generated"' EXIT

# The module excludes itself: `go list -deps` reports the main module, and its
# own licence is LICENSE, not a third-party notice. The path stays
# github.com/NVIDIA/nke-recipes because Go module paths do not follow a GitHub
# organization rename.
for goos in linux darwin; do
  GOOS="$goos" GOARCH=amd64 \
    go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/nkdctl
done | sort -u | grep -v '^$' | grep -v '^github.com/NVIDIA/nke-recipes' > "$linked"

python3 scripts/gen_notices.py "$linked" > "$generated"

if [ "$check" = true ]; then
  if ! diff -u "$out" "$generated"; then
    echo "gen-notices: $out is out of date — run 'make notices' and commit the result" >&2
    exit 1
  fi
  echo "$out is up to date"
else
  # Atomic replace: write the full file to a temp path, then move it into place,
  # so a mid-run failure never leaves a truncated committed file.
  mv "$generated" "$out"
  echo "wrote $out ($(wc -l < "$out" | tr -d ' ') lines)"
fi
