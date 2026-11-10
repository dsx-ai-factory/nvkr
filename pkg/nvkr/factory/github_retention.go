// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

// GitHubArtifactRetentionDays is the provider-artifact lifetime for a validated
// Plan. Dispatch-frozen composites retain their exact replay inputs for 14 days.
// Private registry blobs and upstream source objects require separate retention.
func (plan *Plan) GitHubArtifactRetentionDays() int {
	if len(plan.Environment.GitHubRuntimeSources) != 0 {
		return 14
	}
	return 7
}

func githubDiagnosticRetentionDays(plan *Plan) int {
	if len(plan.Environment.GitHubRuntimeSources) != 0 {
		return plan.GitHubArtifactRetentionDays()
	}
	return 1
}
