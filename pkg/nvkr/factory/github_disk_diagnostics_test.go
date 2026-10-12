// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestInspectionDiagnosticRetention(t *testing.T) {
	for _, kind := range []string{"ordinary", "truncated", "empty", "oversized", "parent-symlink", "existing-file", "final-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			workspace, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			record := []byte("{\"phase\":\"root-scan\",\"event\":\"start\",\"elapsedMillis\":1}\n")
			raw := record
			switch kind {
			case "empty":
				raw = nil
			case "oversized":
				raw = bytes.Repeat(record, diskdiagnostic.MaxRetainedBytes/len(record)+1)
			}
			filename := filepath.Join(root, filepath.FromSlash(githubInspectionLogPath("node-image-arm64")))
			target := filepath.Join(root, "untouched")
			if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "parent-symlink":
				if err := os.Symlink(root, filepath.Join(root, ".nvkr")); err != nil {
					t.Fatal(err)
				}
			case "existing-file", "final-symlink":
				if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "existing-file" {
					err = os.WriteFile(filename, []byte("previous"), 0o600)
				} else {
					err = os.Symlink(target, filename)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err = retainGitHubInspectionLog(t.Context(), workspace, "node-image-arm64", raw, kind == "truncated")
			switch kind {
			case "oversized":
				assertFactoryErrorCode(t, err, e.CodeInternal)
				if !strings.Contains(err.Error(), "exceeds its bounded collector") {
					t.Fatal(err)
				}
				if _, err := os.Stat(filename); !os.IsNotExist(err) {
					t.Fatal("oversized diagnostic created a file")
				}
			case "parent-symlink", "existing-file", "final-symlink":
				assertFactoryErrorCode(t, err, e.CodeInternal)
				want := "publish inspection diagnostic file"
				if kind == "parent-symlink" {
					want = "hold inspection diagnostic parent"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatal(err)
				}
				if raw, err := os.ReadFile(target); err != nil || string(raw) != "untouched" {
					t.Fatal("changed target", err)
				}
				if kind == "existing-file" {
					if raw, _ := os.ReadFile(filename); string(raw) != "previous" {
						t.Fatal("overwrote previous diagnostic")
					}
				}
			case "empty":
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filename); !os.IsNotExist(err) {
					t.Fatal("empty diagnostic created a file")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filename)
				if err != nil || len(raw) > maxGitHubInspectionLogBytes || !bytes.HasSuffix(raw, record) {
					t.Fatalf("invalid retained tail (%d): %v", len(raw), err)
				}
				if kind == "truncated" && !bytes.HasPrefix(raw, []byte(inspectionLogTruncated)) {
					t.Fatal("missing truncation marker")
				}
				info, _ := os.Stat(filename)
				if info.Mode().Perm() != 0o600 {
					t.Fatal("diagnostic file not private")
				}
			}
		})
	}
}

func TestInspectionDiagnosticRetentionContextAdmission(t *testing.T) {
	for _, mode := range []string{"nil", "canceled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			workspace, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			var ctx context.Context
			want := e.CodeUsage
			switch mode {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
				want = e.CodeTimeout
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer cancel()
				want = e.CodeTimeout
			}
			assertFactoryErrorCode(t, retainGitHubInspectionLog(ctx, workspace, "disk", []byte("{}\n"), false), want)
			if _, err := os.Stat(filepath.Join(root, ".nvkr")); !os.IsNotExist(err) {
				t.Fatalf("unadmitted retention created state: %v", err)
			}
		})
	}
}

func TestARCInspectionDiagnosticUploadIsSeparateFromEvidence(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			var document githubWorkflowDocument
			if err := yaml.UnmarshalStrict(bundle.Files[GitHubWorkflowPath], &document); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, job := range document.Jobs {
				for _, step := range job.Steps {
					if step.Name != "Retain inspection diagnostics" {
						continue
					}
					count++
					if step.If != "${{ always() }}" || !step.ContinueOnError || step.TimeoutMinutes != githubDiskDiagnosticUploadMinutes || step.With["if-no-files-found"] != "ignore" || step.With["retention-days"] != "1" || step.With["include-hidden-files"] != "true" {
						t.Fatal("diagnostic retention changes required evidence semantics")
					}
					if !strings.HasPrefix(step.With["path"], ".nvkr/diagnostics/inspection/") || !strings.HasSuffix(step.With["path"], ".jsonl") || strings.ContainsAny(step.With["path"], "*?") {
						t.Fatal("diagnostic upload includes unplanned files")
					}
				}
			}
			if count != 1 {
				t.Fatalf("diagnostic upload count = %d", count)
			}
		})
	}
}

