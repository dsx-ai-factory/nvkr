#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# CUJ: a catalog maintainer previews and commits one machine-managed source pin
# update, then inspects the exact IR change and a rejected hand edit.

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

WORK=${WORK:-.nkd/demo-catalog-change}

usage() {
  cat <<'EOF'
Usage: scripts/demo-catalog-change.sh [options]

Exercise the catalog-maintainer workflow in an isolated catalog copy. A local Git
upstream provides deterministic v1.34.0/v1.34.1 tags, so the journey needs no
network or Docker:
  validate -> preview bump without writes -> pin -> validate -> inspect IR diff
    -> reject a malformed hand edit -> restore the valid lock

Options:
  --work DIR   reusable owned output directory (default: .nkd/demo-catalog-change)
  -h, --help   show this help

Environment defaults: WORK, NKDCTL_BIN.
The resulting catalog is an UNQUALIFIED maintenance fixture, not a signed catalog
release or operator trust object. The repository's test fixture is untouched.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --work) [ "$#" -ge 2 ] || demo_die "missing --work value"; WORK=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) demo_die "unknown flag: $1 (run scripts/demo-catalog-change.sh --help)" ;;
  esac
done

demo_prepare_nkdctl
demo_need_command git
demo_need_command diff
demo_need_command cmp
WORK=$(demo_absolute_path "$WORK")
demo_prepare_work "$WORK" "catalog-change" "$(demo_absolute_path .nkd/demo-catalog-change)"
CATALOG="$WORK/catalog"
SEED="$WORK/upstream-work"
UPSTREAM="$WORK/cri-tools.git"
mkdir -p "$CATALOG"
cp -R testdata/catalogs/kubernetes-layered/. "$CATALOG/"

demo_section "1/6  Create a deterministic local upstream and isolated catalog"
git init -q "$SEED"
git -C "$SEED" config user.name 'NKD Demo'
git -C "$SEED" config user.email 'nkd-demo@example.invalid'
git -C "$SEED" config commit.gpgsign false
git -C "$SEED" config tag.gpgSign false
printf 'fixture release 1.34.0\n' > "$SEED/release.txt"
git -C "$SEED" add release.txt
GIT_AUTHOR_DATE='2025-01-01T00:00:00Z' GIT_COMMITTER_DATE='2025-01-01T00:00:00Z' \
  git -C "$SEED" commit -q -m 'fixture v1.34.0'
git -C "$SEED" tag v1.34.0
printf 'fixture release 1.34.1\n' > "$SEED/release.txt"
git -C "$SEED" add release.txt
GIT_AUTHOR_DATE='2025-01-02T00:00:00Z' GIT_COMMITTER_DATE='2025-01-02T00:00:00Z' \
  git -C "$SEED" commit -q -m 'fixture v1.34.1'
git -C "$SEED" tag v1.34.1
git init --bare -q "$UPSTREAM"
git -C "$SEED" remote add origin "$UPSTREAM"
git -C "$SEED" push -q origin HEAD:refs/heads/main --tags

# Redirect only the cri-tools URL declared by the copied catalog. Every git process
# spawned by nkdctl sees the deterministic local upstream; no network is contacted.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="url.$UPSTREAM.insteadOf"
export GIT_CONFIG_VALUE_0='https://github.com/kubernetes-sigs/cri-tools'

"$NKD" catalog validate --data "$CATALOG"
OLD_DIGEST=$("$NKD" resolve --data "$CATALOG" --component crictl --platform linux/amd64 -o digest)
"$NKD" resolve --data "$CATALOG" --component crictl --platform linux/amd64 -o nodes > "$WORK/ir-before.json"
LOCK="$CATALOG/pins.lock.yaml"
LOCK_BEFORE=$(demo_sha256 "$LOCK")

demo_section "2/6  Preview the newest constraint-matching pin without writing"
set +e
CHECK_OUTPUT=$("$NKD" catalog bump --dir "$CATALOG" --check crictl 2>&1)
CHECK_STATUS=$?
set -e
[ "$CHECK_STATUS" -eq 3 ] || demo_die "catalog bump --check exited $CHECK_STATUS; want diff status 3: $CHECK_OUTPUT"
printf '%s\n' "$CHECK_OUTPUT"
[ "$(demo_sha256 "$LOCK")" = "$LOCK_BEFORE" ] || demo_die "catalog bump --check modified the lock"

demo_section "3/6  Commit the exact v1.34.1 pin through the lock writer"
"$NKD" catalog pin --dir "$CATALOG" --tag v1.34.1 crictl
"$NKD" catalog validate --data "$CATALOG"
NEW_DIGEST=$("$NKD" resolve --data "$CATALOG" --component crictl --platform linux/amd64 -o digest)
[ "$NEW_DIGEST" != "$OLD_DIGEST" ] || demo_die "source pin changed without changing buildDigest"
"$NKD" resolve --data "$CATALOG" --component crictl --platform linux/amd64 -o nodes > "$WORK/ir-after.json"

demo_section "4/6  Inspect the resolved-IR change"
set +e
diff -u "$WORK/ir-before.json" "$WORK/ir-after.json" > "$WORK/ir-diff.txt"
DIFF_STATUS=$?
set -e
[ "$DIFF_STATUS" -eq 1 ] || demo_die "IR diff exited $DIFF_STATUS; want changed bytes"
cat "$WORK/ir-diff.txt"
"$NKD" resolve --data "$CATALOG" --component crictl --platform linux/amd64 \
  -o nodes > "$WORK/ir-repeated.json"
cmp "$WORK/ir-after.json" "$WORK/ir-repeated.json"

demo_section "5/6  Reject a malformed hand edit"
cp "$LOCK" "$WORK/pins.lock.valid.yaml"
awk 'BEGIN { changed=0 } !changed && /commit: [0-9a-f][0-9a-f]*/ { sub(/commit: [0-9a-f][0-9a-f]*/, "commit: deadbeef"); changed=1 } { print }' \
  "$LOCK" > "$WORK/pins.lock.tampered.yaml"
cp "$WORK/pins.lock.tampered.yaml" "$LOCK"
if "$NKD" catalog validate --data "$CATALOG" > "$WORK/hand-edit.out" 2>&1; then
  demo_die "malformed hand-edited pin unexpectedly validated"
fi
cat "$WORK/hand-edit.out"
cp "$WORK/pins.lock.valid.yaml" "$LOCK"
"$NKD" catalog validate --data "$CATALOG"

demo_section "6/6  Summarize the identity change"
cat <<EOF
UNQUALIFIED CATALOG-MAINTENANCE DEMO COMPLETE
  isolated catalog: $CATALOG
  old buildDigest: $OLD_DIGEST
  new buildDigest: $NEW_DIGEST
  IR diff:          $WORK/ir-diff.txt

The machine-managed lock changed only after the preview gate, the resulting build
identity changed, and a malformed hand edit failed closed. No signed catalog release
or production promotion was created.
EOF
