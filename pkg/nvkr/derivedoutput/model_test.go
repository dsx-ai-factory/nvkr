// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package derivedoutput

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestInventoryBuildIdentity(t *testing.T) {
	binding := Binding{Encoding: InventoryV1, RepositoryPrefix: "registry.example.com/nvke", Inputs: []Input{{Name: "chart", Kind: "helm-oci", BuildDigest: "sha256:" + strings.Repeat("a", 64)}}}
	original, err := binding.BuildDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		edit    func(*Binding)
		invalid bool
	}{
		{"destination", func(b *Binding) { b.RepositoryPrefix = "registry.example.com/other" }, false},
		{"named output", func(b *Binding) { b.Inputs[0].Name = "sibling" }, false},
		{"build", func(b *Binding) { b.Inputs[0].BuildDigest = "sha256:" + strings.Repeat("b", 64) }, false},
		{"unsupported kind", func(b *Binding) { b.Inputs[0].Kind = "bundle-oci" }, true},
		{"unknown encoding", func(b *Binding) { b.Encoding = "future" }, true},
		{"implicit registry", func(b *Binding) { b.RepositoryPrefix = "nvke" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := binding.Clone()
			tc.edit(copy)
			digest, err := copy.BuildDigest()
			if tc.invalid {
				var coded *e.Error
				if !errors.As(err, &coded) || (coded.Code != e.CodeVerifyFailed && coded.Code != e.CodeInvalidCatalog) {
					t.Fatalf("uncoded admission: %v", err)
				}
			} else if err != nil || digest == original {
				t.Fatalf("identity did not bind change: %v", err)
			}
		})
	}
	if again, err := binding.BuildDigest(); err != nil || again != original {
		t.Fatal("clone aliased original")
	}
}

func TestInventoryArchiveExactBytes(t *testing.T) {
	input := []byte("{\"apiVersion\":\"aicr.run/v1beta1\"}\n")
	first, err := InventoryArchive(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InventoryArchive(t.Context(), input)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("nondeterministic archive: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil || header.Name != "artifacts/inventory.yaml" || header.Mode != 0o644 || header.Typeflag != tar.TypeReg {
		t.Fatalf("unexpected inventory member: %#v %v", header, err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(raw, input) {
		t.Fatal("inventory bytes changed")
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("unexpected extra member: %v", err)
	}
}

func TestInventoryArchiveVerification(t *testing.T) {
	inventory := []byte("inventory bytes\n")
	archive, err := InventoryArchive(t.Context(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var alternate bytes.Buffer
	writer, err := gzip.NewWriterLevel(&alternate, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		raw, expected []byte
		invalid       bool
	}{
		{"exact", archive, inventory, false},
		{"different compressor", alternate.Bytes(), inventory, false},
		{"substituted document", archive, []byte("other"), true},
		{"truncated gzip", archive[:len(archive)-1], inventory, true},
		{"extra gzip", append(append([]byte(nil), archive...), archive...), inventory, true},
		{"extra bytes", append(append([]byte(nil), archive...), 1), inventory, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInventoryArchive(t.Context(), tc.raw, tc.expected)
			if !tc.invalid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("got %v", err)
			}
		})
	}
}
