# The base indexes and Ubuntu archive snapshot are exact inputs supplied from
# toolchain.lock.json by build.sh. The final image intentionally inherits only
# Ubuntu's PATH environment entry because NKD rejects ambient image settings.
ARG DOWNLOAD_IMAGE
ARG BASE_IMAGE
FROM ${DOWNLOAD_IMAGE} AS download

ARG TARGETARCH
ARG PACKER_VERSION
ARG PACKER_ARCHIVE_SHA256
ARG PACKER_BINARY_SHA256
ARG QEMU_PLUGIN_VERSION
ARG QEMU_PLUGIN_ARCHIVE_SHA256
ARG QEMU_PLUGIN_BINARY_SHA256
ARG GO_VERSION
ARG GO_ARCHIVE_SHA256
ARG CRANE_VERSION
ARG CRANE_ARCHIVE_SHA256
ARG CRANE_BINARY_SHA256
ARG NFPM_VERSION
ARG NFPM_ARCHIVE_SHA256
ARG NFPM_BINARY_SHA256

RUN set -eux; \
    packer_archive="packer_${PACKER_VERSION}_linux_${TARGETARCH}.zip"; \
    plugin_archive="packer-plugin-qemu_${QEMU_PLUGIN_VERSION}_linux_${TARGETARCH}.zip"; \
    curl --fail --location --retry 5 --retry-all-errors \
      "https://releases.hashicorp.com/packer/${PACKER_VERSION}/${packer_archive}" \
      --output "/tmp/${packer_archive}"; \
    printf '%s  %s\n' "${PACKER_ARCHIVE_SHA256}" "/tmp/${packer_archive}" | sha256sum -c; \
    curl --fail --location --retry 5 --retry-all-errors \
      "https://releases.hashicorp.com/packer-plugin-qemu/${QEMU_PLUGIN_VERSION}/${plugin_archive}" \
      --output "/tmp/${plugin_archive}"; \
    printf '%s  %s\n' "${QEMU_PLUGIN_ARCHIVE_SHA256}" "/tmp/${plugin_archive}" | sha256sum -c

RUN set -eux; \
    go_archive="go${GO_VERSION}.linux-${TARGETARCH}.tar.gz"; \
    case "${TARGETARCH}" in \
      amd64) release_arch=x86_64 ;; \
      arm64) release_arch=arm64 ;; \
      *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    crane_archive="go-containerregistry_Linux_${release_arch}.tar.gz"; \
    nfpm_archive="nfpm_${NFPM_VERSION}_Linux_${release_arch}.tar.gz"; \
    curl --fail --location --retry 5 --retry-all-errors \
      "https://go.dev/dl/${go_archive}" --output "/tmp/${go_archive}"; \
    printf '%s  %s\n' "${GO_ARCHIVE_SHA256}" "/tmp/${go_archive}" | sha256sum -c; \
    curl --fail --location --retry 5 --retry-all-errors \
      "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/${crane_archive}" \
      --output "/tmp/${crane_archive}"; \
    printf '%s  %s\n' "${CRANE_ARCHIVE_SHA256}" "/tmp/${crane_archive}" | sha256sum -c; \
    curl --fail --location --retry 5 --retry-all-errors \
      "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/${nfpm_archive}" \
      --output "/tmp/${nfpm_archive}"; \
    printf '%s  %s\n' "${NFPM_ARCHIVE_SHA256}" "/tmp/${nfpm_archive}" | sha256sum -c

FROM ${BASE_IMAGE} AS fetch

ARG TARGETARCH
ARG UBUNTU_SNAPSHOT
ARG PACKER_VERSION
ARG PACKER_ARCHIVE_SHA256
ARG PACKER_BINARY_SHA256
ARG QEMU_PLUGIN_VERSION
ARG QEMU_PLUGIN_ARCHIVE_SHA256
ARG QEMU_PLUGIN_BINARY_SHA256
ARG GO_VERSION
ARG GO_ARCHIVE_SHA256
ARG CRANE_VERSION
ARG CRANE_ARCHIVE_SHA256
ARG CRANE_BINARY_SHA256
ARG NFPM_VERSION
ARG NFPM_ARCHIVE_SHA256
ARG NFPM_BINARY_SHA256

