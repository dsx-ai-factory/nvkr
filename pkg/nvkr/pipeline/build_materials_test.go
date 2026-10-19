// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"golang.org/x/sync/errgroup"
)

func testBuildMaterials(t *testing.T, observations []material.Observation) (*BuildMaterials, func()) {
	t.Helper()
	manifest, err := material.NewManifest(observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	heldManifest, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{
		Label: "test-build-manifest", MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]BuildMaterialInput, 0, len(observations))
	heldInputs := make([]*heldfile.File, 0, len(observations))
	for _, observation := range observations {
		payload := []byte("payload for " + observation.Binding + "\n")
		digest := FileDigest(payload)
		if observation.Digest != digest {
			_ = heldManifest.Close()
			t.Fatalf("test observation %q digest = %q, want %q", observation.Binding, observation.Digest, digest)
		}
		held, err := heldfile.FromBytesContext(t.Context(), payload, heldfile.Options{
			Label: "test-build-material-" + observation.Binding, MaxBytes: MaxBuildMaterialBytes,
			ExpectedDigest: digest,
		})
		if err != nil {
			t.Fatal(err)
		}
		path, err := material.StagingPath(observation.Binding)
		if err != nil {
			t.Fatal(err)
		}
		heldInputs = append(heldInputs, held)
		inputs = append(inputs, BuildMaterialInput{Path: path, SHA256: digest, Content: held})
	}
	result, err := NewBuildMaterials(heldManifest, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return result, func() {
		for _, held := range heldInputs {
			_ = held.Close()
		}
		_ = heldManifest.Close()
	}
}

func TestNewBuildMaterialsRejectsInvalidInputs(t *testing.T) {
	manifest, err := heldfile.FromBytesContext(t.Context(), []byte("manifest\n"), heldfile.Options{
		Label: "test-build-manifest", MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	payload := []byte("payload\n")
	digest := FileDigest(payload)
	content, err := heldfile.FromBytesContext(t.Context(), payload, heldfile.Options{
		Label: "test-build-material", MaxBytes: MaxBuildMaterialBytes, ExpectedDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	valid := BuildMaterialInput{Path: "materials/runtime", SHA256: digest, Content: content}

	tests := []struct {
		name     string
		manifest *heldfile.File
		inputs   []BuildMaterialInput
		maxFile  int64
		maxTotal int64
		want     e.Code
	}{
		{name: "unauthenticated manifest", inputs: []BuildMaterialInput{valid}, want: e.CodeUnverifiedRef},
		{name: "no inputs", manifest: manifest, want: e.CodeEngineCapability},
		{name: "too many inputs", manifest: manifest, inputs: make([]BuildMaterialInput, maxBuildMaterialFiles+1), want: e.CodeEngineCapability},
		{name: "absolute path", manifest: manifest, inputs: []BuildMaterialInput{{Path: "/material", SHA256: digest, Content: content}}, want: e.CodeInvalidCatalog},
		{name: "traversal path", manifest: manifest, inputs: []BuildMaterialInput{{Path: "../material", SHA256: digest, Content: content}}, want: e.CodeInvalidCatalog},
		{name: "manifest collision", manifest: manifest, inputs: []BuildMaterialInput{{Path: "material-manifest.json", SHA256: digest, Content: content}}, want: e.CodeInvalidCatalog},
		{name: "duplicate path", manifest: manifest, inputs: []BuildMaterialInput{valid, valid}, want: e.CodeInvalidCatalog},
		{name: "nil content", manifest: manifest, inputs: []BuildMaterialInput{{Path: valid.Path, SHA256: digest}}, want: e.CodeUnverifiedRef},
		{name: "digest mismatch", manifest: manifest, inputs: []BuildMaterialInput{{Path: valid.Path, SHA256: "sha256:" + strings.Repeat("a", 64), Content: content}}, want: e.CodeUnverifiedRef},
		{name: "per-file size bound", manifest: manifest, inputs: []BuildMaterialInput{valid}, maxFile: content.Size() - 1, maxTotal: content.Size(), want: e.CodeEngineCapability},
		{name: "closure size bound", manifest: manifest, inputs: []BuildMaterialInput{valid, {Path: "materials/other", SHA256: digest, Content: content}}, maxFile: content.Size(), maxTotal: content.Size(), want: e.CodeEngineCapability},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maxFile, maxTotal := test.maxFile, test.maxTotal
			if maxFile == 0 {
				maxFile = MaxBuildMaterialBytes
			}
			if maxTotal == 0 {
				maxTotal = maxBuildMaterialTotalBytes
			}
			materials, err := newBuildMaterials(test.manifest, test.inputs, maxFile, maxTotal)
			if materials != nil || code(err) != test.want {
				t.Fatalf("NewBuildMaterials() = %#v, %v; want nil, %s", materials, err, test.want)
			}
		})
	}
}

func TestAddBoundedSize(t *testing.T) {
	for _, test := range []struct {
		name              string
		total, size       int64
		maxFile, maxTotal int64
		wantTotal         int64
		wantOK            bool
	}{
		{name: "exact limit", total: 4, size: 6, maxFile: 6, maxTotal: 10, wantTotal: 10, wantOK: true},
		{name: "over file limit", size: 7, maxFile: 6, maxTotal: 10},
		{name: "over total limit", total: 5, size: 6, maxFile: 6, maxTotal: 10},
		{name: "near max int overflow", total: math.MaxInt64 - 1, size: 2, maxFile: math.MaxInt64, maxTotal: math.MaxInt64},
		{name: "negative input", total: -1, size: 1, maxFile: 1, maxTotal: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := addBoundedSize(test.total, test.size, test.maxFile, test.maxTotal)
			if got != test.wantTotal || ok != test.wantOK {
				t.Fatalf("addBoundedSize(%d, %d, %d, %d) = %d, %t; want %d, %t",
					test.total, test.size, test.maxFile, test.maxTotal, got, ok, test.wantTotal, test.wantOK)
			}
		})
	}
}

func TestVerifyPrivateFileSnapshotRejectsMutations(t *testing.T) {
	payload := []byte("payload\n")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
		cancel bool
		want   e.Code
	}{
		{name: "undeclared file", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, root, _ string) {
			if err := os.Chmod(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "extra"), []byte("extra\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o555); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink entry", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, _, path string) {
			parent := filepath.Dir(path)
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing", path); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o555); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed content", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, _, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o444); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed mode", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, _, path string) {
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "writable directory", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, _, path string) {
			if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing file", want: e.CodeUnverifiedRef, mutate: func(t *testing.T, _, path string) {
			parent := filepath.Dir(path)
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o555); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "canceled context", cancel: true, want: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() { _ = removePrivateTree(root) })
			materialPath := filepath.Join(root, "materials", "runtime")
			if err := os.MkdirAll(filepath.Dir(materialPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(materialPath, payload, 0o444); err != nil {
				t.Fatal(err)
			}
			if err := sealPrivateInputDirectories(root); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(t, root, materialPath)
			}
			ctx := t.Context()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := verifyPrivateFileSnapshot(ctx, root, []privateFileSnapshot{{
				Path: filepath.Join("materials", "runtime"), SHA256: FileDigest(payload), Mode: 0o444,
			}}, "build-material")
			if code(err) != test.want {
				t.Fatalf("verifyPrivateFileSnapshot() error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestBuildMaterialsStageNativeReadOnlyClosure(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
		Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
		Architecture: "amd64", Digest: payloadDigest,
	}})
	defer closeMaterials()
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
		Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
	staged, err := materials.stage(t.Context(), node, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer staged.cleanup()
	if staged.manifestDigest != materials.manifest.Digest() {
		t.Fatalf("manifest digest = %q, want %q", staged.manifestDigest, materials.manifest.Digest())
	}
	for relative, want := range map[string]string{
		"material-manifest.json":                      material.ManifestSchemaVersion,
		filepath.Join("materials", "runtime-archive"): "payload for runtime-archive\n",
	} {
		path := filepath.Join(staged.root, relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), want) {
			t.Fatalf("staged %s = %q, want content containing %q", relative, raw, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o444 {
			t.Fatalf("staged %s mode = %o, want 0444", relative, info.Mode().Perm())
		}
	}
	for _, directory := range []string{staged.root, filepath.Join(staged.root, "materials")} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o555 {
			t.Fatalf("staged directory %s mode = %o, want 0555", directory, info.Mode().Perm())
		}
	}
	wantMount := []string{"-v", staged.root + ":/nvkr/inputs:ro"}
	if got := buildMaterialMountArgs(staged.root); !slices.Equal(got, wantMount) {
		t.Fatalf("material mount = %#v, want %#v", got, wantMount)
	}
}

func TestBuildMaterialsStageReportsCleanupFailure(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
		Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
		Architecture: "amd64", Digest: payloadDigest,
	}})
	defer closeMaterials()
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
		Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
	stageFailure := e.New(e.CodeBuildFailed, "injected materialization failure")
	cleanupFailure := errors.New("injected cleanup failure")
	staged, err := materials.stageWithOps(t.Context(), node, t.TempDir(), buildMaterialStageOps{
		materializeManifest: func(context.Context, *heldfile.File, string) error { return stageFailure },
		stageInputs: func(context.Context, string, string, []BuildMaterialInput) (*stagedBuildMaterialInputs, error) {
			t.Fatal("stageInputs called after manifest materialization failed")
			return nil, nil
		},
		removeTree: func(string) error { return cleanupFailure },
	})
	if staged != nil {
		t.Fatalf("staged capability = %#v, want nil", staged)
	}
	if code(err) != e.CodeBuildFailed {
		t.Fatalf("stage error = %v, want %s", err, e.CodeBuildFailed)
	}
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("stage error = %v, want joined cleanup failure", err)
	}
}

