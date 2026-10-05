package material

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func assertAcquisitionFailure(t *testing.T, observation *AcquisitionObservation, err error, reason string) {
	t.Helper()
	if observation != nil || errorCode(err) != e.CodeVerifyFailed || reason == "" || !strings.Contains(err.Error(), reason) {
		t.Fatalf("expected VerifyFailed (%s), observation=%v error=%v", reason, observation, err)
	}
}

func TestAcquisitionManifestValidate(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name   string
			mutate func(*AcquisitionManifest)
			reason string
		}{
			{name: "native package"},
			{name: "all package", mutate: func(m *AcquisitionManifest) { m.Packages[0].Architecture = "all" }},
			{name: "schema", mutate: func(m *AcquisitionManifest) { m.SchemaVersion = "other" }, reason: "unsupported package acquisition schema"},
			{name: "scope", mutate: func(m *AcquisitionManifest) { m.Scope = "other" }, reason: "unsupported package acquisition scope"},
			{name: "capture architecture", mutate: func(m *AcquisitionManifest) { m.Architecture = "other"; m.Packages[0].Architecture = "all" }, reason: "unsupported package acquisition architecture"},
			{name: "empty packages", mutate: func(m *AcquisitionManifest) { m.Packages = nil }, reason: "count outside supported bounds"},
			{name: "package count limit", mutate: func(m *AcquisitionManifest) { setAcquisitionPackageCount(m, maxAcquisitionPackages) }},
			{name: "package count overflow", mutate: func(m *AcquisitionManifest) { setAcquisitionPackageCount(m, maxAcquisitionPackages+1) }, reason: "count outside supported bounds"},
			{name: "name", mutate: func(m *AcquisitionManifest) { m.Packages[0].Name = "a\nb" }, reason: "invalid display metadata"},
			{name: "version", mutate: func(m *AcquisitionManifest) { m.Packages[0].Version = "" }, reason: "invalid display metadata"},
			{name: "unverified display tokens", mutate: func(m *AcquisitionManifest) { m.Packages[0].Name = "!!!"; m.Packages[0].Version = "../../x" }},
			{name: "package architecture", mutate: func(m *AcquisitionManifest) { m.Packages[0].Architecture = "other" }, reason: "architecture differs from capture"},
			{name: "digest", mutate: func(m *AcquisitionManifest) { m.Packages[0].Digest = "bad" }, reason: "invalid blob digest"},
			{name: "duplicate digest", mutate: func(m *AcquisitionManifest) { m.Packages = append(m.Packages, m.Packages[0]) }, reason: "digests must be strictly sorted and unique"},
			{name: "size limit", mutate: func(m *AcquisitionManifest) { m.Packages[0].Size = maxAcquisitionPackageBytes }},
			{name: "size overflow", mutate: func(m *AcquisitionManifest) { m.Packages[0].Size = maxAcquisitionPackageBytes + 1 }, reason: "size outside supported bounds"},
			{name: "zero size", mutate: func(m *AcquisitionManifest) { m.Packages[0].Size = 0 }, reason: "size outside supported bounds"},
			{name: "digest-derived path", mutate: func(m *AcquisitionManifest) { m.Packages[0].Path = "packages/other.deb" }, reason: "path is not digest-derived"},
			{name: "missing URIs", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = nil }, reason: "requires bounded repository metadata locations"},
			{name: "URI count limit", mutate: func(m *AcquisitionManifest) { setAcquisitionURICount(m, maxAcquisitionRepositoryURIs) }},
			{name: "URI count overflow", mutate: func(m *AcquisitionManifest) { setAcquisitionURICount(m, maxAcquisitionRepositoryURIs+1) }, reason: "requires bounded repository metadata locations"},
			{name: "URI length limit", mutate: func(m *AcquisitionManifest) {
				m.Packages[0].RepositoryURIs = []string{acquisitionURI(maxAcquisitionRepositoryURIBytes)}
			}},
			{name: "URI length overflow", mutate: func(m *AcquisitionManifest) {
				m.Packages[0].RepositoryURIs = []string{acquisitionURI(maxAcquisitionRepositoryURIBytes + 1)}
			}, reason: "not bounded display text"},
			{name: "URI ordering", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https://h/z", "https://h/a"} }, reason: "locations must be strictly sorted and unique"},
			{name: "URI parse", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https://h/%zz"} }, reason: "parsing acquired package repository location"},
			{name: "URI scheme", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"ftp://h/a"} }, reason: "requires HTTP(S)"},
			{name: "URI host", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https:///a"} }, reason: "requires a host"},
			{name: "URI credentials", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https://user@h/a"} }, reason: "contains credentials"},
			{name: "URI query", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https://h/a?"} }, reason: "contains a query"},
			{name: "URI fragment", mutate: func(m *AcquisitionManifest) { m.Packages[0].RepositoryURIs = []string{"https://h/a#fragment"} }, reason: "contains a fragment"},
			// Locations are raw descriptions, never normalized or used as fetch targets.
			{name: "noncanonical URI description", mutate: func(m *AcquisitionManifest) {
				m.Packages[0].RepositoryURIs = []string{"HTTPS://Archive.invalid:99999/a/../b#"}
			}},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				manifest, _ := acquisitionFixture(architecture)
				if test.mutate != nil {
					test.mutate(&manifest)
				}
				err := manifest.validate()
				if test.reason == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertAcquisitionFailure(t, nil, err, test.reason)
				}
			})
		}
	}
}

