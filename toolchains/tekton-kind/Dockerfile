# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# The test builds the native Linux runner from this checkout with .go-version.
# The pinned base provides Docker CLI, Git and TLS roots without package updates.
FROM docker.io/library/docker:29.2.1-cli@sha256:cab69e2d0a1a2ea9a1ce1060252f439e83483ae41ec09317aecb33b08a0656a5
COPY nvkr /usr/local/bin/nvkr
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/nvkr"]
