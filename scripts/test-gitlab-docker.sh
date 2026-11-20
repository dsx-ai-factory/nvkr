#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
repo=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
export GOTOOLCHAIN=local
export GOMAXPROCS=${GOMAXPROCS:-2}
(
  cd "$repo"
  go test -p 1 -tags=integration ./internal/gitlabdocker -run '^Test(GitLabNVKE|FailureDiagnostics|ContainerCleanupInventory)$' -count=1 -timeout=120m -v
)
