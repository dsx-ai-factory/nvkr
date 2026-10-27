// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func generatedWorkflowFixture(t *testing.T, namespace string) (*GeneratedBundle, string) {
	t.Helper()
	plan := namedGitHubTestPlan(t, namespace)
	if namespace == "development" {
		plan = githubDevelopmentTestPlan(t)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	child := strings.TrimSuffix(bundle.WorkflowPath, ".yml") + "__ownership-fixture.yml"
	return bundle, child
}

// Construct the old ownership description directly: the current writer must
// never be able to create a bundle containing sibling workflows.
func legacyWorkflowManifest(t *testing.T, bundle *GeneratedBundle, child string, raw []byte) []byte {
	t.Helper()
	manifest, _, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Members = append(manifest.Members, GeneratedMember{Path: child, SHA256: pipeline.FileDigest(raw), Mode: 0o644})
	slices.SortFunc(manifest.Members, func(a, b GeneratedMember) int { return strings.Compare(a.Path, b.Path) })
	encoded, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func seedLegacyWorkflow(t *testing.T, root string, bundle *GeneratedBundle, child string) {
	t.Helper()
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	raw := []byte("name: retired fixture\n'on':\n  workflow_call: {}\n")
	writeGeneratedFixture(t, filepath.Join(root, child), raw)
	writeGeneratedFixture(t, filepath.Join(root, bundle.ManifestPath), legacyWorkflowManifest(t, bundle, child, raw))
}

func TestGeneratedBundleRejectsNewChildWorkflowsBeforeOutput(t *testing.T) {
	for _, namespace := range []string{"", "development", "nightly"} {
		t.Run(namespace, func(t *testing.T) {
			bundle, child := generatedWorkflowFixture(t, namespace)
			bundle.Files[child] = []byte("name: unsupported child\n")
			_, _, err := ManifestFor(bundle)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			output := filepath.Join(t.TempDir(), "uncreated")
			assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), output, bundle), e.CodeInvalidCatalog)
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsupported child created output")
			}
		})
	}
}

func TestGeneratedWorkflowLegacyManifestRetirement(t *testing.T) {
	root := t.TempDir()
	var bundles []*GeneratedBundle
	var children []string
	for _, namespace := range []string{"", "development", "nightly", "nightly-other"} {
		bundle, child := generatedWorkflowFixture(t, namespace)
		seedLegacyWorkflow(t, root, bundle, child)
		bundles = append(bundles, bundle)
		children = append(children, child)
	}
	for index, bundle := range bundles {
		assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(root, children[index])); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired workflow remains: %v", err)
		}
		for _, other := range children[index+1:] {
			if _, err := os.Lstat(filepath.Join(root, other)); err != nil {
				t.Fatalf("another owner's workflow changed: %v", err)
			}
		}
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyWorkflowRetirementWaitsForEarlierOwner(t *testing.T) {
	bundle, _ := generatedWorkflowFixture(t, "")
	root := t.TempDir()
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	release, err := acquireBundleLock(ctx, filepath.Join(root, bundle.ManifestPath+".lease"))
	if err != nil {
		t.Fatal(err)
	}
	unlock := sync.OnceFunc(release)
	defer unlock()
	child := strings.TrimSuffix(bundle.WorkflowPath, ".yml") + "__earlier-owner.yml"
	raw := []byte("earlier owner payload\n")
	writeGeneratedFixture(t, filepath.Join(root, child), raw)
	manifest := legacyWorkflowManifest(t, bundle, child, raw)
	finished := make(chan struct{})
	var writeErr error
	go func() {
		defer close(finished)
		writeErr = WriteGeneratedBundle(ctx, root, bundle)
	}()
	defer func() { cancel(); unlock(); <-finished }()
	select {
	case <-finished:
		t.Fatalf("write inspected an earlier owner's uncommitted payload instead of waiting: %v", writeErr)
	case <-time.After(100 * time.Millisecond):
	}
	writeGeneratedFixture(t, filepath.Join(root, bundle.ManifestPath), manifest)
	unlock()
	<-finished
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, child)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("earlier owner's now-authenticated obsolete member was not retired: %v", err)
	}
}

func TestLegacyWorkflowDriftAndRetirement(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"bytes", func(t *testing.T, name string) { writeGeneratedFixture(t, name, []byte("operator bytes\n")) }},
		{"mode", func(t *testing.T, name string) {
			if err := os.Chmod(name, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, name string) {
			if err := os.Rename(name, name+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(name+".saved", name); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing", func(t *testing.T, name string) {
			if err := os.Rename(name, name+".saved"); err != nil {
				t.Fatal(err)
			}
		}},
		{"unchanged", func(*testing.T, string) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, child := generatedWorkflowFixture(t, "")
			root := t.TempDir()
			seedLegacyWorkflow(t, root, bundle, child)
			test.edit(t, filepath.Join(root, child))
			if test.name != "unchanged" {
				assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
			}
			before, err := os.ReadFile(filepath.Join(root, bundle.ManifestPath))
			if err != nil {
				t.Fatal(err)
			}
			err = WriteGeneratedBundle(t.Context(), root, bundle)
			if test.name != "unchanged" && test.name != "missing" {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				after, readErr := os.ReadFile(filepath.Join(root, bundle.ManifestPath))
				if readErr != nil || !bytes.Equal(before, after) {
					t.Fatal("failed retirement changed manifest")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(root, child)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retired child remains: %v", err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyWorkflowUnownedInventory(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		code e.Code
	}{
		{"unowned child", ".github/workflows/nvkr-run-nightly__host-node.yml", e.CodePipelineDrift},
		{"other namespace", ".github/workflows/nvkr-run-other__host-node.yml", ""},
		{"operator workflow", ".github/workflows/operator.yml", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, child := generatedWorkflowFixture(t, "nightly")
			root := t.TempDir()
			raw := []byte("operator bytes\n")
			name := filepath.Join(root, test.path)
			writeGeneratedFixture(t, name, raw)
			err := WriteGeneratedBundle(t.Context(), root, bundle)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				if _, err := os.Lstat(filepath.Join(root, bundle.ManifestPath)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unowned workflow wrote a manifest")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(retained, raw) {
				t.Fatal("inventory changed unowned bytes")
			}
			if test.code == "" {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
				writeGeneratedFixture(t, filepath.Join(root, child), raw)
				assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
			}
		})
	}
}

func TestLegacyWorkflowMalformedOwnershipFailsClosed(t *testing.T) {
	for _, child := range []string{
		".github/workflows/nvkr-run-other__host-node.yml",
		".github/workflows/nvkr-run-nightly__host.yaml",
		".github/workflows/nvkr-run-nightly__host__node.yml",
		".github/workflows/nvkr-run-nightly__" + strings.Repeat("a", 65) + ".yml",
	} {
		t.Run(child, func(t *testing.T) {
			bundle, _ := generatedWorkflowFixture(t, "nightly")
			root := t.TempDir()
			seedLegacyWorkflow(t, root, bundle, child)
			before, err := os.ReadFile(filepath.Join(root, bundle.ManifestPath))
			if err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
			after, err := os.ReadFile(filepath.Join(root, bundle.ManifestPath))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("malformed ownership changed manifest")
			}
			if _, err := os.Lstat(filepath.Join(root, child)); err != nil {
				t.Fatalf("malformed ownership removed a workflow: %v", err)
			}
		})
	}
}

func writeGeneratedFixture(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