COPY --from=download /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=download /tmp/packer_*.zip /tmp/
COPY --from=download /tmp/packer-plugin-qemu_*.zip /tmp/
COPY --from=download /tmp/go*.linux-*.tar.gz /tmp/
COPY --from=download /tmp/go-containerregistry_Linux_*.tar.gz /tmp/
COPY --from=download /tmp/nfpm_*_Linux_*.tar.gz /tmp/

RUN printf '%s\n' 'Acquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt";' \
      >/etc/apt/apt.conf.d/99bootstrap-ca \
    && printf '%s\n' \
      'Types: deb' \
      "URIs: https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}" \
      'Suites: resolute resolute-updates resolute-security' \
      'Components: main universe' \
      'Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg' \
      >/etc/apt/sources.list.d/ubuntu.sources \
    && apt-get -o Acquire::Retries=5 update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        ca-certificates gzip tar unzip \
    && rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99bootstrap-ca

RUN set -eux; \
    packer_archive="packer_${PACKER_VERSION}_linux_${TARGETARCH}.zip"; \
    plugin_archive="packer-plugin-qemu_${QEMU_PLUGIN_VERSION}_linux_${TARGETARCH}.zip"; \
    printf '%s  %s\n' "${PACKER_ARCHIVE_SHA256}" "/tmp/${packer_archive}" | sha256sum -c; \
    mkdir -p /out/packer /out/plugin /out/plugins /out/licenses/packer /out/licenses/packer-plugin-qemu; \
    unzip -q "/tmp/${packer_archive}" -d /out/packer; \
    printf '%s  %s\n' "${PACKER_BINARY_SHA256}" /out/packer/packer | sha256sum -c; \
    install -m 0755 /out/packer/packer /out/packer/packer.bin; \
    install -m 0644 /out/packer/LICENSE.txt /out/licenses/packer/LICENSE.txt; \
    printf '%s  %s\n' "${QEMU_PLUGIN_ARCHIVE_SHA256}" "/tmp/${plugin_archive}" | sha256sum -c; \
    unzip -q "/tmp/${plugin_archive}" -d /out/plugin; \
    plugin="/out/plugin/packer-plugin-qemu_v${QEMU_PLUGIN_VERSION}_x5.0_linux_${TARGETARCH}"; \
    test -x "${plugin}"; \
    printf '%s  %s\n' "${QEMU_PLUGIN_BINARY_SHA256}" "${plugin}" | sha256sum -c; \
    install -m 0644 /out/plugin/LICENSE.txt /out/licenses/packer-plugin-qemu/LICENSE.txt; \
    plugin_directory=/out/plugins/github.com/hashicorp/qemu; \
    plugin_name="packer-plugin-qemu_v${QEMU_PLUGIN_VERSION}_x5.0_linux_${TARGETARCH}"; \
    install -d -m 0755 "${plugin_directory}"; \
    install -m 0755 "${plugin}" "${plugin_directory}/${plugin_name}"; \
    printf '%s' "${QEMU_PLUGIN_BINARY_SHA256}" >"${plugin_directory}/${plugin_name}_SHA256SUM"

RUN set -eux; \
    go_archive="go${GO_VERSION}.linux-${TARGETARCH}.tar.gz"; \
    case "${TARGETARCH}" in \
      amd64) release_arch=x86_64 ;; \
      arm64) release_arch=arm64 ;; \
      *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    crane_archive="go-containerregistry_Linux_${release_arch}.tar.gz"; \
    nfpm_archive="nfpm_${NFPM_VERSION}_Linux_${release_arch}.tar.gz"; \
    printf '%s  %s\n' "${GO_ARCHIVE_SHA256}" "/tmp/${go_archive}" | sha256sum -c; \
    tar -xzf "/tmp/${go_archive}" -C /out; \
    test -x /out/go/bin/go; \
    install -d -m 0755 /out/tools /out/licenses/go /out/licenses/crane /out/licenses/nfpm; \
    install -m 0644 /out/go/LICENSE /out/licenses/go/LICENSE; \
    printf '%s  %s\n' "${CRANE_ARCHIVE_SHA256}" "/tmp/${crane_archive}" | sha256sum -c; \
    tar -xzf "/tmp/${crane_archive}" -C /out/tools crane LICENSE; \
    printf '%s  %s\n' "${CRANE_BINARY_SHA256}" /out/tools/crane | sha256sum -c; \
    install -m 0644 /out/tools/LICENSE /out/licenses/crane/LICENSE; \
    rm /out/tools/LICENSE; \
    printf '%s  %s\n' "${NFPM_ARCHIVE_SHA256}" "/tmp/${nfpm_archive}" | sha256sum -c; \
    tar -xzf "/tmp/${nfpm_archive}" -C /out/tools nfpm LICENSE.md; \
    printf '%s  %s\n' "${NFPM_BINARY_SHA256}" /out/tools/nfpm | sha256sum -c; \
    install -m 0644 /out/tools/LICENSE.md /out/licenses/nfpm/LICENSE.md; \
    rm /out/tools/LICENSE.md

