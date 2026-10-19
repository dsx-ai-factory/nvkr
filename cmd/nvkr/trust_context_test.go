// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
	"golang.org/x/sys/unix"
)

func TestLoadTrustStoreFileUsesBoundedCanonicalSnapshot(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trust.json")
		want := trustStoreFile{Identities: []string{"issuer=https://issuer.example,subject=builder@example.com"}}
		raw, err := canonical.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadTrustStoreFile(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Identities) != 1 || got.Identities[0] != want.Identities[0] {
			t.Fatalf("loaded identities = %#v, want %#v", got.Identities, want.Identities)
		}
	})

	t.Run("absent is empty", func(t *testing.T) {
		got, err := loadTrustStoreFile(context.Background(), filepath.Join(t.TempDir(), "missing.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Identities) != 0 {
			t.Fatalf("loaded identities = %#v, want empty", got.Identities)
		}
	})

	t.Run("oversized regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trust.json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(trust.MaxAuthenticatedDocumentBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTrustStoreFile(context.Background(), path); cliErrorCode(err) != nvkrerr.CodeTrustPolicyMissing {
			t.Fatalf("error = %v, want %s", err, nvkrerr.CodeTrustPolicyMissing)
		}
	})
}

func TestLoadTrustStoreFileRejectsNonRegularPaths(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		link := filepath.Join(dir, "trust.json")
		if err := os.WriteFile(target, []byte(`{"identities":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTrustStoreFile(context.Background(), link); cliErrorCode(err) != nvkrerr.CodeTrustPolicyMissing {
			t.Fatalf("error = %v, want %s", err, nvkrerr.CodeTrustPolicyMissing)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "trust.pipe")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadTrustStoreFile(context.Background(), path); cliErrorCode(err) != nvkrerr.CodeTrustPolicyMissing {
			t.Fatalf("error = %v, want %s", err, nvkrerr.CodeTrustPolicyMissing)
		}
	})
}

func TestLoadTrustStoreFileCanceledBeforePathInspection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadTrustStoreFile(ctx, path); cliErrorCode(err) != nvkrerr.CodeTimeout || !nvkrerr.Retryable(err) {
		t.Fatalf("error = %v, want retryable %s before path inspection", err, nvkrerr.CodeTimeout)
	}
}
