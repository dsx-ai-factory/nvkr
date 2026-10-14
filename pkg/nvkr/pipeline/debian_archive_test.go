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
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianArchiveNode() ir.Node {
	node := sampleGraph().Nodes[0]
	node.OutputType, node.ArtifactPath = "debian-package", "out/package.deb"
	node.Debian = &target.Debian{Distribution: "ubuntu-24.04", Platform: node.Platform}
	return node
}

func archiveTestOwner(t *testing.T, budget int64) *debianArchiveOwner {
	t.Helper()
	owner, err := newDebianArchiveOwner(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	return owner
}

// Controlled private collection proof only: these are arbitrary archive bytes
// and a described prepared input, not a real Debian build or format inspection.
func capturedArchiveFixture(t *testing.T) (*debianArchiveOwner, *DebianProducerArchive, ir.Node, string, string) {
	t.Helper()
	owner := archiveTestOwner(t, 64)
	lease, err := owner.reserve(t.Context(), 64)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "package.deb")
	if err := os.WriteFile(path, []byte("original archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := debianArchiveNode()
	node, err = lease.prepare(t.Context(), node)
	if err != nil {
		t.Fatal(err)
	}
	input := "sha256:" + strings.Repeat("a", 64)
	archive, err := collectDebianProducerArchive(t.Context(), node, &materializedSource{Dir: root, InputDigest: input}, root, "package.deb", lease)
	if err != nil {
		t.Fatal(err)
	}
	return owner, archive, node, input, path
}

func TestDebianArchiveRetainsBytesAndExactProducer(t *testing.T) {
	_, archive, node, input, path := capturedArchiveFixture(t)
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := archive.WriteToContext(t.Context(), &output); err != nil || output.String() != "original archive" {
		t.Fatal("retained bytes changed", err, output.String())
	}
	digest, size, err := archive.identity(t.Context())
	if err != nil || digest != FileDigest(output.Bytes()) || size != int64(output.Len()) {
		t.Fatal("retained identity changed", err)
	}
	if err := archive.matches(t.Context(), node, input); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ir.Node, *string)
	}{
		{"sibling", func(n *ir.Node, _ *string) { n.Component = "other"; n.ID = "build-other" }},
		{"source", func(_ *ir.Node, s *string) { *s = "sha256:" + strings.Repeat("b", 64) }},
		{"target", func(n *ir.Node, _ *string) { copy := *n.Debian; copy.Distribution = "debian-13"; n.Debian = &copy }},
		{"toolchain", func(n *ir.Node, _ *string) { n.Toolchain = "other@sha256:" + strings.Repeat("b", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, source := node, input
			tc.mutate(&changed, &source)
			if err := archive.matches(t.Context(), changed, source); code(err) != e.CodeVerifyFailed {
				t.Fatal("producer substitution accepted", err)
			}
		})
	}
	raw, err := canonical.Marshal(ArtifactResult{debian: archive, Component: node.Component, SHA256: digest, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	var restored ArtifactResult
	if err := canonical.UnmarshalExact(raw, &restored); err != nil || restored.debian != nil {
		t.Fatal("serialized result restored capability", err)
	}
	alias := *archive
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := alias.WriteToContext(t.Context(), io.Discard); code(err) != e.CodeEngineCapability {
		t.Fatal("shallow copy survived close", err)
	}
}

func TestDebianArchiveReservationAndCaptureFailureRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     []byte
		symlink bool
	}{
		{"empty", nil, false}, {"oversized", []byte("too large"), false}, {"symlink", []byte("x"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := archiveTestOwner(t, 4)
			lease, err := owner.reserve(t.Context(), 4)
			if err != nil {
				t.Fatal(err)
			}
			if extra, err := owner.reserve(t.Context(), 1); code(err) != e.CodeEngineCapability || extra != nil {
				t.Fatal("capacity reservation admitted", err)
			}
			root := t.TempDir()
			node, err := lease.prepare(t.Context(), debianArchiveNode())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "package.deb")
			if tc.symlink {
				if err := os.Symlink("absent", path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			archive, err := collectDebianProducerArchive(t.Context(), node, &materializedSource{Dir: root, InputDigest: "sha256:" + strings.Repeat("a", 64)}, root, "package.deb", lease)
			if code(err) != e.CodeVerifyFailed || archive != nil {
				t.Fatal("invalid output retained", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			replacement, err := owner.reserve(t.Context(), 4)
			if err != nil {
				t.Fatal("failed capture leaked capacity", err)
			}
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDebianArchivePrivateBuildAdmissionBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ownerBudget   int64
		packageOutput bool
	}{
		{"no owner", 0, false}, {"insufficient retention", 1, false}, {"wrapping forbidden", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent-source")
			options := RunOptions{Ctx: t.Context(), SourceRoot: root, Stdout: io.Discard, Stderr: io.Discard, Package: tc.packageOutput}
			if tc.ownerBudget > 0 {
				options.debianArchives = archiveTestOwner(t, tc.ownerBudget)
			}
			result, err := (Local{}).build(debianArchiveNode(), options)
			if code(err) != e.CodeEngineCapability || result != nil {
				t.Fatal("unsupported producer admitted", err)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("admission touched source", err)
			}
		})
	}
}

func TestDebianArchivePrivateBuildFailureReleasesReservation(t *testing.T) {
	owner := archiveTestOwner(t, MaxDebianPackageBytes)
	root := filepath.Join(t.TempDir(), "absent-source")
	result, err := (Local{}).build(debianArchiveNode(), RunOptions{
		Ctx: t.Context(), SourceRoot: root, Stdout: io.Discard, Stderr: io.Discard, debianArchives: owner,
	})
	if code(err) != e.CodeBuildFailed || result != nil {
		t.Fatal("missing authenticated source returned a producer result", err)
	}
	lease, err := owner.reserve(t.Context(), MaxDebianPackageBytes)
	if err != nil {
		t.Fatal("failed build leaked its retained-byte permit", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDebianArchiveOwnerCancelsAndJoinsProducer(t *testing.T) {
	owner := archiveTestOwner(t, 64)
	lease, err := owner.reserve(t.Context(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.prepare(t.Context(), debianArchiveNode()); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := lease.begin(t.Context(), debianProduce)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not cancel producer")
	}
	select {
	case <-closed:
		t.Fatal("owner closed before producer joined")
	default:
	}
	finish()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not join producer")
	}
	if len(owner.leases) != 0 {
		t.Fatal("owner leaked reservations")
	}
	if lease, err := owner.reserve(t.Context(), 1); code(err) != e.CodeEngineCapability || lease != nil {
		t.Fatal("closed owner admitted work", err)
	}
}

func TestDebianArchiveFreezesInstructionBeforeProducerWork(t *testing.T) {
	owner := archiveTestOwner(t, 64)
	lease, err := owner.reserve(t.Context(), 64)
	if err != nil {
		t.Fatal(err)
	}
	original := debianArchiveNode()
	frozen, err := lease.prepare(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.deb"), []byte("produced fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := "sha256:" + strings.Repeat("a", 64)
	started, proceed := make(chan struct{}), make(chan struct{})
	result := make(chan *DebianProducerArchive, 1)
	failure := make(chan error, 1)
	go func() {
		ctx, finish, err := lease.begin(t.Context(), debianProduce)
		if err != nil {
			failure <- err
			return
		}
		defer finish()
		close(started)
		<-proceed
		archive, err := collectDebianProducerArchive(ctx, frozen, &materializedSource{Dir: root, InputDigest: input}, root, "package.deb", lease)
		if err != nil {
			failure <- err
			return
		}
		result <- archive
	}()
	select {
	case <-started:
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("producer did not admit frozen instruction")
	}
	original.Command[0] = "substituted-command"
	original.Env[0].Value = "changed-environment"
	original.Debian.Distribution = "debian-13"
	close(proceed)
	select {
	case archive := <-result:
		if err := archive.matches(t.Context(), frozen, input); err != nil {
			t.Fatal("caller mutation changed executed instruction", err)
		}
		if err := archive.matches(t.Context(), original, input); code(err) != e.CodeVerifyFailed {
			t.Fatal("mutated caller instruction replaced original", err)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("producer did not complete private capture")
	}
}

type archiveGateWriter struct{ started, release chan struct{} }

func (w *archiveGateWriter) Write(raw []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(raw), nil
}

func TestDebianArchiveCloseJoinsExports(t *testing.T) {
	owner, archive, _, _, _ := capturedArchiveFixture(t)
	writer := &archiveGateWriter{started: make(chan struct{}), release: make(chan struct{})}
	written := make(chan error, 1)
	go func() { _, err := archive.WriteToContext(t.Context(), writer); written <- err }()
	select {
	case <-writer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("export did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case <-owner.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not cancel exports")
	}
	select {
	case <-closed:
		t.Fatal("owner released an active export")
	default:
	}
	// A foreign io.Writer must return before its owner can join it.
	close(writer.release)
	if err := <-written; code(err) != e.CodeTimeout {
		t.Fatal("canceled export returned success", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("export cleanup did not join")
	}
}

func TestDebianArchiveConcurrentRevocation(t *testing.T) {
	owner, archive, node, input, _ := capturedArchiveFixture(t)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 8 {
				err := archive.matches(t.Context(), node, input)
				if err != nil && code(err) != e.CodeEngineCapability && code(err) != e.CodeTimeout {
					t.Error("unexpected match failure", err)
				}
				_, err = archive.WriteToContext(t.Context(), io.Discard)
				if err != nil && code(err) != e.CodeEngineCapability && code(err) != e.CodeTimeout {
					t.Error("unexpected export failure", err)
				}
			}
		})
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
}

func TestDebianArchiveOwnerInputAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		budget int64
		want   e.Code
	}{
		{"nil context", nil, 1, e.CodeUsage},
		{"canceled", canceled, 1, e.CodeTimeout},
		{"zero capacity", t.Context(), 0, e.CodeEngineCapability},
		{"negative capacity", t.Context(), -1, e.CodeEngineCapability},
		{"oversized capacity", t.Context(), MaxDebianPackageBytes + 1, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := newDebianArchiveOwner(tc.ctx, tc.budget)
			if code(err) != tc.want || owner != nil {
				t.Fatal("invalid owner admitted", err)
			}
		})
	}
}
