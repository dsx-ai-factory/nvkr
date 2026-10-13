// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubRunSummariesHaveTwoOwners(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"default", githubTestPlan},
		{"native platforms", func(t *testing.T) *Plan {
			return nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
		}},
		{"metadata publication", referenceTestPlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := tc.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workflow := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
			starts, ends := 0, 0
			finalizations := 0
			for id, job := range workflow.Jobs {
				for index, step := range job.Steps {
					if step.ID == "finalize-catalog" && strings.Contains(step.Run, "run finalize --definition") {
						finalizations++
					}
					if !strings.Contains(step.Run, "GITHUB_STEP_SUMMARY") {
						continue
					}
					if step.Uses != "" || !step.ContinueOnError || strings.Contains(step.Run, "./nvkr-exec") {
						t.Fatalf("summary depends on execution or can affect results: %+v", step)
					}
					switch step.ID {
					case githubRunStartSummaryID:
						starts++
						wantIndex := 0
						if len(plan.Environment.GitHubRunners) > 0 {
							wantIndex = 1
							if job.Steps[0].Name != githubPlatformCheckName {
								t.Fatal("summary displaced platform admission")
							}
						}
						if !strings.HasPrefix(id, "build-runner-") || index != wantIndex || job.Steps[index+1].Uses != plan.Actions["checkout"].Uses() || step.If != "${{ success() && "+githubDispatchCondition(plan)+" }}" {
							t.Fatalf("opening summary is not before admitted bootstrap: %s/%d", id, index)
						}
					case githubRunEndSummaryID:
						ends++
						if id != "nvkr-lifecycle-run" || index != len(job.Steps)-1 || step.If != "${{ always() }}" || job.Steps[index-1].ID != githubRunUploadID {
							t.Fatal("closing summary does not follow final Run upload unconditionally")
						}
					default:
						t.Fatalf("unexpected summary writer: %s/%s", id, step.ID)
					}
				}
			}
			if starts != 1 || ends != 1 || finalizations != 1 {
				t.Fatalf("summary owners: opening=%d closing=%d finalization=%d", starts, ends, finalizations)
			}
		})
	}
}

func TestGitHubRunSummaryResults(t *testing.T) {
	for _, tc := range []struct {
		name, results, finalize, job, upload, status, finalization string
	}{
		{"complete", "success success", "success", "success", "success", "Completed", "completed"},
		{"producer failed", "success failure", "failure", "failure", "success", "Failed", "failed"},
		{"bootstrap failed", "failure skipped", "skipped", "failure", "failure", "Failed", "not run"},
		{"recovery failed", "success success", "skipped", "failure", "failure", "Failed", "not run"},
		{"producer skipped", "success skipped", "skipped", "success", "success", "Incomplete (skipped)", "not run"},
		{"producer canceled", "success cancelled", "skipped", "failure", "failure", "Cancelled", "not run"},      //nolint:misspell // GitHub result enum spelling.
		{"finalizer canceled", "success success", "cancelled", "cancelled", "failure", "Cancelled", "cancelled"}, //nolint:misspell // GitHub result enum spelling.
		{"upload failed", "success success", "success", "failure", "failure", "Failed", "completed"},
		{"upload skipped", "success success", "success", "success", "skipped", "Incomplete (unknown status)", "completed"},
		{"unknown result", "success invented", "success", "success", "success", "Incomplete (unknown status)", "completed"},
		{"missing result", "success", "success", "success", "success", "Incomplete (unknown status)", "completed"},
		{"extra result", "success success success", "success", "success", "success", "Incomplete (unknown status)", "completed"},
		{"missing finalization", "success success", "", "success", "failure", "Incomplete (unknown status)", "not run"},
		{"unknown finalizer", "success success", "invented", "success", "success", "Incomplete (unknown status)", "unknown"},
		{"unknown job", "success success", "success", "invented", "success", "Incomplete (unknown status)", "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := filepath.Join(t.TempDir(), "summary.md")
			env := map[string]string{
				"GITHUB_STEP_SUMMARY": sink, "NVKR_JOB_RESULTS": tc.results,
				"NVKR_FINALIZE_OUTCOME": tc.finalize, "NVKR_JOB_STATUS": tc.job, "NVKR_UPLOAD_OUTCOME": tc.upload,
				"NVKR_RUN_URL":          "https://github.com/example/repository/actions/runs/123",
				"NVKR_RUN_ARTIFACT_URL": "https://github.com/example/repository/actions/runs/123/artifacts/456",
			}
			runGitHubSummaryScript(t, githubRunEndSummary(2).Run, env)
			raw, err := os.ReadFile(sink)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "## NVKR Run result: "+tc.status+"\n") || !strings.Contains(string(raw), "Finalization: "+tc.finalization+".") || !bytes.Contains(raw, []byte("[Job logs]")) {
				t.Fatalf("wrong summary:\n%s", raw)
			}
			if bytes.Contains(raw, []byte("[Run record artifact]")) != (tc.upload == "success") {
				t.Fatalf("artifact link differs from upload outcome: %s", raw)
			}
		})
	}
}

func TestGitHubRunRenderedSummaryResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"default", githubTestPlan},
		{"native platforms", func(t *testing.T) *Plan {
			return nativeGitHubTestPlan(t, map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"})
		}},
		{"metadata publication", referenceTestPlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := tc.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			raw := bundle.Files[GitHubWorkflowPath]
			if PerOutputPublication(plan) {
				var tree yamlv3.Node
				if err := yamlv3.Unmarshal(raw, &tree); err != nil {
					t.Fatal(err)
				}
				var hasAlias func(*yamlv3.Node) bool
				hasAlias = func(node *yamlv3.Node) bool {
					if node.Kind == yamlv3.AliasNode {
						return true
					}
					for _, child := range node.Content {
						if hasAlias(child) {
							return true
						}
					}
					return false
				}
				if !hasAlias(&tree) {
					t.Fatal("publication fixture does not exercise compact workflow decoding")
				}
			}
			workflow := decodeGitHubWorkflow(t, raw)
			finalizer := workflow.Jobs["nvkr-lifecycle-run"]
			if len(finalizer.Needs) == 0 || len(finalizer.Steps) == 0 {
				t.Fatal("rendered finalizer has no dependencies or steps")
			}
			closing := finalizer.Steps[len(finalizer.Steps)-1]
			if closing.ID != githubRunEndSummaryID || closing.Env["NVKR_JOB_RESULTS"] != "${{ join(needs.*.result, ' ') }}" {
				t.Fatal("closing summary does not receive the rendered finalizer's dependency results")
			}
			for _, result := range []struct {
				name, dependency, status string
				failed, skipped          int
			}{
				{"all successful", "success", "Completed", 0, 0},
				{"one failed", "failure", "Failed", 1, 0},
				{"one skipped", "skipped", "Incomplete (skipped)", 0, 1},
			} {
				t.Run(result.name, func(t *testing.T) {
					// Derive provider results from the emitted needs, independently of
					// the expected count embedded in the emitted summary script.
					results := make([]string, len(finalizer.Needs))
					for index := range finalizer.Needs {
						results[index] = "success"
					}
					results[len(results)-1] = result.dependency
					sink := filepath.Join(t.TempDir(), "summary.md")
					runGitHubSummaryScript(t, closing.Run, map[string]string{
						"GITHUB_STEP_SUMMARY": sink, "NVKR_JOB_RESULTS": strings.Join(results, " "),
						"NVKR_JOB_STATUS": "success", "NVKR_FINALIZE_OUTCOME": "success", "NVKR_UPLOAD_OUTCOME": "success",
					})
					summary, err := os.ReadFile(sink)
					if err != nil {
						t.Fatal(err)
					}
					counts := fmt.Sprintf("Upstream jobs: %d succeeded · %d failed · 0 cancelled · %d skipped · 0 unknown.", len(results)-result.failed-result.skipped, result.failed, result.skipped) //nolint:misspell // GitHub result enum spelling.
					if !strings.Contains(string(summary), "## NVKR Run result: "+result.status+"\n") || !strings.Contains(string(summary), counts) {
						t.Fatalf("wrong rendered summary for %d dependencies:\n%s", len(finalizer.Needs), summary)
					}
				})
			}
		})
	}
}

