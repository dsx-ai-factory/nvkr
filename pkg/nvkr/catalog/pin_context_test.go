// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPinNetworkOperationsRejectCanceledContextBeforeSubprocess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListTags(ctx, "must-not-be-contacted.invalid/repo"); pinErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("ListTags canceled code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeTimeout, err)
	}
	if _, err := ResolvePin(ctx, Source{Repo: "must-not-be-contacted.invalid/repo"}, "v1.0.0"); pinErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("ResolvePin canceled code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeTimeout, err)
	}
}

func TestPinNetworkOperationsRejectNilContext(t *testing.T) {
	//nolint:staticcheck // The exported boundary must reject a nil context before subprocess I/O.
	if _, err := ListTags(nil, "must-not-be-contacted.invalid/repo"); pinErrorCode(err) != nvkrerr.CodeUsage {
		t.Fatalf("ListTags nil-context code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeUsage, err)
	}
}

func TestLoadLockContextRejectsCancellationBeforeFilesystemIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadLockContext(ctx, filepath.Join(t.TempDir(), "missing")); pinErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("LoadLockContext canceled code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeTimeout, err)
	}
}

func TestLoadLockContextRejectsOversizedRegistry(t *testing.T) {
	directory := t.TempDir()
	file, err := os.Create(filepath.Join(directory, RegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxCatalogFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockContext(context.Background(), directory); pinErrorCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("LoadLockContext oversized code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeInvalidCatalog, err)
	}
}

func TestWriteLockContextRejectsCancellationBeforeFilesystemIO(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, RegistryFile), []byte(lockTestRegistry), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockContext(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteLockContext(ctx, directory, lock); pinErrorCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("WriteLockContext canceled code = %q, want %q: %v", pinErrorCode(err), nvkrerr.CodeTimeout, err)
	}
	for _, name := range []string{LockFile, "." + LockFile + ".lock"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("pre-canceled write created %s: %v", name, err)
		}
	}
}

func pinErrorCode(err error) nvkrerr.Code {
	if coded, ok := err.(*nvkrerr.Error); ok {
		return coded.Code
	}
	return ""
}
