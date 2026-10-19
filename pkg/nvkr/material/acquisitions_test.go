// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func acquisitionFixture(architecture string) (AcquisitionManifest, []byte) {
	payload := []byte("fixture archive bytes; not a Debian package")
	hash := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	return AcquisitionManifest{SchemaVersion: AcquisitionSchemaVersion, Architecture: architecture, Scope: "apt-pre-install-pkgs",
		Packages: []AcquiredPackage{{Name: "fixture", Version: "1:2.0-1", Architecture: architecture, Digest: digest,
			Size: int64(len(payload)), Path: "packages/" + hex.EncodeToString(hash[:]) + ".deb",
			RepositoryURIs: []string{"https://archive.example.invalid/pool/fixture.deb"}}}}, payload
}

func acquisitionTar(t *testing.T, manifest AcquisitionManifest, payload []byte, mutate func(*tar.Header), extra bool) []byte {
	t.Helper()
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return acquisitionTarWithManifestJSON(t, manifest, raw, payload, mutate, extra)
}

func acquisitionTarWithManifestJSON(t *testing.T, manifest AcquisitionManifest, raw, payload []byte, mutate func(*tar.Header), extra bool) []byte {
	t.Helper()
	return acquisitionTarWithHeaders(t, manifest, raw, payload, nil, mutate, extra)
}

