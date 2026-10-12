// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func helmFixtureArchive(t *testing.T, metadata string, headers ...*tar.Header) []byte {
	t.Helper()
	var records []helmFixtureRecord
	if metadata != "" {
		records = append(records, helmFixtureRecord{header: tar.Header{Name: "demo/Chart.yaml", Mode: 0o644}, body: metadata})
	}
	for _, header := range headers {
		records = append(records, helmFixtureRecord{header: *header})
	}
	return helmFixtureArchiveRecords(t, records)
}

type helmFixtureRecord struct {
	header tar.Header
	body   string
}

func helmFixtureArchiveRecords(t *testing.T, records []helmFixtureRecord) []byte {
	t.Helper()
	var out bytes.Buffer
	zipped := gzip.NewWriter(&out)
	archive := tar.NewWriter(zipped)
	for _, record := range records {
		record.header.Size = int64(len(record.body))
		if err := archive.WriteHeader(&record.header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(record.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestHelmIndexResolutionPinsExactDownloadedPackage(t *testing.T) {
	payload := helmFixtureArchive(t, "apiVersion: v2\nname: demo\nversion: 1.2.3\ndescription: fixture\n")
	digest := pinFixtureDigest(string(payload))
	for _, tc := range []struct {
		name, advertised, location string
	}{
		{"relative URL", strings.TrimPrefix(digest, "sha256:"), "packages/demo-1.2.3.tgz"},
		{"absolute URL", digest, "https://cdn.example/demo-1.2.3.tgz"},
		{"no advertised digest", "", "../packages/demo-1.2.3.tgz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			fetch := func(_ context.Context, location string, limit int64) ([]byte, error) {
				calls = append(calls, location)
				if len(calls) == 1 {
					if location != "https://repo.example/charts/index.yaml" || limit != maxHelmIndexBytes {
						t.Fatalf("unexpected index request %s, bound %d", location, limit)
					}
					return []byte(fmt.Sprintf("apiVersion: v1\ngenerated: fixture\nentries:\n  demo:\n  - name: demo\n    version: 1.2.3\n    urls: [%q]\n    digest: %q\n  - name: demo\n    version: 9.9.9\n    urls: [https://wrong.example/package]\n", tc.location, tc.advertised)), nil
				}
				if limit != maxHelmPackageBytes {
					t.Fatalf("package bound %d", limit)
				}
				return payload, nil
			}
			location, got, err := resolveHelmChartWith(t.Context(), HelmChart{Name: "demo", Repository: "https://repo.example/charts"}, "1.2.3", fetch, nil)
			if err != nil || got != digest || len(calls) != 2 || location != calls[1] {
				t.Fatalf("pin = %s %s, %v; requests %v", location, got, err, calls)
			}
			want := "https://repo.example/charts/" + tc.location
			switch tc.name {
			case "absolute URL":
				want = tc.location
			case "no advertised digest":
				want = "https://repo.example/packages/demo-1.2.3.tgz"
			}
			if location != want {
				t.Fatalf("resolved URL %s, want %s", location, want)
			}
		})
	}
}

func TestHelmIndexResolutionRejectsAmbiguityAndDrift(t *testing.T) {
	entry := "  - name: demo\n    version: 1.2.3\n    urls: [demo.tgz]\n"
	for _, tc := range []struct {
		name, index string
		code        e.Code
	}{
		{"missing exact version", "apiVersion: v1\nentries: {}", e.CodeInvalidCatalog},
		{"duplicate exact version", "apiVersion: v1\nentries:\n  demo:\n" + entry + entry, e.CodeInvalidCatalog},
		{"wrong chart identity", "apiVersion: v1\nentries:\n  demo:\n" + strings.Replace(entry, "name: demo", "name: other", 1), e.CodeInvalidCatalog},
		{"multiple URLs", "apiVersion: v1\nentries:\n  demo:\n" + strings.Replace(entry, "[demo.tgz]", "[demo.tgz, other.tgz]", 1), e.CodeInvalidCatalog},
		{"empty URL", "apiVersion: v1\nentries:\n  demo:\n" + strings.Replace(entry, "[demo.tgz]", "['']", 1), e.CodeInvalidCatalog},
		{"plain HTTP package", "apiVersion: v1\nentries:\n  demo:\n" + strings.Replace(entry, "demo.tgz", "http://example.com/demo.tgz", 1), e.CodeInvalidCatalog},
		{"credentials in package", "apiVersion: v1\nentries:\n  demo:\n" + strings.Replace(entry, "demo.tgz", "https://user:pass@example.com/demo.tgz", 1), e.CodeInvalidCatalog},
		{"duplicate key", "apiVersion: v1\napiVersion: v1\nentries: {}", e.CodeInvalidCatalog},
		{"extra document", "apiVersion: v1\nentries:\n  demo:\n" + entry + "---\n{}", e.CodeInvalidCatalog},
		{"wrong API", "apiVersion: v2\nentries:\n  demo:\n" + entry, e.CodeInvalidCatalog},
		{"invalid digest", "apiVersion: v1\nentries:\n  demo:\n" + entry + "    digest: bad\n", e.CodeInvalidCatalog},
		{"digest drift", "apiVersion: v1\nentries:\n  demo:\n" + entry + "    digest: '" + strings.Repeat("0", 64) + "'\n", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetch := func(_ context.Context, location string, _ int64) ([]byte, error) {
				if strings.HasSuffix(location, "/index.yaml") {
					return []byte(tc.index), nil
				}
				return helmFixtureArchive(t, "name: demo\nversion: 1.2.3\n"), nil
			}
			location, digest, err := resolveHelmChartWith(t.Context(), HelmChart{Name: "demo", Repository: "https://repo.example"}, "1.2.3", fetch, nil)
			assertCatalogContractCode(t, err, tc.code)
			if location != "" || digest != "" {
				t.Fatal("failed resolution exposed partial pin")
			}
		})
	}
}

func TestHelmArchiveRejectsUnsafeOrWrongSubjectPackages(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		header         *tar.Header
	}{
		{name: "wrong name", metadata: "name: other\nversion: 1.2.3\n"},
		{name: "wrong version", metadata: "name: demo\nversion: 1.2.4\n"},
		{name: "duplicate identity", metadata: "name: demo\nname: demo\nversion: 1.2.3\n"},
		{name: "second YAML document", metadata: "name: demo\nversion: 1.2.3\n---\nname: other\n"},
		{name: "missing root chart"},
		{name: "traversal", header: &tar.Header{Name: "demo/../escape"}},
		{name: "absolute path", header: &tar.Header{Name: "/demo/escape"}},
		{name: "duplicate path", header: &tar.Header{Name: "demo/Chart.yaml"}},
		{name: "symlink", header: &tar.Header{Name: "demo/link", Typeflag: tar.TypeSymlink, Linkname: "Chart.yaml"}},
		{name: "hardlink", header: &tar.Header{Name: "demo/link", Typeflag: tar.TypeLink, Linkname: "demo/Chart.yaml"}},
		{name: "other root", header: &tar.Header{Name: "other/file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := tc.metadata
			var headers []*tar.Header
			if tc.header != nil {
				metadata = "name: demo\nversion: 1.2.3\n"
				headers = append(headers, tc.header)
			}
			err := validateHelmArchive(t.Context(), helmFixtureArchive(t, metadata, headers...), "demo", "1.2.3")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
		})
	}
	archive := helmFixtureArchive(t, "name: demo\nversion: 1.2.3\n")
	archive[len(archive)-1] ^= 0xff
	assertCatalogContractCode(t, validateHelmArchive(t.Context(), archive, "demo", "1.2.3"), e.CodeInvalidCatalog)
}

