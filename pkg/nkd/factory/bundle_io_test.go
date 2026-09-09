package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteGeneratedBundleIsRerunnableAndCheckDetectsDrift(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for iteration := 0; iteration < 2; iteration++ {
		if err := WriteGeneratedBundle(context.Background(), root, bundle); err != nil {
			t.Fatalf("write %d: %v", iteration, err)
		}
		if err := CheckGeneratedBundle(context.Background(), root, bundle); err != nil {
			t.Fatalf("check %d: %v", iteration, err)
		}
	}
	workflow := filepath.Join(root, filepath.FromSlash(GitHubWorkflowPath))
	if err := os.WriteFile(workflow, []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(context.Background(), root, bundle); err == nil {
		t.Fatal("hand-edited workflow passed drift check")
	}
	if err := WriteGeneratedBundle(context.Background(), root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(context.Background(), root, bundle); err != nil {
		t.Fatal(err)
	}
}

func TestWriteGeneratedBundleRejectsSymlinkedOwnedDirectory(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".github")); err != nil {
		t.Fatal(err)
	}
	if err := WriteGeneratedBundle(context.Background(), root, bundle); err == nil {
		t.Fatal("symlinked workflow directory was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("factory writer escaped through symlink: %v", entries)
	}
}