func setAcquisitionPackageCount(manifest *AcquisitionManifest, count int) {
	entry := manifest.Packages[0]
	manifest.Packages = make([]AcquiredPackage, count)
	for index := range manifest.Packages {
		entry.Digest = fmt.Sprintf("sha256:%064x", index)
		entry.Path = "packages/" + strings.TrimPrefix(entry.Digest, "sha256:") + ".deb"
		manifest.Packages[index] = entry
	}
}

func setAcquisitionURICount(manifest *AcquisitionManifest, count int) {
	locations := make([]string, count)
	for index := range locations {
		locations[index] = fmt.Sprintf("https://h/%02d", index)
	}
	manifest.Packages[0].RepositoryURIs = locations
}

func acquisitionURI(size int) string {
	return "https://h/" + strings.Repeat("x", size-len("https://h/"))
}

func TestAcquisitionRegularRequiresEveryHeaderClause(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*tar.Header)
	}{
		{name: "type", mutate: func(h *tar.Header) { h.Typeflag = tar.TypeDir }},
		{name: "link name", mutate: func(h *tar.Header) { h.Linkname = "target" }},
		{name: "GNU format", mutate: func(h *tar.Header) { h.Format = tar.FormatGNU }},
		{name: "PAX records", mutate: func(h *tar.Header) { h.PAXRecords = map[string]string{"comment": "hidden"} }},
		{name: "negative size", mutate: func(h *tar.Header) { h.Size = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := &tar.Header{Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, Size: 1}
			if !acquisitionRegular(header) {
				t.Fatal("valid control rejected")
			}
			test.mutate(header)
			if acquisitionRegular(header) {
				t.Fatal("invalid header accepted")
			}
		})
	}
	if acquisitionRegular(nil) {
		t.Fatal("nil header accepted")
	}
}

func TestObserveAcquisitionArchiveMemberBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*tar.Header)
		size   int64
		reason string
	}{
		{name: "directory", mutate: func(h *tar.Header) { h.Typeflag = tar.TypeDir; h.Size = 0 }, reason: "regular USTAR member"},
		{name: "hard link", mutate: func(h *tar.Header) { h.Typeflag = tar.TypeLink; h.Linkname = "target"; h.Size = 0 }, reason: "regular USTAR member"},
		{name: "PAX member", mutate: func(h *tar.Header) { h.Format = tar.FormatPAX; h.PAXRecords = map[string]string{"comment": "hidden"} }, reason: "hidden headers before package"},
		{name: "wrong size", size: 1, reason: "member size differs from manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, payload := acquisitionFixture("amd64")
			if test.size != 0 {
				manifest.Packages[0].Size = test.size
			}
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, acquisitionTar(t, manifest, payload, test.mutate, false)))
			assertAcquisitionFailure(t, observation, err, test.reason)
		})
	}
	for _, size := range []int64{0, maxAcquisitionManifestBytes, maxAcquisitionManifestBytes + 1} {
		t.Run(fmt.Sprintf("manifest size %d", size), func(t *testing.T) {
			var encoded bytes.Buffer
			writer := tar.NewWriter(&encoded)
			if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, Size: size}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.CopyN(writer, strings.NewReader(strings.Repeat("x", int(size))), size); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, encoded.Bytes()))
			reason := "acquisition manifest size outside supported bounds"
			if size == maxAcquisitionManifestBytes {
				reason = "decoding package acquisition manifest"
			}
			assertAcquisitionFailure(t, observation, err, reason)
		})
	}
}

// Keep a real cancelable context, including Done, and deterministically cancel
// at successive checkpoints without relying on sleeps or multi-GiB fixtures.
type acquisitionCancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
	at     int
}

