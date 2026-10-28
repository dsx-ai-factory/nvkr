// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func nativeGoFixture(t *testing.T) (root, compiler, manifest, digest string) {
	t.Helper()
	requireNativePackerExecution(t)
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	program := packerTestHelperExecutable(t)
	if err := os.Link(program, filepath.Join(root, "bin", "go")); err != nil {
		t.Fatal(err)
	}
	compiler = program
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("fixture Go SDK\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec, err := CaptureNativeGo(t.Context(), root, compiler)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestNativeGoToolchainAuthenticatesInstalledBytes(t *testing.T) {
	for _, scenario := range []string{"exact", "changed SDK", "changed compiler", "wrong digest", "noncanonical manifest", "unknown field", "missing manifest", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root, compiler, manifest, digest := nativeGoFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := e.CodeUnverifiedRef
			switch scenario {
			case "changed SDK":
				if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed compiler":
				compiler = filepath.Join(t.TempDir(), "compiler")
				if err := os.WriteFile(compiler, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
				want = e.CodeEngineCapability
			case "wrong digest":
				digest = "sha256:" + strings.Repeat("a", 64)
			case "noncanonical manifest", "unknown field":
				raw, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "noncanonical manifest" {
					raw = append(raw, '\n')
				} else {
					raw = append([]byte(`{"unknown":true,`), raw[1:]...)
				}
				if err := os.WriteFile(manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
				want = e.CodeInvalidCatalog
			case "missing manifest":
				manifest += ".absent"
				want = e.CodeEngineCapability
			case "canceled":
				cancel()
				want = e.CodeEngineCapability
			}
			tool, err := OpenNativeGo(ctx, manifest, digest, root, compiler)
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				defer tool.Close()
				return
			}
			if tool != nil || code(err) != want {
				t.Fatalf("receipt=%v error=%v want %s", tool, err, want)
			}
		})
	}
}

func TestNativeGoRejectsWrongOrClosedReceiptBeforeSourceWork(t *testing.T) {
	for _, scenario := range []string{"missing", "closed", "wrong identity", "qualified", "not launched"} {
		t.Run(scenario, func(t *testing.T) {
			root, compiler, manifest, digest := nativeGoFixture(t)
			tool, err := OpenNativeGo(t.Context(), manifest, digest, root, compiler)
			if err != nil {
				t.Fatal(err)
			}
			defer tool.Close()
			node := ir.Node{BuilderType: ir.BuilderNativeGo, Toolchain: digest, Platform: tool.spec.Platform}
			options := RunOptions{Ctx: t.Context(), NativeGo: tool, Launch: true, SourceRoot: filepath.Join(t.TempDir(), "absent")}
			want := e.CodeEngineCapability
			switch scenario {
			case "missing":
				options.NativeGo = nil
			case "closed":
				_ = tool.Close()
				want = e.CodeUnverifiedRef
			case "wrong identity":
				node.Toolchain = "sha256:" + strings.Repeat("b", 64)
				want = e.CodeUnverifiedRef
			case "qualified":
				options.Qualification = &QualifiedExecution{}
			case "not launched":
				options.Launch = false
			}
			result, err := buildNativeGo(node, options)
			if result != nil || code(err) != want {
				t.Fatalf("result=%v error=%v want %s", result, err, want)
			}
			if _, err := os.Lstat(options.SourceRoot); !os.IsNotExist(err) {
				t.Fatal("rejected capability touched the source workspace")
			}
		})
	}
}

func TestNativeGoRunAdmitsAllSelectedBuildsBeforeFetching(t *testing.T) {
	for _, scenario := range []string{"missing", "closed", "wrong identity", "wrong platform", "qualified", "not launched", "packaged"} {
		t.Run(scenario, func(t *testing.T) {
			root, compiler, manifest, digest := nativeGoFixture(t)
			tool, err := OpenNativeGo(t.Context(), manifest, digest, root, compiler)
			if err != nil {
				t.Fatal(err)
			}
			defer tool.Close()
			fetch := testFetchNode("fetch-native", "https://must-not-fetch.invalid/source", testSourceCommit, testSourceTree,
				"source", "registry.example/vendor@sha256:"+strings.Repeat("c", 64), "")
			build := completeTestBuild(ir.Node{ID: "build-native", Kind: ir.KindBuild, Component: "native",
				BuilderType: ir.BuilderNativeGo, Toolchain: digest, Command: []string{"go", "build"}, OutputType: "host-package"}, fetch)
			build.Platform, build.ToolchainPlatform = runtime.GOOS+"/"+runtime.GOARCH, runtime.GOOS+"/"+runtime.GOARCH
			build.Network, build.SourceReadOnly, build.Env = ir.NetworkDefault, false, testBuildEnv(runtime.GOARCH)
			for i := range build.Env {
				if build.Env[i].Name == "GOOS" {
					build.Env[i].Value = runtime.GOOS
				}
			}
			graph := testSourceGraph(fetch, build)
			graph.Platform = build.Platform
			selected := mustGraphDigest(t, graph)
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			options := RunOptions{Ctx: t.Context(), NativeGo: tool, Launch: true, SourceRoot: workspace}
			want := e.CodeEngineCapability
			switch scenario {
			case "missing":
				options.NativeGo = nil
			case "closed":
				_ = tool.Close()
				want = e.CodeUnverifiedRef
			case "wrong identity":
				tool.digest = "sha256:" + strings.Repeat("b", 64)
				want = e.CodeUnverifiedRef
			case "wrong platform":
				tool.spec.Platform = "foreign/arm64"
				want = e.CodeUnverifiedRef
			case "qualified":
				options.Qualification = &QualifiedExecution{}
			case "not launched":
				options.Launch = false
			case "packaged":
				options.Package = true
			}
			var output strings.Builder
			options.Stdout, options.Stderr = &output, &output
			for _, boundary := range []string{"run", "exec-node"} {
				var err error
				if boundary == "run" {
					_, err = (Local{}).Run(graph, selected, options)
				} else {
					_, err = (Local{}).ExecNode(build, options)
				}
				if code(err) != want || output.Len() != 0 {
					t.Fatalf("%s admission = %v, output=%q; want %s before work", boundary, err, output.String(), want)
				}
				if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
					t.Fatalf("%s touched source workspace before admission", boundary)
				}
			}
		})
	}
}
