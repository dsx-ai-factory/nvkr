// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func cloneDebianBindingGraph(t *testing.T, graph *ir.Graph) *ir.Graph {
	t.Helper()
	raw, err := canonical.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	var result ir.Graph
	if err := canonical.UnmarshalExact(raw, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func TestDebianProducerCompleteBuildAndSessionBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	build := mustGraphDigest(t, graph)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	other, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	executor, err := session.admitDebianProducers(t.Context(), graph, build, []debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireBuildContext(t.Context(), session, cloneDebianBindingGraph(t, graph), build); err != nil {
		t.Fatal(err)
	}
	changed := cloneDebianBindingGraph(t, graph)
	for i := range changed.Nodes {
		if changed.Nodes[i].Kind == ir.KindFetch {
			changed.Nodes[i].Repo = "https://example.invalid/replacement"
		}
	}
	for _, tc := range []struct {
		name    string
		session *NodeSession
		graph   *ir.Graph
		build   string
		want    e.Code
	}{
		{"foreign owner", other, graph, build, e.CodeVerifyFailed},
		{"missing graph", session, nil, build, e.CodeProvenanceMissing},
		{"wrong Build", session, graph, "sha256:" + strings.Repeat("0", 64), e.CodeVerifyFailed},
		{"changed fetch description", session, changed, mustGraphDigest(t, changed), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.RequireBuildContext(t.Context(), tc.session, tc.graph, tc.build); code(err) != tc.want {
				t.Fatal("Build/session substitution admitted", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := executor.RequireBuildContext(ctx, session, graph, build); code(err) != e.CodeTimeout {
		t.Fatal("canceled Build comparison admitted", err)
	}
	if err := session.CloseChecked(); err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireBuildContext(t.Context(), session, graph, build); code(err) != e.CodeEngineCapability {
		t.Fatal("closed owner restored Build join", err)
	}
}

func TestDebianProducerSelectionMustPrecedeWork(t *testing.T) {
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
	var instruction ir.Node
	for _, node := range graph.Nodes {
		if node.ID == "build-a" {
			instruction = node
		}
	}
	if err := executor.RequireSelectionContext(t.Context(), "artifact/a", instruction); err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireSelectionContext(t.Context(), "artifact/sibling", instruction); code(err) != e.CodeVerifyFailed {
		t.Fatal("unselected sibling admitted", err)
	}
	options := sessionDebianOptions(t, root)
	options.Runtime = debianExecutorRuntime(t)
	if _, _, err := executor.run(t.Context(), "artifact/a", options); err != nil {
		t.Fatal(err)
	}
	if err := executor.RequireSelectionContext(t.Context(), "artifact/a", instruction); code(err) != e.CodeVerifyFailed {
		t.Fatal("already invoked producer adopted after work", err)
	}
}

// These controlled private invocations emit arbitrary bytes, not a real package.
func TestDebianProducerDescriptionOwnsOriginalCompletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	build := mustGraphDigest(t, graph)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	executor, err := session.admitDebianProducers(t.Context(), graph, build, []debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	options := sessionDebianOptions(t, root)
	options.Runtime = debianExecutorRuntime(t)
	archive, result, err := executor.run(t.Context(), "artifact/a", options)
	if err != nil {
		t.Fatal(err)
	}
	description, err := archive.DescribeContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if description.BuildDigest != build || description.Subject != "artifact/a" || description.InputDigest != result.InputDigest || description.Digest != result.SHA256 || description.Size != result.Size {
		t.Fatal("description differs from original completion", description)
	}
	description.Instruction.Command[0] = "replaced"
	description.Instruction.Debian.Distribution = "replaced"
	again, err := archive.DescribeContext(t.Context())
	if err != nil || again.Instruction.Command[0] == "replaced" || again.Instruction.Debian.Distribution == "replaced" {
		t.Fatal("description mutated frozen producer", err)
	}
	raw, err := canonical.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DebianProducerArchive
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, err := decoded.DescribeContext(t.Context()); got != nil || code(err) != e.CodeProvenanceMissing {
		t.Fatal("decoded description restored issuing archive", err)
	}
	alias := *archive
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := alias.DescribeContext(t.Context()); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("closed archive alias restored description operation", err)
	}
}

func debianBindingFixture(t *testing.T) (*NodeSession, *DebianProducerExecutor, ir.Node, RunOptions) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "src")
	graph := debianExecutorGraph(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	executor, err := session.admitDebianProducers(t.Context(), graph, mustGraphDigest(t, graph),
		[]debianProducerSelection{{"artifact/a", "build-a"}})
	if err != nil {
		t.Fatal(err)
	}
	var instruction ir.Node
	for _, node := range graph.Nodes {
		if node.ID == "build-a" {
			instruction = node
		}
	}
	options := sessionDebianOptions(t, root)
	options.Runtime = debianExecutorRuntime(t)
	return session, executor, instruction, options
}

// The barrier proves a competing invocation is registered while installation
// owns the subject lock. No sleep, real package or container service is involved.
func TestDebianProducerBindingAtomicBeforeInvocation(t *testing.T) {
	session, executor, instruction, options := debianBindingFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	bound := make(chan error, 1)
	var binding *DebianProducerBinding
	go func() {
		bound <- executor.BindSelectionContext(ctx, "artifact/a", instruction,
			func(ctx context.Context, issued *DebianProducerBinding) error {
				binding = issued
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	competing := make(chan error, 1)
	go func() {
		archive, result, err := executor.run(ctx, "artifact/a", options)
		if archive != nil || result != nil {
			competing <- errors.New("unbound invocation returned output")
			return
		}
		competing <- err
	}()
	for {
		session.state.mu.Lock()
		registered := len(session.state.calls) >= 2
		work := session.state.debianArchives != nil || len(session.state.sources) != 0
		session.state.mu.Unlock()
		if work {
			t.Fatal("producer performed work during binding installation")
		}
		if registered {
			break
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	close(release)
	if err := <-bound; err != nil {
		t.Fatal(err)
	}
	if err := <-competing; code(err) != e.CodeVerifyFailed {
		t.Fatal("unbound invocation bypassed exclusive binding", err)
	}
	called := false
	if err := executor.BindSelectionContext(ctx, "artifact/a", instruction,
		func(context.Context, *DebianProducerBinding) error { called = true; return nil }); code(err) != e.CodeVerifyFailed || called {
		t.Fatal("another Run claimed the same subject", err)
	}
	// A decoded binding cannot invoke the claimed subject. The original alias
	// has the exact issuing state and may perform the one private invocation.
	raw, err := canonical.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DebianProducerBinding
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	options.debianBinding = decoded.state
	if a, r, err := executor.run(ctx, "artifact/a", options); a != nil || r != nil || code(err) != e.CodeVerifyFailed {
		t.Fatal("decoded binding invoked producer", err)
	}
	alias := *binding
	options.debianBinding = alias.state
	if a, r, err := executor.run(ctx, "artifact/a", options); err != nil || a == nil || r == nil {
		t.Fatal("original exclusive binding lost invocation", err)
	}
}

func TestDebianProducerBindingFailedInstallationRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cancel  bool
		failure error
		want    e.Code
	}{
		{"uncoded callback failure", false, errors.New("installation failed"), e.CodeInternal},
		{"coded callback failure", false, e.New(e.CodeVerifyFailed, "foreign Run"), e.CodeVerifyFailed},
		{"canceled callback completion", true, nil, e.CodeTimeout},
		{"canceled callback error", true, context.Canceled, e.CodeTimeout},
		{"canceled coded callback failure", true, e.New(e.CodeVerifyFailed, "installation failure during cancellation"), e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, executor, instruction, options := debianBindingFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var stale *DebianProducerBinding
			err := executor.BindSelectionContext(ctx, "artifact/a", instruction,
				func(_ context.Context, issued *DebianProducerBinding) error {
					stale = issued
					if tc.cancel {
						cancel()
					}
					return tc.failure
				})
			if code(err) != tc.want {
				t.Fatal("failed binding reported wrong code", err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatal("binding lost cancellation cause", err)
			}
			if tc.failure != nil && !errors.Is(err, tc.failure) {
				t.Fatal("binding lost installation failure", err)
			}
			executor.state.mu.Lock()
			retained := executor.state.bindings["artifact/a"] != nil || executor.state.consumed["artifact/a"]
			executor.state.mu.Unlock()
			if retained {
				t.Fatal("failed installation retained subject reservation")
			}
			options.debianBinding = stale.state
			if a, r, err := executor.run(t.Context(), "artifact/a", options); a != nil || r != nil || code(err) != e.CodeVerifyFailed {
				t.Fatal("failed installation's leaked binding invoked producer", err)
			}
			var original *DebianProducerBinding
			if err := executor.BindSelectionContext(t.Context(), "artifact/a", instruction,
				func(_ context.Context, issued *DebianProducerBinding) error { original = issued; return nil }); err != nil {
				t.Fatal(err)
			}
			options.debianBinding = original.state
			if a, r, err := executor.run(t.Context(), "artifact/a", options); err != nil || a == nil || r == nil {
				t.Fatal("failed binding prevented a fresh original binding", err)
			}
		})
	}
}

func TestDebianProducerBindingRejectsForeignCapability(t *testing.T) {
	_, original, instruction, options := debianBindingFixture(t)
	_, foreign, foreignInstruction, _ := debianBindingFixture(t)
	var binding *DebianProducerBinding
	if err := foreign.BindSelectionContext(t.Context(), "artifact/a", foreignInstruction,
		func(_ context.Context, issued *DebianProducerBinding) error { binding = issued; return nil }); err != nil {
		t.Fatal(err)
	}
	options.debianBinding = binding.state
	if a, r, err := original.run(t.Context(), "artifact/a", options); a != nil || r != nil || code(err) != e.CodeVerifyFailed {
		t.Fatal("foreign binding claimed unbound producer", err)
	}
	if err := original.BindSelectionContext(t.Context(), "artifact/sibling", instruction,
		func(context.Context, *DebianProducerBinding) error { t.Fatal("unselected callback ran"); return nil }); code(err) != e.CodeVerifyFailed {
		t.Fatal("unselected subject bound", err)
	}
}
