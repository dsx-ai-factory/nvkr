// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func sessionDebianNode(t *testing.T, root string) ir.Node {
	t.Helper()
	fetch := sessionSource(t, root)
	node := completeTestBuild(ir.Node{ID: "build-a", Kind: ir.KindBuild, Component: "a"}, fetch)
	node.OutputType, node.ArtifactPath = "debian-package", "out/a"
	node.Debian = &target.Debian{Distribution: "ubuntu-24.04", Platform: node.Platform}
	return node
}

func sessionDebianOptions(t *testing.T, root string) RunOptions {
	t.Helper()
	return RunOptions{Ctx: t.Context(), SourceRoot: root, Runtime: sessionRuntime(t), Launch: true,
		Stdout: io.Discard, Stderr: io.Discard}
}

// The runtime fixture emits arbitrary bytes. This proves private execution,
// source-closure checks and retention; it is not a real Debian/daemon run.
func TestNodeSessionDebianRetainsActualPrivateCompletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	node := sessionDebianNode(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	options := sessionDebianOptions(t, root)
	result, err := session.execDebianProducer(node, options)
	if err != nil || result == nil || result.debian == nil {
		t.Fatal("missing producer completion", err)
	}
	if result.Qualification != ArtifactUnqualified || !canonical.IsDigest(result.InputDigest) {
		t.Fatal("invalid private source description")
	}
	if err := result.debian.matches(t.Context(), node, result.InputDigest); err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if _, err := result.debian.WriteToContext(t.Context(), &original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.AbsPath, []byte("path substitution"), 0o600); err != nil {
		t.Fatal(err)
	}
	var retained bytes.Buffer
	if _, err := result.debian.WriteToContext(t.Context(), &retained); err != nil || !bytes.Equal(original.Bytes(), retained.Bytes()) {
		t.Fatal("producer bytes were substituted", err)
	}
	missing := options
	missing.SourceRoot = filepath.Join(t.TempDir(), "missing")
	if got, err := session.execDebianProducer(node, missing); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("retained-byte capacity admitted another producer", err)
	}
	if err := result.debian.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := session.execDebianProducer(node, missing); got != nil || code(err) != e.CodeBuildFailed {
		t.Fatal("failed source preparation lost its reservation", err)
	}
	result, err = session.execDebianProducer(node, options)
	if err != nil {
		t.Fatal("failed producer leaked capacity", err)
	}
	alias := *session
	if err := alias.closeOwned(); err != nil {
		t.Fatal(err)
	}
	if _, err := result.debian.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
		t.Fatal("session close retained archive authority", err)
	}
	if got, err := session.execDebianProducer(node, options); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("closed session admitted producer", err)
	}
}

func TestNodeSessionDebianPublicExecutionRemainsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	node := sessionDebianNode(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	options := sessionDebianOptions(t, filepath.Join(t.TempDir(), "missing"))
	for _, tc := range []struct {
		name string
		run  func() (*ArtifactResult, error)
	}{
		{"session", func() (*ArtifactResult, error) { return session.ExecNode(node, options) }},
		{"standalone", func() (*ArtifactResult, error) { return (Local{}).ExecNode(node, options) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.run(); got != nil || code(err) != e.CodeEngineCapability {
				t.Fatal("public execution opened", err)
			}
		})
	}
	if session.state.debianArchives != nil {
		t.Fatal("public guard allocated retained-byte owner")
	}
}

func TestNodeSessionDebianAdmissionBeforeSourceWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*RunOptions)
		want e.Code
	}{
		{"nil context", func(o *RunOptions) { o.Ctx = nil }, e.CodeUsage},
		{"direct execution", func(o *RunOptions) { o.Launch = false }, e.CodeEngineCapability},
		{"dry run", func(o *RunOptions) { o.DryRun = true }, e.CodeEngineCapability},
		{"wrapper", func(o *RunOptions) { o.Package = true }, e.CodeEngineCapability},
		{"qualification", func(o *RunOptions) { o.Qualification = &QualifiedExecution{} }, e.CodeEngineCapability},
		{"foreign source owner", func(o *RunOptions) { o.sources = newPreparedSourceCache() }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			options := RunOptions{Ctx: t.Context(), Launch: true, SourceRoot: filepath.Join(t.TempDir(), "absent")}
			tc.edit(&options)
			if result, err := session.execDebianProducer(debianArchiveNode(), options); result != nil || code(err) != tc.want {
				t.Fatal("invalid admission reached source work", err)
			}
			if session.state.debianArchives != nil || len(session.state.sources) != 0 {
				t.Fatal("invalid admission created owners")
			}
		})
	}
}

