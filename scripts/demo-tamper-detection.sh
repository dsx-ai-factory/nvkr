#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# CUJ: a security reviewer changes bytes at several public boundaries and observes
# digest-integrity, immutable-publication, and archive-shape failures.

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

WORK=${WORK:-.nvkr/demo-tamper-detection}

usage() {
  cat <<'EOF'
Usage: scripts/demo-tamper-detection.sh [options]

Create small local fixtures and demonstrate fail-closed behavior for:
  - changed artifact digests in a local diagnostic OCI publication;
  - rejection of extraction from that damaged publication;
  - corrupt transport archives, destination overwrite, and escaping symlinks.

Options:
  --work DIR   reusable owned output directory (default: .nvkr/demo-tamper-detection)
  -h, --help   show this help

Environment defaults: WORK, NVKR_BIN.
All records and carriers in this script are UNQUALIFIED fixtures. Expected failures
are the result; the script does not claim a build, signature, or release verdict.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --work) [ "$#" -ge 2 ] || demo_die "missing --work value"; WORK=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) demo_die "unknown flag: $1 (run scripts/demo-tamper-detection.sh --help)" ;;
  esac
done

demo_prepare_nvkr
demo_need_command cmp
demo_need_command dd
demo_need_command python3
WORK=$(demo_absolute_path "$WORK")
demo_prepare_work "$WORK" "tamper-detection" "$(demo_absolute_path .nvkr/demo-tamper-detection)"
demo_section "1/4  Inspect and extract an intact diagnostic publication"
mkdir -p "$WORK/artifacts"
ARTIFACT="$WORK/artifacts/demo-artifact"
printf 'original artifact bytes\n' > "$ARTIFACT"
BUNDLE="$WORK/bundle"
LAYOUT="$WORK/publication"
"$NVKR" publish bundle --file "demo-artifact=$ARTIFACT" --arch amd64 --out "$BUNDLE"
"$NVKR" publish root --bundle "demo=$BUNDLE" --out "$LAYOUT"
"$NVKR" publish inspect --layout "$LAYOUT"
"$NVKR" publish extract --layout "$LAYOUT" --bundle demo --out "$WORK/intact"
cmp "$ARTIFACT" "$WORK/intact/demo-artifact"

demo_section "2/4  Reject changed artifact bytes before extraction"
PAYLOAD_DIGEST=$(python3 - "$BUNDLE" <<'PYTHON'
import json
import pathlib
import sys
root = pathlib.Path(sys.argv[1])
index = json.loads((root / "index.json").read_text())
manifest_digest = index["manifests"][0]["digest"].split(":", 1)[1]
manifest = json.loads((root / "blobs" / "sha256" / manifest_digest).read_text())
print(manifest["layers"][0]["digest"].split(":", 1)[1])
PYTHON
)
printf 'tampered artifact bytes\n' >> "$LAYOUT/blobs/sha256/$PAYLOAD_DIGEST"
if "$NVKR" publish extract --layout "$LAYOUT" --bundle demo --out "$WORK/forbidden" > "$WORK/artifact-tamper.out" 2>&1; then
  demo_die "changed artifact bytes unexpectedly extracted"
fi
[ ! -e "$WORK/forbidden" ] || demo_die "damaged publication created an extraction destination"
cat "$WORK/artifact-tamper.out"

demo_section "3/4  Exercise exact, immutable transport publication"
PRODUCER="$WORK/producer"
CONSUMER="$WORK/consumer"
mkdir -p "$PRODUCER/payload"
printf 'transported exact bytes\n' > "$PRODUCER/payload/artifact.bin"
ARCHIVE="$PRODUCER/carriers/good.tar"
"$NVKR" transport-pack --root "$PRODUCER" --archive "$ARCHIVE" --path payload/artifact.bin
"$NVKR" transport-unpack --root "$CONSUMER" --archive "$ARCHIVE" --path payload/artifact.bin
cmp "$PRODUCER/payload/artifact.bin" "$CONSUMER/payload/artifact.bin"
if "$NVKR" transport-unpack --root "$CONSUMER" --archive "$ARCHIVE" --path payload/artifact.bin > "$WORK/overwrite.out" 2>&1; then
  demo_die "transport unpack overwrote an existing destination"
fi
cat "$WORK/overwrite.out"

demo_section "4/4  Reject a corrupt carrier and an escaping symlink"
BAD_ARCHIVE="$WORK/corrupt.tar"
cp "$ARCHIVE" "$BAD_ARCHIVE"
printf 'X' | dd of="$BAD_ARCHIVE" bs=1 seek=0 conv=notrunc 2>/dev/null
BAD_ROOT="$WORK/corrupt-consumer"
if "$NVKR" transport-unpack --root "$BAD_ROOT" --archive "$BAD_ARCHIVE" --path payload/artifact.bin > "$WORK/corrupt.out" 2>&1; then
  demo_die "corrupt transport archive unexpectedly unpacked"
fi
[ ! -e "$BAD_ROOT/payload" ] || demo_die "corrupt archive published a partial destination"
cat "$WORK/corrupt.out"

printf 'outside bytes\n' > "$WORK/outside"
ln -s ../outside "$PRODUCER/escape"
ESCAPE_ARCHIVE="$PRODUCER/carriers/escape.tar"
if "$NVKR" transport-pack --root "$PRODUCER" --archive "$ESCAPE_ARCHIVE" --path escape > "$WORK/symlink.out" 2>&1; then
  demo_die "escaping transport symlink unexpectedly packed"
fi
[ ! -e "$ESCAPE_ARCHIVE" ] || demo_die "rejected symlink published an archive"
cat "$WORK/symlink.out"

cat <<EOF

UNQUALIFIED TAMPER-DETECTION JOURNEY COMPLETE
  damaged publication: $LAYOUT
  intact extraction:   $WORK/intact
  valid carrier:       $ARCHIVE

Changed publication bytes were rejected before extraction, and unsafe carrier
publication failed without replacing or partially publishing declared roots.
These local fixtures do not establish signatures or Release authority. Consumers
must still authenticate and re-hash transported artifacts under their policy.
EOF
