// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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
