# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# The ADR-18 §3 pinned helper image: rootless podman + the reproducibly-built nkd-exec helper,
# so a Model-B job can run every node on ONE digest-pinned image (nkd-exec on PATH) with no
# from-source bootstrap. Assembled by build-nkd-exec.sh; the build context is ./out.
#
# Trust note: this image is only as trustworthy as (a) RUNTIME_BASE pinned by digest, and
# (b) the nkd-exec bytes reproducing to the profile RunnerDigest in helper-image.lock.json.
# Publish it through nkd's own signed/SBOM'd release path, not a hand build.

# RUNTIME_BASE is the rootless-podman base, pinned by @sha256. Its real value lives in the
# operator's Environment runtime config; the 0…0 placeholder MUST be replaced.
ARG RUNTIME_BASE=quay.io/podman/stable@sha256:0000000000000000000000000000000000000000000000000000000000000000
FROM ${RUNTIME_BASE}

# Set automatically for `podman/docker build --platform linux/<arch>`.
ARG TARGETARCH

# The reproducibly-built helper (== nkdctl). Its sha256 must equal the profile RunnerDigest
# so the image and the from-source bootstrap deliver byte-identical bytes (ADR-18 §3).
COPY nkd-exec-${TARGETARCH} /usr/local/bin/nkd-exec
RUN chmod 0755 /usr/local/bin/nkd-exec

# The podman base already ships podman, crun, and fuse-overlayfs. Fetch nodes need git and
# TLS roots. NOTE: `dnf install` is NOT byte-reproducible (it resolves live repo state); pin
# exact package versions (or use a lockfile/pinned base that already includes them) before
# claiming a reproducible image digest.
RUN (command -v git >/dev/null 2>&1 || (command -v dnf >/dev/null 2>&1 && dnf -y install git ca-certificates)) || true

# nkd-exec is on PATH; ADR-18 image-mode jobs invoke it directly (no ./ prefix, no re-hash).
# No ENTRYPOINT/USER is forced: the CI engine supplies the job command, and the runner tier
# (VM vs container executor) decides the rootless-podman user/nesting posture (ADR-18 §3).
