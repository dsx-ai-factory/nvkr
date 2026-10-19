// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestStreamedChunksCloseInOrderWithoutOuterArchive(t *testing.T) {
	l, layers, _, manifest := chunkFixture(t)
	destination := t.TempDir()
	closed := make([]bool, len(layers))
	for i, layer := range layers {
		layers[i] = editedChunk{Layer: layer, open: func() (io.ReadCloser, error) {
			if i != 0 && !closed[i-1] {
				t.Fatal("next chunk opened before previous response closed")
			}
			err := filepath.WalkDir(destination, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Name() == "payload.tar" {
					t.Fatal("download staged the complete outer archive")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			source, err := layer.Compressed()
			if err != nil {
				return nil, err
			}
			return trackedChunkCloser{ReadCloser: source, closed: &closed[i]}, nil
		}}
	}
	if err := restoreLayers(t.Context(), layers, manifest, l, destination); err != nil {
		t.Fatal(err)
	}
	for i, done := range closed {
		if !done {
			t.Fatalf("chunk %d response was not closed", i)
		}
	}
	raw, err := os.ReadFile(filepath.Join(destination, "bin/tool"))
	if err != nil || string(raw) != strings.Repeat("binary bytes ", 180) {
		t.Fatalf("streamed carrier bytes differ: %v", err)
	}
}

type trackedChunkCloser struct {
	io.ReadCloser
	closed *bool
}

func (r trackedChunkCloser) Close() error {
	*r.closed = true
	return r.ReadCloser.Close()
}

func TestFinalChunkFailureCannotPublishExtractedFiles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		edit       func([]byte) []byte
		closeError bool
		want       e.Code
	}{
		{name: "digest", edit: func(raw []byte) []byte { raw[len(raw)-1] ^= 1; return raw }, want: e.CodeVerifyFailed},
		{name: "short", edit: func(raw []byte) []byte { return raw[:len(raw)-1] }, want: e.CodeVerifyFailed},
		{name: "extra", edit: func(raw []byte) []byte { return append(raw, 1) }, want: e.CodeVerifyFailed},
		{name: "close", edit: func(raw []byte) []byte { return raw }, closeError: true, want: e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, layers, _, manifest := chunkFixture(t)
			last := len(layers) - 1
			reader, err := layers[last].Compressed()
			if err != nil {
				t.Fatal(err)
			}
			raw, readErr := io.ReadAll(reader)
			if closeErr := reader.Close(); readErr != nil || closeErr != nil {
				t.Fatalf("reading fixture: %v %v", readErr, closeErr)
			}
			closed := false
			layers[last] = editedChunk{Layer: layers[last], open: func() (io.ReadCloser, error) {
				var closeErr error
				if tc.closeError {
					closeErr = errors.New("untrusted provider diagnostic")
				}
				return observedChunkReader{Reader: bytes.NewReader(tc.edit(raw)), closed: &closed, closeError: closeErr}, nil
			}}
			destination := t.TempDir()
			err = restoreLayers(t.Context(), layers, manifest, l, destination)
			assertCode(t, err, tc.want)
			if strings.Contains(err.Error(), "untrusted provider diagnostic") {
				t.Fatal("provider diagnostics leaked through stream errors")
			}
			if !closed {
				t.Fatal("last response was not closed after rejection")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed final chunk exposed extracted files: %v", err)
			}
		})
	}
}
