// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The fake runtime stands in for docker/podman: it locates the output mount and
// acts as the tool would. It proves executor behavior, not container isolation.
func fakePinnedToolRuntime(t *testing.T, body string) string {
	t.Helper()
	script := `#!/bin/sh
case " $* " in *" --network=none "*) ;; *) exit 90;; esac
out=
for a in "$@"; do case "$a" in *:/nvkr/output) out=${a%:/nvkr/output};; esac; done
[ -n "$out" ] || exit 91
` + body + "\n"
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdmitPinnedToolHostRefusesEmulation(t *testing.T) {
	node := pinnedToolNode(nil)
	node.Platform = map[string]string{"amd64": "linux/arm64", "arm64": "linux/amd64"}[runtime.GOARCH]
	if got := pinnedToolCode(AdmitPinnedToolHost(node)); got != e.CodeEngineCapability {
		t.Fatalf("cross-architecture pinned tool admitted with code %q", got)
	}
}

func TestAdmitPinnedToolRequiresToolInput(t *testing.T) {
	node := pinnedToolNode(nil)
	node.Platform = "linux/" + runtime.GOARCH
	opts := RunOptions{Ctx: t.Context(), ToolExecutable: "/held/aicr"}
	if err := admitPinnedTool(node, opts); err != nil {
		t.Fatalf("matching host refused: %v", err)
	}
	// The runtime's name selects the container identity, so any other name is refused.
	for _, runtimePath := range []string{"docker", "/usr/bin/docker", "/usr/bin/podman"} {
		opts.Runtime = runtimePath
		if err := admitPinnedTool(node, opts); err != nil {
			t.Fatalf("runtime %q refused: %v", runtimePath, err)
		}
	}
	opts.Runtime = "/usr/local/bin/nerdctl"
	if got := pinnedToolCode(admitPinnedTool(node, opts)); got != e.CodeEngineCapability {
		t.Fatalf("runtime %q admitted with code %q", opts.Runtime, got)
	}
	opts.Runtime, opts.ToolExecutable = "", ""
	if got := pinnedToolCode(admitPinnedTool(node, opts)); got != e.CodeEngineCapability {
		t.Fatalf("missing tool input admitted with code %q", got)
	}
}

// An AICR-like layout writer refuses an existing target, even an empty directory,
// so the executor must hand it an absent path and then admit exactly one layout.
func TestPinnedToolExecutorHandsLayoutWriterAnAbsentPath(t *testing.T) {
	refuseExisting := `case " $* " in *" oci-layout:/nvkr/output/layout "*) ;; *) exit 92;; esac
target="$out/layout"
if [ -e "$target" ]; then echo "refusing existing $target" >&2; exit 4; fi
mkdir -p "$target/blobs/sha256"
printf '{"imageLayoutVersion":"1.0.0"}' > "$target/oci-layout"
printf '{"schemaVersion":2,"manifests":[]}' > "$target/index.json"
printf blob > "$target/blobs/sha256/` + strings.Repeat("a", 64) + `"`
	executable := []byte("#!/bin/sh\nexit 0\n")
	for _, test := range []struct {
		name    string
		extra   string
		timeout time.Duration
		code    e.Code
	}{
		{name: "layout at the absent path"},
		{name: "file beside the layout", extra: `; printf s > "$out/layout.sig"`, code: e.CodeBuildFailed},
		{name: "layout writer exits nonzero", extra: "; exit 3", code: e.CodeBuildFailed},
		{name: "layout writer canceled", extra: "; sleep 30", timeout: 200 * time.Millisecond, code: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
				defer cancel()
			}
			root := t.TempDir()
			tool := filepath.Join(root, "tools", "aicr")
			if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tool, executable, 0o755); err != nil {
				t.Fatal(err)
			}
			node := pinnedToolNode(executable)
			node.Platform = "linux/" + runtime.GOARCH
			node.OutputType, node.ArtifactPath = "oci-layout", ""
			node.Command = []string{"bundle", "--output", "oci-layout:/nvkr/output/layout"}
			result, err := (Local{}).ExecNode(node, RunOptions{Ctx: ctx, Launch: true,
				Runtime: fakePinnedToolRuntime(t, refuseExisting+test.extra), SourceRoot: filepath.Join(root, "src"),
				ToolExecutable: tool, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
			if got := pinnedToolCode(err); got != test.code {
				t.Fatalf("ExecNode() code %q (%v); want %q", got, err, test.code)
			}
			output := filepath.Join(root, "out", "recipe")
			staged, _ := filepath.Glob(filepath.Join(root, "out", ".nvkr-*"))
			if len(staged) != 0 {
				t.Fatalf("private staging remains: %q", staged)
			}
			if test.code != "" {
				if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
					t.Fatalf("failed run left partial output: %v", statErr)
				}
				return
			}
			if result.TransportPath != output || result.SHA256 != FileDigest([]byte(`{"schemaVersion":2,"manifests":[]}`)) {
				t.Fatalf("wrong layout observation: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(output, "oci-layout")); err != nil {
				t.Fatalf("layout is not the output root: %v", err)
			}
		})
	}
}

