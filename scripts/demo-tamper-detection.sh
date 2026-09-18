#!/usr/bin/env bash
# CUJ: a security reviewer changes bytes at several public boundaries and observes
# exact-scope, immutable-publication, and archive-shape failures.

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/.."
# shellcheck source=scripts/_demo-common.sh
source "$SCRIPT_DIR/_demo-common.sh"

WORK=${WORK:-.nkd/demo-tamper-detection}

usage() {
  cat <<'EOF'
Usage: scripts/demo-tamper-detection.sh [options]

Create small local fixtures and demonstrate fail-closed behavior for:
  - changed artifact digests in exact-scope diagnostic observations;
  - diagnostic observations presented to a qualified SBOM consumer;
  - corrupt transport archives, destination overwrite, and escaping symlinks.

Options:
  --work DIR   reusable owned output directory (default: .nkd/demo-tamper-detection)
  -h, --help   show this help

Environment defaults: WORK, NKDCTL_BIN.
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

demo_prepare_nkdctl
demo_need_command cmp
demo_need_command dd
WORK=$(demo_absolute_path "$WORK")
demo_prepare_work "$WORK" "tamper-detection" "$(demo_absolute_path .nkd/demo-tamper-detection)"
QUERY=(--data testdata/catalogs/kubernetes-layered --component kubectl --platform linux/amd64)
BUILD_DIGEST=$("$NKD" resolve "${QUERY[@]}" -o digest)

write_observation() {
  local path=$1 digest=$2
  printf '%s' "{\"buildDigest\":\"$BUILD_DIGEST\",\"components\":[{\"engineClass\":\"prebuilt-acquire\",\"name\":\"demo-artifact\",\"outputType\":\"host-package\",\"sha256\":\"$digest\"}],\"schemaVersion\":\"nkd.dev/validation-vector/v3\"}" > "$path"
}

demo_section "1/4  Detect changed artifact bytes through exact observation comparison"
mkdir -p "$WORK/artifacts"
ARTIFACT="$WORK/artifacts/demo-artifact"
printf 'original artifact bytes\n' > "$ARTIFACT"
ORIGINAL_DIGEST="sha256:$(demo_sha256 "$ARTIFACT")"
OBSERVED_A="$WORK/observed-original.json"
OBSERVED_EQUAL="$WORK/observed-equal.json"
OBSERVED_TAMPERED="$WORK/observed-tampered.json"
write_observation "$OBSERVED_A" "$ORIGINAL_DIGEST"
cp "$OBSERVED_A" "$OBSERVED_EQUAL"
"$NKD" build --bake-off --compare "$OBSERVED_A" --compare "$OBSERVED_EQUAL"

printf 'tampered artifact bytes\n' > "$ARTIFACT"
TAMPERED_DIGEST="sha256:$(demo_sha256 "$ARTIFACT")"
write_observation "$OBSERVED_TAMPERED" "$TAMPERED_DIGEST"
if "$NKD" build --bake-off --compare "$OBSERVED_A" --compare "$OBSERVED_TAMPERED" > "$WORK/artifact-tamper.out" 2>&1; then
  demo_die "changed artifact digest unexpectedly compared equal"
fi
cat "$WORK/artifact-tamper.out"

demo_section "2/4  Keep diagnostic observations out of qualified consumers"
if "$NKD" sbom "${QUERY[@]}" --expected-digests "$OBSERVED_A" --out "$WORK/forbidden-sbom.json" > "$WORK/authority-boundary.out" 2>&1; then
  demo_die "diagnostic observation unexpectedly entered the qualified SBOM path"
fi
[ ! -e "$WORK/forbidden-sbom.json" ] || demo_die "rejected diagnostic input published an SBOM"
cat "$WORK/authority-boundary.out"

demo_section "3/4  Exercise exact, immutable transport publication"
PRODUCER="$WORK/producer"
CONSUMER="$WORK/consumer"
mkdir -p "$PRODUCER/payload"
printf 'transported exact bytes\n' > "$PRODUCER/payload/artifact.bin"
ARCHIVE="$PRODUCER/carriers/good.tar"
"$NKD" transport-pack --root "$PRODUCER" --archive "$ARCHIVE" --path payload/artifact.bin
"$NKD" transport-unpack --root "$CONSUMER" --archive "$ARCHIVE" --path payload/artifact.bin
cmp "$PRODUCER/payload/artifact.bin" "$CONSUMER/payload/artifact.bin"
if "$NKD" transport-unpack --root "$CONSUMER" --archive "$ARCHIVE" --path payload/artifact.bin > "$WORK/overwrite.out" 2>&1; then
  demo_die "transport unpack overwrote an existing destination"
fi
cat "$WORK/overwrite.out"

demo_section "4/4  Reject a corrupt carrier and an escaping symlink"
BAD_ARCHIVE="$WORK/corrupt.tar"
cp "$ARCHIVE" "$BAD_ARCHIVE"
printf 'X' | dd of="$BAD_ARCHIVE" bs=1 seek=0 conv=notrunc 2>/dev/null
BAD_ROOT="$WORK/corrupt-consumer"
if "$NKD" transport-unpack --root "$BAD_ROOT" --archive "$BAD_ARCHIVE" --path payload/artifact.bin > "$WORK/corrupt.out" 2>&1; then
  demo_die "corrupt transport archive unexpectedly unpacked"
fi
[ ! -e "$BAD_ROOT/payload" ] || demo_die "corrupt archive published a partial destination"
cat "$WORK/corrupt.out"

printf 'outside bytes\n' > "$WORK/outside"
ln -s ../outside "$PRODUCER/escape"
ESCAPE_ARCHIVE="$PRODUCER/carriers/escape.tar"
if "$NKD" transport-pack --root "$PRODUCER" --archive "$ESCAPE_ARCHIVE" --path escape > "$WORK/symlink.out" 2>&1; then
  demo_die "escaping transport symlink unexpectedly packed"
fi
[ ! -e "$ESCAPE_ARCHIVE" ] || demo_die "rejected symlink published an archive"
cat "$WORK/symlink.out"

cat <<EOF

UNQUALIFIED TAMPER-DETECTION JOURNEY COMPLETE
  original observation: $OBSERVED_A
  tampered observation: $OBSERVED_TAMPERED
  valid carrier:        $ARCHIVE

Changed diagnostic bytes were detected, diagnostic data could not enter
the qualified SBOM path, and unsafe carrier publication failed without replacing or
partially publishing declared roots. Transport itself is not content authority; real
consumers must still authenticate and re-hash the transported artifacts.
EOF