func TestHelmArchiveAllowsOnlyIdenticalRepeatedSubchartFiles(t *testing.T) {
	root := helmFixtureRecord{header: tar.Header{Name: "demo/Chart.yaml", Mode: 0o644}, body: "name: demo\nversion: 1.2.3\n"}
	for _, tc := range []struct {
		name   string
		path   string
		change func(*helmFixtureRecord)
		good   bool
	}{
		{name: "identical subchart metadata", path: "demo/charts/spike-pilot/Chart.yaml", good: true},
		{name: "identical subchart template", path: "demo/charts/spike-pilot/templates/deployment.yaml", good: true},
		{name: "conflicting contents", change: func(r *helmFixtureRecord) { r.body = strings.Replace(r.body, "1.2.3", "9.9.9", 1) }},
		{name: "conflicting size", change: func(r *helmFixtureRecord) { r.body += "extra" }},
		{name: "conflicting mode", change: func(r *helmFixtureRecord) { r.header.Mode = 0o755 }},
		{name: "conflicting uid", change: func(r *helmFixtureRecord) { r.header.Uid = 1 }},
		{name: "conflicting gid", change: func(r *helmFixtureRecord) { r.header.Gid = 1 }},
		{name: "conflicting uname", change: func(r *helmFixtureRecord) { r.header.Uname = "different" }},
		{name: "conflicting gname", change: func(r *helmFixtureRecord) { r.header.Gname = "different" }},
		{name: "conflicting modification time", change: func(r *helmFixtureRecord) { r.header.ModTime = r.header.ModTime.Add(time.Second) }},
		{name: "conflicting access time", change: func(r *helmFixtureRecord) { r.header.AccessTime = r.header.AccessTime.Add(time.Second) }},
		{name: "conflicting PAX metadata", change: func(r *helmFixtureRecord) { r.header.PAXRecords = map[string]string{"fixture.note": "different"} }},
		{name: "directory replaces file", change: func(r *helmFixtureRecord) { r.header.Typeflag = tar.TypeDir; r.body = "" }},
		{name: "symlink replaces file", change: func(r *helmFixtureRecord) {
			r.header.Typeflag = tar.TypeSymlink
			r.header.Linkname = "Chart.yaml"
			r.body = ""
		}},
		{name: "repeated package root metadata", path: "demo/Chart.yaml"},
		{name: "repeated package template", path: "demo/templates/deployment.yaml"},
		{name: "repeated packed subchart", path: "demo/charts/spike-pilot.tgz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.path
			if name == "" {
				name = "demo/charts/spike-pilot/Chart.yaml"
			}
			first := helmFixtureRecord{header: tar.Header{
				Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Format: tar.FormatPAX,
				ModTime: time.Unix(100, 0), AccessTime: time.Unix(101, 0),
				PAXRecords: map[string]string{"fixture.note": "same"},
			}, body: "name: spike-pilot\nversion: 1.2.3\n"}
			second := first
			if tc.change != nil {
				tc.change(&second)
			}
			archive := helmFixtureArchiveRecords(t, []helmFixtureRecord{root, first, second})
			err := validateHelmArchive(t.Context(), archive, "demo", "1.2.3")
			if tc.good {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
	// Even a byte-identical repeated root identity remains invalid.
	err := validateHelmArchive(t.Context(), helmFixtureArchiveRecords(t, []helmFixtureRecord{root, root}), "demo", "1.2.3")
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}

func TestHelmRepeatedSubchartRecordsStillConsumeEntryBudget(t *testing.T) {
	root := helmFixtureRecord{header: tar.Header{Name: "demo/Chart.yaml", Mode: 0o644}, body: "name: demo\nversion: 1.2.3\n"}
	repeated := helmFixtureRecord{header: tar.Header{Name: "demo/charts/subchart/file", Mode: 0o644}, body: "identical"}
	for _, count := range []int{maxHelmFiles, maxHelmFiles + 1} {
		t.Run(fmt.Sprintf("%d records", count), func(t *testing.T) {
			records := make([]helmFixtureRecord, count)
			records[0] = root
			for index := 1; index < count; index++ {
				records[index] = repeated
			}
			err := validateHelmArchive(t.Context(), helmFixtureArchiveRecords(t, records), "demo", "1.2.3")
			if count == maxHelmFiles {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}

type helmFixtureOCI struct {
	v1.Image
	manifest *v1.Manifest
	payload  []byte
}

func (image helmFixtureOCI) Manifest() (*v1.Manifest, error) { return image.manifest, nil }
func (image helmFixtureOCI) LayerByDigest(v1.Hash) (v1.Layer, error) {
	return static.NewLayer(image.payload, helmContentType), nil
}

func TestHelmOCIValidatesMediaTypesAndExactContent(t *testing.T) {
	payload := helmFixtureArchive(t, "name: demo\nversion: 1.2.3+build.1\n")
	digest := pinFixtureDigest(string(payload))
	hash, err := v1.NewHash(digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*v1.Manifest)
		code   e.Code
	}{
		{name: "valid"},
		{name: "image config", mutate: func(m *v1.Manifest) { m.Config.MediaType = types.OCIConfigJSON }, code: e.CodeInvalidCatalog},
		{name: "image layer", mutate: func(m *v1.Manifest) { m.Layers[0].MediaType = types.OCILayer }, code: e.CodeInvalidCatalog},
		{name: "no layers", mutate: func(m *v1.Manifest) { m.Layers = nil }, code: e.CodeInvalidCatalog},
		{name: "multiple layers", mutate: func(m *v1.Manifest) { m.Layers = append(m.Layers, m.Layers[0]) }, code: e.CodeInvalidCatalog},
		{name: "wrong digest", mutate: func(m *v1.Manifest) { m.Layers[0].Digest.Hex = strings.Repeat("0", 64) }, code: e.CodeVerifyFailed},
		{name: "wrong length", mutate: func(m *v1.Manifest) { m.Layers[0].Size++ }, code: e.CodeFetchFailed},
		{name: "oversize", mutate: func(m *v1.Manifest) { m.Layers[0].Size = maxHelmPackageBytes + 1 }, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &v1.Manifest{Config: v1.Descriptor{MediaType: helmConfigType}, Layers: []v1.Descriptor{{MediaType: helmContentType, Digest: hash, Size: int64(len(payload))}}}
			if tc.mutate != nil {
				tc.mutate(manifest)
			}
			read := func(_ context.Context, ref name.Reference) (v1.Image, error) {
				if ref.Name() != "registry.example/charts/demo:1.2.3_build.1" {
					t.Fatalf("OCI reference %s", ref)
				}
				return helmFixtureOCI{manifest: manifest, payload: payload}, nil
			}
			location, got, err := resolveHelmChartWith(t.Context(), HelmChart{Name: "demo", Repository: "oci://registry.example/charts"}, "1.2.3+build.1", nil, read)
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
				if got != "" || location != "" {
					t.Fatal("failed resolution exposed partial pin")
				}
				return
			}
			if err != nil || got != digest || location != "https://registry.example/v2/charts/demo/blobs/"+digest {
				t.Fatalf("OCI package pin = %s %s, %v", location, got, err)
			}
		})
	}
}

func TestHelmAdmissionBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, repository, chart, version string
		cancel                           bool
		code                             e.Code
	}{
		{"plain HTTP", "http://repo.example", "demo", "1.2.3", false, e.CodeInvalidCatalog},
		{"credential URL", "https://user:pass@repo.example", "demo", "1.2.3", false, e.CodeInvalidCatalog},
		{"unsafe chart", "https://repo.example", "../demo", "1.2.3", false, e.CodeInvalidCatalog},
		{"unsafe repository", "https://repo.example/a/../b", "demo", "1.2.3", false, e.CodeInvalidCatalog},
		{"range", "https://repo.example", "demo", ">=1.2.3", false, e.CodeInvalidCatalog},
		{"numeric prerelease zero", "https://repo.example", "demo", "1.2.3-beta.01", false, e.CodeInvalidCatalog},
		{"wildcard", "https://repo.example", "demo", "1.2.x", false, e.CodeInvalidCatalog},
		{"canceled", "https://repo.example", "demo", "1.2.3", true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			_, _, err := resolveHelmChartWith(ctx, HelmChart{Name: tc.chart, Repository: tc.repository}, tc.version, func(context.Context, string, int64) ([]byte, error) {
				t.Fatal("contacted upstream before admission")
				return nil, nil
			}, nil)
			assertCatalogContractCode(t, err, tc.code)
		})
	}
}

type helmRoundTripper func(*http.Request) (*http.Response, error)

func (fn helmRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestHelmHTTPBoundsAndTransport(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		length     int64
		status     int
		fail       bool
	}{
		{"exact bound", "1234", 4, 200, false},
		{"declared oversize", "12345", 5, 200, true},
		{"streamed oversize", "12345", -1, 200, true},
		{"truncated", "12", 4, 200, true},
		{"HTTP failure", "", 0, 404, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: helmRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			body, err := fetchHelmWithClient(t.Context(), client, "https://repo.example", 4)
			if tc.fail {
				assertCatalogContractCode(t, err, e.CodeFetchFailed)
				if body != nil {
					t.Fatal("partial body returned")
				}
			} else if err != nil || string(body) != tc.body {
				t.Fatalf("body %q, %v", body, err)
			}
		})
	}
	transport := helmTransport{helmRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request reached transport")
		return nil, nil
	})}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/v2/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
	body := &helmBoundedBody{ReadCloser: io.NopCloser(strings.NewReader("12345")), remaining: 4}
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("unbounded OCI response")
	}
}

func TestHelmVersionLookupPreservesOriginalText(t *testing.T) {
	for _, version := range []string{"v1.2.3", "1.2.3-rc.1", "1.2.3+build.01", "1.2"} {
		t.Run(version, func(t *testing.T) {
			payload := helmFixtureArchive(t, fmt.Sprintf("apiVersion: v2\nname: demo\nversion: %q\n", version))
			location := "https://repo.example/demo-" + version + ".tgz"
			fetch := func(_ context.Context, url string, _ int64) ([]byte, error) {
				if url == "https://repo.example/index.yaml" {
					return []byte(fmt.Sprintf("apiVersion: v1\nentries:\n  demo:\n  - name: demo\n    version: %q\n    urls: [%q]\n", version, location)), nil
				}
				if url != location {
					t.Fatalf("version changed in package URL: %s", url)
				}
				return payload, nil
			}
			got, digest, err := resolveHelmChartWith(t.Context(), HelmChart{Name: "demo", Repository: "https://repo.example"}, version, fetch, nil)
			if err != nil || got != location || digest != pinFixtureDigest(string(payload)) {
				t.Fatalf("pin = %q %q, %v", got, digest, err)
			}
		})
	}
}