func TestInspectionDiagnosticRetentionAcceptsSampledLastBeforeFinish(t *testing.T) {
	workspace, _, err := securepath.OpenDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	collector := diskdiagnostic.NewCollector()
	subject := "sha256:" + strings.Repeat("a", 64)
	receiver := diskdiagnostic.NewSubjectReceiver(collector, "disk-image", subject)
	for index := 0; index <= diskdiagnostic.MaxForwardedRecords; index++ {
		_, _ = fmt.Fprintf(receiver, "%s{\"phase\":\"root-scan\",\"event\":\"start\",\"elapsedMillis\":1,\"ordinal\":%d}\n", diskdiagnostic.Prefix, index)
	}
	// The forwarding cap guarantees the final record is sampled out, regardless
	// of elapsed wall time. Retention must fit it before the stream is finalized.
	raw, truncated := collector.Snapshot()
	last, err := json.Marshal(diskdiagnostic.Record{
		Phase: "root-scan", Event: "start", ElapsedMillis: 1,
		Ordinal: diskdiagnostic.MaxForwardedRecords, Kind: "disk-image", Subject: subject,
	})
	if err != nil {
		t.Fatal(err)
	}
	last = append(last, '\n')
	if receiver.Unexpected() || !truncated || len(raw) <= diskdiagnostic.MaxRetainedBytes-2*diskdiagnostic.MaxRecordBytes || len(raw) > diskdiagnostic.MaxRetainedBytes || !bytes.HasSuffix(raw, last) || bytes.Count(raw, last) != 1 {
		t.Fatalf("near-capacity sampled-last snapshot missing: bytes=%d truncated=%t unexpected=%t", len(raw), truncated, receiver.Unexpected())
	}
	if err := retainGitHubInspectionLog(t.Context(), workspace, "disk", raw, truncated); err != nil {
		t.Fatalf("retention rejected a bounded sampled-last snapshot: %v", err)
	}
	retained, err := os.ReadFile(filepath.Join(workspace.Name(), githubInspectionLogPath("disk")))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(inspectionLogTruncated), raw...)
	if len(retained) > maxGitHubInspectionLogBytes || !bytes.Equal(retained, want) {
		t.Fatal("retention changed or exceeded the bounded sampled-last snapshot")
	}
	receiver.Finish()
}

