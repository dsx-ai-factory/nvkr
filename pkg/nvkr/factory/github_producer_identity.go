// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// GitHubProducerIdentity returns the generated path and display name of an admitted Plan.
func GitHubProducerIdentity(plan *Plan) (string, string, error) {
	if plan == nil {
		return "", "", e.New(e.CodeUsage, "producer identity requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return "", "", err
	}
	return githubLayoutFor(plan).workflowPath, githubWorkflowName(plan), nil
}
