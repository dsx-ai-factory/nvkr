package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestExecutionBundleDriftClassification(t *testing.T) {
	for _, kind := range []string{"exact", "stale-pins", "hand-edit", "incomplete-bundle"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "workflow.yml")
			fixture := newExecutionBundleFixture(t, out)
			if err := writeExecutionBundle(t.Context(), out, fixture.emitted, fixture.nodes, fixture.transportPlan, fixture.manifest); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "stale-pins":
				raw := strings.ReplaceAll(string(fixture.emitted), fixture.buildDigest, "sha256:"+strings.Repeat("f", 64))
				if raw == string(fixture.emitted) {
					t.Fatal("fixture lacks build header")
				}
				if err := os.WriteFile(out, []byte(raw), 0o644); err != nil {
					t.Fatal(err)
				}
			case "hand-edit":
				raw := []byte(strings.Replace(string(fixture.emitted), "jobs:", "jobz:", 1))
				if string(raw) == string(fixture.emitted) {
					t.Fatal("fixture lacks workflow jobs")
				}
				if err := os.WriteFile(out, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			case "incomplete-bundle":
				if err := os.Remove(filepath.Join(filepath.Dir(out), pipeline.NodesFile)); err != nil {
					t.Fatal(err)
				}
			}
			err := bundleCheck(t.Context(), out, "github", fixture.buildDigest, fixture.emitted, fixture.nodes, fixture.transportPlan, true)
			if kind == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertLifecycleCommandCode(t, err, e.CodePipelineDrift)
			if !strings.Contains(err.Error(), "("+kind+")") {
				t.Fatalf("wrong drift class: %v", err)
			}
		})
	}
}

func TestBundleFileStateBindsExistingBytesAndRejectsLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "member")
	state, err := bundleFileState(t.Context(), path)
	if err != nil || state != "absent" {
		t.Fatalf("missing state: %s, %v", state, err)
	}
	raw := []byte(strings.Repeat("member bytes", 20000))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = bundleFileState(t.Context(), path)
	if err != nil || state != pipeline.FileDigest(raw) {
		t.Fatalf("state lost byte identity: %s, %v", state, err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	_, err = bundleFileState(t.Context(), link)
	assertLifecycleCommandCode(t, err, e.CodeWriteConflict)
}
