// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVirtualUploadMatchesExistingTransportBytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		length int
		mode   os.FileMode
	}{
		{"empty", "empty", 0, 0o600},
		{"partial block", ".nvkr/carriers/prepared.tar", 511, 0o644},
		{"exact block", "nested/payload", 512, 0o751},
		{"next block", "nested/payload", 513, 0o400},
		{"PAX long name", strings.Repeat("nested/", 22) + "payload.tar", 2049, 0o755},
		{"PAX unicode name", "nested/载体.tar", 128*1024 + 19, 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			payload := bytes.Repeat([]byte("x"), test.length)
			writePayload(t, root, test.path, string(payload), test.mode)
			original := filepath.Join(root, "original.tar")
			if err := transportarchive.Pack(t.Context(), original, root, []string{test.path}); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := openUploadArchive(t.Context(), root, []string{test.path})
			if err != nil {
				t.Fatal(err)
			}
			defer archive.close()
			if !archive.virtual || archive.temporary != "" || archive.size != int64(len(want)) {
				t.Fatal("single-file upload created a staging copy or changed the archive size")
			}
			actual, err := io.ReadAll(io.NewSectionReader(archive.reader, 0, archive.size))
			if err != nil || !bytes.Equal(actual, want) {
				t.Fatalf("virtual stream differs from existing v2 TAR bytes: %v", err)
			}
			// Registry chunks and retries reopen arbitrary ranges, including
			// framing/payload boundaries and reads across the final footer.
			for _, offset := range []int64{0, 1, 511, 512, archive.size - 1, archive.size} {
				buffer := make([]byte, 777)
				n, err := archive.reader.ReadAt(buffer, offset)
				length := min(int64(len(buffer)), archive.size-offset)
				if int64(n) != length || !bytes.Equal(buffer[:n], want[offset:offset+length]) ||
					length < int64(len(buffer)) && err != io.EOF || length == int64(len(buffer)) && err != nil {
					t.Fatalf("range at %d changed TAR bytes: n=%d err=%v", offset, n, err)
				}
			}
		})
	}
}

func TestUploadArchiveDoesNotStageSingleFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		paths   []string
		virtual bool
	}{
		{"existing carrier", []string{"payload/carrier.tar"}, true},
		{"directory fallback", []string{"payload"}, false},
		{"multiple-root fallback", []string{"payload/carrier.tar", "other"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, "payload/carrier.tar", "carrier bytes", 0o644)
			writePayload(t, root, "other", "other bytes", 0o600)
			before, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := openUploadArchive(t.Context(), root, test.paths)
			if err != nil {
				t.Fatal(err)
			}
			if archive.virtual != test.virtual || (archive.temporary == "") != test.virtual {
				t.Fatal("upload selected the wrong staging route")
			}
			during, err := os.ReadDir(root)
			if err != nil || test.virtual && !reflect.DeepEqual(before, during) {
				t.Fatal("virtual upload wrote to the workspace")
			}
			_, digest, err := chunkArchive(t.Context(), archive.reader, archive.size, maxChunkBytes)
			if err != nil || archive.verify(t.Context(), digest) != nil {
				t.Fatalf("stable upload verification failed: %v", err)
			}
			archive.close()
			after, err := os.ReadDir(root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("upload retained staging or removed the source")
			}
		})
	}
}

func TestVirtualUploadRejectsMutationEvenWhenMetadataIsRestored(t *testing.T) {
	for _, mutation := range []string{"same metadata", "truncate", "grow", "mode", "canceled"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, "carrier.tar", "original payload", 0o644)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			archive, err := openUploadArchive(ctx, root, []string{"carrier.tar"})
			if err != nil {
				t.Fatal(err)
			}
			defer archive.close()
			_, digest, err := chunkArchive(ctx, archive.reader, archive.size, maxChunkBytes)
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(root, "carrier.tar")
			want := e.CodeVerifyFailed
			switch mutation {
			case "same metadata":
				if err := os.WriteFile(filename, []byte("modified payload"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filename, time.Now(), archive.info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := archive.unchanged(ctx); err != nil {
					t.Fatalf("fixture did not restore original metadata: %v", err)
				}
			case "truncate":
				if err := os.Truncate(filename, 1); err != nil {
					t.Fatal(err)
				}
			case "grow":
				if err := os.Truncate(filename, archive.info.Size()+1); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filename, 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
				want = e.CodeTimeout
			}
			assertCode(t, archive.verify(ctx, digest), want)
		})
	}
}

