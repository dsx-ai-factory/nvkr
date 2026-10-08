# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Existing pinned Helm closure; no downloads, package installs or added binaries.
# The wrapper enables a declared shell sequence during source preparation rather
# than overriding the entrypoint at an executable boundary.
FROM alpine/helm@sha256:a6cf54599ccb99d90cf0712b30f03fdb3cab062e6b94e0418cc4db7e8a1464b2
ENTRYPOINT []
CMD ["helm", "--help"]
