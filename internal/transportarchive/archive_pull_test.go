// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type cancelingArchiveReader struct {
	reader *bytes.Reader
	cancel context.CancelFunc
	read   int
}

func (reader *cancelingArchiveReader) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	reader.read += n
	if reader.read >= 8192 {
		reader.cancel()
	}
	return n, err
}

func TestTransportTarSourceCancelsInsideHiddenHeaders(t *testing.T) {
	// tar.Next consumes GNU long-name records internally, so cancellation only
	// between visible entries would read every extension before returning.
	var record bytes.Buffer
	writer := tar.NewWriter(&record)
	if err := writer.WriteHeader(&tar.Header{Name: "extension", Size: 10, Mode: 0o644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "tree/file\x00"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	header := bytes.Clone(record.Bytes()[:1024])
	header[156] = tar.TypeGNULongName
	copy(header[148:156], "        ")
	var checksum int
	for _, value := range header[:512] {
		checksum += int(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelingArchiveReader{reader: bytes.NewReader(bytes.Repeat(header, 128)), cancel: cancel}
	source, closeSource, err := openTransportTarSource(ctx, reader, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSource()
	_, err = tar.NewReader(source).Next()
	if !errors.Is(err, context.Canceled) || reader.read > 8192 {
		t.Fatalf("hidden headers delayed cancellation: bytes=%d err=%v", reader.read, err)
	}
}

func TestUnpackConflictRemovesReadOnlyPrivateStage(t *testing.T) {
	for _, mode := range []int64{0o555, 0o500} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "carrier.tar")
			writeTestTar(t, archive, []*tar.Header{
				{Name: "tree", Typeflag: tar.TypeDir, Mode: mode},
				{Name: "tree/sub", Typeflag: tar.TypeDir, Mode: mode},
				{Name: "tree/sub/file", Typeflag: tar.TypeReg, Mode: 0o444, Size: 1},
			})
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tree"), 0o700); err != nil {
				t.Fatal(err)
			}
			err := Unpack(t.Context(), archive, root, []string{"tree"})
			if transportCode(err) != e.CodeImmutableViolation {
				t.Fatalf("destination conflict returned wrong code: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "tree" {
				t.Fatalf("private stage survived rollback: %v, %v", entries, err)
			}
		})
	}
}

type cancelingArchiveOpenReader struct {
	cancel context.CancelFunc
	magic  []byte
}

func (reader *cancelingArchiveOpenReader) Read(p []byte) (int, error) {
	if len(reader.magic) > 0 {
		n := copy(p, reader.magic)
		reader.magic = reader.magic[n:]
		return n, nil
	}
	reader.cancel()
	return 0, context.Canceled
}

func TestTransportChartDetectionReportsOpeningCancellation(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &cancelingArchiveOpenReader{cancel: cancel}
			if gzip {
				reader.magic = []byte{0x1f, 0x8b}
			}
			_, err := detectTransportChartRootFrom(ctx, reader, true)
			if transportCode(err) != e.CodeTimeout {
				t.Fatalf("opening cancellation returned %v, want timeout", err)
			}
		})
	}
}