FROM ${BASE_IMAGE}

ARG TARGETARCH
ARG UBUNTU_SNAPSHOT
ARG QEMU_PLUGIN_VERSION
COPY --from=download /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN set -eux; \
	case "${TARGETARCH}" in \
	  amd64|arm64) ;; \
	  *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
	esac; \
    printf '%s\n' 'Acquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt";' \
      >/etc/apt/apt.conf.d/99bootstrap-ca \
    && printf '%s\n' \
      'Types: deb' \
      "URIs: https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}" \
      'Suites: resolute resolute-updates resolute-security' \
      'Components: main universe' \
      'Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg' \
      >/etc/apt/sources.list.d/ubuntu.sources \
    && apt-get -o Acquire::Retries=5 update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
        bash \
        ca-certificates \
        cloud-image-utils \
        coreutils \
        curl \
        dash \
        dpkg-dev \
        file \
        gcc \
        genisoimage \
        git \
        gnupg \
        gzip \
        libc6-dev \
        make \
        openssh-client \
        p7zip-full \
        python3 \
        qemu-utils \
        ovmf \
        qemu-efi-aarch64 \
        qemu-system-arm \
        qemu-system-x86 \
        tar \
		unzip \
		xorriso \
	&& rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99bootstrap-ca /etc/apt/sources.list.d/ubuntu.sources

COPY --from=fetch /out/packer/packer.bin /usr/local/bin/packer
COPY --from=fetch /out/plugins/ /nkd/toolchain/packer/plugins/
COPY --from=fetch /out/go/ /usr/local/go/
COPY --from=fetch /out/tools/crane /usr/local/bin/crane
COPY --from=fetch /out/tools/nfpm /usr/local/bin/nfpm
COPY --from=fetch /out/licenses/ /usr/share/doc/nkd-packer-toolchain/licenses/

RUN set -eux; \
    ln -s /usr/local/go/bin/go /usr/local/bin/go; \
    ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
    test -x /usr/local/bin/packer; \
	test -x "/nkd/toolchain/packer/plugins/github.com/hashicorp/qemu/packer-plugin-qemu_v${QEMU_PLUGIN_VERSION}_x5.0_linux_${TARGETARCH}"; \
	test -f "/nkd/toolchain/packer/plugins/github.com/hashicorp/qemu/packer-plugin-qemu_v${QEMU_PLUGIN_VERSION}_x5.0_linux_${TARGETARCH}_SHA256SUM"; \
	command -v qemu-img; \
	command -v qemu-system-aarch64; \
	command -v qemu-system-x86_64; \
    command -v ssh-keygen; \
    command -v bash; \
    command -v sh; \
    command -v install; \
    command -v mktemp; \
    command -v stat; \
    command -v chown; \
    command -v dirname; \
    command -v python3; \
    command -v tar; \
    command -v sha256sum; \
	command -v curl; \
	command -v cloud-localds; \
	command -v dpkg-scanpackages; \
	command -v file; \
	command -v gcc; \
	command -v git; \
	command -v go; \
	command -v gpg; \
	command -v gzip; \
	command -v make; \
	command -v crane; \
	command -v nfpm; \
	command -v 7z; \
	command -v unzip; \
	command -v xorriso; \
	test -f /usr/share/OVMF/OVMF_CODE_4M.fd; \
	test -f /usr/share/OVMF/OVMF_VARS_4M.fd; \
	test -f /usr/share/AAVMF/AAVMF_CODE.fd; \
	test -f /usr/share/AAVMF/AAVMF_VARS.fd
