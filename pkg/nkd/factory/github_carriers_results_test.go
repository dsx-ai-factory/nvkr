// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubRecoveryErrorDetail(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  string
	}{
		{"disk full", syscall.ENOSPC, "NKD-E-VERIFY-FAILED: no space left on device"},
		{"quota", syscall.EDQUOT, "NKD-E-VERIFY-FAILED: disk quota exceeded"},
		{"permissions", os.ErrPermission, "NKD-E-VERIFY-FAILED: permission denied"},
		{"untrusted archive detail", fmt.Errorf("MUST-NOT-BE-LOGGED"), "NKD-E-VERIFY-FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := e.Wrap(e.CodeVerifyFailed, test.cause, "untrusted archive path MUST-NOT-BE-LOGGED")
			if got := githubRecoveryErrorDetail(err); got != test.want {
				t.Fatalf("diagnostic %q want %q", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"uncoded", fmt.Errorf("MUST-NOT-BE-LOGGED"), string(e.CodeInternal)},
		{"timeout", e.Wrap(e.CodeTimeout, context.DeadlineExceeded, "MUST-NOT-BE-LOGGED"), string(e.CodeTimeout)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubRecoveryErrorDetail(test.err); got != test.want {
				t.Fatalf("diagnostic %q want %q", got, test.want)
			}
		})
	}
}

func TestGitHubRecoveryStorageDiagnostics(t *testing.T) {
	for _, cause := range []struct {
		name string
		err  error
	}{
		{"disk-full", syscall.ENOSPC},
		{"quota", syscall.EDQUOT},
	} {
		t.Run(cause.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			t.Chdir(t.TempDir())
			var stdout, stderr bytes.Buffer
			count := 0
			err := recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, carrierTestResults(t, plan)),
				GitHubStepOptions{Stdout: &stdout, Stderr: &stderr, SummaryPath: "summary.md"},
				func(context.Context, Artifact) (string, error) {
					count++
					return "restore-failed", e.Wrap(e.CodeVerifyFailed, cause.err, "MUST-NOT-BE-LOGGED")
				}, writeGitHubRestoreOutcome)
			if err != nil || count != len(plan.Graph.Artifacts) {
				t.Fatalf("ordinary storage failure stopped diagnostic recovery: count=%d error=%v", count, err)
			}
			totals := fmt.Sprintf("Recovery totals: visited=%d restored=0 restore-failed=%d (no-space=%d) missing=0 job-failed=0; persistence: outcome-write-failed=0 (no-space=0)", count, count, count)
			summary, err := os.ReadFile("summary.md")
			if err != nil || !strings.Contains(string(summary), totals) || !strings.Contains(stdout.String(), totals) ||
				!strings.Contains(stderr.String(), "::error::Artifact recovery exhausted storage;") ||
				strings.Contains(stdout.String(), "::error::") ||
				strings.Contains(stdout.String()+stderr.String()+string(summary), "MUST-NOT-BE-LOGGED") {
				t.Fatalf("unsafe or missing storage diagnostics: stdout=%s stderr=%s summary=%s error=%v", stdout.String(), stderr.String(), summary, err)
			}
			if err := ValidateHostedRestores(t.Context(), ".nkd/factory", plan); factoryErrorCode(err) != e.CodeProvenanceMissing {
				t.Fatalf("diagnostic recovery authorized failed restores: %v", err)
			}
		})
	}
}

func TestGitHubRecoveryEarlyReturnDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name                            string
		restoreCause, writeCause        error
		cancel                          bool
		wantCode                        e.Code
		wantRestored, wantFailed        int
		wantRestoreSpace, wantWritten   int
		wantWriteFailed, wantWriteSpace int
	}{
		{"outcome disk full", nil, syscall.ENOSPC, false, e.CodeInternal, 1, 0, 0, 1, 1, 1},
		{"outcome quota", nil, syscall.EDQUOT, false, e.CodeInternal, 1, 0, 0, 1, 1, 1},
		{"outcome permission", nil, os.ErrPermission, false, e.CodeInternal, 1, 0, 0, 1, 1, 0},
		{"restore and outcome disk full", syscall.ENOSPC, syscall.ENOSPC, false, e.CodeInternal, 0, 1, 1, 1, 1, 1},
		{"canceled restore", context.Canceled, nil, true, e.CodeTimeout, 0, 1, 0, 0, 0, 0},
		{"disk full then canceled", syscall.ENOSPC, nil, true, e.CodeTimeout, 0, 1, 1, 0, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			t.Chdir(t.TempDir())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var stdout, stderr, combined bytes.Buffer
			restored, written := 0, 0
			err := recoverGitHubArtifacts(ctx, plan, carrierTestJSON(t, carrierTestResults(t, plan)),
				GitHubStepOptions{Stdout: io.MultiWriter(&stdout, &combined), Stderr: io.MultiWriter(&stderr, &combined), SummaryPath: "summary.md"},
				func(context.Context, Artifact) (string, error) {
					restored++
					if test.cancel {
						cancel()
					}
					if test.restoreCause != nil {
						return "restore-failed", e.Wrap(e.CodeVerifyFailed, test.restoreCause, "MUST-NOT-BE-LOGGED")
					}
					return "restored:success", nil
				}, func(context.Context, string, string) error {
					written++
					return e.Wrap(e.CodeInternal, test.writeCause, "MUST-NOT-BE-LOGGED")
				})
			if factoryErrorCode(err) != test.wantCode || restored != 1 || written != test.wantWritten {
				t.Fatalf("error=%v restored=%d writes=%d", err, restored, written)
			}
			totals := fmt.Sprintf("Recovery totals: visited=1 restored=%d restore-failed=%d (no-space=%d) missing=0 job-failed=0; persistence: outcome-write-failed=%d (no-space=%d)",
				test.wantRestored, test.wantFailed, test.wantRestoreSpace, test.wantWriteFailed, test.wantWriteSpace)
			summary, readErr := os.ReadFile("summary.md")
			if readErr != nil || !strings.Contains(stdout.String(), totals) || !strings.Contains(string(summary), totals) ||
				strings.Index(combined.String(), totals) >= strings.Index(combined.String(), "Failed:") {
				t.Fatalf("missing or late early-return totals: %s summary=%s error=%v", combined.String(), summary, readErr)
			}
			annotation := "::error::Artifact recovery exhausted storage; restoration or outcome persistence is incomplete."
			wantAnnotation := test.wantRestoreSpace+test.wantWriteSpace > 0
			if strings.Contains(stderr.String(), annotation) != wantAnnotation || strings.Contains(stdout.String(), "::error::") ||
				strings.Contains(combined.String()+string(summary), "MUST-NOT-BE-LOGGED") {
				t.Fatalf("unsafe or misrouted early-return annotation: stdout=%s stderr=%s summary=%s", stdout.String(), stderr.String(), summary)
			}
			if !test.cancel && !strings.Contains(stdout.String(), "Recovery outcome failure:") {
				t.Fatal("outcome persistence failure omitted from diagnostics")
			}
			if err := ValidateHostedRestores(t.Context(), ".nkd/factory", plan); factoryErrorCode(err) != e.CodeProvenanceMissing {
				t.Fatalf("early-return diagnostics authorized incomplete restores: %v", err)
			}
		})
	}
}

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
