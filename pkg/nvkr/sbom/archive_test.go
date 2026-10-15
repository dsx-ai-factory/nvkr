// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func contentArchive(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, header := range headers {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(writer, archiveZeroReader{}, header.Size); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type archiveZeroReader struct{}

func (archiveZeroReader) Read(raw []byte) (int, error) { clear(raw); return len(raw), nil }

func compressedContentArchive(t *testing.T, raw []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := gzip.NewWriter(&result)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestArchiveContentInspectionBindsMemberBytesWithoutPackageClaims(t *testing.T) {
	raw := helmTestArchive(t, map[string]string{"ui/app.js": "compiled asset", "internal/seed/default-seed.yaml": "seed: true\n"})
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			body := slices.Clone(raw)
			if compressed {
				body = compressedContentArchive(t, body)
			}
			filename := filepath.Join(t.TempDir(), "arbitrary-name")
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
			inventory, err := InspectDependencies(t.Context(), filename, digest, "file-archive", Scanner{})
			if err != nil || inventory.Method != archiveContentMethod || inventory.ScannerDigest != "" || len(inventory.Components) != 2 {
				t.Fatalf("inventory=%+v, error=%v", inventory, err)
			}
			if inventory.Components[1].Name != "ui/app.js" || inventory.Components[1].Hashes[0].Content != fmt.Sprintf("%x", sha256.Sum256([]byte("compiled asset"))) {
				t.Fatalf("wrong exact member inventory: %+v", inventory)
			}
			base := archiveContentSubject(t)
			base.Metadata.Component.Hashes[0].Content = strings.TrimPrefix(digest, "sha256:")
			bom, err := WithDependencies(base, inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := bom.Marshal()
			if err != nil || bom.Metadata.Component.Type != "file" || !reflect.DeepEqual(bom.Metadata.Component.Hashes, base.Metadata.Component.Hashes) || len(encoded) == 0 {
				t.Fatalf("subject binding changed or invalid: %+v, %v", bom, err)
			}
			_, err = InspectDependencies(t.Context(), filename, digest, "tarball", Scanner{})
			assertDependencyCode(t, err, e.CodeProvenanceMissing) // No generic fallback from strict Helm.
			body[len(body)/2] ^= 1
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = InspectDependencies(t.Context(), filename, digest, "file-archive", Scanner{})
			assertDependencyCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestArchiveContentInspectionRejectsAmbiguousMembers(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []tar.Header
		code    e.Code
	}{
		{"empty", nil, e.CodeProvenanceMissing},
		{"directories only", []tar.Header{{Name: "ui/", Typeflag: tar.TypeDir}}, e.CodeProvenanceMissing},
		{"duplicate file", []tar.Header{{Name: "a"}, {Name: "a"}}, e.CodeVerifyFailed},
		{"duplicate directory", []tar.Header{{Name: "ui/", Typeflag: tar.TypeDir}, {Name: "ui/", Typeflag: tar.TypeDir}}, e.CodeVerifyFailed},
		{"file parent with intervening name", []tar.Header{{Name: "a"}, {Name: "a-b"}, {Name: "a/x"}}, e.CodeVerifyFailed},
		{"reverse file parent", []tar.Header{{Name: "a/x"}, {Name: "a"}}, e.CodeVerifyFailed},
		{"directory file collision", []tar.Header{{Name: "a/", Typeflag: tar.TypeDir}, {Name: "a"}}, e.CodeVerifyFailed},
		{"parent traversal", []tar.Header{{Name: "../escape"}}, e.CodeVerifyFailed},
		{"noncanonical path", []tar.Header{{Name: "a/../escape"}}, e.CodeVerifyFailed},
		{"absolute path", []tar.Header{{Name: "/escape"}}, e.CodeVerifyFailed},
		{"backslash", []tar.Header{{Name: `a\escape`}}, e.CodeVerifyFailed},
		{"control character", []tar.Header{{Name: "a\npath"}}, e.CodeVerifyFailed},
		{"oversized path", []tar.Header{{Name: strings.Repeat("a", 4097)}}, e.CodeVerifyFailed},
		{"symlink", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/outside"}}, e.CodeVerifyFailed},
		{"hardlink", []tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "outside"}}, e.CodeVerifyFailed},
		{"fifo", []tar.Header{{Name: "pipe", Typeflag: tar.TypeFifo}}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			components, err := inspectArchiveContents(t.Context(), bytes.NewReader(contentArchive(t, test.headers...)))
			assertDependencyCode(t, err, test.code)
			if components != nil {
				t.Fatal("rejected archive returned an inventory")
			}
		})
	}
}

