# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Assemble ko plus the frozen base cache; NVSentinel itself still builds with ko.
ARG TARGETARCH
FROM docker.io/library/golang@sha256:eef6a67266eeed3c86dd47fd01b32faa8bf0229eb83eb3d4d466e80391bd3820 AS sdk-amd64
FROM docker.io/library/golang@sha256:6c83d163c89d1dbe66f8fc466870d66fe6325d15bb5ad1730cdc0b482fab1eec AS sdk-arm64
FROM sdk-${TARGETARCH} AS sdk
FROM docker.io/library/ubuntu@sha256:f610ab94648195aa356059f5b41d6085c9d4d903c072430cdd1af7bdb646106b AS root-amd64
FROM docker.io/library/ubuntu@sha256:08571ca13e00ca07a2a84eab83a959b4242e22cceb16486a11bef1428c9e93a7 AS root-arm64
FROM root-${TARGETARCH}
ARG TARGETARCH
COPY --from=sdk /usr/local/go /usr/local/go
COPY --from=sdk /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY ${TARGETARCH}/ko /usr/local/bin/ko
COPY ${TARGETARCH}/ko-cache /opt/ko-cache
COPY licenses /usr/share/doc/nvkr-ko
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN set -eu; \
    test "$(go env GOVERSION)" = go1.27.0; \
    test "$(go env GOARCH)" = "$TARGETARCH"; \
    case "$TARGETARCH" in \
      amd64) expected=b40e46d2770b91dabbecb9ac0e26bff6a308c924eddfe37f7747fbd5cfda8983 ;; \
      arm64) expected=46aefb6f9170b387d6dbba00041000ddb7320e38115ca926299bd665910f66f0 ;; \
      *) exit 2 ;; \
    esac; \
    printf '%s  /usr/local/bin/ko\n' "$expected" | sha256sum -c -; \
    ko version; command -v tar
