// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func nativeDebianExecutor(t *testing.T, ctx context.Context) (*DebianProducerExecutor, *NodeSession, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	graph.Platform = "linux/" + runtime.GOARCH
	for i := range graph.Nodes {
		n := &graph.Nodes[i]
		if n.Kind == ir.KindBuild {
			n.Platform = graph.Platform
			n.Debian.Platform = graph.Platform
			n.Env = testBuildEnv(runtime.GOARCH)
		}
	}
	session, err := NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	executor, err := session.admitDebianProducers(ctx, graph, mustGraphDigest(t, graph), []debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	return executor, session, root
}

// A shell fixture supplies controlled runtime/image observations and arbitrary
// archive bytes. It proves held dispatch/revalidation, not Docker or Debian.
func nativeDebianRuntime(t *testing.T, arch, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	changed := filepath.Join(dir, "changed")
	cleaned := filepath.Join(dir, "cleaned")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
 version)
  v=1; if [ -f %q ]; then v=2; fi
  printf '{"Client":{"Version":"1"},"Server":{"Version":"%%s"}}' "$v"; exit 0 ;;
 info) printf '"controlled-daemon"'; exit 0 ;;
 image) printf '{"Id":"sha256:%s","RepoDigests":["example.test/toolchain@sha256:%s"],"Os":"linux","Architecture":"%s","Config":{"Env":[],"Volumes":null}}'; exit 0 ;;
 rm) printf cleaned > %q; if [ %q = cleanup-failure ]; then exit 7; fi; exit 0 ;;
esac
out=
while [ "$#" -gt 0 ]; do
 case "$1" in
  -v) case "$2" in *:/src/out) out=${2%%:/src/out} ;; esac; shift 2 ;;
  *) shift ;;
 esac
done
[ -n "$out" ]
printf 'held controlled archive' > "$out/a"
if [ %q = changed ]; then printf changed > %q; fi
`, changed, strings.Repeat("a", 64), strings.Repeat("c", 64), arch, cleaned, mode, mode, changed)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, cleaned
}

func TestDebianExecutorHeldRuntimePreservesDispatchAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	executor, session, root := nativeDebianExecutor(t, ctx)
	path, cleaned := nativeDebianRuntime(t, runtime.GOARCH, "stable")
	if err := executor.admitRuntime(ctx, path); err != nil {
		t.Fatal(err)
	}
	if session.state.debianArchives != nil || len(session.state.sources) != 0 {
		t.Fatal("runtime admission performed producer work")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, result, err := executor.run(ctx, "artifact/a", RunOptions{Ctx: ctx, Launch: true, SourceRoot: root, Runtime: path, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || archive == nil || result == nil {
		t.Fatal("held producer followed replaced runtime path", err)
	}
	if _, err := os.Stat(cleaned); err != nil {
		t.Fatal("cleanup did not use original held executable", err)
	}
	if err := executor.RequireArchiveContext(ctx, "artifact/a", archive, result.InputDigest); err != nil {
		t.Fatal(err)
	}
}

func TestDebianExecutorHeldRuntimeRejectsChangedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, arch, mode string
		admission        bool
	}{
		{"wrong image architecture", "unsupported", "stable", true},
		{"changed daemon after build", runtime.GOARCH, "changed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			executor, session, root := nativeDebianExecutor(t, ctx)
			path, _ := nativeDebianRuntime(t, tc.arch, tc.mode)
			err := executor.admitRuntime(ctx, path)
			if tc.admission {
				if code(err) != e.CodePreflightFailed || session.state.debianArchives != nil || len(session.state.sources) != 0 {
					t.Fatal("bad image admitted producer work", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			archive, result, err := executor.run(ctx, "artifact/a", RunOptions{Ctx: ctx, Launch: true, SourceRoot: root, Runtime: path, Stdout: io.Discard, Stderr: io.Discard})
			if archive != nil || result != nil || code(err) != e.CodeEngineCapability {
				t.Fatal("changed runtime returned success", err)
			}
			lease, err := session.state.debianArchives.reserve(ctx, MaxDebianPackageBytes)
			if err != nil {
				t.Fatal("rejected completion retained capacity", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebianExecutorHeldRuntimeRejectsCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	executor, session, root := nativeDebianExecutor(t, ctx)
	path, _ := nativeDebianRuntime(t, runtime.GOARCH, "cleanup-failure")
	if err := executor.admitRuntime(ctx, path); err != nil {
		t.Fatal(err)
	}
	archive, result, err := executor.run(ctx, "artifact/a", RunOptions{Ctx: ctx, Launch: true, SourceRoot: root, Runtime: path, Stdout: io.Discard, Stderr: io.Discard})
	if archive != nil || result != nil || code(err) != e.CodeBuildFailed {
		t.Fatal("failed owned cleanup transferred a producer archive", err)
	}
	lease, err := session.state.debianArchives.reserve(ctx, MaxDebianPackageBytes)
	if err != nil {
		t.Fatal("failed cleanup retained archive capacity", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}