func TestArchiveContentInspectionBoundsAndAuthenticatesCompleteEncoding(t *testing.T) {
	raw := contentArchive(t, tar.Header{Name: "asset", Size: 3})
	badChecksum := compressedContentArchive(t, raw)
	badChecksum[len(badChecksum)-8] ^= 1
	var oversized bytes.Buffer
	writer := tar.NewWriter(&oversized)
	if err := writer.WriteHeader(&tar.Header{Name: "too-large", Size: maxArchiveMember + 1}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		source io.Reader
	}{
		{"truncated member", bytes.NewReader(raw[:514])},
		{"gzip checksum", bytes.NewReader(badChecksum)},
		{"gzip trailer", bytes.NewReader(compressedContentArchive(t, raw)[:15])},
		{"nonzero tail", bytes.NewReader(append(slices.Clone(raw), 1))},
		{"gzip raw garbage", bytes.NewReader(append(compressedContentArchive(t, raw), 1))},
		{"member size", bytes.NewReader(oversized.Bytes())},
		{"expanded padding", io.MultiReader(bytes.NewReader(raw), io.LimitReader(archiveZeroReader{}, maxArchiveExpanded))},
	} {
		t.Run(test.name, func(t *testing.T) {
			components, err := inspectArchiveContents(t.Context(), test.source)
			assertDependencyCode(t, err, e.CodeVerifyFailed)
			if components != nil {
				t.Fatal("rejected archive returned an inventory")
			}
		})
	}
	var records []tar.Header
	for index := 0; index <= maxArchiveRecords; index++ {
		records = append(records, tar.Header{Name: fmt.Sprintf("asset-%d", index)})
	}
	_, err := inspectArchiveContents(t.Context(), bytes.NewReader(contentArchive(t, records...)))
	assertDependencyCode(t, err, e.CodeVerifyFailed)
}

func TestArchiveContentInspectionRejectsSparsePAX(t *testing.T) {
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "asset", Format: tar.FormatPAX, PAXRecords: map[string]string{"GNU.xparse.major": "2"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Match the existing internal/helmarchive sparse fixture technique: the
	// standard writer omits GNU sparse keys, so restore equal-length PAX keys.
	encoded := bytes.ReplaceAll(raw.Bytes(), []byte("GNU.xparse."), []byte("GNU.sparse."))
	header, err := tar.NewReader(bytes.NewReader(encoded)).Next()
	if err != nil || header.PAXRecords["GNU.sparse.major"] != "2" {
		t.Fatalf("invalid sparse fixture: %+v, %v", header, err)
	}
	_, err = inspectArchiveContents(t.Context(), bytes.NewReader(encoded))
	assertDependencyCode(t, err, e.CodeVerifyFailed)
}

func TestArchiveContentInspectionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	raw := contentArchive(t, tar.Header{Name: "asset", Size: 32768})
	_, err := inspectArchiveContents(ctx, &cancelProbeReader{reader: bytes.NewReader(raw), cancel: cancel})
	assertDependencyCode(t, err, e.CodeTimeout)
}

func TestArchiveContentInspectionCancellationDuringGzipHeader(t *testing.T) {
	var raw bytes.Buffer
	writer := gzip.NewWriter(&raw)
	// Force header decoding to read beyond bufio's initial compressed input.
	writer.Extra = make([]byte, 8192)
	if _, err := writer.Write(contentArchive(t, tar.Header{Name: "asset", Size: 1})); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := inspectArchiveContents(ctx, &cancelProbeReader{reader: bytes.NewReader(raw.Bytes()), cancel: cancel})
	assertDependencyCode(t, err, e.CodeTimeout)
}

