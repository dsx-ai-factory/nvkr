// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The native bridge uses the real owning source/retention path and controlled
// runtime observations with arbitrary bytes, not a real daemon or Debian package.
func TestDebianProducerBridgeNativeCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	graph.Platform = "linux/" + runtime.GOARCH
	var instruction ir.Node
	for i := range graph.Nodes {
		n := &graph.Nodes[i]
		if n.Kind == ir.KindBuild {
			n.Platform = graph.Platform
			n.Debian.Platform = graph.Platform
			n.Env = testBuildEnv(runtime.GOARCH)
			instruction = *n
		}
	}
	session, err := NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	path, _ := nativeDebianRuntime(t, runtime.GOARCH, "success")
	executor, err := session.AdmitDebianProducer(ctx, graph, mustGraphDigest(t, graph), "artifact/a", instruction.ID, path)
	if err != nil {
		t.Fatal(err)
	}
	var binding *DebianProducerBinding
	if err := executor.BindSelectionContext(ctx, "artifact/a", instruction, func(_ context.Context, b *DebianProducerBinding) error { binding = b; return nil }); err != nil {
		t.Fatal(err)
	}
	archive, result, err := executor.RunBoundContext(ctx, binding, DebianProducerRun{SourceRoot: root, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || archive == nil || result == nil {
		t.Fatal("missing native bridge completion", err)
	}
	if err := executor.RequireArchiveContext(ctx, "artifact/a", archive, result.InputDigest); err != nil {
		t.Fatal(err)
	}
	if err := session.CloseChecked(); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.WriteToContext(ctx, io.Discard); code(err) != e.CodeEngineCapability {
		t.Fatal("closed session retained bridge bytes", err)
	}
}
