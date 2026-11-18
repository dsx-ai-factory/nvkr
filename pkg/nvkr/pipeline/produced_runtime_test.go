// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func runtimeReceiptFixture(t *testing.T) (ir.Node, ArtifactResult, RunOptions, string) {
	return runtimeReceiptFixtureFiles(t, nil)
}

func runtimeReceiptFixtureFiles(t *testing.T, files map[string]string) (ir.Node, ArtifactResult, RunOptions, string) {
	t.Helper()
	node := ir.Node{ID: "build-runtime", Kind: ir.KindBuild, Component: "runtime", BuilderType: ir.BuilderMkosi,
		OutputType: "oci-image", ArtifactPath: "nvkr-output/runtime"}
	options := RunOptions{Ctx: t.Context(), MkosiScratch: t.TempDir(), SourceRoot: filepath.Join(t.TempDir(), "src")}
	root := artifactHostPath(options, node)
	if err := os.MkdirAll(filepath.Join(root, "blobs/sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	var total int64
	write := func(name string, raw []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		total += int64(len(raw))
	}
	encode := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	blob := func(raw []byte, media string) ociclosure.Descriptor {
		t.Helper()
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		write("blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), raw)
		return ociclosure.Descriptor{MediaType: media, Digest: digest, Size: int64(len(raw))}
	}
	var layerBytes bytes.Buffer
	archive := tar.NewWriter(&layerBytes)
	if err := archive.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	directories := map[string]bool{}
	var names []string
	for name := range files {
		names = append(names, name)
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			directories[parent] = true
		}
	}
	var ordered []string
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	slices.Sort(ordered)
	for _, directory := range ordered {
		if err := archive.WriteHeader(&tar.Header{Name: directory + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	layer := blob(layerBytes.Bytes(), "application/vnd.oci.image.layer.v1.tar")
	config := blob(encode(map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{layer.Digest}},
		"config": map[string]any{"Cmd": []string{"/sbin/init"}}}), "application/vnd.oci.image.config.v1+json")
	manifest := blob(encode(ociclosure.ManifestDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", Config: config, Layers: []ociclosure.Descriptor{layer}}), "application/vnd.oci.image.manifest.v1+json")
	write("index.json", encode(ociclosure.IndexDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []ociclosure.Descriptor{manifest}}))
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	return node, ArtifactResult{Component: node.Component, OutputType: node.OutputType, Qualification: ArtifactUnqualified,
		InputDigest: "sha256:" + strings.Repeat("1", 64), BuilderType: ir.BuilderMkosi, EngineClass: ir.BuilderMkosi, Path: node.ArtifactPath,
		SHA256: manifest.Digest, Size: total, TransportPath: root, AbsPath: filepath.Join(root, "blobs/sha256", strings.TrimPrefix(manifest.Digest, "sha256:"))}, options, layer.Digest
}

func TestProducedRuntimeReceiptRejectsSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ArtifactResult)
	}{
		{"prepared source", func(r *ArtifactResult) { r.InputDigest = "sha256:" + strings.Repeat("2", 64) }},
		{"component", func(r *ArtifactResult) { r.Component = "other" }},
		{"builder", func(r *ArtifactResult) { r.BuilderType = ir.BuilderVMImage }},
		{"observed engine", func(r *ArtifactResult) { r.EngineClass = "other" }},
		{"artifact path", func(r *ArtifactResult) { r.Path = "other" }},
		{"transport path", func(r *ArtifactResult) { r.TransportPath += "-other" }},
		{"subject", func(r *ArtifactResult) { r.SHA256 = "sha256:" + strings.Repeat("3", 64) }},
		{"size", func(r *ArtifactResult) { r.Size++ }},
		{"image route", func(r *ArtifactResult) { r.Ref = "example.invalid/image@" + r.SHA256 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, result, options, _ := runtimeReceiptFixture(t)
			tool, err := captureExecutedRuntime(t.Context(), "linux/amd64", "sha256:"+strings.Repeat("4", 64), node, result, options)
			if err != nil {
				t.Fatal(err)
			}
			defer tool.Close()
			tc.change(&result)
			if code(tool.AuthenticateResultContext(t.Context(), result)) != e.CodeVerifyFailed {
				t.Fatal("substituted runtime result accepted")
			}
		})
	}
}

func TestProducedRuntimeReceiptRetainsAndRevokesExactClosure(t *testing.T) {
	node, result, options, layerDigest := runtimeReceiptFixture(t)
	build := "sha256:" + strings.Repeat("4", 64)
	tool, err := captureExecutedRuntime(t.Context(), "linux/amd64", build, node, result, options)
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	binding, err := tool.BindingContext(t.Context())
	if err != nil || binding.BuildDigest != build || binding.Platform != "linux/amd64" || binding.Component != node.Component ||
		binding.Digest != result.SHA256 || binding.Size != result.Size || binding.LayerDigest != layerDigest || binding.LayoutDigest == "" || binding.TerminalDigest == "" {
		t.Fatalf("runtime receipt lost its exact production binding: %+v %v", binding, err)
	}
	if err := tool.AuthenticateResultContext(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	copy := *tool
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = copy.BindingContext(t.Context())
	if code(err) != e.CodeGateUnmet {
		t.Fatalf("shallow copy retained revoked runtime authority: %v", err)
	}
}

func TestProducedRuntimeReceiptDetectsLiveLayoutDrift(t *testing.T) {
	for _, replaceDirectory := range []bool{false, true} {
		node, result, options, layer := runtimeReceiptFixture(t)
		tool, err := captureExecutedRuntime(t.Context(), "linux/amd64", "sha256:"+strings.Repeat("4", 64), node, result, options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tool.Close() })
		if replaceDirectory {
			original := result.TransportPath + "-original"
			if err := os.Rename(result.TransportPath, original); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(result.TransportPath, os.DirFS(original)); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(filepath.Join(result.TransportPath, "blobs/sha256", strings.TrimPrefix(layer, "sha256:")), []byte("changed"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := tool.state.layer.AssertUnchangedContext(t.Context()); code(err) != e.CodeVerifyFailed {
				t.Fatalf("changed retained tar was accepted: %v", err)
			}
		}
		_, err = tool.BindingContext(t.Context())
		if code(err) != e.CodeVerifyFailed {
			t.Fatalf("runtime layout substitution accepted: %v", err)
		}
	}
}

func TestAbsentProducedRuntimeCannotAuthorizeConsumption(t *testing.T) {
	for _, tool := range []*ProducedRuntime{nil, {}} {
		_, err := tool.BindingContext(t.Context())
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeGateUnmet {
			t.Fatalf("absent runtime receipt accepted: %v", err)
		}
	}
}
