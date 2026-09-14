#!/usr/bin/env bash
# Regenerate THIRD_PARTY_NOTICES.md from the modules linked into the nkdctl
# binary. Tools invoked as separate processes are deliberately excluded: they
# are not linked, so Apache-2.0 section 4(d) does not reach them.
set -euo pipefail
cd "$(dirname "$0")/.."
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/nkdctl \
  | sort -u | grep -v '^$' | grep -v '^github.com/NVIDIA/nke-recipes' > /tmp/nkd-linked.txt
python3 scripts/gen_notices.py /tmp/nkd-linked.txt > THIRD_PARTY_NOTICES.md
echo "wrote THIRD_PARTY_NOTICES.md ($(wc -l < THIRD_PARTY_NOTICES.md | tr -d ' ') lines)"
