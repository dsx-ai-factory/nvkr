// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"slices"
	"strings"
)

// Results have already passed exact original-job accounting, including collector
// expansion. Prefer a real failure over canceled or skipped dependents. The
// provider result schema has no timestamps: ties use job ID, not inferred time.
func githubProviderClosureStatus(jobs []string, results map[string]string) string {
	ordered := slices.Clone(jobs)
	slices.Sort(ordered)
	for _, outcome := range []string{"failure", "cancelled", "skipped"} { //nolint:misspell // GitHub enum.
		for _, job := range ordered {
			if results[job] == outcome {
				return "job-failed:" + outcome + ":" + job
			}
		}
	}
	for _, job := range ordered {
		if results[job] != "success" {
			return "job-failed"
		}
	}
	return "restored:success"
}

// This is presentation of a failed description, never an authorization path.
// Only an admitted original job ID and a known failure enum may enter the error.
// Legacy, malformed or foreign descriptions retain the generic completeness
// error, and no description can suppress artifact or evidence validation.
func githubProviderFailureMessage(plan *Plan, status string) string {
	detail, ok := strings.CutPrefix(status, "job-failed:")
	if !ok {
		return ""
	}
	outcome, job, ok := strings.Cut(detail, ":")
	if !ok || (outcome != "failure" && outcome != "cancelled" && outcome != "skipped") { //nolint:misspell // GitHub enum.
		return ""
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return ""
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil || !slices.Contains(jobs, job) {
		return ""
	}
	label := job
	for _, stage := range stages {
		if stage.ID == job {
			label = stage.Name
			break
		}
	}
	return fmt.Sprintf("upstream provider job %q (%q) reported %s; inspect its log for the original error", job, label, outcome)
}
