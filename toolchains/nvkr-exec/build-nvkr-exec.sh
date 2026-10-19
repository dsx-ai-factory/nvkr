#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Reproducibly build the ADR-18 `nvkr-exec` helper (== nvkr) for linux/amd64 and
# linux/arm64, capture the per-arch binary digest (the ADR-07 RunnerDigest), and OPTIONALLY
# assemble the pinned helper image (the ADR-18 HelperImage). It is deliberately hermetic
# about the binary and honest about what it cannot do here: it never pushes and never signs.
#
#   toolchains/nvkr-exec/build-nvkr-exec.sh            # build both arch binaries + verify determinism + write lock
#   toolchains/nvkr-exec/build-nvkr-exec.sh --image    # also assemble the image (needs podman|docker)
#
# The binary digest is toolchain-specific: pin GO to the .go-version toolchain so ANY
# builder reproduces the same bytes. GOTOOLCHAIN forces the exact patch.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
out="$here/out"
lock="$here/helper-image.lock.json"

ARCHES=(amd64 arm64)
GO="${GO:-go}"
# The recorded binary digest is only meaningful for an EXACT Go version, so the canonical
# builder must use .go-version's toolchain. Default GOTOOLCHAIN=local so this runs offline
# with the host toolchain; publishers export GOTOOLCHAIN=go<.go-version> to force the pin.
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
CANONICAL_TOOLCHAIN="go$(cat "$repo/.go-version")"
ACTUAL_TOOLCHAIN="$("$GO" version | awk '{print $3}')"
if [ "$ACTUAL_TOOLCHAIN" != "$CANONICAL_TOOLCHAIN" ]; then
  echo "WARNING: building with $ACTUAL_TOOLCHAIN but the canonical pin is $CANONICAL_TOOLCHAIN;" >&2
  echo "         the recorded digest is a DEMO — publishers must build under $CANONICAL_TOOLCHAIN." >&2
fi
# The rootless-podman base the image layers onto. PIN THIS BY DIGEST before publishing —
# the 0…0 placeholder is a demo; the real value belongs in the operator's Environment
# runtime config (runtimeImage / helperImage), not in code.
RUNTIME_BASE="${RUNTIME_BASE:-quay.io/podman/stable@sha256:0000000000000000000000000000000000000000000000000000000000000000}"
IMAGE_REPO="${IMAGE_REPO:-ghcr.io/dsx-ai-factory/nvkr-exec}"

want_image=0
[ "${1:-}" = "--image" ] && want_image=1

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# One reproducible build. CGO off + -trimpath + -buildvcs=false + empty -buildid make the
# ELF byte-identical across machines; SOURCE_DATE_EPOCH pins any tar/layer timestamps.
build_one() {
  local arch="$1" dest="$2"
  ( cd "$repo" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1}" \
      GOFLAGS='' \
      "$GO" build -trimpath -buildvcs=false -ldflags=-buildid= -o "$dest" ./cmd/nvkr )
}

rm -rf "$out"; mkdir -p "$out"
source_ref="$(cd "$repo" && git rev-parse HEAD 2>/dev/null || echo unknown)"
echo "nvkr-exec reproducible build — toolchain $ACTUAL_TOOLCHAIN (canonical $CANONICAL_TOOLCHAIN), source $source_ref"

declare -A DIGEST
for arch in "${ARCHES[@]}"; do
  build_one "$arch" "$out/nvkr-exec-$arch"
  first="$(sha256 "$out/nvkr-exec-$arch")"
  # Determinism gate: a second build MUST match, or the binary cannot back a RunnerDigest.
  build_one "$arch" "$out/nvkr-exec-$arch.verify"
  second="$(sha256 "$out/nvkr-exec-$arch.verify")"
  if [ "$first" != "$second" ]; then
    echo "ERROR: $arch build is non-deterministic ($first != $second); refusing to record a RunnerDigest" >&2
    exit 1
  fi
  rm -f "$out/nvkr-exec-$arch.verify"
  DIGEST[$arch]="sha256:$first"
  echo "  $arch  RunnerDigest=${DIGEST[$arch]}  (deterministic)"
done

# Optional image assembly. Cannot push or sign here; only builds + captures the manifest
# digest so the value that goes into HelperImage is real, not guessed.
declare -A IMAGE
if [ "$want_image" = 1 ]; then
  runtime="$(command -v podman || command -v docker || true)"
  if [ -z "$runtime" ]; then
    echo "  --image requested but no podman/docker found; skipping image assembly" >&2
  else
    for arch in "${ARCHES[@]}"; do
      tag="$IMAGE_REPO:dev-$arch"
      "$runtime" build --platform "linux/$arch" \
        --build-arg "RUNTIME_BASE=$RUNTIME_BASE" \
        -f "$here/Dockerfile" -t "$tag" "$out"
      IMAGE[$arch]="$("$runtime" inspect --format '{{index .RepoDigests 0}}' "$tag" 2>/dev/null || echo "$tag (not pushed; no digest yet)")"
      echo "  $arch  image=${IMAGE[$arch]}"
    done
  fi
fi

# Write the lock. binarySHA256 == the profile RunnerDigest the from-source bootstrap
# verifies AND the image's nvkr-exec must reproduce to; imageDigest == HelperImage.
{
  printf '{\n'
  printf '  "schemaVersion": "nvkr.dev/helper-image/v1",\n'
  printf '  "goToolchain": "%s",\n' "$ACTUAL_TOOLCHAIN"
  printf '  "canonicalToolchain": "%s",\n' "$CANONICAL_TOOLCHAIN"
  printf '  "sourceRef": "%s",\n' "$source_ref"
  printf '  "runtimeBase": "%s",\n' "$RUNTIME_BASE"
  printf '  "imageRepo": "%s",\n' "$IMAGE_REPO"
  printf '  "helper": {\n'
  for i in "${!ARCHES[@]}"; do
    arch="${ARCHES[$i]}"; sep=","; [ "$i" -eq $((${#ARCHES[@]}-1)) ] && sep=""
    printf '    "%s": { "binarySHA256": "%s", "imageDigest": %s }%s\n' \
      "$arch" "${DIGEST[$arch]}" "$([ -n "${IMAGE[$arch]:-}" ] && printf '"%s"' "${IMAGE[$arch]}" || printf null)" "$sep"
  done
  printf '  },\n'
  printf '  "note": "binarySHA256 is the ADR-07 RunnerDigest (profile pin); imageDigest is the ADR-18 HelperImage. This lock is UNSIGNED and NOT pushed. Pin runtimeBase and publish+sign before use."\n'
  printf '}\n'
} > "$lock"
echo "wrote $lock"
