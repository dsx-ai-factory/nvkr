// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestWriteGeneratedBundleRetiresOnlyUnchangedManifestMembers(t *testing.T) {
	bundle, err := RenderGitHub(context.Background(), githubTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		edit     bool
		unlisted bool
	}{
		{name: "unchanged obsolete graph"},
		{name: "changed obsolete graph", edit: true},
		{name: "unlisted graph", unlisted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			prior := *bundle
			prior.Files = make(map[string][]byte, len(bundle.Files)+1)
			for name, raw := range bundle.Files {
				prior.Files[name] = raw
			}
			obsolete := filepath.ToSlash(filepath.Join(filepath.Dir(bundle.ManifestPath), "retired", "old.json"))
			if !test.unlisted {
				prior.Files[obsolete] = []byte("old graph\n")
			}
			if err := WriteGeneratedBundle(context.Background(), root, &prior); err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(root, bundle.ManifestPath)
			before, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			obsoletePath := filepath.Join(root, obsolete)
			if test.edit || test.unlisted {
				if err := os.MkdirAll(filepath.Dir(obsoletePath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(obsoletePath, []byte("operator bytes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err = WriteGeneratedBundle(context.Background(), root, bundle)
			if test.edit || test.unlisted {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				after, readErr := os.ReadFile(manifestPath)
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected retirement changed prior manifest")
				}
				retained, readErr := os.ReadFile(obsoletePath)
				if readErr != nil || string(retained) != "operator bytes\n" {
					t.Fatal("rejected retirement removed operator bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(obsoletePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("obsolete member remains: %v", err)
			}
			if err := CheckGeneratedBundle(context.Background(), root, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

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

func TestCheckGeneratedBundleRejectsUnexpectedOwnedPayloads(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		relative string
		symlink  bool
	}{
		{name: "graph", relative: "graphs/unexpected.json"},
		{name: "composition", relative: "compositions/unexpected.json"},
		{name: "symlink", relative: "graphs/unexpected-link.json", symlink: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := WriteGeneratedBundle(context.Background(), root, bundle); err != nil {
				t.Fatal(err)
			}
			unownedWorkflow := filepath.Join(root, ".github", "workflows", "operator.yml")
			if err := os.WriteFile(unownedWorkflow, []byte("name: operator\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedBundle(context.Background(), root, bundle); err != nil {
				t.Fatalf("operator-owned workflow was treated as generated Run drift: %v", err)
			}
			unexpected := filepath.Join(root, filepath.Dir(filepath.FromSlash(bundle.ManifestPath)), filepath.FromSlash(test.relative))
			if err := os.MkdirAll(filepath.Dir(unexpected), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.symlink {
				if err := os.Symlink(filepath.Join(root, filepath.FromSlash(bundle.PlanPath)), unexpected); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(unexpected, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			err := CheckGeneratedBundle(context.Background(), root, bundle)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodePipelineDrift {
				t.Fatalf("unexpected owned payload returned %v, want %s", err, e.CodePipelineDrift)
			}
		})
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

// Raw staging, sync and publication I/O must not reach callers uncoded, and
// an owner's own code (usage, drift, timeout) must survive unchanged.
func TestWriteGeneratedBundleCodesEveryFailure(t *testing.T) {
	bundle, err := RenderGitHub(t.Context(), githubTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	previous := syncGeneratedDirectory
	t.Cleanup(func() { syncGeneratedDirectory = previous })
	for _, test := range []struct {
		name string
		sync error
		want e.Code
	}{
		{"success", nil, ""},
		{"coded", e.New(e.CodeUnavailable, "injected coded sync failure"), e.CodeUnavailable},
		{"raw directory sync", errors.New("injected raw sync failure"), e.CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			syncGeneratedDirectory = func(directory *os.File) error {
				if test.sync != nil {
					return test.sync
				}
				return previous(directory)
			}
			err := WriteGeneratedBundle(t.Context(), t.TempDir(), bundle)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, test.want)
			if !errors.Is(err, test.sync) {
				t.Fatal("coded write error lost its cause")
			}
		})
	}
}

// An early raw staging failure names the logical member, keeps the
// os.PathError and its cause, and leaves every published byte in place.
func TestWriteGeneratedBundleStagingFailureKeepsPublishedBytes(t *testing.T) {
	bundle, err := RenderGitHub(t.Context(), githubTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	// Prior bytes differ from the bundle, so any publication would change them.
	if err := os.WriteFile(filepath.Join(root, bundle.WorkflowPath), []byte("operator bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		tree := map[string]string{}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			tree[path] = fmt.Sprintf("%s %q", info.Mode(), raw)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return tree
	}
	before := snapshot()

	previous := syncStagedMember
	t.Cleanup(func() { syncStagedMember = previous })
	cause := errors.New("injected raw staging failure")
	var staged string
	syncStagedMember = func(file *os.File) error {
		staged = file.Name()
		return &os.PathError{Op: "sync", Path: file.Name(), Err: cause}
	}
	err = WriteGeneratedBundle(t.Context(), root, bundle)
	assertFactoryErrorCode(t, err, e.CodeInternal)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "sync" || pathErr.Path != staged || !errors.Is(err, cause) {
		t.Fatalf("staging error lost its os.PathError or cause: %v", err)
	}
	first := sortedFileNames(bundle.Files)[0]
	if !strings.Contains(err.Error(), "staging generated member "+first+":") {
		t.Fatalf("staging error does not name logical member %s: %v", first, err)
	}
	if after := snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed staging changed published bytes:\nbefore %v\nafter  %v", before, after)
	}
}
