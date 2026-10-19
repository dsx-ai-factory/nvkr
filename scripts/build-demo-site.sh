#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Package the self-contained presentation pages without rewriting their bytes.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [ "${1:-}" = --help ]; then
  printf 'Usage: bash scripts/build-demo-site.sh [NEW_OUTPUT_DIR]\nDefault: dist/demo-site in the repository. Output must not already exist.\n'
  exit 0
fi
if [ "$#" -gt 1 ]; then printf 'site: expected at most one output directory\n' >&2; exit 2; fi
OUT=${1:-"$ROOT/dist/demo-site"}
FILES=(index.html nvkr-team-tour.html demo-team-concepts.html demo-team-explorer.html demo-team-walkthrough.html)
for file in "${FILES[@]}"; do
  if [ ! -f "$ROOT/demos/$file" ] || [ -L "$ROOT/demos/$file" ]; then
    printf 'site: expected regular source file: %s\n' "$file" >&2
    exit 2
  fi
done
if [ -e "$OUT" ] || [ -L "$OUT" ]; then
  printf 'site: output already exists; choose a fresh directory: %s\n' "$OUT" >&2
  exit 2
fi
mkdir -p -- "$(dirname "$OUT")"
mkdir -- "$OUT"
for file in "${FILES[@]}"; do cp -- "$ROOT/demos/$file" "$OUT/$file"; done
: > "$OUT/.nojekyll"
printf 'Static demo site: %s\nPreview with:\n  python3 -m http.server 8765 --bind 127.0.0.1 --directory %q\n' "$OUT" "$OUT"