func acquisitionTarWithHeaders(t *testing.T, manifest AcquisitionManifest, raw, payload []byte, manifestHeader, packageHeader func(*tar.Header), extra bool) []byte {
	t.Helper()
	var destination bytes.Buffer
	writer := tar.NewWriter(&destination)
	write := func(name string, content []byte, mutate func(*tar.Header)) {
		header := &tar.Header{Name: name, Mode: 0o444, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if mutate != nil {
			mutate(header)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("manifest.json", raw, manifestHeader)
	if payload != nil {
		write(manifest.Packages[0].Path, payload, packageHeader)
	}
	if extra {
		write("extra.deb", []byte("undeclared"), nil)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return destination.Bytes()
}

func holdAcquisition(t *testing.T, raw []byte) *heldfile.File {
	t.Helper()
	name := filepath.Join(t.TempDir(), "capture.tar")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := heldfile.OpenContext(context.Background(), name, heldfile.Options{Label: "capture", MaxBytes: MaxAcquisitionArchiveBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return held
}

func TestObserveAcquisitionArchiveChecksBytesWithoutClaimingPackageMetadata(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			manifest, payload := acquisitionFixture(architecture)
			held := holdAcquisition(t, acquisitionTar(t, manifest, payload, nil, false))
			observation, err := ObserveAcquisitionArchive(context.Background(), held)
			if err != nil {
				t.Fatal(err)
			}
			digest, size, document := observation.Binding()
			if digest != held.Digest() || size != held.Size() || !canonical.IsDigest(document) {
				t.Fatalf("observation has wrong exact-byte binding: %s %d %s", digest, size, document)
			}
			copy := observation.Description()
			copy.Packages[0].Name = "changed"
			copy.Packages[0].RepositoryURIs[0] = "https://changed.invalid/"
			if observation.Description().Packages[0].Name != "fixture" || observation.Description().Packages[0].RepositoryURIs[0] != manifest.Packages[0].RepositoryURIs[0] {
				t.Fatal("description mutation changed observation")
			}
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			if got := errorCode(func() error { _, err := ObserveAcquisitionArchive(context.Background(), held); return err }()); got != e.CodeVerifyFailed {
				t.Fatalf("closed archive error = %s", got)
			}
		})
	}
}

func TestObserveAcquisitionArchiveRejectsBadCaptures(t *testing.T) {
	reasons := map[string]string{
		"wrong schema":                  "unsupported package acquisition schema",
		"wrong scope":                   "unsupported package acquisition scope",
		"wrong architecture":            "unsupported package acquisition architecture",
		"package architecture mismatch": "package architecture differs",
		"unsafe path":                   "path is not digest-derived", "metadata control": "invalid display metadata",
		"missing URI":    "requires bounded repository metadata locations",
		"credential URI": "contains credentials", "query URI": "contains a query",
		"duplicate record":  "digests must be strictly sorted and unique",
		"missing package":   "reading held package acquisition archive",
		"substituted bytes": "differs from its manifest digest",
		"wrong path":        "member path differs", "symbolic link": "requires a regular USTAR member",
		"extra package": "extra or malformed member", "hidden trailing bytes": "nonzero trailing data",
	}
	for _, test := range []struct {
		name     string
		mutate   func(*AcquisitionManifest)
		header   func(*tar.Header)
		payload  func([]byte) []byte
		extra    bool
		trailing bool
	}{
		{name: "wrong schema", mutate: func(m *AcquisitionManifest) { m.SchemaVersion = "other" }},
		{name: "wrong scope", mutate: func(m *AcquisitionManifest) { m.Scope = "complete-disk" }},
		{name: "wrong architecture", mutate: func(m *AcquisitionManifest) { m.Architecture = "ppc64" }},
		{name: "package architecture mismatch", mutate: func(m *AcquisitionManifest) { m.Packages[0].Architecture = "arm64" }},
		{name: "unsafe path", mutate: func(m *AcquisitionManifest) { m.Packages[0].Path = "../package.deb" }},
		{name: "metadata control", mutate: func(m *AcquisitionManifest) { m.Packages[0].Name = "a\nb" }},
		{name: "missing URI", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = nil }},
		{name: "credential URI", mutate: func(m *AcquisitionManifest) {
			m.Packages[0].RepositoryURIs[0] = "https://user:password@example.invalid/a"
		}},
		{name: "query URI", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs[0] += "?token=secret" }},
		{name: "duplicate record", mutate: func(m *AcquisitionManifest) { m.Packages = append(m.Packages, m.Packages[0]) }},
		{name: "missing package", payload: func([]byte) []byte { return nil }},
		{name: "substituted bytes", payload: func(b []byte) []byte { b[0] ^= 1; return b }},
		{name: "wrong path", header: func(h *tar.Header) { h.Name = "packages/wrong.deb" }},
		{name: "symbolic link", header: func(h *tar.Header) { h.Typeflag = tar.TypeSymlink; h.Linkname = "/etc/passwd"; h.Size = 0 }},
		{name: "extra package", extra: true},
		{name: "hidden trailing bytes", trailing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, payload := acquisitionFixture("amd64")
			if test.mutate != nil {
				test.mutate(&manifest)
			}
			if test.payload != nil {
				payload = test.payload(payload)
			}
			raw := acquisitionTar(t, manifest, payload, test.header, test.extra)
			if test.trailing {
				raw = append(raw, []byte("hidden")...)
			}
			got, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, raw))
			assertAcquisitionFailure(t, got, err, reasons[test.name])
		})
	}
}

func TestObserveAcquisitionArchiveAcceptsZeroRecordPadding(t *testing.T) {
	manifest, payload := acquisitionFixture("amd64")
	document, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(document)
	for _, test := range []struct {
		name string
		size int
	}{{name: "one record", size: 10240}, {name: "two records", size: 20480}} {
		t.Run(test.name, func(t *testing.T) {
			raw := acquisitionTar(t, manifest, payload, nil, false)
			// Python tarfile's record padding follows the two-block terminator.
			raw = append(raw, make([]byte, test.size-len(raw))...)
			archiveHash := sha256.Sum256(raw)
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, raw))
			if err != nil || observation == nil {
				t.Fatalf("zero record padding rejected: observation=%v error=%v", observation, err)
			}
			digest, size, document := observation.Binding()
			if digest != "sha256:"+hex.EncodeToString(archiveHash[:]) || size != int64(len(raw)) || document != "sha256:"+hex.EncodeToString(manifestHash[:]) {
				t.Fatalf("padded capture has wrong exact-byte binding: %s %d %s", digest, size, document)
			}
		})
	}
}