func TestNodeSessionDebianCloseCancelsAndJoinsProducer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	node := sessionDebianNode(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	writer := &sessionBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(writer.unblock)
	type key struct{}
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.WithValue(t.Context(), key{}, "value"), deadline)
	defer cancel()
	options := sessionDebianOptions(t, root)
	options.Ctx, options.Stdout = ctx, writer
	done := make(chan error, 1)
	go func() {
		got, err := session.execDebianProducer(node, options)
		if got != nil {
			done <- e.New(e.CodeInternal, "canceled producer returned a result")
			return
		}
		done <- err
	}()
	awaitSession(t, writer.started, "private producer barrier")
	session.state.mu.Lock()
	var admitted context.Context
	for call := range session.state.calls {
		admitted = call
	}
	session.state.mu.Unlock()
	if admitted == nil || admitted.Value(key{}) != "value" {
		t.Fatal("caller context lost")
	}
	if got, ok := admitted.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatal("caller deadline lost")
	}
	closed := make(chan error, 1)
	go func() { closed <- session.closeOwned() }()
	awaitSession(t, session.state.ctx.Done(), "session cancel")
	if admitted.Err() != context.Canceled {
		t.Fatal("producer resumed without synchronous cancellation")
	}
	select {
	case <-closed:
		t.Fatal("close returned before producer joined")
	default:
	}
	writer.unblock()
	if err := <-done; code(err) != e.CodeTimeout {
		t.Fatal("canceled private producer", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestNodeSessionDebianCloseJoinsArchiveReader(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	node := sessionDebianNode(t, root)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	result, err := session.execDebianProducer(node, sessionDebianOptions(t, root))
	if err != nil {
		t.Fatal(err)
	}
	writer := &archiveGateWriter{started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(writer.release) }) }
	t.Cleanup(unblock)
	done := make(chan error, 1)
	go func() { _, err := result.debian.WriteToContext(t.Context(), writer); done <- err }()
	awaitSession(t, writer.started, "archive reader")
	result.debian.lease.owner.mu.Lock()
	var admitted context.Context
	for call := range result.debian.lease.calls {
		admitted = call
	}
	result.debian.lease.owner.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- session.closeOwned() }()
	awaitSession(t, session.state.ctx.Done(), "session cancel")
	if admitted == nil || admitted.Err() != context.Canceled {
		t.Fatal("archive reader cancellation was asynchronous")
	}
	select {
	case <-closed:
		t.Fatal("close released a live reader")
	default:
	}
	unblock()
	if err := <-done; code(err) != e.CodeTimeout {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestNodeSessionDebianRejectsFailedCleanup(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "cancellation"}[canceled], func(t *testing.T) {
			owner, archive, _, _, _ := capturedArchiveFixture(t)
			result := &ArtifactResult{debian: archive}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var cleanup error
			want := e.CodeBuildFailed
			if canceled {
				cancel()
				want = e.CodeTimeout
			} else {
				cleanup = e.New(e.CodeBuildFailed, "owned removal failed")
			}
			if err := finishDebianSessionProducer(ctx, &result, nil, cleanup); code(err) != want || result != nil {
				t.Fatal("invalid completion survived", err)
			}
			if _, err := archive.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
				t.Fatal("failed completion kept archive", err)
			}
			lease, err := owner.reserve(t.Context(), 64)
			if err != nil {
				t.Fatal("failed completion leaked permit", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNodeSessionDebianCancellationPreservesProducerAndCleanupFailures(t *testing.T) {
	owner, archive, _, _, _ := capturedArchiveFixture(t)
	result := &ArtifactResult{debian: archive}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	producerErr := e.New(e.CodeBuildFailed, "canceled runtime command")
	cleanupErr := e.New(e.CodeBuildFailed, "reported container removal failure")
	err := finishDebianSessionProducer(ctx, &result, producerErr, cleanupErr)
	if code(err) != e.CodeTimeout || result != nil || !errors.Is(err, context.Canceled) ||
		!errors.Is(err, producerErr) || !errors.Is(err, cleanupErr) {
		t.Fatal("cancellation changed code or lost failures", err)
	}
	if _, err := archive.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
		t.Fatal("canceled completion retained archive", err)
	}
	lease, err := owner.reserve(t.Context(), 64)
	if err != nil {
		t.Fatal("canceled completion leaked capacity", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}
