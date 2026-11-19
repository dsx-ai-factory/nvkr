// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubProviderClosurePrioritizesActualFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results map[string]string
		want    string
	}{
		{"success", map[string]string{"a": "success", "z": "success"}, "restored:success"},
		{"failure before skipped", map[string]string{"a": "skipped", "z": "failure"}, "job-failed:failure:z"},
		{"failure before cancellation", map[string]string{"a": "cancelled", "z": "failure"}, "job-failed:failure:z"}, //nolint:misspell // GitHub enum.
		{"independent failures ordered by ID", map[string]string{"a": "failure", "z": "failure"}, "job-failed:failure:a"},
		{"cancellation before skipped", map[string]string{"a": "skipped", "z": "cancelled"}, "job-failed:cancelled:z"}, //nolint:misspell // GitHub enum.
		{"skipped", map[string]string{"a": "success", "z": "skipped"}, "job-failed:skipped:z"},
		{"missing result", map[string]string{"a": "success"}, "job-failed"},
		{"unknown result", map[string]string{"a": "success", "z": "unknown"}, "job-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubProviderClosureStatus([]string{"z", "a"}, tc.results); got != tc.want {
				t.Fatalf("closure status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitHubRecoveryPreservesUpstreamFailureForFinalizer(t *testing.T) {
	plan := referenceTestPlan(t)
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil || len(jobs) < 2 || len(stages) == 0 {
		t.Fatal("fixture needs multiple provider jobs", err)
	}
	failed := stages[len(stages)-1]
	results := carrierTestResults(t, plan)
	for _, job := range jobs {
		results[job] = map[string]any{"result": "skipped"}
	}
	results[failed.ID] = map[string]any{"result": "failure"}
	t.Chdir(t.TempDir())
	var output bytes.Buffer
	err = recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, results), GitHubStepOptions{Stdout: &output, Stderr: &output},
		func(context.Context, Artifact) (string, error) {
			t.Error("attempted restoration from an unsuccessful job")
			return "", nil
		}, writeGitHubRestoreOutcome)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateHostedRestores(t.Context(), ".nvkr/factory", plan)
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	for _, text := range []string{failed.ID, failed.Name, "reported failure", "original error"} {
		if !strings.Contains(err.Error(), text) || !strings.Contains(output.String(), text) {
			t.Fatalf("recovery/finalizer omitted %q: error=%v, recovery=%s", text, err, output.String())
		}
	}
}

func TestHostedFailureDescriptionCannotAuthorizeOrInjectDiagnostics(t *testing.T) {
	plan := referenceTestPlan(t)
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status string
		detail bool
	}{
		{"known job", "job-failed:failure:" + jobs[0], true},
		{"legacy marker", "job-failed", false},
		{"unknown job", "job-failed:failure:foreign-job", false},
		{"unknown outcome", "job-failed:unknown:" + jobs[0], false},
		{"forged success detail", "job-failed:success:" + jobs[0], false},
		{"success marker cannot replace missing carriers", "restored:success", false},
		{"annotation injection", "job-failed:failure:" + jobs[0] + "\n::error::injected", false},
		{"missing description", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "factory")
			if err := writeFactoryFile(t.Context(), HostedRestoreOutcomePath(workspace, "all-provider-jobs"), []byte(tc.status+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			err := ValidateHostedRestores(t.Context(), workspace, plan)
			assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			if strings.Contains(err.Error(), "upstream provider job") != tc.detail || strings.Contains(err.Error(), "foreign-job") || strings.Contains(err.Error(), "injected") {
				t.Fatalf("unadmitted diagnostic or lost known job: %v", err)
			}
		})
	}
}