func TestObserveAcquisitionArchiveCancellationAndStrictJSON(t *testing.T) {
	manifest, payload := acquisitionFixture("amd64")
	raw := acquisitionTar(t, manifest, payload, nil, false)
	held := holdAcquisition(t, raw)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if observation, err := ObserveAcquisitionArchive(ctx, held); observation != nil || errorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled observation = %v, error = %v", observation, err)
	}
	var missingContext context.Context
	if observation, err := ObserveAcquisitionArchive(missingContext, held); observation != nil || errorCode(err) != e.CodeUsage {
		t.Fatalf("nil context observation = %v, error = %v", observation, err)
	}
	canonicalDocument, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	document := string(canonicalDocument)
	for _, test := range []struct {
		name     string
		document string
		valid    bool
		message  string
	}{
		{name: "canonical complete capture", document: document, valid: true},
		{name: "unsupported producer schema", document: strings.Replace(document, AcquisitionSchemaVersion, "unsupported.dev/package-acquisitions/v1", 1), message: "unsupported package acquisition schema"},
		{name: "duplicate field", document: strings.Replace(document, `"schemaVersion":`, `"schemaVersion":"`+AcquisitionSchemaVersion+`","schemaVersion":`, 1), message: "decoding package acquisition manifest"},
		{name: "unknown field", document: strings.TrimSuffix(document, "}") + `,"unknown":true}`, message: "decoding package acquisition manifest"},
		{name: "leading whitespace", document: " " + document, message: "decoding package acquisition manifest"},
		{name: "malformed JSON", document: strings.Repeat("x", 100), message: "decoding package acquisition manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := acquisitionTarWithManifestJSON(t, manifest, []byte(test.document), payload, nil, false)
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, capture))
			if test.valid {
				if observation == nil || err != nil {
					t.Fatalf("canonical complete capture rejected: observation=%v error=%v", observation, err)
				}
			} else {
				assertAcquisitionFailure(t, observation, err, test.message)
			}
		})
	}
}

func TestObserveAcquisitionArchiveRequiresExactOrdering(t *testing.T) {
	manifest, first := acquisitionFixture("amd64")
	second := []byte("another captured package")
	hash := sha256.Sum256(second)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	entry := manifest.Packages[0]
	entry.Name, entry.Digest, entry.Size, entry.Path = "second", digest, int64(len(second)), "packages/"+hex.EncodeToString(hash[:])+".deb"
	manifest.Packages = append(manifest.Packages, entry)
	slices.SortFunc(manifest.Packages, func(a, b AcquiredPackage) int { return strings.Compare(a.Digest, b.Digest) })
	payloads := map[string][]byte{manifest.Packages[0].Digest: first, digest: second}
	// Match each fixture payload by content rather than relying on digest order.
	firstHash := sha256.Sum256(first)
	payloads["sha256:"+hex.EncodeToString(firstHash[:])] = first
	for _, test := range []struct {
		name           string
		manifestOrder  bool
		memberOrder    bool
		uriOrder       bool
		manifestSecond bool
	}{
		{name: "canonical two-package control"},
		{name: "unsorted manifest", manifestOrder: true},
		{name: "unsorted members", memberOrder: true},
		{name: "unsorted URIs", uriOrder: true},
		{name: "manifest is not first", manifestSecond: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			candidate.Packages = slices.Clone(manifest.Packages)
			members := slices.Clone(manifest.Packages)
			if test.manifestOrder {
				slices.Reverse(candidate.Packages)
			}
			if test.memberOrder {
				slices.Reverse(members)
			}
			if test.uriOrder {
				candidate.Packages[0].RepositoryURIs = []string{"https://archive.example.invalid/z", "https://archive.example.invalid/a"}
			}
			raw, err := canonical.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			writer := tar.NewWriter(&encoded)
			write := func(name string, content []byte) {
				t.Helper()
				if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o444, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(content); err != nil {
					t.Fatal(err)
				}
			}
			if !test.manifestSecond {
				write("manifest.json", raw)
			}
			for index, member := range members {
				write(member.Path, payloads[member.Digest])
				if test.manifestSecond && index == 0 {
					write("manifest.json", raw)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, encoded.Bytes()))
			if test.name == "canonical two-package control" {
				if err != nil || observation == nil {
					t.Fatalf("valid ordered capture rejected: %v", err)
				}
			} else {
				reason := map[string]string{
					"unsorted manifest":     "digests must be strictly sorted and unique",
					"unsorted members":      "member path differs",
					"unsorted URIs":         "repository locations must be strictly sorted and unique",
					"manifest is not first": "requires manifest.json first",
				}[test.name]
				assertAcquisitionFailure(t, observation, err, reason)
			}
		})
	}
}