func (ctx *acquisitionCancelContext) Err() error {
	ctx.calls++
	if ctx.at > 0 && ctx.calls == ctx.at {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestObserveAcquisitionArchiveMidReadCancellation(t *testing.T) {
	manifest, payload := acquisitionFixture("arm64")
	raw := acquisitionTar(t, manifest, payload, nil, false)
	raw = append(raw, make([]byte, 10240-len(raw))...)
	held := holdAcquisition(t, raw)
	control, controlCancel := context.WithCancel(context.Background())
	defer controlCancel()
	counted := &acquisitionCancelContext{Context: control, cancel: controlCancel}
	if observation, err := ObserveAcquisitionArchive(counted, held); observation == nil || err != nil {
		t.Fatalf("positive control: %v", err)
	}
	for checkpoint := 1; checkpoint <= counted.calls; checkpoint++ {
		t.Run(fmt.Sprintf("checkpoint %d", checkpoint), func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &acquisitionCancelContext{Context: base, cancel: cancel, at: checkpoint}
			observation, err := ObserveAcquisitionArchive(ctx, held)
			if observation != nil || errorCode(err) != e.CodeTimeout {
				t.Fatalf("observation=%v error=%v", observation, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := acquisitionReader{ctx: ctx, source: io.NewSectionReader(bytes.NewReader(raw), 0, int64(len(raw)))}
	if n, err := reader.Read(make([]byte, 512)); n != 0 || err != context.Canceled || reader.offset() != 0 {
		t.Fatalf("canceled reader advanced: %d %v %d", n, err, reader.offset())
	}
	if errorCode(acquisitionReadError(ctx, io.ErrUnexpectedEOF)) != e.CodeTimeout {
		t.Fatal("cancellation mapped to integrity failure")
	}
}

func TestAcquisitionEmptyDescriptionsAndArchives(t *testing.T) {
	for _, observation := range []*AcquisitionObservation{nil, {}} {
		digest, size, manifest := observation.Binding()
		if digest != "" || size != 0 || manifest != "" || observation.Description().SchemaVersion != "" || observation.Description().Packages != nil {
			t.Fatal("empty description claims a verified subject")
		}
	}
	observation, err := ObserveAcquisitionArchive(context.Background(), nil)
	assertAcquisitionFailure(t, observation, err, "archive is absent or oversized")
	observation, err = ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, nil))
	assertAcquisitionFailure(t, observation, err, "archive is absent or oversized")
}

func TestAcquisitionURIParseErrorDoesNotEchoCredentials(t *testing.T) {
	manifest, _ := acquisitionFixture("amd64")
	manifest.Packages[0].RepositoryURIs = []string{"https://user:private-password@h/%zz"}
	err := manifest.validate()
	assertAcquisitionFailure(t, nil, err, "parsing acquired package repository location")
	if strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), manifest.Packages[0].RepositoryURIs[0]) {
		t.Fatal("URI parser diagnostic exposes source credentials")
	}
}

func TestObserveAcquisitionArchiveRejectsGlobalPAXHeader(t *testing.T) {
	manifest, payload := acquisitionFixture("amd64")
	var prefix bytes.Buffer
	writer := tar.NewWriter(&prefix)
	if err := writer.WriteHeader(&tar.Header{Name: "global", Typeflag: tar.TypeXGlobalHeader, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "undeclared"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Strip only the writer's zero terminator, retaining its standard global
	// header+data. archive/tar still owns all header/protocol interpretation.
	raw := append(bytes.Clone(prefix.Bytes()[:prefix.Len()-1024]), acquisitionTar(t, manifest, payload, nil, false)...)
	observation, err := ObserveAcquisitionArchive(context.Background(), holdAcquisition(t, raw))
	assertAcquisitionFailure(t, observation, err, "hidden headers before manifest")
}

func TestObserveAcquisitionArchiveAllowsDigestBoundOpaqueMemberPadding(t *testing.T) {
	manifest, payload := acquisitionFixture("amd64")
	raw := acquisitionTar(t, manifest, payload, nil, false)
	// Member padding is not another parser-visible header or package. It is
	// deliberately bound only as part of the complete archive's exact blob hash.
	for _, offset := range []int{1023, 2047} {
		t.Run(fmt.Sprintf("padding byte %d", offset), func(t *testing.T) {
			candidate := bytes.Clone(raw)
			candidate[offset] = 0x7f
			held := holdAcquisition(t, candidate)
			observation, err := ObserveAcquisitionArchive(context.Background(), held)
			if err != nil {
				t.Fatal(err)
			}
			digest, size, _ := observation.Binding()
			if digest != held.Digest() || size != held.Size() {
				t.Fatal("opaque bytes not bound to archive")
			}
		})
	}
}
