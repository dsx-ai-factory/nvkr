package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func TestLocalRunMaterializesSharedSourceOnce(t *testing.T) {
	work := t.TempDir()
	sourceRoot := filepath.Join(work, "src")
	destination := "shared-source"
	repository := filepath.Join(sourceRoot, destination)
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "NKD Test"},
		{"config", "user.email", "nkd@example.test"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "input.txt"), []byte("shared pinned input\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "input.txt"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if output, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	commit, err := gitOut(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitOut(context.Background(), repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}

	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fetch := testFetchNode("fetch-shared", repository, commit, tree, destination, toolchain, "")
	buildA := completeTestBuild(ir.Node{ID: "build-a", Kind: ir.KindBuild, Component: "a", Toolchain: toolchain}, fetch)
	buildB := completeTestBuild(ir.Node{ID: "build-b", Kind: ir.KindBuild, Component: "b", Toolchain: toolchain}, fetch)
	// build-c shares the checkout but needs a different writable mountpoint, so it
	// must get a distinct prepared snapshot. With two work slots, it should prepare
	// alongside a/b instead of losing a slot to b waiting on a's single-flight key.
	buildC := completeTestBuild(ir.Node{
		ID: "build-c", Kind: ir.KindBuild, Component: "c", Toolchain: toolchain,
		WriteDir: "alternate", ArtifactPath: "alternate/c",
	}, fetch)
	graph := testSourceGraph("shared-source", fetch, buildA, buildB, buildC)

	runtimePath := filepath.Join(work, "fake-runtime")
	if err := os.WriteFile(runtimePath, []byte(`#!/usr/bin/env bash
set -eu
name=
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --name) name=$2; shift 2 ;;
    -v)
      case "$2" in
        *:/src:ro) ;;
        *:/src/*) out=${2%%:*} ;;
      esac
      shift 2
      ;;
    *) shift ;;
  esac
done
[ -n "$name" ] && [ -n "$out" ]
component=${name##*build-}
mkdir -p "$out"
printf 'built %s\n' "$component" > "$out/$component"
`), 0o755); err != nil {
		t.Fatal(err)
	}

	var progress bytes.Buffer
	if _, err := (Local{}).Run(graph, mustGraphDigest(t, graph), RunOptions{
		Ctx: context.Background(), SourceRoot: sourceRoot, Runtime: runtimePath,
		Jobs: 2, Stdout: &progress, Stderr: &progress,
	}); err != nil {
		t.Fatalf("shared-source run failed: %v\n%s", err, progress.String())
	}
	for _, phase := range []string{
		"checkout closure re-hash verified in ",
		"private normalized snapshot copied in ",
		"private snapshot re-hash verified in ",
		"no patches; verified snapshot digest is the final input digest",
	} {
		if got := strings.Count(progress.String(), phase); got != 2 {
			t.Errorf("three builds with two exact source keys ran phase %q %d times, want twice:\n%s", phase, got, progress.String())
		}
	}
	if strings.Contains(progress.String(), "final patched-input hash computed in ") {
		t.Fatalf("patch-free shared source performed a redundant final hash:\n%s", progress.String())
	}
	log := progress.String()
	startA := strings.Index(log, "[start] source "+destination+" for a ")
	startB := strings.Index(log, "[start] source "+destination+" for b ")
	startC := strings.Index(log, "[start] source "+destination+" for c ")
	firstDone := strings.Index(log, "[done] source ")
	if startA < 0 || startC < 0 || startB >= 0 || firstDone < 0 || startA > firstDone || startC > firstDone {
		t.Fatalf("scheduler did not fill both slots with distinct source keys before a shared-key waiter:\n%s", log)
	}
	if got := strings.Count(log, "reused the run's exact verified private snapshot"); got != 3 {
		t.Fatalf("prepared source cache served %d builds, want three:\n%s", got, log)
	}
	if entries, err := os.ReadDir(filepath.Join(work, "snapshots")); err != nil || len(entries) != 0 {
		t.Fatalf("run-owned prepared snapshots were not reclaimed: entries=%v err=%v", entries, err)
	}
}

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
	closure, err := sourceTreeDigest(source)
	if err != nil {
		b.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, destination)
	if err != nil {
		b.Fatal(err)
	}
	if err := writeSourceReceipt(receiptPath, sourceReceipt{
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
	closure, err := sourceTreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceReceipt(receiptPath, sourceReceipt{
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