func archiveContentSubject(t *testing.T) *BOM {
	t.Helper()
	bom := dependencySubject(t)
	for index := range bom.Metadata.Component.Properties {
		if bom.Metadata.Component.Properties[index].Name == "nvkr:outputType" {
			bom.Metadata.Component.Properties[index].Value = "tarball"
		}
	}
	return bom
}

func TestArchiveContentSerializedSemanticsFailClosed(t *testing.T) {
	component := Component{Type: "file", Name: "ui/app.js", Hashes: []Hash{{Alg: "SHA-256", Content: strings.Repeat("c", 64)}}}
	for _, test := range []struct {
		name   string
		change func(*BOM)
	}{
		{"subject type", func(b *BOM) { b.Metadata.Component.Type = "application" }},
		{"subject format", func(b *BOM) {
			for i := range b.Metadata.Component.Properties {
				if b.Metadata.Component.Properties[i].Name == "nvkr:outputType" {
					b.Metadata.Component.Properties[i].Value = "host-package"
				}
			}
		}},
		{"scanner claim", func(b *BOM) {
			b.Metadata.Props = append(b.Metadata.Props, Property{Name: "nvkr:dependencyScannerDigest", Value: "sha256:" + strings.Repeat("d", 64)})
		}},
		{"package claim", func(b *BOM) { b.Components[0].Type = "library" }},
		{"package URL", func(b *BOM) { b.Components[0].PURL = "pkg:npm/imagined@1" }},
		{"version", func(b *BOM) { b.Components[0].Version = "1" }},
		{"package property", func(b *BOM) { b.Components[0].Properties = []Property{{Name: "nvkr:package", Value: "invented"}} }},
		{"unsafe member", func(b *BOM) { b.Components[0].Name = "../outside" }},
		{"file parent", func(b *BOM) {
			b.Components[0].Name = "a"
			b.Components = append(b.Components, Component{Type: "file", Name: "a/x", Hashes: []Hash{{Alg: "SHA-256", Content: strings.Repeat("e", 64)}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bom, err := WithDependencies(archiveContentSubject(t), []Component{component}, archiveContentMethod, "")
			if err != nil {
				t.Fatal(err)
			}
			test.change(bom)
			slices.SortFunc(bom.Metadata.Props, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
			assertDependencyCode(t, bom.Validate(), e.CodeVerifyFailed)
		})
	}
	_, err := WithDependencies(archiveContentSubject(t), []Component{component}, archiveContentMethod, "sha256:"+strings.Repeat("d", 64))
	assertDependencyCode(t, err, e.CodeVerifyFailed)
}

// Opt-in evidence from a real pinned-source asset archive, kept outside the
// repository. No package discovery, external workflow or signing claim follows.
func TestRealArchiveContentInspection(t *testing.T) {
	filename := os.Getenv("NVKR_SBOM_ASSET_ARCHIVE")
	if filename == "" {
		t.Skip("requires a real pinned-source runtime asset archive")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	inventory, err := InspectDependencies(t.Context(), filename, digest, "file-archive", Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, component := range inventory.Components {
		names[component.Name] = true
	}
	for _, name := range []string{"internal/seed/default-seed.yaml", "ui/app.js", "ui/vendor/THIRD_PARTY_NOTICES.md", "ui/vendor/xterm/xterm.min.js", "ui/vendor/highlightjs/highlight.min.js"} {
		if !names[name] {
			t.Fatalf("real asset inventory omitted %s", name)
		}
	}
	base := archiveContentSubject(t)
	base.Metadata.Component.Hashes[0].Content = strings.TrimPrefix(digest, "sha256:")
	bom, err := WithDependencies(base, inventory.Components, inventory.Method, inventory.ScannerDigest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("subject=%s regular-members=%d complete-document-bytes=%d method=%s", digest, len(inventory.Components), len(encoded), inventory.Method)
}
