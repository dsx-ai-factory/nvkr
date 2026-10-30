# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Both inputs are native manifest digests from toolchain.lock.json. The Go SDK
# is the only compiler copied from the donor; dpkg and the runtime come from
# the selected Ubuntu image. This component uses CGO_ENABLED=0.
ARG GO_IMAGE
ARG UBUNTU_IMAGE
FROM ${GO_IMAGE} AS go_sdk
FROM ${UBUNTU_IMAGE}
ARG UBUNTU_VERSION
ARG TARGETARCH
COPY --from=go_sdk /usr/local/go /usr/local/go
COPY --from=go_sdk /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
RUN set -eu; \
    . /etc/os-release; \
    test "$ID" = ubuntu; test "$VERSION_ID" = "$UBUNTU_VERSION"; \
    test "$(go env GOVERSION)" = go1.27.0; \
    test "$(go env GOARCH)" = "$TARGETARCH"; \
    test "$(dpkg --print-architecture)" = "$TARGETARCH"; \
    dpkg-deb --version; \
    command -v install; command -v find; command -v touch; \
    command -v tar; command -v gzip
