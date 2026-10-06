package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
				if err := os.Symlink(root, filepath.Join(root, ".nkd")); err != nil {
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
			if _, err := os.Stat(filepath.Join(root, ".nkd")); !os.IsNotExist(err) {
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
					if step.If != "${{ always() }}" || !step.ContinueOnError || step.With["if-no-files-found"] != "ignore" || step.With["retention-days"] != "1" || step.With["include-hidden-files"] != "true" {
						t.Fatal("diagnostic retention changes required evidence semantics")
					}
					if !strings.HasPrefix(step.With["path"], ".nkd/diagnostics/inspection/") || !strings.HasSuffix(step.With["path"], ".jsonl") || strings.ContainsAny(step.With["path"], "*?") {
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
			for index := 0; index < 3; index++ {
				_, _ = fmt.Fprintf(receiver, "%s{\"phase\":\"root-scan\",\"event\":\"start\",\"elapsedMillis\":%d}\n", diskdiagnostic.Prefix, index)
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
				if strings.HasPrefix(entry.Name(), ".nkd-download-") {
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
