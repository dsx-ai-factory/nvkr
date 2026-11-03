// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func pinnedToolNode(executable []byte) ir.Node {
	return ir.Node{ID: "build-recipe", Kind: ir.KindBuild, Component: "recipe", BuilderType: ir.BuilderPinnedTool, Tool: "aicr",
		Artifact: "https://example.test/aicr.tar.gz", Digest: FileDigest(executable),
		ExecutableArchive: &ir.ExecutableArchive{Format: "tar-gzip", Digest: "sha256:" + strings.Repeat("a", 64), Member: "bin/aicr"},
		Toolchain:         "example.test/runner@sha256:" + strings.Repeat("c", 64), Platform: "linux/arm64", Network: ir.NetworkNone,
		Env:     []ir.EnvVar{{Name: "AICR_LOG_FORMAT", Value: "text"}},
		Command: []string{"recipe", "--output", ir.PinnedToolOutputDir + "/recipe.yaml"}, Workdir: ir.PinnedToolWorkdir,
		OutputType: ir.OutputFile, ArtifactPath: "recipe.yaml", Version: "1.2.0"}
}

func pinnedToolCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	if err != nil {
		return "uncoded"
	}
	return ""
}

// Dry run ends admission after the lane checks, so the host check cannot stand in
// for a deleted one. The missing-tool case needs a matching host and is Linux-only.
func TestAdmitPinnedToolBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ir.Node, *RunOptions)
	}{
		{"qualified execution", func(_ *ir.Node, o *RunOptions) { o.Qualification = &QualifiedExecution{} }},
		{"packaging", func(_ *ir.Node, o *RunOptions) { o.Package = true }},
		{"undeclared output form", func(n *ir.Node, _ *RunOptions) { n.OutputType = "raw" }},
		{"network", func(n *ir.Node, _ *RunOptions) { n.Network = ir.NetworkDefault }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := pinnedToolNode([]byte("tool"))
			opts := RunOptions{Ctx: t.Context(), DryRun: true, ToolExecutable: "/held/aicr"}
			if err := admitPinnedTool(node, opts); err != nil {
				t.Fatalf("baseline dry run refused: %v", err)
			}
			test.change(&node, &opts)
			if got := pinnedToolCode(admitPinnedTool(node, opts)); got != e.CodeEngineCapability {
				t.Fatalf("admitPinnedTool() code %q; want %s", got, e.CodeEngineCapability)
			}
		})
	}
	node := pinnedToolNode([]byte("tool"))
	if _, err := (Local{}).ExecNode(node, RunOptions{Ctx: t.Context(), DryRun: true, ToolExecutable: "/held/aicr"}); pinnedToolCode(err) != e.CodeEngineCapability {
		t.Fatalf("non-launched exec-node admitted a pinned tool: %v", err)
	}
	if result, err := buildPinnedTool(node, RunOptions{Ctx: t.Context(), DryRun: true, Stdout: &strings.Builder{}}); result != nil || err != nil {
		t.Fatalf("dry run did work: %+v, %v", result, err)
	}
}

func TestPinnedToolRunArgsAreFixed(t *testing.T) {
	node := pinnedToolNode([]byte("tool"))
	invocation := func(identity ...string) []string {
		args := append([]string{"run", "--rm", "--name", "nvkr-1", "--platform", "linux/arm64", "--network=none", "--read-only"}, identity...)
		return append(args, "--tmpfs", "/nvkr/home:mode=1777", "--tmpfs", "/nvkr/tmp:mode=1777", "--tmpfs", "/nvkr/work:mode=1777",
			"-v", "/private/tool:/nvkr/tool:ro", "-v", "/private/out:/nvkr/output", "-v", "/private/inputs:/nvkr/inputs:ro",
			"-w", "/nvkr/work", "--entrypoint", "/bin/sh", node.Toolchain, "-c", pinnedToolBootstrap, "nvkr-pinned-tool",
			"HOME=/nvkr/home", "LC_ALL=C", "TMPDIR=/nvkr/tmp", "TZ=UTC", "AICR_LOG_FORMAT=text",
			"/nvkr/tool/aicr", "recipe", "--output", "/nvkr/output/recipe.yaml")
	}
	// Docker runs as the host user (ADR-18); Podman's mapping makes container root the
	// invoking user, and a host UID or image USER would map to a subordinate one.
	for runtimeName, want := range map[string][]string{
		"docker": invocation("--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())),
		"podman": invocation("--user", "0:0"),
	} {
		got := pinnedToolRunArgs(node, runtimeName, "nvkr-1", "/private/tool", "aicr", "/private/inputs", "/private/out")
		if !slices.Equal(got, want) {
			t.Fatalf("%s pinned tool invocation\n got %q\nwant %q", runtimeName, got, want)
		}
	}
}

// env(1) would read a staged name containing '=' as an assignment and run the next
// argument from the image instead of the pinned tool.
func TestStagePinnedToolRefusesAssignmentName(t *testing.T) {
	node := pinnedToolNode([]byte("tool"))
	node.ExecutableArchive.Member = "bin/aicr=1"
	parent := t.TempDir()
	if _, _, err := stagePinnedToolExecutable(t.Context(), node, "/held/aicr", parent); pinnedToolCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("stagePinnedToolExecutable() = %v; want %s", err, e.CodeInvalidCatalog)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("refused staging left %v, %v", entries, err)
	}
}