func TestObserveAcquisitionArchiveRejectsHiddenExtensions(t *testing.T) {
	manifest, payload := acquisitionFixture("amd64")
	raw := acquisitionTar(t, manifest, payload, nil, false)
	var extended bytes.Buffer
	writer := tar.NewWriter(&extended)
	if err := writer.WriteHeader(&tar.Header{Name: strings.Repeat("x", 200), Mode: 0o444, Typeflag: tar.TypeReg, Format: tar.FormatGNU}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// The standard writer emits a GNU long-name header and its one-block payload.
	// Its NUL-terminated visible name hides otherwise undeclared bytes.
	for _, test := range []struct {
		name     string
		offset   int
		target   string
		truncate bool
		reason   string
	}{
		{name: "before manifest", offset: 0, target: "manifest.json", reason: "hidden headers before manifest"},
		{name: "before package", offset: 1024, target: manifest.Packages[0].Path, reason: "hidden headers before package"},
		{name: "before terminator", offset: len(raw) - 1024, target: "hidden", reason: "hidden extension members or an incomplete terminator"},
		{name: "instead of terminator at EOF", offset: len(raw) - 1024, target: "hidden", truncate: true, reason: "nonzero terminator"},
	} {
		t.Run(test.name, func(t *testing.T) {
			extension := bytes.Clone(extended.Bytes()[:1024])
			clear(extension[512:])
			copy(extension[512:], test.target+"\x00undeclared payload")
			capture := append(bytes.Clone(raw[:test.offset]), extension...)
			if !test.truncate {
				capture = append(capture, raw[test.offset:]...)
			}
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, capture))
			assertAcquisitionFailure(t, observation, err, test.reason)
		})
	}
}

func TestObserveRealAcquisitionArchive(t *testing.T) {
	name := os.Getenv("NVKR_TEST_ACQUISITION_ARCHIVE")
	if name == "" {
		t.Skip("real source-owned APT capture is opt-in")
	}
	held, err := heldfile.OpenContext(context.Background(), name, heldfile.Options{Label: "real APT capture", MaxBytes: MaxAcquisitionArchiveBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	observation, err := ObserveAcquisitionArchive(context.Background(), held)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, document := observation.Binding()
	t.Logf("verified actual capture bytes: archive=%s size=%d manifest=%s architecture=%s packages=%d (not live-Run authority)",
		digest, size, document, observation.Description().Architecture, len(observation.Description().Packages))
}

func TestObservePythonTarfileCapture(t *testing.T) {
	encoded, err := os.ReadFile("testdata/python-acquisition.tar.gz.base64")
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	archive, size, manifest := observation.Binding()
	if archive != "sha256:a5e6923cb20e8ce240bb4d95e1762c675a374046f01d6a5f22a1e99523f0dbd9" || size != 10240 || manifest != "sha256:0b32af5d57f7a0a4a929bacaa1433f471bf81e4f911d3f679406a0d4bd7de5f5" {
		t.Fatalf("Python producer format changed: %s %d %s", archive, size, manifest)
	}
	if description := observation.Description(); description.Architecture != "arm64" || len(description.Packages) != 1 || description.Packages[0].Architecture != "all" {
		t.Fatalf("unexpected description: %+v", description)
	}
}
