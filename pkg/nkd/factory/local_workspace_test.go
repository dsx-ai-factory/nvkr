package factory

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

func TestLocalWorkspaceRejectsUnownedOrIndirectPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T)
	}{
		{"unowned contents", func(t *testing.T) { mustWriteWorkspaceFixture(t, ".nkd/run/canary", []byte("preserve")) }},
		{"invalid marker", func(t *testing.T) { mustWriteWorkspaceFixture(t, ".nkd/run/"+localWorkspaceMarker, []byte("invalid")) }},
		{"future marker", func(t *testing.T) {
			mustWriteWorkspaceFixture(t, ".nkd/run/"+localWorkspaceMarker, []byte(`{"schemaVersion":"future","planDigest":"anything"}`))
		}},
		{"file in path", func(t *testing.T) { mustWriteWorkspaceFixture(t, ".nkd/run", []byte("preserve")) }},
		{"symlink in path", func(t *testing.T) {
			if err := os.Mkdir(".nkd", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), ".nkd/run"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			tc.setup(t)
			err := ensureLocalWorkspace(t.Context(), ".nkd/run", pipeline.FileDigest([]byte("plan")))
			assertFactoryErrorCode(t, err, e.CodeImmutableViolation)
		})
	}
}

func mustWriteWorkspaceFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCachedFactoryToolRequiresExactExecutableBytes(t *testing.T) {
	t.Chdir(t.TempDir())
	const workspace = ".nkd/run"
	const path = ".nkd/run/tools/tool"
	raw := []byte("tool bytes")
	node := Node{ID: "tool", Kind: NodeAcquire, ExecutableDigest: pipeline.FileDigest(raw), Outputs: []string{"tool"}}
	plan := &Plan{Graph: ArtifactGraph{Artifacts: []Artifact{{ID: "tool", Path: ".nkd/factory/tools/tool"}}}}
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T)
		want    bool
	}{
		{"missing", nil, false},
		{"exact", func(t *testing.T) { mustWriteWorkspaceFixture(t, path, raw) }, true},
		{"changed bytes", func(t *testing.T) { mustWriteWorkspaceFixture(t, path, []byte("changed")) }, false},
		{"symlink", func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing", path); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tc.prepare != nil {
				tc.prepare(t)
			}
			got, err := reusableFactoryTool(t.Context(), plan, node, workspace)
			if err != nil || got != tc.want {
				t.Fatalf("reuse = %v, %v", got, err)
			}
		})
	}
}

func TestFailedRunnerCompilationCleansPrivateStage(t *testing.T) {
	plan := githubTestPlan(t)
	root := t.TempDir()
	t.Chdir(root)
	compiler := filepath.Join(root, "compiler")
	mustWriteWorkspaceFixture(t, compiler, []byte("#!/bin/sh\nexit 7\n"))
	if err := os.Chmod(compiler, 0o755); err != nil {
		t.Fatal(err)
	}
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/run", RunnerSource: root, Go: compiler})
	_, err := prepareLocalRunners(t.Context(), plan, options, []string{"linux/amd64"})
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	entries, err := os.ReadDir(filepath.Join(options.Workspace, ".runner"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed compilation published files: %v", entries)
	}
}