func TestBuildMaterialsRejectsMismatchedConsumer(t *testing.T) {
	payloadDigest := FileDigest([]byte("payload for runtime-archive\n"))
	for _, test := range []struct {
		name   string
		mutate func(*ir.Node)
		code   e.Code
	}{
		{name: "wrong component", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Materials[0].Component = "other"
		}},
		{name: "wrong architecture", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Platform = "linux/arm64"
		}},
		{name: "missing binding", code: e.CodePipelineDrift, mutate: func(node *ir.Node) {
			node.Materials = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
				Binding: "runtime-archive", Component: "runtime", Kind: "tarball",
				Architecture: "amd64", Digest: payloadDigest,
			}})
			defer closeMaterials()
			node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic, Platform: "linux/amd64",
				Materials: []ir.MaterialBinding{{Binding: "runtime-archive", Component: "runtime", Producer: "pull-runtime"}}}
			test.mutate(&node)
			_, _, err := materials.validateFor(t.Context(), node)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("validation error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestBuildMaterialAdmissionRejectsMissingAndExtraCapabilities(t *testing.T) {
	node := ir.Node{ID: "build-composer", BuilderType: ir.BuilderOCIHermetic,
		Materials: []ir.MaterialBinding{{Binding: "runtime", Component: "runtime", Producer: "pull-runtime"}}}
	if err := admitBuildMaterials(node, RunOptions{}); code(err) != e.CodeEngineCapability {
		t.Fatalf("missing material capability = %v", err)
	}
	if err := admitBuildMaterials(node, RunOptions{DryRun: true}); err != nil {
		t.Fatalf("dry-run required material bytes: %v", err)
	}
	node.Materials = nil
	if err := admitBuildMaterials(node, RunOptions{BuildMaterials: &BuildMaterials{}}); code(err) != e.CodePipelineDrift {
		t.Fatalf("extra material capability = %v", err)
	}
}