// The layout walk admits only directories and regular files; a FIFO is neither.
func TestAdmitPinnedToolLayoutRefusesSpecialFile(t *testing.T) {
	root := t.TempDir()
	staging, outRoot := filepath.Join(root, "staging"), filepath.Join(root, "out")
	layout := filepath.Join(staging, "layout")
	if err := writeTestLayout(layout); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(layout, "blobs", "sha256", "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := pinnedToolNode(nil)
	node.OutputType, node.ArtifactPath = "oci-layout", ""
	if _, _, err := admitPinnedToolLayout(t.Context(), staging, outRoot, node); pinnedToolCode(err) != e.CodeBuildFailed {
		t.Fatalf("admitPinnedToolLayout() = %v; want %s", err, e.CodeBuildFailed)
	}
	if _, err := os.Lstat(outRoot); !os.IsNotExist(err) {
		t.Fatalf("refused layout reached the output root: %v", err)
	}
}

func TestPinnedToolExecutorContract(t *testing.T) {
	executable := []byte("#!/bin/sh\nexit 0\n")
	for _, test := range []struct {
		name    string
		runtime string
		tool    []byte
		timeout time.Duration
		code    e.Code
	}{
		{name: "one observed file", runtime: `printf recipe > "$out/recipe.yaml"`},
		{name: "tool exits nonzero", runtime: `printf partial > "$out/recipe.yaml"; exit 3`, code: e.CodeBuildFailed},
		{name: "directory output", runtime: `mkdir "$out/recipe.yaml"`, code: e.CodeBuildFailed},
		// Cleanup must clear a read-only tree, or every later run fails resetting it.
		{name: "read-only directory left by a failed run", runtime: `mkdir "$out/cache"; printf x > "$out/cache/x"; chmod 0555 "$out/cache"; exit 3`, code: e.CodeBuildFailed},
		{name: "signature beside output", runtime: `printf r > "$out/recipe.yaml"; printf s > "$out/recipe.yaml.sig"`, code: e.CodeBuildFailed},
		{name: "held executable differs from pin", runtime: `printf r > "$out/recipe.yaml"`, tool: []byte("other"), code: e.CodeVerifyFailed},
		{name: "cancellation", runtime: `printf r > "$out/recipe.yaml"; sleep 30`, timeout: 200 * time.Millisecond, code: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			tool := filepath.Join(root, "tools", "aicr")
			held := executable
			if test.tool != nil {
				held = test.tool
			}
			if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tool, held, 0o755); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if test.timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
				defer cancel()
			}
			node := pinnedToolNode(executable)
			node.Platform = "linux/" + runtime.GOARCH
			result, err := (Local{}).ExecNode(node, RunOptions{Ctx: ctx, Launch: true, Runtime: fakePinnedToolRuntime(t, test.runtime),
				SourceRoot: filepath.Join(root, "src"), ToolExecutable: tool, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
			if got := pinnedToolCode(err); got != test.code {
				t.Fatalf("ExecNode() code %q (%v); want %q", got, err, test.code)
			}
			output := filepath.Join(root, "out", "recipe")
			if test.code != "" {
				if result != nil {
					t.Fatalf("failed run returned %+v", result)
				}
				if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
					t.Fatalf("failed run left partial output: %v", statErr)
				}
				return
			}
			if result.SHA256 != FileDigest([]byte("recipe")) || result.InputDigest != node.Digest ||
				result.BuilderType != node.BuilderType || result.AbsPath != filepath.Join(output, "recipe.yaml") {
				t.Fatalf("wrong pinned-tool observation: %+v", result)
			}
		})
	}
}