func TestAdmitPinnedToolOutputForm(t *testing.T) {
	for _, test := range []struct {
		name  string
		write func(string) error
		code  e.Code
	}{
		{"one regular file", func(dir string) error { return os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte("r"), 0o644) }, ""},
		{"no output", func(string) error { return nil }, e.CodeBuildFailed},
		{"extra entry", func(dir string) error {
			return errors.Join(os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte("r"), 0o644),
				os.WriteFile(filepath.Join(dir, "recipe.yaml.sig"), []byte("s"), 0o644))
		}, e.CodeBuildFailed},
		{"directory tree", func(dir string) error { return os.Mkdir(filepath.Join(dir, "recipe.yaml"), 0o755) }, e.CodeBuildFailed},
		{"symbolic link", func(dir string) error {
			return errors.Join(os.WriteFile(filepath.Join(dir, "target"), []byte("r"), 0o644),
				os.Symlink("target", filepath.Join(dir, "recipe.yaml")))
		}, e.CodeBuildFailed},
		{"wrong name", func(dir string) error { return os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("r"), 0o644) }, e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := test.write(dir); err != nil {
				t.Fatal(err)
			}
			sum, size, err := admitPinnedToolOutput(context.Background(), dir, pinnedToolNode(nil))
			if got := pinnedToolCode(err); got != test.code {
				t.Fatalf("admitPinnedToolOutput() code %q (%v); want %q", got, err, test.code)
			}
			if test.code == "" && (sum != FileDigest([]byte("r")) || size != 1) {
				t.Fatalf("wrong observation %s %d", sum, size)
			}
		})
	}
}

// writeTestLayout writes the physical form of an OCI layout root only: its index lists
// nothing and its blob name does not authenticate its bytes. The executor observes the
// index alone; the layout import owns OCI semantics and blob integrity (ADR-27 §5).
func writeTestLayout(dir string) error {
	return errors.Join(os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755),
		os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644),
		os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":2,"manifests":[]}`), 0o644),
		os.WriteFile(filepath.Join(dir, "blobs", "sha256", strings.Repeat("a", 64)), []byte("blob"), 0o644))
}

func TestAdmitPinnedToolLayoutForm(t *testing.T) {
	layout := func(staging string) string { return filepath.Join(staging, "layout") }
	for _, test := range []struct {
		name  string
		write func(string) error
		code  e.Code
	}{
		{"one layout", func(s string) error { return writeTestLayout(layout(s)) }, ""},
		{"no output", func(string) error { return nil }, e.CodeBuildFailed},
		{"entry beside the layout", func(s string) error {
			return errors.Join(writeTestLayout(layout(s)), os.WriteFile(filepath.Join(s, "layout.sig"), []byte("s"), 0o644))
		}, e.CodeBuildFailed},
		{"signature in the layout root", func(s string) error {
			return errors.Join(writeTestLayout(layout(s)), os.WriteFile(filepath.Join(layout(s), "index.json.sig"), []byte("s"), 0o644))
		}, e.CodeBuildFailed},
		{"missing index", func(s string) error {
			return errors.Join(writeTestLayout(layout(s)), os.Remove(filepath.Join(layout(s), "index.json")))
		}, e.CodeBuildFailed},
		{"layout is a file", func(s string) error { return os.WriteFile(layout(s), []byte("x"), 0o644) }, e.CodeBuildFailed},
		{"link inside blobs", func(s string) error {
			return errors.Join(writeTestLayout(layout(s)), os.Symlink("/etc/passwd", filepath.Join(layout(s), "blobs", "sha256", "link")))
		}, e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			staging, outRoot := filepath.Join(root, "staging"), filepath.Join(root, "out")
			if err := os.Mkdir(staging, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := test.write(staging); err != nil {
				t.Fatal(err)
			}
			node := pinnedToolNode(nil)
			node.OutputType, node.ArtifactPath = ir.OutputOCILayout, ""
			sum, size, err := admitPinnedToolLayout(context.Background(), staging, outRoot, node)
			if got := pinnedToolCode(err); got != test.code {
				t.Fatalf("admitPinnedToolLayout() code %q (%v); want %q", got, err, test.code)
			}
			if test.code != "" {
				if _, statErr := os.Lstat(outRoot); !os.IsNotExist(statErr) {
					t.Fatalf("refused layout reached the output root: %v", statErr)
				}
				return
			}
			index := []byte(`{"schemaVersion":2,"manifests":[]}`)
			if sum != FileDigest(index) || size != int64(len(index)) {
				t.Fatalf("observed %s %d; want the layout index", sum, size)
			}
			if _, err := os.Stat(filepath.Join(outRoot, "oci-layout")); err != nil {
				t.Fatalf("layout did not become the output root: %v", err)
			}
		})
	}
}
