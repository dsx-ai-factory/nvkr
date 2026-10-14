// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func debianExecutorGraph(t *testing.T, root string) *ir.Graph {
	t.Helper()
	node := sessionDebianNode(t, root)
	return testSourceGraph(sessionSource(t, root), node)
}

// This fixture produces identical arbitrary bytes for separate invocations. It
// tests named-subject separation, not Debian format or container authentication.
func debianExecutorRuntime(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(path, []byte(`#!/bin/sh
set -eu
[ "$1" != rm ] || exit 0
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -v) case "$2" in *:/src/out) out=${2%:/src/out} ;; esac; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$out" ]
printf 'same controlled archive' > "$out/a"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDebianExecutorOriginalNamedCompletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	build := mustGraphDigest(t, graph)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	selections := []debianProducerSelection{{"artifact/first/a", "build-a"}, {"artifact/second/a", "build-a"}}
	executor, err := session.admitDebianProducers(t.Context(), graph, build, selections)
	if err != nil {
		t.Fatal(err)
	}
	other, err := session.admitDebianProducers(t.Context(), graph, build, selections)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the authored graph after admission cannot change execution.
	for i := range graph.Nodes {
		if graph.Nodes[i].Kind == ir.KindBuild {
			graph.Nodes[i].Command[0] = "substituted"
			graph.Nodes[i].Debian.Distribution = "other-profile"
		}
	}
	options := sessionDebianOptions(t, root)
	options.Runtime = debianExecutorRuntime(t)
	archive, result, err := executor.run(t.Context(), selections[0].subject, options)
	if err != nil || archive == nil || result == nil {
		t.Fatal("missing original invocation", err)
	}
	var first bytes.Buffer
	if _, err := archive.WriteToContext(t.Context(), &first); err != nil {
		t.Fatal(err)
	}
	alias := *executor
	if err := alias.RequireArchiveContext(t.Context(), selections[0].subject, archive, result.InputDigest); err != nil {
		t.Fatal("shallow alias lost original ownership", err)
	}
	for _, tc := range []struct {
		name, subject, input string
		executor             *DebianProducerExecutor
	}{
		{"sibling", selections[1].subject, result.InputDigest, executor},
		{"new executor", selections[0].subject, result.InputDigest, other},
		{"source substitution", selections[0].subject, "sha256:" + strings.Repeat("0", 64), executor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.executor.RequireArchiveContext(t.Context(), tc.subject, archive, tc.input); code(err) != e.CodeVerifyFailed {
				t.Fatal("substitution admitted", err)
			}
		})
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if a, r, err := alias.run(t.Context(), selections[0].subject, options); a != nil || r != nil || code(err) != e.CodeVerifyFailed {
		t.Fatal("closed archive allowed replay of original invocation", err)
	}
	second, secondResult, err := executor.run(t.Context(), selections[1].subject, options)
	if err != nil {
		t.Fatal(err)
	}
	var secondBytes bytes.Buffer
	if _, err := second.WriteToContext(t.Context(), &secondBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), secondBytes.Bytes()) || result.SHA256 != secondResult.SHA256 || result.InputDigest != secondResult.InputDigest {
		t.Fatal("fixture did not prove equal-byte/source sibling case")
	}
	if err := executor.RequireArchiveContext(t.Context(), selections[0].subject, second, secondResult.InputDigest); code(err) != e.CodeVerifyFailed {
		t.Fatal("equal bytes authorized sibling reuse", err)
	}
	if err := executor.RequireArchiveContext(t.Context(), selections[1].subject, second, secondResult.InputDigest); err != nil {
		t.Fatal(err)
	}
	if err := session.CloseChecked(); err != nil {
		t.Fatal(err)
	}
	if err := alias.RequireArchiveContext(t.Context(), selections[1].subject, second, secondResult.InputDigest); code(err) != e.CodeEngineCapability {
		t.Fatal("session close retained named archive authority", err)
	}
	if _, err := second.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
		t.Fatal("session close retained bytes", err)
	}
}

func TestDebianExecutorSelectionBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Graph, *[]debianProducerSelection, *string)
		want e.Code
	}{
		{"wrong Build", func(_ *ir.Graph, _ *[]debianProducerSelection, b *string) { *b = "sha256:" + strings.Repeat("0", 64) }, e.CodeVerifyFailed},
		{"unknown producer", func(_ *ir.Graph, s *[]debianProducerSelection, _ *string) { (*s)[0].nodeID = "absent" }, e.CodeInvalidCatalog},
		{"duplicate subject", func(_ *ir.Graph, s *[]debianProducerSelection, _ *string) { *s = append(*s, (*s)[0]) }, e.CodeInvalidCatalog},
		{"unsorted subjects", func(_ *ir.Graph, s *[]debianProducerSelection, _ *string) {
			*s = append(*s, debianProducerSelection{"a", "build-a"})
		}, e.CodeInvalidCatalog},
		{"unsafe subject", func(_ *ir.Graph, s *[]debianProducerSelection, _ *string) { (*s)[0].subject = " unsafe " }, e.CodeInvalidCatalog},
		{"malformed unselected fetch", func(g *ir.Graph, _ *[]debianProducerSelection, _ *string) { g.Nodes[1].Commit = "bad" }, e.CodeUnpinnedSource},
		{"missing terminal", func(g *ir.Graph, _ *[]debianProducerSelection, _ *string) { g.Nodes = g.Nodes[:2] }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := debianExecutorGraph(t, filepath.Join(t.TempDir(), "src"))
			build := mustGraphDigest(t, graph)
			selections := []debianProducerSelection{{"artifact/first/a", "build-a"}}
			tc.edit(graph, &selections, &build)
			session, err := NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			if got, err := session.admitDebianProducers(t.Context(), graph, build, selections); got != nil || code(err) != tc.want {
				t.Fatal("invalid selection admitted", err)
			}
			if session.state.debianArchives != nil || len(session.state.sources) != 0 {
				t.Fatal("selection performed source or archive work")
			}
		})
	}
}

func TestDebianExecutorFailureAndForeignSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	executor, err := session.admitDebianProducers(t.Context(), graph, mustGraphDigest(t, graph), []debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	options := sessionDebianOptions(t, filepath.Join(t.TempDir(), "missing"))
	options.debianExecutor, options.debianSubject = executor, "artifact/a"
	if got, err := other.execDebianProducer(graph.Nodes[0], options); got != nil || code(err) != e.CodeVerifyFailed || other.state.debianArchives != nil {
		t.Fatal("foreign session reached source work", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if a, r, err := executor.run(ctx, "artifact/a", options); a != nil || r != nil || code(err) != e.CodeTimeout {
		t.Fatal("canceled request admitted", err)
	}
	if a, r, err := executor.run(t.Context(), "artifact/a", options); a != nil || r != nil || code(err) != e.CodeBuildFailed {
		t.Fatal("failed producer retained success", err)
	}
	if a, r, err := executor.run(t.Context(), "artifact/a", options); a != nil || r != nil || code(err) != e.CodeVerifyFailed {
		t.Fatal("failed attempt was replayed", err)
	}
	// Failure releases capacity; a newly issued attempt may perform its own work.
	next, err := session.admitDebianProducers(t.Context(), graph, mustGraphDigest(t, graph), []debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	options = sessionDebianOptions(t, root)
	if a, r, err := next.run(t.Context(), "artifact/a", options); err != nil || a == nil || r == nil {
		t.Fatal("failed attempt leaked retained capacity", err)
	}
}

func TestDebianExecutorFreezesOriginalSessionState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	handle, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := *handle
	t.Cleanup(original.Close)
	executor, err := handle.admitDebianProducers(t.Context(), graph, mustGraphDigest(t, graph),
		[]debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replacement.Close)
	*handle = *replacement
	archive, result, err := executor.run(t.Context(), "artifact/a", sessionDebianOptions(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if original.state.debianArchives == nil || replacement.state.debianArchives != nil {
		t.Fatal("facade replacement redirected original execution")
	}
	if err := replacement.CloseChecked(); err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireArchiveContext(t.Context(), "artifact/a", archive, result.InputDigest); err != nil {
		t.Fatal("replacement session revoked original ownership", err)
	}
	if err := original.CloseChecked(); err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireArchiveContext(t.Context(), "artifact/a", archive, result.InputDigest); code(err) != e.CodeEngineCapability {
		t.Fatal("original session close retained archive authority", err)
	}
}
