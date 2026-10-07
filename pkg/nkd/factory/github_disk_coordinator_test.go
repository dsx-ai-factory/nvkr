package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestGitHubDiskCoordinatorPreservesOperationFailures(t *testing.T) {
	for _, test := range []struct {
		name, kind, wantMessage string
		interrupt               string
		failure                 error
		wantCode                e.Code
	}{
		{"success", NodeBuild, "", "", nil, ""},
		{"ordinary failure", NodeBuild, "original producer failure", "", e.New(e.CodeBuildFailed, "original producer failure"), e.CodeBuildFailed},
		{"coincident deadline", NodeBuild, "original producer failure", "deadline", e.New(e.CodeBuildFailed, "original producer failure"), e.CodeBuildFailed},
		{"coincident cancellation", NodeBuild, "original producer failure", "cancel", e.New(e.CodeBuildFailed, "original producer failure"), e.CodeBuildFailed},
		{"deadline cause", NodeBuild, `ARC disk producer "selected" exhausted its bounded deadline`, "deadline", context.DeadlineExceeded, e.CodeTimeout},
		{"cancellation cause", NodeBuild, `ARC disk producer "selected" canceled`, "cancel", context.Canceled, e.CodeTimeout},
		{"existing timeout", NodeBuild, "owning producer timed out", "deadline", e.Wrap(e.CodeTimeout, context.DeadlineExceeded, "owning producer timed out"), e.CodeTimeout},
		{"verification deadline", NodeVerify, `ARC disk operation "selected" exhausted its bounded deadline`, "deadline", context.DeadlineExceeded, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "github-output")
			t.Setenv("GITHUB_OUTPUT", output)
			ctx, cancel := context.WithCancel(t.Context())
			if test.interrupt == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			stage := githubStage{Nodes: []Node{{ID: "selected", Kind: test.kind, Requirements: &execution.Requirements{TimeoutSeconds: 1}}}}
			owner := &LocalRunResult{}
			var active context.Context
			completed := false
			err := coordinateGitHubDiskExecution(ctx, nil, func(ctx context.Context) error {
				return executeGitHubDiskNodes(ctx, &Plan{}, stage, owner, GitHubStepOptions{}, func(nodeCtx context.Context, _ Node) error {
					active = nodeCtx
					if _, ok := nodeCtx.Deadline(); test.kind == NodeBuild && !ok {
						t.Fatal("producer has no bounded context")
					}
					if test.interrupt == "cancel" {
						cancel()
					}
					if test.interrupt != "" {
						<-nodeCtx.Done()
					}
					return test.failure
				}, func(context.Context) error { completed = true; return nil })
			})
			if active == nil {
				t.Fatal("dispatcher was not exercised")
			}
			if test.failure == nil {
				if err != nil || !completed || len(owner.Nodes) != 1 || active.Err() != context.Canceled {
					t.Fatalf("success failed to complete and release producer context: owner=%+v complete=%v err=%v context=%v", owner.Nodes, completed, err, active.Err())
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.wantCode || coded.Msg != test.wantMessage || !errors.Is(err, test.failure) {
					t.Fatalf("error=%v; want %s %q preserving original cause", err, test.wantCode, test.wantMessage)
				}
				if completed || len(owner.Nodes) != 0 {
					t.Fatal("failed operation reached completion or recorded success")
				}
				if test.wantCode != e.CodeTimeout || test.interrupt == "cancel" || test.name == "existing timeout" {
					if strings.Contains(err.Error(), "exhausted its bounded deadline") {
						t.Fatalf("failure was falsely labeled a new exhausted deadline: %v", err)
					}
				}
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("absent diagnostic owner published a route: %v", err)
			}
		})
	}
}

func TestGitHubDiskCoordinatorUnavailableDiagnosticRoute(t *testing.T) {
	for _, mode := range []string{"nil", "unused", "closed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "github-output")
			t.Setenv("GITHUB_OUTPUT", output)
			var diagnostics *pipeline.PackerDiagnosticSink
			if mode != "nil" {
				held, err := os.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				diagnostics, err = pipeline.NewPackerDiagnosticSink(t.Context(), held)
				_ = held.Close()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "closed" {
					if err := diagnostics.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			failure := e.New(e.CodeBuildFailed, "controlled producer failure")
			err := coordinateGitHubDiskExecution(t.Context(), diagnostics, func(context.Context) error { return failure })
			if err != failure || diagnostics.RetainedPath() != "" {
				t.Fatalf("unavailable route changed primary error or issued a path: %v", err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("unavailable diagnostic owner published output: %v", err)
			}
		})
	}
}
