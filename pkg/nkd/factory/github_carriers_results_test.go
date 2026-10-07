// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubCarriersRejectProviderDescriptionsBeforeWork(t *testing.T) {
	plan := githubTestPlan(t)
	results := carrierTestResults(t, plan)
	valid := carrierTestJSON(t, results)
	var stageJob, bootstrapJob string
	for job := range results {
		if strings.HasPrefix(job, "build-runner-") {
			bootstrapJob = job
		} else {
			stageJob = job
		}
	}
	without := func(job string) string {
		changed := carrierTestResults(t, plan)
		delete(changed, job)
		return carrierTestJSON(t, changed)
	}
	complex := carrierTestResults(t, plan)
	outputs := map[string]string{}
	for i := 0; i < maxGitHubProviderTokens/2; i++ {
		outputs[fmt.Sprintf("k%d", i)] = ""
	}
	complex[stageJob] = map[string]any{"result": "success", "outputs": outputs}
	cases := []struct{ name, raw string }{
		{"empty", ""},
		{"missing-all", "{}"},
		{"missing-stage", without(stageJob)},
		{"missing-bootstrap", without(bootstrapJob)},
		{"top-array", "[]"},
		{"top-null", "null"},
		{"truncated", valid[:len(valid)-1]},
		{"trailing", valid + "{}"},
		{"unexpected-job", strings.TrimSuffix(valid, "}") + `,"unexpected":{"result":"success"}}`},
		{"duplicate-job", strings.TrimSuffix(valid, "}") + "," + strconv.Quote(stageJob) + `:{"result":"success"}}`},
		{"duplicate-result", strings.Replace(valid, `"result":"success"`, `"result":"success","result":"failure"`, 1)},
		{"missing-result", strings.Replace(valid, `"result":"success"`, `"outputs":{}`, 1)},
		{"unknown-field", strings.Replace(valid, `"result":"success"`, `"result":"success","other":"MUST-NOT-BE-LOGGED"`, 1)},
		{"unknown-result", strings.Replace(valid, `"result":"success"`, `"result":"MUST-NOT-BE-LOGGED"`, 1)},
		{"result-null", strings.Replace(valid, `"result":"success"`, `"result":null`, 1)},
		{"outputs-null", strings.Replace(valid, `"outputs":{}`, `"outputs":null`, 1)},
		{"outputs-number", strings.Replace(valid, `"outputs":{}`, `"outputs":{"secret":23}`, 1)},
		{"outputs-array", strings.Replace(valid, `"outputs":{}`, `"outputs":{"secret":[]}`, 1)},
		{"outputs-nested", strings.Replace(valid, `"outputs":{}`, `"outputs":{"secret":{"deeper":"MUST-NOT-BE-LOGGED"}}`, 1)},
		{"duplicate-output", strings.Replace(valid, `"outputs":{}`, `"outputs":{"secret":"one","secret":"two"}`, 1)},
		{"duplicate-outputs-field", strings.Replace(valid, `"outputs":{}`, `"outputs":{},"outputs":{}`, 1)},
		{"invalid-utf8", valid + string([]byte{0xff})},
		{"too-large", strings.Repeat(" ", maxGitHubProviderBytes) + valid},
		{"too-complex", carrierTestJSON(t, complex)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var output bytes.Buffer
			err := RecoverGitHubArtifacts(context.Background(), plan, test.raw, GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: "summary.md"})
			if factoryErrorCode(err) != e.CodeInvalidCatalog {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "MUST-NOT-BE-LOGGED") {
				t.Fatal("provider description leaked through error")
			}
			if test.name == "too-complex" && !strings.Contains(err.Error(), "complexity") {
				t.Fatalf("complexity bound not exercised: %v", err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 || output.Len() != 0 {
				t.Fatalf("provider rejection performed work: %v %v %s", entries, err, output.String())
			}
		})
	}
}

func TestGitHubCarriersAcceptExactProviderResults(t *testing.T) {
	plan := githubTestPlan(t)
	results := carrierTestResults(t, plan)
	jobs := make([]string, 0, len(results))
	for job := range results {
		jobs = append(jobs, job)
	}
	sort.Strings(jobs)
	statuses := []string{"success", "failure", "cancelled", "skipped"} //nolint:misspell // GitHub result enum spelling.
	for i, job := range jobs {
		results[job] = map[string]any{"result": statuses[i%len(statuses)], "outputs": map[string]string{"ignored": "::warning:: do not log ${{secrets.A}}"}}
	}
	observed, err := parseGitHubProviderResults(context.Background(), carrierTestJSON(t, results), jobs)
	if err != nil {
		t.Fatal(err)
	}
	for i, job := range jobs {
		if observed[job] != statuses[i%len(statuses)] {
			t.Fatalf("result %s=%s", job, observed[job])
		}
	}
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil || !equalFactoryStrings(derived, jobs) {
		t.Fatalf("finalizer jobs=%v expected=%v error=%v", derived, jobs, err)
	}
}
