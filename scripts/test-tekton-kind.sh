#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail
repo=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
export GOTOOLCHAIN=local
export GOMAXPROCS=${GOMAXPROCS:-2}
# The test creates and destroys only its own randomly named kind cluster. It
# retains reports in NVKR_TEKTON_EVIDENCE; keys and kubeconfig never go there.
(
  cd "$repo"
  go test -p 1 -tags=integration ./internal/tektonkind -run '^TestKindCore$' -count=1 -timeout=150m -v
)
