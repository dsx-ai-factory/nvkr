// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package helmarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInspectRejectsSparsePAXFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		records map[string]string
		sparse  bool
	}{
		{name: "ordinary PAX file", records: map[string]string{"comment": "regular chart asset"}},
		{name: "GNU sparse 0.1", sparse: true, records: map[string]string{
			"GNU.sparse.major": "0", "GNU.sparse.minor": "1", "GNU.sparse.realsize": "16777216",
			"GNU.sparse.numblocks": "1", "GNU.sparse.map": "16777216,0",
		}},
		{name: "GNU sparse implicit version", sparse: true, records: map[string]string{
			"GNU.sparse.size": "16777216", "GNU.sparse.numblocks": "1", "GNU.sparse.map": "16777216,0",
		}},
		{name: "unknown sparse extension", sparse: true, records: map[string]string{"GNU.sparse.major": "2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := chartWithPAXFile(t, test.records)
			identity, err := Inspect(t.Context(), archive)
			if !test.sparse {
				if err != nil || identity != (Identity{Name: "demo", Version: "1.2.3"}) {
					t.Fatalf("ordinary PAX chart rejected: %+v, %v", identity, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || identity != (Identity{}) {
				t.Fatalf("sparse chart returned identity=%+v, error=%v", identity, err)
			}
		})
	}
}

func chartWithPAXFile(t *testing.T, records map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	chart := []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n")
	if err := writer.WriteHeader(&tar.Header{Name: "demo/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(chart))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(chart); err != nil {
		t.Fatal(err)
	}
	// Go deliberately does not write sparse entries. Emit equal-length neutral
	// PAX keys, then restore their GNU spellings in the extension's data block.
	// PAX record lengths and tar header checksums remain unchanged.
	encoded := make(map[string]string, len(records))
	for key, value := range records {
		encoded[strings.Replace(key, "GNU.sparse.", "GNU.xparse.", 1)] = value
	}
	if err := writer.WriteHeader(&tar.Header{Name: "demo/asset", Typeflag: tar.TypeReg, Mode: 0o644, PAXRecords: encoded, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := bytes.ReplaceAll(raw.Bytes(), []byte("GNU.xparse."), []byte("GNU.sparse."))
	// Confirm the fixture is a readable PAX regular file, including the sparse
	// map case whose tiny physical record represents a large logical body.
	reader := tar.NewReader(bytes.NewReader(archive))
	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}
	header, err := reader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || len(header.PAXRecords) != len(records) {
		t.Fatalf("invalid PAX fixture: %+v, %v", header, err)
	}
	if records["GNU.sparse.map"] != "" && header.Size != 16<<20 {
		t.Fatalf("fixture lost sparse logical extent: %d", header.Size)
	}
	var compressed bytes.Buffer
	zipped := gzip.NewWriter(&compressed)
	if _, err := zipped.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