func TestDiskCompletionReportingPreservesOwnerResult(t *testing.T) {
	for _, mode := range []string{"committed then canceled", "failure then canceled", "owning timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var primary error
			wantStatus, wantResult := 0, "completed"
			switch mode {
			case "failure then canceled":
				primary = e.New(e.CodeVerifyFailed, "exact owning failure")
				wantStatus, wantResult = 22, "failed"
			case "owning timeout":
				primary = e.Wrap(e.CodeTimeout, context.DeadlineExceeded, "exact owning deadline")
				wantStatus, wantResult = 31, "failed"
			}
			collector := diskdiagnostic.NewCollector()
			receiver := diskdiagnostic.NewReceiver(collector)
			_, _ = io.WriteString(receiver, diskdiagnostic.Prefix+`{"phase":"root-scan","event":"start","elapsedMillis":1}`+"\n")
			receiver.Finish()
			var stdout, stderr bytes.Buffer
			options := GitHubStepOptions{Stdout: &stdout, Stderr: &stderr, SummaryPath: filepath.Join(t.TempDir(), "summary.md")}
			err := reportGitHubDiskCompletion(ctx, options, "Complete disk", collector, func(operationCtx context.Context) error {
				if operationCtx != ctx {
					t.Fatal("completion did not receive its original context")
				}
				cancel()
				return primary
			})
			if err != primary || GitHubStepExitCode(err) != wantStatus {
				t.Fatalf("reporter replaced the owning result: got %v, want %v", err, primary)
			}
			if mode == "owning timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("owning timeout cause lost")
			}
			summary, readErr := os.ReadFile(options.SummaryPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(summary), fmt.Sprintf("| %s | %d |", wantResult, wantStatus)) || !strings.Contains(string(summary), "Inspection: root-scan") {
				t.Fatalf("summary differs from owning result: %s", summary)
			}
			if primary == nil {
				if !strings.Contains(stdout.String(), "Completed: Complete disk") || stderr.Len() != 0 {
					t.Fatalf("committed operation mislabeled: stdout=%s stderr=%s", &stdout, &stderr)
				}
			} else if strings.Contains(stdout.String(), "Completed:") || !strings.Contains(stdout.String(), `"phase":"root-scan"`) || !strings.Contains(stderr.String(), fmt.Sprintf("at Inspection: root-scan (exit %d,", wantStatus)) {
				t.Fatalf("owning failure diagnostics lost: stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}

func TestInspectionDiagnosticTailWiring(t *testing.T) {
	for _, mode := range []string{"success", "failure", "canceled", "retention-failure", "deferred"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			workspace, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			var stdout, stderr bytes.Buffer
			options := GitHubStepOptions{Stdout: &stdout, Stderr: &stderr, SummaryPath: filepath.Join(root, "summary.md")}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			collector := diskdiagnostic.NewCollector()
			receiver := diskdiagnostic.NewReceiver(collector)
			// Changing the member ordinal crosses the forwarding limit without
			// depending on wall-clock sampling or scheduler latency.
			for index := 0; index <= diskdiagnostic.MaxForwardedRecords; index++ {
				_, _ = fmt.Fprintf(receiver, "%s{\"phase\":\"root-scan\",\"event\":\"start\",\"elapsedMillis\":1,\"ordinal\":%d}\n", diskdiagnostic.Prefix, index)
			}
			receiver.Finish()
			if mode == "deferred" {
				collector = nil
			}
			primary := error(nil)
			if mode != "success" && mode != "deferred" {
				primary = e.New(e.CodeVerifyFailed, "primary unchanged")
			}
			result := githubProgress(ctx, options, "Inspect disk", nil, func(progress *githubStepProgress) error {
				if mode == "canceled" {
					cancel()
				}
				reportGitHubInspectionDiagnostics(progress, collector, primary)
				return primary
			})
			if mode == "canceled" {
				assertFactoryErrorCode(t, result, e.CodeTimeout)
			} else if result != primary {
				t.Fatal("diagnostics changed primary", result)
			}
			if primary != nil && !errors.Is(result, primary) {
				t.Fatal("lost primary cause")
			}
			filename := filepath.Join(root, githubInspectionLogPath("disk"))
			if mode == "retention-failure" {
				if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte("previous"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			finishGitHubInspectionDiagnostics(ctx, workspace, "disk", collector, options)
			if mode == "deferred" {
				if _, err := os.Stat(filename); !os.IsNotExist(err) {
					t.Fatal("deferred mode retained inspection", err)
				}
				return
			}
			summary, err := os.ReadFile(options.SummaryPath)
			if err != nil || !bytes.Contains(summary, []byte("Inspection: root-scan")) {
				t.Fatalf("last inspection phase not in summary: %s %v", summary, err)
			}
			if mode == "success" {
				if strings.Contains(stdout.String(), "::group::Inspection") || strings.Contains(stdout.String(), `"phase"`) {
					t.Fatal("successful inspection dumped snapshot")
				}
			} else if !strings.Contains(stdout.String(), "::group::Inspection diagnostics\n"+inspectionLogTruncated) || !strings.Contains(stdout.String(), "::endgroup::") {
				t.Fatal("failure dump lost folding or truncation", stdout.String())
			}
			raw, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retention-failure" {
				if string(raw) != "previous" || !strings.Contains(stderr.String(), "publish inspection diagnostic file") {
					t.Fatal("lost retention cause or overwrote previous file", stderr.String())
				}
			} else if !bytes.HasPrefix(raw, []byte(inspectionLogTruncated)) || !bytes.Contains(raw, []byte(`"phase":"root-scan"`)) {
				t.Fatal("retention missed before workspace closure", string(raw))
			}
			entries, err := os.ReadDir(filepath.Dir(filename))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".nvkr-download-") {
					t.Fatal("temporary diagnostic survived completion")
				}
			}
			// The original held directory remains usable until the caller closes it.
			if _, err := workspace.Stat(); err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubDiskDiagnosticsAndDeadlineSemantics(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, mutation := range []string{"valid", "missing", "duplicate", "always upload", "substituted readiness output", "workspace path", "substituted route output", "long retention", "ambient credentials", "unbounded upload", "changed upload duration", "late upload", "outer equals inner"} {
			t.Run(platform+"/"+mutation, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				stages, _, err := githubStages(plan)
				if err != nil {
					t.Fatal(err)
				}
				var stage githubStage
				for _, candidate := range stages {
					if candidate.DiskOutput != "" {
						stage = candidate
					}
				}
				steps := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath]).Jobs[stage.ID].Steps
				index := slices.IndexFunc(steps, func(s githubStep) bool { return s.Name == "Upload failed Packer diagnostics" })
				if index < 0 {
					t.Fatal("no diagnostic upload")
				}
				switch mutation {
				case "missing":
					steps = slices.Delete(steps, index, index+1)
				case "duplicate":
					steps = append(steps, steps[index])
				case "always upload":
					steps[index].If = "${{ always() }}"
				case "substituted readiness output":
					steps[index].If = "${{ failure() && steps.other.outputs.packer_diagnostics != '' }}"
				case "workspace path":
					steps[index].With["path"] = "."
				case "substituted route output":
					steps[index].With["path"] = "${{ steps.other.outputs.packer_diagnostics }}"
				case "long retention":
					steps[index].With["retention-days"] = "90"
				case "ambient credentials":
					steps[index].Env = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"}
				case "unbounded upload":
					steps[index].TimeoutMinutes = 0
				case "changed upload duration":
					steps[index].TimeoutMinutes = githubDiskDiagnosticUploadMinutes + 1
				case "late upload":
					steps[index-1], steps[index] = steps[index], steps[index-1]
				case "outer equals inner":
					steps[index-1].TimeoutMinutes = int(stage.diskExecutionBudget / time.Minute)
				}
				err = validateGitHubStageExecution(plan, stage, steps)
				if mutation == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				}
			})
		}
	}
}