func TestVirtualUploadHoldsTheSelectedInode(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "carrier", "original payload", 0o644)
	archive, err := openUploadArchive(t.Context(), root, []string{"carrier"})
	if err != nil {
		t.Fatal(err)
	}
	defer archive.close()
	_, digest, err := chunkArchive(t.Context(), archive.reader, archive.size, maxChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "carrier"), filepath.Join(root, "retained")); err != nil {
		t.Fatal(err)
	}
	writePayload(t, root, "carrier", "replacement payload", 0o644)
	if err := archive.verify(t.Context(), digest); err != nil {
		t.Fatalf("pathname replacement redirected the held source: %v", err)
	}
}

func TestVirtualUploadRejectsUnsafeParentsAndTruncatedReads(t *testing.T) {
	for _, test := range []string{"parent symlink", "member symlink", "truncated read"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, "nested/carrier", "original payload", 0o644)
			switch test {
			case "parent symlink":
				if err := os.Symlink("nested", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				_, err := openUploadArchive(t.Context(), root, []string{"link/carrier"})
				assertCode(t, err, e.CodeVerifyFailed)
			case "member symlink":
				if err := os.Symlink("carrier", filepath.Join(root, "nested/link")); err != nil {
					t.Fatal(err)
				}
				_, err := openUploadArchive(t.Context(), root, []string{"nested/link"})
				assertCode(t, err, e.CodeVerifyFailed)
			case "truncated read":
				archive, err := openUploadArchive(t.Context(), root, []string{"nested/carrier"})
				if err != nil {
					t.Fatal(err)
				}
				defer archive.close()
				if err := os.Truncate(filepath.Join(root, "nested/carrier"), 1); err != nil {
					t.Fatal(err)
				}
				_, _, err = chunkArchive(t.Context(), archive.reader, archive.size, maxChunkBytes)
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestVirtualUploadRehashesAfterRegistryDeduplication(t *testing.T) {
	root := t.TempDir()
	writePayload(t, root, "carrier", "original payload", 0o644)
	filename := filepath.Join(root, "carrier")
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	f := newStorageFixture(t, func(check int) (int, any) {
		// First upload seeds the exact chunks. On the second upload, change
		// bytes after hashing but before registry deduplication can skip them.
		if check == 4 {
			if err := os.WriteFile(filename, []byte("modified payload"), 0o644); err != nil {
				t.Error(err)
			}
			if err := os.Chtimes(filename, time.Now(), info.ModTime()); err != nil {
				t.Error(err)
			}
		}
		return http.StatusOK, privatePackage()
	})
	if _, err := f.b.upload(t.Context(), testRequest, root, []string{"carrier"}, testOptions); err != nil {
		t.Fatal(err)
	}
	locator, err := f.b.upload(t.Context(), testRequest, root, []string{"carrier"}, testOptions)
	assertCode(t, err, e.CodeVerifyFailed)
	if locator.Reference != "" || f.gets != 4 {
		t.Fatal("deduplicated upload returned a locator without revalidating the held bytes")
	}
}

func TestVirtualUploadHeaderAndAggregateBounds(t *testing.T) {
	var buffer virtualHeaderBuffer
	if _, err := buffer.Write(make([]byte, maxVirtualUploadHeaderBytes)); err != nil {
		t.Fatal(err)
	}
	_, err := buffer.Write([]byte{1})
	assertCode(t, err, e.CodeVerifyFailed)
	if buffer.Len() != maxVirtualUploadHeaderBytes {
		t.Fatal("header retained bytes past its bound")
	}
	root := t.TempDir()
	writePayload(t, root, "carrier", "", 0o644)
	file, info, err := holdSingleUpload(t.Context(), root, "carrier")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, size := range []int64{-1, transportarchive.MaxArchiveBytes, transportarchive.MaxArchiveBytes + 1} {
		_, err := newVirtualUpload(t.Context(), file, uploadSizedInfo{FileInfo: info, size: size}, "carrier")
		assertCode(t, err, e.CodeVerifyFailed)
	}
	// PAX encodes large sizes without allocating or reading that payload.
	virtual, err := newVirtualUpload(t.Context(), file, uploadSizedInfo{FileInfo: info, size: 9 << 30}, "carrier")
	if err != nil {
		t.Fatal(err)
	}
	header, err := tar.NewReader(bytes.NewReader(virtual.header)).Next()
	if err != nil || header.Size != 9<<30 || header.Name != "carrier" {
		t.Fatalf("large PAX member header = %+v, %v", header, err)
	}
}

type uploadSizedInfo struct {
	os.FileInfo
	size int64
}

func (info uploadSizedInfo) Size() int64 { return info.size }
