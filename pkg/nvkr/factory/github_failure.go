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
// admitted dependency DAG puts an upstream failure before consequential failures;
// independent roots use job ID, not inferred time.
func githubProviderClosureStatus(jobs []string, results map[string]string, needs map[string][]string) string {
	ordered := slices.Clone(jobs)
	slices.Sort(ordered)
	for _, outcome := range []string{"failure", "cancelled", "skipped"} { //nolint:misspell // GitHub enum.
		// Walk through successful intermediates too: completion-scheduled jobs
		// can succeed despite an unsuccessful ancestor. The DAG is validated.
		ancestors := map[string]bool{}
		var hasAncestor func(string) bool
		hasAncestor = func(job string) bool {
			if found, known := ancestors[job]; known {
				return found
			}
			for _, dependency := range needs[job] {
				if results[dependency] == outcome || hasAncestor(dependency) {
					ancestors[job] = true
					return true
				}
			}
			ancestors[job] = false
			return false
		}
		for _, job := range ordered {
			if results[job] == outcome && !hasAncestor(job) {
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
	label := ""
	for _, stage := range stages {
		if stage.ID == job {
			label = fmt.Sprintf(" (%q)", stage.Name)
			break
		}
	}
	return fmt.Sprintf("upstream provider job %q%s reported %s; inspect its log for the original error", job, label, outcome)
}
