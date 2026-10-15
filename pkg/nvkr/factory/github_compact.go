// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// GitHub rejects workflow files above 500 KB.
const maxGitHubWorkflowBytes = 500000

func validateGitHubWorkflowSize(raw []byte) error {
	if len(raw) > maxGitHubWorkflowBytes {
		return e.New(e.CodeEngineCapability, "generated workflow is %d bytes; GitHub permits at most %d bytes per file", len(raw), maxGitHubWorkflowBytes)
	}
	return nil
}
