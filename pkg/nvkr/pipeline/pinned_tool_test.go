// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
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

func TestAdmitPinnedToolBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ir.Node, *RunOptions)
	}{
		{"qualified execution", func(_ *ir.Node, o *RunOptions) { o.Qualification = &QualifiedExecution{} }},
		{"packaging", func(_ *ir.Node, o *RunOptions) { o.Package = true }},
		{"layout output before bundle-oci import", func(n *ir.Node, _ *RunOptions) { n.OutputType, n.ArtifactPath = ir.OutputOCILayout, "" }},
		{"network", func(n *ir.Node, _ *RunOptions) { n.Network = ir.NetworkDefault }},
		{"missing tool input", func(_ *ir.Node, o *RunOptions) { o.ToolExecutable = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := pinnedToolNode([]byte("tool"))
			opts := RunOptions{Ctx: t.Context(), ToolExecutable: "/held/aicr"}
			test.change(&node, &opts)
			if got := pinnedToolCode(admitPinnedTool(node, opts)); got != e.CodeEngineCapability {
				t.Fatalf("admitPinnedTool() code %q; want %s", got, e.CodeEngineCapability)
			}
		})
	}
	node := pinnedToolNode([]byte("tool"))
	if _, err := (Local{}).ExecNode(node, RunOptions{Ctx: t.Context(), ToolExecutable: "/held/aicr"}); pinnedToolCode(err) != e.CodeEngineCapability {
		t.Fatalf("non-launched exec-node admitted a pinned tool: %v", err)
	}
	if result, err := buildPinnedTool(node, RunOptions{Ctx: t.Context(), DryRun: true, Stdout: &strings.Builder{}}); result != nil || err != nil {
		t.Fatalf("dry run did work: %+v, %v", result, err)
	}
}

func TestPinnedToolRunArgsAreFixed(t *testing.T) {
	node := pinnedToolNode([]byte("tool"))
	got := pinnedToolRunArgs(node, "nvkr-1", "/private/tool", "aicr", "/private/inputs", "/private/out")
	want := []string{"run", "--rm", "--name", "nvkr-1", "--platform", "linux/arm64", "--network=none", "--read-only",
		"--tmpfs", "/nvkr/home", "--tmpfs", "/nvkr/tmp", "--tmpfs", "/nvkr/work",
		"-v", "/private/tool:/nvkr/tool:ro", "-v", "/private/out:/nvkr/output", "-v", "/private/inputs:/nvkr/inputs:ro",
		"-w", "/nvkr/work", "--entrypoint", "/bin/sh", node.Toolchain, "-c", pinnedToolBootstrap, "nvkr-pinned-tool",
		"HOME=/nvkr/home", "LC_ALL=C", "TMPDIR=/nvkr/tmp", "TZ=UTC", "AICR_LOG_FORMAT=text",
		"/nvkr/tool/aicr", "recipe", "--output", "/nvkr/output/recipe.yaml"}
	if !slices.Equal(got, want) {
		t.Fatalf("pinned tool invocation\n got %q\nwant %q", got, want)
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
