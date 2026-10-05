# Diagnostic libguestfs runtime. Ubuntu archive signatures authenticate the
# frozen repository; the resulting image digest must be recorded after building.
FROM curlimages/curl@sha256:935d9100e9ba842cdb060de42472c7ca90cfe9a7c96e4dacb55e79e560b3ff40 AS certificates
FROM certificates AS scanner
USER root
ARG TARGETARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) scanner_sha=8fcb33017a0dc1058298c923c436d19dfa68ae93968e0b423248542e3afb9fc3 ;; \
      arm64) scanner_sha=a7fd2b784e6664acd44719270574f6cd8c6864fc2b1700bf9099bd1cccda7d7f ;; \
      *) echo 'unsupported scanner architecture' >&2; exit 1 ;; \
    esac; \
    curl --fail --location --max-time 300 --max-filesize 134217728 \
      --output /tmp/syft.tar.gz \
      "https://github.com/anchore/syft/releases/download/v1.51.1/syft_1.51.1_linux_${TARGETARCH}.tar.gz"; \
    printf '%s  /tmp/syft.tar.gz\n' "${scanner_sha}" | sha256sum -c -; \
    tar -xzf /tmp/syft.tar.gz -C /tmp syft; \
    chmod 0755 /tmp/syft
FROM ubuntu@sha256:008173c23f95b170204355c12626cb5a965d779a7e1283b09e9cffbb1bf33ca3

ARG TARGETARCH
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=scanner /tmp/syft /usr/local/bin/syft
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) qemu_package=qemu-system-x86 ;; \
      arm64) qemu_package=qemu-system-arm ;; \
      *) echo 'unsupported inspection architecture' >&2; exit 1 ;; \
    esac; \
    printf '%s\n' 'Acquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt";' \
      > /etc/apt/apt.conf.d/99bootstrap-ca; \
    printf '%s\n' 'Types: deb' \
      'URIs: https://snapshot.ubuntu.com/ubuntu/20260920T000000Z' \
      'Suites: noble noble-updates noble-security' \
      'Components: main universe' \
      'Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg' \
      > /etc/apt/sources.list.d/ubuntu.sources; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
      guestfs-tools=1.52.0-2ubuntu5 \
      guestfish=1:1.52.0-5ubuntu3 \
      libguestfs0t64=1:1.52.0-5ubuntu3 \
      linux-image-virtual=6.8.0-139.139 \
      "${qemu_package}=1:8.2.2+ds-0ubuntu1.18" \
      qemu-utils=1:8.2.2+ds-0ubuntu1.18; \
    chmod 0644 /boot/vmlinuz-*; \
    mkdir -p /usr/share/nkd-disk-inspect; \
    dpkg-query -W -f='${Package}\t${Version}\t${Architecture}\n' \
      > /usr/share/nkd-disk-inspect/packages.unsorted.tsv; \
    LC_ALL=C sort /usr/share/nkd-disk-inspect/packages.unsorted.tsv \
      > /usr/share/nkd-disk-inspect/packages.tsv; \
    rm /usr/share/nkd-disk-inspect/packages.unsorted.tsv; \
    sha256sum /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish /usr/bin/dpkg-deb /usr/local/bin/syft \
      > /usr/share/nkd-disk-inspect/executables.sha256; \
    rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99bootstrap-ca \
      /etc/apt/sources.list.d/ubuntu.sources /etc/ssl/certs/ca-certificates.crt

# Build this static helper from the exact NKD source with its pinned Go version
# before image assembly. Its bytes become part of the selected OCI closure.
COPY bin/nkd-disk-inspect-${TARGETARCH} /usr/local/bin/nkd-disk-inspect
USER 65534:65534
WORKDIR /tmp
