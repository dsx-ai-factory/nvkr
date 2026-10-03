package material

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
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
	var destination bytes.Buffer
	writer := tar.NewWriter(&destination)
	write := func(name string, content []byte, alter bool) {
		header := &tar.Header{Name: name, Mode: 0o444, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if alter && mutate != nil {
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
	write("manifest.json", raw, false)
	if payload != nil {
		write(manifest.Packages[0].Path, payload, true)
	}
	if extra {
		write("extra.deb", []byte("undeclared"), false)
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
			if got != nil || errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("observation = %v, error = %v", got, err)
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
	for _, document := range []string{`{"schemaVersion":"a","schemaVersion":"b"}`, `{"unknown":true}`, " " + string(canonicalDocument), strings.Repeat("x", 100)} {
		var encoded bytes.Buffer
		writer := tar.NewWriter(&encoded)
		if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o444, Size: int64(len(document)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(document)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, encoded.Bytes())); observation != nil || errorCode(err) != e.CodeVerifyFailed {
			t.Fatalf("malformed acquisition document accepted: observation=%v error=%v", observation, err)
		}
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
			} else if observation != nil || errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("unordered capture accepted: observation=%v error=%v", observation, err)
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
		name   string
		offset int
		target string
	}{
		{name: "before manifest", offset: 0, target: "manifest.json"},
		{name: "before package", offset: 1024, target: manifest.Packages[0].Path},
		{name: "before terminator", offset: len(raw) - 1024, target: "hidden"},
	} {
		t.Run(test.name, func(t *testing.T) {
			extension := bytes.Clone(extended.Bytes()[:1024])
			clear(extension[512:])
			copy(extension[512:], test.target+"\x00undeclared payload")
			capture := append(bytes.Clone(raw[:test.offset]), extension...)
			capture = append(capture, raw[test.offset:]...)
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, capture))
			if observation != nil || errorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("hidden extension accepted: observation=%v error=%v", observation, err)
			}
		})
	}
}

func TestObserveRealAcquisitionArchive(t *testing.T) {
	name := os.Getenv("NKD_TEST_ACQUISITION_ARCHIVE")
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
