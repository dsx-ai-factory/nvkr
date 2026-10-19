// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundlearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func archiveFixture(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	zipped := gzip.NewWriter(&raw)
	writer := tar.NewWriter(zipped)
	for _, header := range headers {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestInspectExactInventory(t *testing.T) {
	archive := archiveFixture(t,
		tar.Header{Name: "z", Mode: 0o755, Typeflag: tar.TypeReg, Size: 3},
		tar.Header{Name: "manifests/", Mode: 0o755, Typeflag: tar.TypeDir},
		tar.Header{Name: "manifests/a.yaml", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1})
	entries, err := Inspect(t.Context(), bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Path != "manifests" || !entries[0].Directory ||
		entries[1].Path != "manifests/a.yaml" || entries[1].Mode != 0o644 || entries[1].Size != 1 ||
		entries[1].Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("x"))) || entries[2].Mode != 0o755 {
		t.Fatalf("unexpected inventory: %+v", entries)
	}
}

func TestInspectRejectsUnsafeArchives(t *testing.T) {
	file := tar.Header{Name: "entry", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1}
	for _, test := range []struct {
		name    string
		headers []tar.Header
	}{
		{"empty", nil},
		{"directory only", []tar.Header{{Name: "empty/", Mode: 0o755, Typeflag: tar.TypeDir}}},
		{"duplicate", []tar.Header{file, file}},
		{"traversal", []tar.Header{{Name: "../escape", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"absolute", []tar.Header{{Name: "/escape", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"unclean", []tar.Header{{Name: "./file", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"backslash", []tar.Header{{Name: "a\\file", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"drive", []tar.Header{{Name: "C:file", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"control", []tar.Header{{Name: "a\nfile", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"symlink", []tar.Header{{Name: "link", Linkname: "entry", Mode: 0o777, Typeflag: tar.TypeSymlink}}},
		{"hardlink", []tar.Header{{Name: "link", Linkname: "entry", Mode: 0o644, Typeflag: tar.TypeLink}}},
		{"fifo", []tar.Header{{Name: "fifo", Mode: 0o644, Typeflag: tar.TypeFifo}}},
		{"setuid", []tar.Header{{Name: "file", Mode: 0o4644, Typeflag: tar.TypeReg}}},
		{"file parent", []tar.Header{file, {Name: "entry/child", Mode: 0o644, Typeflag: tar.TypeReg}}},
		{"file parent after child", []tar.Header{{Name: "entry/child", Mode: 0o644, Typeflag: tar.TypeReg}, file}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Inspect(t.Context(), bytes.NewReader(archiveFixture(t, test.headers...)))
			if errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestInspectBoundsAndFraming(t *testing.T) {
	archive := archiveFixture(t, tar.Header{Name: "file", Mode: 0o644, Typeflag: tar.TypeReg, Size: 20})
	badChecksum := append([]byte(nil), archive...)
	badChecksum[len(badChecksum)-5] ^= 1
	for _, test := range []struct {
		name                 string
		raw                  []byte
		compressed, expanded int64
		count                int
	}{
		{"exact", archive, int64(len(archive)), 2048, 1},
		{"compressed bound", archive, int64(len(archive)) - 1, 2048, 1},
		{"expanded bound", archive, int64(len(archive)), 1024, 1},
		{"entry bound", archive, int64(len(archive)), 2048, 0},
		{"checksum", badChecksum, 4096, 4096, 10},
		{"truncated gzip", archive[:len(archive)-1], 4096, 4096, 10},
		{"second gzip", append(append([]byte(nil), archive...), archive...), 4096, 4096, 10},
		{"trailing bytes", append(append([]byte(nil), archive...), 0), 4096, 4096, 10},
		{"not gzip", []byte("tar"), 4096, 4096, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := inspect(t.Context(), bytes.NewReader(test.raw), test.compressed, test.expanded, test.count)
			if test.name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("got %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Inspect(ctx, bytes.NewReader(archive)); errorCode(err) != e.CodeTimeout {
		t.Fatalf("cancellation: %v", err)
	}
	var nilContext context.Context
	if _, err := Inspect(nilContext, bytes.NewReader(archive)); errorCode(err) != e.CodeUsage {
		t.Fatalf("nil context: %v", err)
	}
}

func errorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestInspectSparseAndTarTrailer(t *testing.T) {
	for _, test := range []struct {
		name    string
		records map[string]string
		trailer []byte
		valid   bool
	}{
		{"ordinary PAX", map[string]string{"comment": "bundle asset"}, nil, true},
		{"sparse PAX", map[string]string{"GNU.sparse.major": "0", "GNU.sparse.minor": "1", "GNU.sparse.realsize": "16777216", "GNU.sparse.numblocks": "1", "GNU.sparse.map": "16777216,0"}, nil, false},
		{"unknown sparse PAX", map[string]string{"GNU.sparse.major": "2"}, nil, false},
		{"file capabilities", map[string]string{"SCHILY.xattr.security.capability": "privileged"}, nil, false},
		{"ACL", map[string]string{"SCHILY.acl.access": "user::rwx"}, nil, false},
		{"unknown extension", map[string]string{"custom.unknown": "value"}, nil, false},
		{"zero padding", nil, make([]byte, 1024), true},
		{"nonzero trailer", nil, []byte("second archive"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded := map[string]string{}
			for key, value := range test.records {
				encoded[strings.Replace(key, "GNU.sparse.", "GNU.xparse.", 1)] = value
			}
			original := archiveFixture(t, tar.Header{Name: "file", Mode: 0o644, Typeflag: tar.TypeReg, PAXRecords: encoded, Format: tar.FormatPAX})
			zipped, err := gzip.NewReader(bytes.NewReader(original))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(zipped)
			if err != nil {
				t.Fatal(err)
			}
			_ = zipped.Close()
			raw = bytes.ReplaceAll(raw, []byte("GNU.xparse."), []byte("GNU.sparse."))
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(append(raw, test.trailer...)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(t.Context(), bytes.NewReader(compressed.Bytes()))
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("unsafe tar accepted: %v", err)
			}
		})
	}
}

func TestInspectRejectsOldGNUSparse(t *testing.T) {
	archive := archiveFixture(t, tar.Header{Name: "sparse", Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatGNU})
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	// The writer deliberately cannot emit sparse files. Change this empty GNU
	// regular record to an empty sparse record and repair its header checksum.
	raw[156] = tar.TypeGNUSparse
	copy(raw[148:156], []byte("        "))
	sum := 0
	for _, value := range raw[:512] {
		sum += int(value)
	}
	copy(raw[148:156], []byte(fmt.Sprintf("%06o\x00 ", sum)))
	header, err := tar.NewReader(bytes.NewReader(raw)).Next()
	if err != nil || header.Typeflag != tar.TypeGNUSparse {
		t.Fatalf("invalid GNU sparse fixture: %+v %v", header, err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Inspect(t.Context(), bytes.NewReader(compressed.Bytes()))
	if errorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("old GNU sparse admitted: %v", err)
	}
}