func TestGitHubRunSummariesEscapeDescriptions(t *testing.T) {
	plan := githubTestPlan(t)
	plan.Name = "untrusted ' ` $(exit 99) ${{ secrets.TEST }}\n::error::<script>|"
	start := githubRunStartSummary(plan)
	if strings.Contains(start.Run, "${{ secrets.TEST }}") {
		t.Fatal("description retained expression syntax")
	}
	sink := filepath.Join(t.TempDir(), "summary.md")
	runGitHubSummaryScript(t, start.Run, map[string]string{"GITHUB_STEP_SUMMARY": sink})
	raw, err := os.ReadFile(sink)
	if err != nil || !strings.Contains(string(raw), githubRunSummaryText(plan.Name)) || bytes.Contains(raw, []byte("\n::error::")) || bytes.Contains(raw, []byte("<script>")) {
		t.Fatalf("unsafe summary: %s / %v", raw, err)
	}
	sink = filepath.Join(t.TempDir(), "summary.md")
	runGitHubSummaryScript(t, githubRunEndSummary(2).Run, map[string]string{
		"GITHUB_STEP_SUMMARY": sink, "NVKR_JOB_RESULTS": "success $(exit 99)\n::error::<script>",
		"NVKR_JOB_STATUS": "<script>", "NVKR_FINALIZE_OUTCOME": "::error::", "NVKR_UPLOAD_OUTCOME": "success",
		"NVKR_RUN_URL": "https://example.invalid/)\n::error::", "NVKR_RUN_ARTIFACT_URL": "https://example.invalid/<script>",
	})
	raw, err = os.ReadFile(sink)
	if err != nil || bytes.Contains(raw, []byte("<script>")) || bytes.Contains(raw, []byte("::error::")) || bytes.Contains(raw, []byte("[Job logs]")) || bytes.Contains(raw, []byte("[Run record artifact]")) {
		t.Fatalf("unsafe runtime summary: %s / %v", raw, err)
	}
}

func TestGitHubRunSummaryJobCounts(t *testing.T) {
	for _, tc := range []struct{ name, results, counts string }{
		{"all successful", "success success success success success", "5 succeeded · 0 failed · 0 cancelled · 0 skipped · 0 unknown"}, //nolint:misspell // GitHub result enum spelling.
		{"mixed", "success failure cancelled skipped unexpected", "1 succeeded · 1 failed · 1 cancelled · 1 skipped · 1 unknown"},     //nolint:misspell // GitHub result enum spelling.
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := filepath.Join(t.TempDir(), "summary.md")
			runGitHubSummaryScript(t, githubRunEndSummary(5).Run, map[string]string{
				"GITHUB_STEP_SUMMARY": sink, "NVKR_JOB_RESULTS": tc.results,
				"NVKR_JOB_STATUS": "success", "NVKR_FINALIZE_OUTCOME": "success", "NVKR_UPLOAD_OUTCOME": "success",
			})
			raw, err := os.ReadFile(sink)
			if err != nil || !strings.Contains(string(raw), "Upstream jobs: "+tc.counts+".") {
				t.Fatalf("wrong job counts: %s / %v", raw, err)
			}
		})
	}
}

func TestGitHubRunSummarySinkFailuresAreOptional(t *testing.T) {
	plan := githubTestPlan(t)
	for _, tc := range []struct{ name, script string }{
		{"opening", githubRunStartSummary(plan).Run},
		{"closing", githubRunEndSummary(2).Run},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, sink := range []string{"", root, filepath.Join(root, "missing", "summary.md")} {
				runGitHubSummaryScript(t, tc.script, map[string]string{"GITHUB_STEP_SUMMARY": sink})
			}
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			symlink := filepath.Join(root, "summary.md")
			if err := os.Symlink(target, symlink); err != nil {
				t.Fatal(err)
			}
			runGitHubSummaryScript(t, tc.script, map[string]string{"GITHUB_STEP_SUMMARY": symlink})
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "unchanged" {
				t.Fatalf("summary followed symlink: %s / %v", raw, err)
			}
		})
	}
}

func TestGitHubRunSummaryContractRejectsTampering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"opening guard", "if: ${{ success() && " + githubDispatchCondition(plan) + " }}", "if: ${{ always() }}"},
		{"closing guard", "id: summarize-run-end\n        if: ${{ always() }}", "id: summarize-run-end\n        if: ${{ success() }}"},
		{"blocking diagnostics", "continue-on-error: true\n        run: |", "continue-on-error: false\n        run: |"},
		{"status input", "join(needs.*.result, '' '')", "join(needs.*.result, ''failure'')"},
		{"missing upload identity", "id: upload-run-record", "id: another-upload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for path, raw := range bundle.Files {
				mutated.Files[path] = raw
			}
			original := bundle.Files[GitHubWorkflowPath]
			changed := bytes.Replace(original, []byte(tc.before), []byte(tc.after), 1)
			if bytes.Equal(changed, original) {
				t.Fatal("mutation did not reach the workflow")
			}
			mutated.Files[GitHubWorkflowPath] = changed
			mutated.WorkflowDigest = pipeline.FileDigest(changed)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &mutated), e.CodeEngineCapability)
		})
	}
}

func runGitHubSummaryScript(t *testing.T, script string, env map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "-e", "-c", script)
	command.Dir = t.TempDir()
	for _, key := range sortedKeys(env) {
		command.Env = append(command.Env, key+"="+env[key])
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("diagnostics changed the step result: %v\n%s", err, output)
	}
}
