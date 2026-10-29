// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
)

type githubExecutionPartition = executionPartition

func partitionGitHubJobs(ctx context.Context, dependencies map[string][]string, layout executionPartitioning) ([]executionPartition, error) {
	return partitionExecutionGraph(ctx, dependencies, layout)
}
