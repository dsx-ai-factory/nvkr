package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestRunLocalDryRunIsCompleteAndPerformsNoIO(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := githubTestPlan(t)
	var output bytes.Buffer
	result, err := RunLocal(context.Background(), plan, LocalRunOptions{
		Workspace: ".nkd/factory-dry-run", DryRun: true, Publish: false, Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != len(plan.Graph.Nodes) {
		t.Fatalf("dry-run nodes = %d, want %d", len(result.Nodes), len(plan.Graph.Nodes))
	}
	if !strings.Contains(output.String(), "[skipped] publish/tools") ||
		!strings.Contains(output.String(), "package/tools") {
		t.Fatalf("dry-run did not expose packaging and opt-in publication:\n%s", output.String())
	}
	if _, err := os.Lstat(".nkd/factory-dry-run"); !os.IsNotExist(err) {
		t.Fatalf("dry-run created workspace: %v", err)
	}
}

func TestRunLocalRejectsUnsafeWorkspaceBeforeDependencies(t *testing.T) {
	plan := githubTestPlan(t)
	_, err := RunLocal(context.Background(), plan, LocalRunOptions{Workspace: t.TempDir(), DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "repository-relative child of .nkd") {
		t.Fatalf("unsafe workspace error = %v", err)
	}
}

func TestLocalRunnerIsRerunnableAndPreservesSourceCache(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	plan := githubTestPlan(t)
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/factory-test", Jobs: 3})
	deps := localRunDeps{
		prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) {
			return map[string]string{"linux/amd64": "fake"}, nil
		},
		runBuild: func(_ context.Context, build BuildPlan, options LocalRunOptions) (*pipeline.Report, error) {
			report := &pipeline.Report{BuildDigest: build.BuildDigest}
			for _, component := range build.Components {
				var planned Artifact
				for _, artifact := range plan.Graph.Artifacts {
					if artifact.ID == "artifact/"+build.Name+"/"+component {
						planned = artifact
						break
					}
				}
				path, err := relocatedFactoryPath(planned.Path, options.Workspace)
				if err != nil {
					return nil, err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, []byte(component), 0o755); err != nil {
					return nil, err
				}
				report.Artifacts = append(report.Artifacts, pipeline.ArtifactResult{
					Component: component, OutputType: planned.Kind, Qualification: pipeline.ArtifactUnqualified,
					AbsPath: path, SHA256: "sha256:" + strings.Repeat("a", 64), Size: int64(len(component)),
				})
			}
			return report, nil
		},
		executeNode: func(_ context.Context, plan *Plan, node Node, options LocalRunOptions, _ map[string]string) error {
			_, artifacts := planIndex(plan)
			for _, output := range node.Outputs {
				path, err := relocatedFactoryPath(artifacts[output].Path, options.Workspace)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(node.ID), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for run := 0; run < 2; run++ {
		if run == 1 {
			cache := filepath.Join(options.Workspace, "worker", "src", "cache-sentinel")
			if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			canary := filepath.Join(root, "outside-canary")
			if err := os.WriteFile(canary, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			packagePath := filepath.Join(options.Workspace, "packages", "tools")
			if err := os.Remove(packagePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(canary, packagePath); err != nil {
				t.Fatal(err)
			}
		}
		result, err := runLocal(context.Background(), plan, options, deps)
		if err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
		if len(result.Nodes) != len(plan.Graph.Nodes) {
			t.Fatalf("run %d nodes = %d, want %d", run+1, len(result.Nodes), len(plan.Graph.Nodes))
		}
	}
	cache := filepath.Join(options.Workspace, "worker", "src", "cache-sentinel")
	if raw, err := os.ReadFile(cache); err != nil || string(raw) != "preserve" {
		t.Fatalf("source cache was not preserved: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(options.Workspace, "run.json")); err != nil {
		t.Fatalf("run record missing: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "outside-canary")); err != nil || string(raw) != "outside" {
		t.Fatalf("rerun followed a derived-output symlink: %q, %v", raw, err)
	}
}
