// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func BenchmarkPreparedSourceReuse(b *testing.B) {
	root := filepath.Join(b.TempDir(), "work", "src")
	destination := "shared-source"
	source := filepath.Join(root, destination)
	if err := os.MkdirAll(source, 0o755); err != nil {
		b.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 512) // 8 KiB
	for i := 0; i < 512; i++ {                               // 4 MiB, many-file workload
		name := filepath.Join(source, fmt.Sprintf("input-%04d", i))
		if err := os.WriteFile(name, payload, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	fetch := ir.Node{ID: "fetch-shared", Dest: destination, Commit: goodSourceCommit, TreeHash: "tree"}
	vendorDigest, err := fetchVendorSpecDigest(fetch)
	if err != nil {
		b.Fatal(err)
	}
	closure, err := sourceTreeDigestContext(b.Context(), source)
	if err != nil {
		b.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, destination)
	if err != nil {
		b.Fatal(err)
	}
	if err := writeSourceReceiptContext(b.Context(), receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, ClosureDigest: closure,
	}); err != nil {
		b.Fatal(err)
	}
	build := ir.Node{
		ID: "build-shared", Kind: ir.KindBuild, Component: "shared",
		SourceDest: destination, SourceCommit: fetch.Commit, SourceTreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, SourceReadOnly: true, WriteDir: "out",
		Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "123"}},
	}
	base := RunOptions{
		Ctx: context.Background(), SourceRoot: root,
		progress: &progressSink{w: io.Discard},
	}
	const consumers = 4

	b.Run("uncached-four-consumers", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for consumer := 0; consumer < consumers; consumer++ {
				value, err := materializeSourceUncached(build, base)
				if err != nil {
					b.Fatal(err)
				}
				value.cleanup()
			}
		}
	})
	b.Run("shared-four-consumers", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cache := newPreparedSourceCache()
			opts := base
			opts.sources = cache
			for consumer := 0; consumer < consumers; consumer++ {
				if _, err := materializeSource(build, opts); err != nil {
					b.Fatal(err)
				}
			}
			cache.close()
		}
	})
	b.Run("shared-concurrent-four-consumers", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cache := newPreparedSourceCache()
			opts := base
			opts.sources = cache
			errs := make(chan error, consumers)
			for consumer := 0; consumer < consumers; consumer++ {
				go func() {
					_, err := materializeSource(build, opts)
					errs <- err
				}()
			}
			for consumer := 0; consumer < consumers; consumer++ {
				if err := <-errs; err != nil {
					b.Fatal(err)
				}
			}
			cache.close()
		}
	})
}

func TestPreparedSourceCacheDoesNotMergeDifferentPatchInputs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work", "src")
	destination := "shared-source"
	source := filepath.Join(root, destination)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := ir.Node{ID: "fetch-shared", Dest: destination, Commit: goodSourceCommit, TreeHash: "tree"}
	vendorDigest, err := fetchVendorSpecDigest(fetch)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := sourceTreeDigestContext(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceReceiptContext(t.Context(), receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, ClosureDigest: closure,
	}); err != nil {
		t.Fatal(err)
	}

	patchFor := func(replacement string) ir.PatchRef {
		content := "diff --git a/input.txt b/input.txt\n--- a/input.txt\n+++ b/input.txt\n@@ -1 +1 @@\n-old\n+" + replacement + "\n"
		return ir.PatchRef{Name: "patches/" + replacement + ".patch", SHA256: digestBytes([]byte(content)), Content: content}
	}
	base := ir.Node{
		Kind: ir.KindBuild, SourceDest: destination, SourceCommit: fetch.Commit,
		SourceTreeHash: fetch.TreeHash, VendorSpecDigest: vendorDigest,
		SourceReadOnly: true, WriteDir: "out", Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "123"}},
	}
	buildA := base
	buildA.ID, buildA.Component, buildA.Patches = "build-a", "a", []ir.PatchRef{patchFor("a")}
	buildB := base
	buildB.ID, buildB.Component, buildB.Patches = "build-b", "b", []ir.PatchRef{patchFor("b")}

	cache := newPreparedSourceCache()
	defer cache.close()
	var progress bytes.Buffer
	opts := RunOptions{
		Ctx: context.Background(), SourceRoot: root, sources: cache,
		progress: &progressSink{w: &progress},
	}
	a, err := materializeSource(buildA, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := materializeSource(buildB, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Dir == b.Dir || a.InputDigest == b.InputDigest {
		t.Fatalf("different patches shared prepared-source authority: a=%+v b=%+v", a, b)
	}
	if got := strings.Count(progress.String(), "private normalized snapshot copied in "); got != 2 {
		t.Fatalf("different patch inputs materialized %d snapshots, want two:\n%s", got, progress.String())
	}
}
