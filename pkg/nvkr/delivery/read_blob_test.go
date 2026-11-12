// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestReadBlobMemorySnapshot(t *testing.T) {
	for _, kind := range []string{"valid", "corrupt", "size", "bound", "symlink", "fifo", "directory", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "blobs", "sha256")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			raw := []byte("bounded metadata")
			descriptor := Descriptor{Digest: digestBytes(raw), Size: int64(len(raw))}
			path := filepath.Join(dir, strings.TrimPrefix(descriptor.Digest, "sha256:"))
			switch kind {
			case "symlink":
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				data := bytes.Clone(raw)
				if kind == "corrupt" {
					data[0] ^= 1
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bound := int64(100)
			if kind == "size" {
				descriptor.Size++
			}
			if kind == "bound" {
				bound = 1
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			got, err := readBlob(ctx, root, descriptor, bound)
			if kind == "valid" {
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatalf("snapshot=%q error=%v", got, err)
				}
				if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, raw) {
					t.Fatal("snapshot followed later file mutation")
				}
			} else {
				code := e.CodeVerifyFailed
				if kind == "canceled" {
					code = e.CodeTimeout
				}
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != code || got != nil {
					t.Fatalf("snapshot=%q error=%v, want %s", got, err, code)
				}
			}
		})
	}
}

func TestMetadataReadSessionRechecksAcrossVisitsAndOwnsBytes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("verified metadata")
	descriptor := Descriptor{Digest: digestBytes(raw), Size: int64(len(raw))}
	path := filepath.Join(dir, strings.TrimPrefix(descriptor.Digest, "sha256:"))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := withMetadataReads(t.Context())
	first, err := readBlob(ctx, root, descriptor, 100)
	if err != nil {
		t.Fatal(err)
	}
	first[0] ^= 1
	again, err := readBlob(ctx, root, descriptor, 100)
	if err != nil || !bytes.Equal(again, raw) {
		t.Fatalf("memo aliased caller: %q %v", again, err)
	}
	wrongSize := descriptor
	wrongSize.Size++
	if _, err := readBlob(ctx, root, wrongSize, 100); err == nil {
		t.Fatal("memo bypassed descriptor size")
	}
	if _, err := readBlob(ctx, root, descriptor, 1); err == nil {
		t.Fatal("memo bypassed caller bound")
	}
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBlob(withMetadataReads(t.Context()), root, descriptor, 100); err == nil {
		t.Fatal("new visit trusted previous verified bytes")
	}
	reads, _ := ctx.Value(metadataReadsKey{}).(*metadataReads)
	reads.bytes = maxMetadataReadMemo
	reads.put("over-budget", raw)
	if _, ok := reads.get("over-budget"); ok {
		t.Fatal("memo exceeded memory bound")
	}
}
