// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"maps"
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
	bundle.Files[child] = []byte("name: fixture\n'on':\n  workflow_call: {}\n")
	return bundle, child
}

func TestGeneratedWorkflowMembersUseExistingManifest(t *testing.T) {
	root := t.TempDir()
	var bundles []*GeneratedBundle
	for _, namespace := range []string{"", "development", "nightly", "nightly-other"} {
		bundle, child := generatedWorkflowFixture(t, namespace)
		manifest, _, err := ManifestFor(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.SchemaVersion != "nvkr.dev/generated-run/v1" || manifest.WorkflowPath != bundle.WorkflowPath || manifest.WorkflowDigest != pipeline.FileDigest(bundle.Files[bundle.WorkflowPath]) {
			t.Fatal("child workflow changed manifest schema or entry identity")
		}
		index := slices.IndexFunc(manifest.Members, func(member GeneratedMember) bool { return member.Path == child })
		if index < 0 || manifest.Members[index].Mode != 0o644 || manifest.Members[index].SHA256 != pipeline.FileDigest(bundle.Files[child]) {
			t.Fatal("child workflow omitted exact bytes or mode from manifest")
		}
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, bundle)
	}
	for _, bundle := range bundles {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGeneratedWorkflowLegacyManifestMigration(t *testing.T) {
	bundle, _ := generatedWorkflowFixture(t, "")
	legacy := *bundle
	legacy.Files = maps.Clone(bundle.Files)
	for name := range legacy.Files {
		if IsGeneratedGitHubChildWorkflowPath(name) {
			delete(legacy.Files, name)
		}
	}
	root := t.TempDir()
	for _, generation := range []*GeneratedBundle{&legacy, bundle, &legacy} {
		if err := WriteGeneratedBundle(t.Context(), root, generation); err != nil {
			t.Fatal(err)
		}
		if err := CheckGeneratedBundle(t.Context(), root, generation); err != nil {
			t.Fatal(err)
		}
	}
	for name := range bundle.Files {
		if IsGeneratedGitHubChildWorkflowPath(name) {
			if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("v1 retirement retained child %s: %v", name, err)
			}
		}
	}
}

func TestGeneratedWorkflowClaimsWaitForEarlierOwner(t *testing.T) {
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
	future := *bundle
	future.Files = maps.Clone(bundle.Files)
	child := strings.TrimSuffix(bundle.WorkflowPath, ".yml") + "__earlier-owner.yml"
	future.Files[child] = []byte("earlier owner payload\n")
	writeGeneratedFixture(t, filepath.Join(root, child), future.Files[child])
	_, manifest, err := ManifestFor(&future)
	if err != nil {
		t.Fatal(err)
	}
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

func TestGeneratedWorkflowDriftAndRetirement(t *testing.T) {
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
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			test.edit(t, filepath.Join(root, child))
			if test.name != "unchanged" {
				assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
			}
			next := *bundle
			next.Files = maps.Clone(bundle.Files)
			delete(next.Files, child)
			before, err := os.ReadFile(filepath.Join(root, bundle.ManifestPath))
			if err != nil {
				t.Fatal(err)
			}
			err = WriteGeneratedBundle(t.Context(), root, &next)
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
			if err := CheckGeneratedBundle(t.Context(), root, &next); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeneratedWorkflowUnownedClaimsAndInventory(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected e.Code
	}{
		{"conflicting destination", e.CodeWriteConflict},
		{"unexpected reserved child", e.CodePipelineDrift},
		{"exact interrupted payload", ""},
		{"operator and other namespace", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, child := generatedWorkflowFixture(t, "nightly")
			root := t.TempDir()
			name := child
			raw := []byte("operator bytes\n")
			switch test.name {
			case "unexpected reserved child":
				name = strings.TrimSuffix(bundle.WorkflowPath, ".yml") + "__unlisted.yml"
			case "exact interrupted payload":
				raw = bundle.Files[child]
			case "operator and other namespace":
				name = ".github/workflows/operator.yml"
				writeGeneratedFixture(t, filepath.Join(root, ".github/workflows/nvkr-run-other__host-node.yml"), raw)
			}
			writeGeneratedFixture(t, filepath.Join(root, name), raw)
			err := WriteGeneratedBundle(t.Context(), root, bundle)
			if test.expected != "" {
				assertFactoryErrorCode(t, err, test.expected)
				if _, err := os.Lstat(filepath.Join(root, filepath.Dir(bundle.ManifestPath))); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refused ownership claim created its private directory or lease")
				}
				retained, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || !bytes.Equal(retained, raw) {
					t.Fatal("refused ownership claim changed operator bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			writeGeneratedFixture(t, filepath.Join(root, strings.TrimSuffix(bundle.WorkflowPath, ".yml")+"__unlisted.yml"), raw)
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
		})
	}
}

func TestGeneratedWorkflowCrossNamespaceRetirementFailsClosed(t *testing.T) {
	bundle, _ := generatedWorkflowFixture(t, "nightly")
	other, child := generatedWorkflowFixture(t, "nightly-other")
	root := t.TempDir()
	for _, selected := range []*GeneratedBundle{bundle, other} {
		if err := WriteGeneratedBundle(t.Context(), root, selected); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Members = append(manifest.Members, GeneratedMember{Path: child, Mode: 0o644, SHA256: pipeline.FileDigest(other.Files[child])})
	slices.SortFunc(manifest.Members, func(a, b GeneratedMember) int { return strings.Compare(a.Path, b.Path) })
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeGeneratedFixture(t, filepath.Join(root, bundle.ManifestPath), append(raw, '\n'))
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
	if err := CheckGeneratedBundle(t.Context(), root, other); err != nil {
		t.Fatalf("foreign child changed: %v", err)
	}
	changed := *bundle
	changed.Files = maps.Clone(bundle.Files)
	changed.Files[child] = other.Files[child]
	output := filepath.Join(t.TempDir(), "uncreated")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), output, &changed), e.CodeInvalidCatalog)
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid namespace created output")
	}
}

func TestOversizedChildWorkflowCannotWriteGeneratedBundle(t *testing.T) {
	bundle, child := generatedWorkflowFixture(t, "")
	bundle.Files[child] = bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1)
	output := filepath.Join(t.TempDir(), "uncreated")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), output, bundle), e.CodeEngineCapability)
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized child created output")
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
