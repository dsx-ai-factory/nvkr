// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const lockTestRegistry = `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  s: {repo: https://github.com/example/source}
components:
  x: {platforms: [linux/amd64, linux/arm64], fromSource: false, exclusionReason: test-only}
`

func lockPin(tag, commitByte string, epoch int64) Pin {
	return Pin{
		Tag: tag, Commit: strings.Repeat(commitByte, 40), TreeHash: strings.Repeat("f", 40),
		SourceDateEpoch: epoch,
	}
}

func lockYAML(pinBody string) []byte {
	return []byte("apiVersion: nvkr.dev/lock/v1\nkind: PinLock\npins:\n" + pinBody)
}

func TestLockSchemaDispatchIsExact(t *testing.T) {
	validPin := "  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n"
	valid := lockYAML(validPin)
	cases := []struct {
		name string
		lock []byte
	}{
		{"missing apiVersion", bytes.Replace(valid, []byte("apiVersion: nvkr.dev/lock/v1\n"), nil, 1)},
		{"future apiVersion", bytes.Replace(valid, []byte("nvkr.dev/lock/v1"), []byte("nvkr.dev/lock/v2"), 1)},
		{"pre-rebrand namespace", bytes.Replace(valid, []byte("nvkr.dev/lock/v1"), []byte("nkd.dev/lock/v1"), 1)},
		{"missing kind", bytes.Replace(valid, []byte("kind: PinLock\n"), nil, 1)},
		{"wrong kind", bytes.Replace(valid, []byte("PinLock"), []byte("OtherLock"), 1)},
		{"unknown field", append(append([]byte(nil), valid...), []byte("futureField: true\n")...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(lockTestRegistry), LockFile: tc.lock})
			if codeOf(err) != nvkrerr.CodeInvalidCatalog {
				t.Fatalf("exact schema dispatch must reject input, got %v", err)
			}
		})
	}
}

func TestCatalogSchemaDispatchIsExact(t *testing.T) {
	validLock := lockYAML("  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n")
	for _, reg := range [][]byte{
		bytes.Replace([]byte(lockTestRegistry), []byte("nvkr.dev/catalog/v3"), []byte("nvkr.dev/catalog/v4"), 1),
		bytes.Replace([]byte(lockTestRegistry), []byte("SourceBuildRegistry"), []byte("FutureRegistry"), 1),
	} {
		if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: reg, LockFile: validLock}); codeOf(err) != nvkrerr.CodeInvalidCatalog {
			t.Fatalf("future catalog schema must fail closed, got %v", err)
		}
	}
}

func TestLockCompletenessAndOrphansFailClosed(t *testing.T) {
	validPin := "  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n"
	tests := []struct {
		name string
		lock []byte
		want nvkrerr.Code
	}{
		{"missing lock", nil, nvkrerr.CodeUnpinnedSource},
		{"missing declared source", []byte("apiVersion: nvkr.dev/lock/v1\nkind: PinLock\npins: {}\n"), nvkrerr.CodeUnpinnedSource},
		{"orphan source", lockYAML(validPin + "  orphan: {tag: v1.0.0, commit: " + strings.Repeat("b", 40) + ", treeHash: " + strings.Repeat("e", 40) + ", sourceDateEpoch: 1}\n"), nvkrerr.CodeInvalidCatalog},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := bytesProvider{RegistryFile: []byte(lockTestRegistry)}
			if tc.lock != nil {
				provider[LockFile] = tc.lock
			}
			if _, err := LoadContext(context.Background(), provider); codeOf(err) != tc.want {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestLockRejectsOrphanArtifactAndUnknownArchitecture(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	sourcePin := "  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n"
	orphan := append(lockYAML(sourcePin), []byte("artifacts:\n  ghost: {tag: v1, digests: {amd64: "+digest+"}}\n")...)
	if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(lockTestRegistry), LockFile: orphan}); codeOf(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("orphan artifact pin must fail closed, got %v", err)
	}

	prebuiltRegistry := strings.Replace(lockTestRegistry,
		"x: {platforms: [linux/amd64, linux/arm64], fromSource: false, exclusionReason: test-only}",
		"x: {platforms: [linux/amd64, linux/arm64], source: s, outputType: raw, prebuilt: {url: https://example.invalid/x, evidence: {none: true}}}", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(prebuiltRegistry), LockFile: lockYAML(sourcePin)}); codeOf(err) != nvkrerr.CodeUnpinnedSource {
		t.Fatalf("missing declared prebuilt artifact pin must fail closed, got %v", err)
	}
	incompleteArch := append(lockYAML(sourcePin), []byte("artifacts:\n  x: {tag: v1, digests: {amd64: "+digest+"}}\n")...)
	if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(prebuiltRegistry), LockFile: incompleteArch}); codeOf(err) != nvkrerr.CodeUnpinnedSource {
		t.Fatalf("missing required arm64 artifact digest must fail closed, got %v", err)
	}
	unknownArch := append(lockYAML(sourcePin), []byte("artifacts:\n  x: {tag: v1, digests: {riscv64: "+digest+"}}\n")...)
	if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(prebuiltRegistry), LockFile: unknownArch}); codeOf(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("undeclared lock architecture must fail closed, got %v", err)
	}
}

func TestWriteLockCASAtomicAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RegistryFile), []byte(lockTestRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	initial := lockYAML("  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n")
	if err := os.WriteFile(filepath.Join(dir, LockFile), initial, 0o644); err != nil {
		t.Fatal(err)
	}

	left, err := LoadLockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	right, err := LoadLockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	left.Pins["s"] = lockPin("v2.0.0", "b", 2)
	right.Pins["s"] = lockPin("v3.0.0", "c", 3)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*PinLock{left, right} {
		wg.Add(1)
		go func(candidate *PinLock) {
			defer wg.Done()
			<-start
			results <- WriteLockContext(context.Background(), dir, candidate)
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		switch codeOf(err) {
		case "":
			success++
		case nvkrerr.CodeWriteConflict:
			conflict++
		default:
			t.Fatalf("unexpected writer result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("want one commit and one CAS conflict, got success=%d conflict=%d", success, conflict)
	}

	winner, err := LoadLockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if winner.Pins["s"].Tag != "v2.0.0" && winner.Pins["s"].Tag != "v3.0.0" {
		t.Fatalf("final lock is neither complete winner: %+v", winner.Pins["s"])
	}
	before, err := os.ReadFile(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(before, []byte("# Machine-managed")) {
		t.Fatalf("machine-managed header missing: %q", before)
	}
	if err := WriteLockContext(context.Background(), dir, winner); err != nil {
		t.Fatalf("writing unchanged loaded snapshot: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("deterministic lock serialization changed without a semantic change")
	}
}

func TestWriteLockRequiresUnforgeableLoadedSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RegistryFile), []byte(lockTestRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	fabricated := &PinLock{Pins: map[string]Pin{"s": lockPin("v1.0.0", "a", 1)}}
	if err := WriteLockContext(context.Background(), dir, fabricated); codeOf(err) != nvkrerr.CodeWriteConflict {
		t.Fatalf("fabricated update receipt must not authorize a write, got %v", err)
	}

	loaded, err := LoadLockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Pins["s"] = lockPin("v1.0.0", "a", 1)
	if err := WriteLockContext(context.Background(), dir, loaded); err != nil {
		t.Fatalf("LoadLock absent snapshot must authorize one atomic create: %v", err)
	}
}

func TestWriteLockCASAlsoBindsRegistrySnapshot(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, RegistryFile)
	lockPath := filepath.Join(dir, LockFile)
	if err := os.WriteFile(registryPath, []byte(lockTestRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	initial := lockYAML("  s: {tag: v1.0.0, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("f", 40) + ", sourceDateEpoch: 1}\n")
	if err := os.WriteFile(lockPath, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Pins["s"] = lockPin("v2.0.0", "b", 2)

	// Even a schema-equivalent registry edit invalidates the update session. The pin
	// resolver may have observed source declarations, so lock-only CAS is insufficient.
	changedRegistry := []byte(lockTestRegistry + "\n# concurrent catalog edit\n")
	if err := os.WriteFile(registryPath, changedRegistry, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockContext(context.Background(), dir, loaded); codeOf(err) != nvkrerr.CodeWriteConflict {
		t.Fatalf("registry drift must invalidate the lock update snapshot, got %v", err)
	}
	got, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, initial) {
		t.Fatal("registry CAS conflict modified the prior valid lock")
	}
}
