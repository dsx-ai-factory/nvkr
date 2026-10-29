// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
)

type githubExecutionPartition = executionPartition

func partitionGitHubJobs(ctx context.Context, dependencies map[string][]string, maxJobs int) ([]executionPartition, error) {
	return partitionExecutionGraph(ctx, dependencies, maxJobs)
}
