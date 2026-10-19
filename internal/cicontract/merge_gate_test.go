// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestMergeGateRequiresEveryCodeJob(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/merge-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs []string              `json:"needs"`
			Steps []storageWorkflowStep `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	gate := workflow.Jobs["gate"]
	codeJobs := []string{"check", "lint", "vulnerabilities", "tests"}
	for _, name := range append(slices.Clone(codeJobs), "check-skip", "check-paths") {
		if !slices.Contains(gate.Needs, name) {
			t.Fatalf("gate does not depend on %s", name)
		}
	}
	if len(gate.Steps) != 1 || gate.Steps[0].Run == "" {
		t.Fatal("missing executable aggregate gate")
	}
	cases := []struct {
		name, code, job, result string
		wantSuccess             bool
	}{
		{"all code checks pass", "true", "", "", true},
		{"unchanged code", "false", "", "", true},
		{"missing skip companion", "false", "check-skip", "skipped", false},
		{"classifier failed", "true", "check-paths", "failure", false},
		{"another gate failed", "true", "docs", "failure", false},
		{"unknown classification", "", "", "", false},
	}
	for _, job := range codeJobs {
		for _, result := range []string{"skipped", "failure", "cancelled"} { //nolint:misspell // GitHub result enum spelling.
			cases = append(cases, struct {
				name, code, job, result string
				wantSuccess             bool
			}{job + " " + result, "true", job, result, false})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			needs := map[string]map[string]string{}
			for _, job := range gate.Needs {
				needs[job] = map[string]string{"result": "success"}
			}
			if tc.code == "true" {
				needs["check-skip"]["result"] = "skipped"
			} else {
				for _, job := range codeJobs {
					needs[job]["result"] = "skipped"
				}
			}
			if tc.job != "" {
				needs[tc.job]["result"] = tc.result
			}
			results := make([]string, 0, len(gate.Needs))
			for _, job := range gate.Needs {
				results = append(results, needs[job]["result"])
			}
			encodedNeeds, err := json.Marshal(needs)
			if err != nil {
				t.Fatal(err)
			}
			encodedResults, err := json.Marshal(results)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer(
				"${{ toJSON(needs) }}", string(encodedNeeds),
				"${{ toJSON(needs.*.result) }}", string(encodedResults),
				"${{ needs.check-paths.result }}", needs["check-paths"]["result"],
				"${{ needs.check-paths.outputs.code }}", tc.code,
			).Replace(gate.Steps[0].Run)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "bash", "-eu", "-c", script).CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("gate success=%t, want %t: %v\n%s", err == nil, tc.wantSuccess, err, output)
			}
		})
	}
}
