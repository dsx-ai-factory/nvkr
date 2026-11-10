# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Diagnostic libguestfs runtime. Ubuntu archive signatures authenticate the
# frozen repository; the resulting image digest must be recorded after building.
FROM scratch AS nvkr_archives
FROM curlimages/curl@sha256:3fb7e13e5d179a845c97d78204f3f1064d1907037f26a3ebf91710571151fd93 AS certificates
FROM certificates AS scanner
USER root
ARG TARGETARCH
ARG NVKR_ARCHIVE_MODE=download
RUN --mount=type=secret,id=nvkr_ca_bundle,mode=0444 --mount=type=bind,from=nvkr_archives,target=/nvkr/archives set -eux; \
    if [ -r /run/secrets/nvkr_ca_bundle ]; then export CURL_CA_BUNDLE=/run/secrets/nvkr_ca_bundle; fi; \
    case "${TARGETARCH}" in \
      amd64) scanner_sha=bf52519eb57f675d47c07d43213cb6dff6449f3d6d1147ff6701ed91f44eef0c ;; \
      arm64) scanner_sha=aa3d3e07344f590bcce8d31043b752daa76c8a56c2aa34ceeee63c6168152582 ;; \
      *) echo 'unsupported scanner architecture' >&2; exit 1 ;; \
    esac; \
    case "${NVKR_ARCHIVE_MODE}" in \
    local) cp "/nvkr/archives/syft_1.57.1_linux_${TARGETARCH}.tar.gz" /tmp/syft.tar.gz ;; \
    download) curl --fail --location --max-time 300 --max-filesize 134217728 \
      --output /tmp/syft.tar.gz \
      "https://github.com/anchore/syft/releases/download/v1.57.1/syft_1.57.1_linux_${TARGETARCH}.tar.gz" ;; \
    *) echo 'unsupported archive mode' >&2; exit 1 ;; esac; \
    printf '%s  /tmp/syft.tar.gz\n' "${scanner_sha}" | sha256sum -c -; \
    tar -xzf /tmp/syft.tar.gz -C /tmp syft; \
    chmod 0755 /tmp/syft
FROM ubuntu@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852

ARG TARGETARCH
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=scanner /tmp/syft /usr/local/bin/syft
RUN --mount=type=secret,id=nvkr_ca_bundle,mode=0444 set -eux; \
    case "${TARGETARCH}" in \
      amd64) qemu_package=qemu-system-x86 ;; \
      arm64) qemu_package=qemu-system-arm ;; \
      *) echo 'unsupported inspection architecture' >&2; exit 1 ;; \
    esac; \
    printf '%s\n' 'Acquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt";' \
      > /etc/apt/apt.conf.d/99bootstrap-ca; \
    if [ -r /run/secrets/nvkr_ca_bundle ]; then \
      printf '%s\n' 'Acquire::https::CaInfo "/run/secrets/nvkr_ca_bundle";' > /etc/apt/apt.conf.d/99bootstrap-ca; \
    fi; \
    printf '%s\n' 'Types: deb' \
      'URIs: https://snapshot.ubuntu.com/ubuntu/20261109T000000Z' \
      'Suites: noble noble-updates noble-security' \
      'Components: main universe' \
      'Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg' \
      > /etc/apt/sources.list.d/ubuntu.sources; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
      guestfs-tools=1.52.0-2ubuntu5 \
      guestfish=1:1.52.0-5ubuntu3 \
      libguestfs0t64=1:1.52.0-5ubuntu3 \
      linux-image-virtual=6.8.0-149.149 \
      "${qemu_package}=1:8.2.2+ds-0ubuntu1.18" \
      qemu-utils=1:8.2.2+ds-0ubuntu1.18; \
    chmod 0644 /boot/vmlinuz-*; \
    mkdir -p /usr/share/nvkr-disk-inspect; \
    dpkg-query -W -f='${Package}\t${Version}\t${Architecture}\n' \
      > /usr/share/nvkr-disk-inspect/packages.unsorted.tsv; \
    LC_ALL=C sort /usr/share/nvkr-disk-inspect/packages.unsorted.tsv \
      > /usr/share/nvkr-disk-inspect/packages.tsv; \
    rm /usr/share/nvkr-disk-inspect/packages.unsorted.tsv; \
    sha256sum /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish /usr/bin/dpkg-deb /usr/local/bin/syft \
      > /usr/share/nvkr-disk-inspect/executables.sha256; \
    rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99bootstrap-ca \
      /etc/apt/sources.list.d/ubuntu.sources /etc/ssl/certs/ca-certificates.crt

# Build this static helper from the exact NVKR source with its pinned Go version
# before image assembly. Its bytes become part of the selected OCI closure.
COPY --chmod=0755 bin/nvkr-disk-inspect-${TARGETARCH} /usr/local/bin/nvkr-disk-inspect
USER 65534:65534
WORKDIR /tmp