func TestBuildMaterialsStageDifferentConsumersConcurrently(t *testing.T) {
	type consumer struct {
		binding   string
		component string
		materials *BuildMaterials
	}
	consumers := make([]consumer, 0, 2)
	for _, item := range []struct{ binding, component string }{
		{binding: "agent-package", component: "agent"},
		{binding: "runtime-archive", component: "runtime"},
	} {
		digest := FileDigest([]byte("payload for " + item.binding + "\n"))
		materials, closeMaterials := testBuildMaterials(t, []material.Observation{{
			Binding: item.binding, Component: item.component, Kind: "tarball",
			Architecture: "amd64", Digest: digest,
		}})
		defer closeMaterials()
		consumers = append(consumers, consumer{
			binding: item.binding, component: item.component,
			materials: materials,
		})
	}
	parent := t.TempDir()
	roots := make(chan string, len(consumers))
	group, ctx := errgroup.WithContext(t.Context())
	for index := range consumers {
		item := consumers[index]
		group.Go(func() error {
			node := ir.Node{ID: "build-" + item.component, BuilderType: ir.BuilderOCIHermetic,
				Platform: "linux/amd64", Materials: []ir.MaterialBinding{{
					Binding: item.binding, Component: item.component, Producer: "pull-" + item.component,
				}}}
			staged, err := item.materials.stage(ctx, node, parent)
			if err != nil {
				return err
			}
			defer staged.cleanup()
			roots <- staged.root
			raw, err := os.ReadFile(filepath.Join(staged.root, "materials", item.binding))
			if err != nil {
				return err
			}
			if string(raw) != "payload for "+item.binding+"\n" {
				return fmt.Errorf("consumer %s received %q", item.component, raw)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	close(roots)
	seen := make(map[string]bool, len(consumers))
	for root := range roots {
		if seen[root] {
			t.Fatalf("concurrent material consumers shared staging root %q", root)
		}
		seen[root] = true
	}
}
