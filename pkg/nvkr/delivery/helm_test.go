// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func helmChartArchiveFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBuildHelmChartArtifactProducesConformantChart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chartData := helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"demo/templates/deployment.yaml": "kind: Deployment\n",
	})
	out := filepath.Join(t.TempDir(), "packaged")
	result, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData)
	if err != nil {
		t.Fatal(err)
	}
	if result.LayoutPath != out || result.RootDigest == "" || result.SemanticDigest == "" {
		t.Fatalf("unexpected result: %#v", result)
	}

	// Read back via go-containerregistry's generic OCI-layout access, matching how
	// the delivery Push path (registry.go) consumes the committed layout: it never
	// needs to know this is a Helm artifact.
	path, err := layout.FromPath(out)
	if err != nil {
		t.Fatal(err)
	}
	index, err := path.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(indexManifest.Manifests) != 1 {
		t.Fatalf("expected one manifest, got %d", len(indexManifest.Manifests))
	}
	raw, err := path.Bytes(indexManifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := v1.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 {
		t.Fatalf("schema version: %d", manifest.SchemaVersion)
	}
	if string(manifest.Config.MediaType) != MediaHelmConfig {
		t.Fatalf("config media type: %s", manifest.Config.MediaType)
	}
	if len(manifest.Layers) != 1 || string(manifest.Layers[0].MediaType) != MediaHelmChartLayer {
		t.Fatalf("layers: %#v", manifest.Layers)
	}
	if manifest.ArtifactType != "application/vnd.nvkr.helm-chart.v1" {
		t.Fatalf("artifact type: %s", manifest.ArtifactType)
	}

	descriptor, layerData, err := ReadArtifact(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest != indexManifest.Manifests[0].Digest.String() {
		t.Fatalf("descriptor digest mismatch: %s vs %s", descriptor.Digest, indexManifest.Manifests[0].Digest)
	}
	if !bytes.Equal(layerData, chartData) {
		t.Fatalf("layer bytes differ from packaged chart")
	}
}

func TestBuildHelmChartArtifactSetsDeterministicManifestAnnotations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chartData := helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\ndescription: A demo chart\n" +
			"home: https://example.test/demo\nsources:\n  - https://github.com/example/demo\n" +
			"maintainers:\n  - name: someone\ndependencies:\n  - name: sub\n    repository: https://example.test\n    version: 1.0.0\n",
	})
	out := filepath.Join(t.TempDir(), "packaged")
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); err != nil {
		t.Fatal(err)
	}
	path, err := layout.FromPath(out)
	if err != nil {
		t.Fatal(err)
	}
	index, err := path.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := path.Bytes(indexManifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := v1.ParseManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"org.opencontainers.image.title":       "demo",
		"org.opencontainers.image.version":     "1.2.3",
		"org.opencontainers.image.description": "A demo chart",
		"org.opencontainers.image.url":         "https://example.test/demo",
	}
	for key, value := range want {
		if manifest.Annotations[key] != value {
			t.Fatalf("annotation %s = %q, want %q (all: %#v)", key, manifest.Annotations[key], value, manifest.Annotations)
		}
	}
	if _, ok := manifest.Annotations["org.opencontainers.image.created"]; ok {
		t.Fatalf("manifest carries a non-deterministic created annotation: %#v", manifest.Annotations)
	}
	if _, ok := manifest.Annotations["org.opencontainers.image.source"]; ok {
		t.Fatalf("manifest source annotation can auto-link a private GHCR package: %#v", manifest.Annotations)
	}

	// The config blob widens to the full Chart.yaml schema: maintainers and
	// dependencies survive, matching what `helm package`+`helm push` encodes.
	config, err := readBlob(ctx, out, Descriptor{MediaType: string(manifest.Config.MediaType), Digest: manifest.Config.Digest.String(), Size: manifest.Config.Size}, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded helmChartConfig
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Sources) != 1 || decoded.Sources[0] != "https://github.com/example/demo" {
		t.Fatalf("source metadata did not round-trip: %#v", decoded.Sources)
	}
	if len(decoded.Maintainers) != 1 || decoded.Maintainers[0].Name != "someone" {
		t.Fatalf("maintainers did not round-trip: %#v", decoded.Maintainers)
	}
	if len(decoded.Dependencies) != 1 || decoded.Dependencies[0].Name != "sub" || decoded.Dependencies[0].Repository != "https://example.test" {
		t.Fatalf("dependencies did not round-trip: %#v", decoded.Dependencies)
	}
}

func helmChartImage(t *testing.T, chartData []byte) (name.Digest, v1.Image) {
	t.Helper()
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "packaged")
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); err != nil {
		t.Fatal(err)
	}
	descriptor, err := PayloadDescriptor(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := v1.NewHash(descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	img, err := layout.Path(out).Image(hash)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewDigest(fmt.Sprintf("registry.example/charts/demo@%s", descriptor.Digest))
	if err != nil {
		t.Fatal(err)
	}
	return ref, img
}

func TestHelmChartTagsDeriveFromChartConfig(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	}))
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	discovery, content, err := helmChartTags(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.TagStr() != "1.2.3" || discovery.Context().Name() != ref.Context().Name() {
		t.Fatalf("discovery tag = %s, want 1.2.3", discovery.Name())
	}
	wantContent := "1.2.3-" + digest.Hex
	if content.TagStr() != wantContent || content.Context().Name() != ref.Context().Name() {
		t.Fatalf("content tag = %s, want %s", content.Name(), wantContent)
	}
}

// helm show/pull --version <version> must resolve the discovery tag against an
// unmodified real Helm client, so it must be exactly the chart's SemVer
// version with no NVKR-specific suffix.
func TestHelmChartTagsDiscoveryTagMatchesPlainVersion(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 9.9.9\n",
	}))
	discovery, _, err := helmChartTags(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.TagStr() != "9.9.9" {
		t.Fatalf("discovery tag = %s, want exactly the chart version", discovery.TagStr())
	}
}

// A plain-HTTP registry must propagate through to tag parsing exactly like it
// does for every other reference in this package; defaulting to https here
// would fail tagging after the manifest is already durably published.
func TestHelmChartTagsPropagatesPlainHTTP(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.0.0\n",
	}))
	if _, _, err := helmChartTags(ref, img, name.Insecure); err != nil {
		t.Fatal(err)
	}
}

// A SemVer build-metadata suffix is legal in Chart.yaml but illegal in an OCI
// tag; Helm's own OCI client substitutes "_" for "+", so the derived tags
// follow that spelling rather than failing closed on a valid chart.
func TestHelmChartTagsSubstitutesBuildMetadata(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3+build.5\n",
	}))
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	discovery, content, err := helmChartTags(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.TagStr() != "1.2.3_build.5" {
		t.Fatalf("discovery tag = %s", discovery.TagStr())
	}
	want := "1.2.3_build.5-" + digest.Hex
	if content.TagStr() != want || content.Context().Name() != ref.Context().Name() {
		t.Fatalf("content tag = %s, want %s", content.Name(), want)
	}
}

// Two chart builds published under the same nominal version must never
// collide on the same mutable content tag: it is a deterministic, pure
// function of the version and the exact published manifest, so different
// content always yields a different content tag (the discovery tag, exactly
// like real Helm's own mutable version tag, is unaffected and identical).
func TestHelmChartTagsContentTagIsCollisionFreeAcrossContent(t *testing.T) {
	t.Parallel()
	refA, imgA := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"demo/templates/deployment.yaml": "kind: Deployment\nfirst\n",
	}))
	refB, imgB := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"demo/templates/deployment.yaml": "kind: Deployment\nsecond\n",
	}))
	_, contentA, err := helmChartTags(refA, imgA)
	if err != nil {
		t.Fatal(err)
	}
	_, contentB, err := helmChartTags(refB, imgB)
	if err != nil {
		t.Fatal(err)
	}
	if contentA.TagStr() == contentB.TagStr() {
		t.Fatalf("distinct chart content produced the same content tag %s", contentA.TagStr())
	}
	if !strings.HasPrefix(contentA.TagStr(), "1.2.3-") || !strings.HasPrefix(contentB.TagStr(), "1.2.3-") {
		t.Fatalf("content tags = %s, %s", contentA.TagStr(), contentB.TagStr())
	}
}

// A version shaped so it would be illegal as an OCI tag (a space, an embedded
// "/") is also illegal strict SemVer, so validateHelmChartMetadata now catches
// it at chart-build time, before any registry tag is ever derived from it.
func TestBuildHelmChartArtifactRejectsAnUntaggableVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, version := range []string{"1.2.3 dev", "1.2.3/4"} {
		chartData := helmChartArchiveFixture(t, map[string]string{
			"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: " + version + "\n",
		})
		out := filepath.Join(t.TempDir(), "packaged")
		if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
			t.Fatalf("version %q: expected a fail-closed error, got %v", version, err)
		}
	}
}

// helmChartArchiveFixtureEntries builds a fixture like helmChartArchiveFixture
// but from an ordered slice, so a test can place two entries under the same
// tar name (a duplicate) or under two different top-level directories without
// Go map key uniqueness getting in the way.
func helmChartArchiveFixtureEntries(t *testing.T, entries []struct{ name, content string }) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// helmChartMetadata must fail closed on an archive whose declared chart
// identity is ambiguous: two top-level directories each claiming to be the
// chart, or the same Chart.yaml path appearing twice. Silently keeping only
// the last-seen entry would let a crafted or malformed chart archive publish
// under an identity its own bytes never uniquely declared.
func TestBuildHelmChartArtifactRejectsAmbiguousChartIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		entries []struct{ name, content string }
	}{
		{"two top-level roots, no explicit directory entries", []struct{ name, content string }{
			{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.0.0\n"},
			{"other/Chart.yaml", "apiVersion: v2\nname: other\nversion: 1.0.0\n"},
		}},
		{"duplicate Chart.yaml in one root", []struct{ name, content string }{
			{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.0.0\n"},
			{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 2.0.0\n"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "packaged")
			chartData := helmChartArchiveFixtureEntries(t, tc.entries)
			_, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData)
			if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
				t.Fatalf("expected a fail-closed error, got %v", err)
			}
		})
	}
}

func TestBuildHelmChartArtifactRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		artifactType string
		chartData    []byte
	}{
		{"not gzip", "application/vnd.nvkr.helm-chart.v1", []byte("not a tarball")},
		{"missing Chart.yaml", "application/vnd.nvkr.helm-chart.v1",
			helmChartArchiveFixture(t, map[string]string{"demo/values.yaml": "{}\n"})},
		{"incomplete Chart.yaml", "application/vnd.nvkr.helm-chart.v1",
			helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\n"})},
		{"invalid artifact type", "",
			helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.0.0\n"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "packaged")
			_, err := BuildHelmChartArtifact(ctx, out, tc.artifactType, tc.chartData)
			if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) &&
				e.ExitCode(err) != e.ExitCode(e.New(e.CodeInvalidCatalog, "expected")) {
				t.Fatalf("expected a fail-closed error, got %v", err)
			}
		})
	}
}

// A Chart.yaml just over the 1 MiB bound must be rejected before it is ever
// decoded, so a weakened or off-by-one bound would be caught here.
func TestBuildHelmChartArtifactRejectsOversizedChartYAML(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oversized := strings.Repeat("#", maxHelmChartYAMLBytes) + "\napiVersion: v2\nname: demo\nversion: 1.0.0\n"
	chartData := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": oversized})
	out := filepath.Join(t.TempDir(), "packaged")
	_, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("expected a fail-closed error for an over-budget Chart.yaml, got %v", err)
	}
}

// helmChartMetadata must apply the same authoritative rules Helm's own
// chart.Metadata.Validate enforces before a real `helm package` would accept
// this Chart.yaml, instead of accepting any non-empty name and version and
// deferring the failure to whatever consumer later runs the real Helm loader.
func TestBuildHelmChartArtifactValidatesAuthoritativeChartMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct{ name, chartYAML string }{
		{"missing apiVersion", "name: demo\nversion: 1.0.0\n"},
		{"invalid apiVersion", "apiVersion: v3\nname: demo\nversion: 1.0.0\n"},
		{"invalid type", "apiVersion: v2\nname: demo\nversion: 1.0.0\ntype: library-x\n"},
		{"non-semver version", "apiVersion: v2\nname: demo\nversion: not-a-version\n"},
		{"invalid numeric prerelease", "apiVersion: v2\nname: demo\nversion: 1.0.0-beta.01\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chartData := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": tc.chartYAML})
			out := filepath.Join(t.TempDir(), "packaged")
			_, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData)
			if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
				t.Fatalf("expected a fail-closed error, got %v", err)
			}
		})
	}
}

func TestBuildHelmChartArtifactAcceptsValidChartTypesAndAPIVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct{ name, chartYAML string }{
		{"leading v", "apiVersion: v2\nname: demo\nversion: v1.0.0\n"},
		{"abbreviated version", "apiVersion: v2\nname: demo\nversion: \"1.0\"\n"},
		{"v1 legacy chart", "apiVersion: v1\nname: demo\nversion: 1.0.0\n"},
		{"v2 application chart", "apiVersion: v2\nname: demo\nversion: 1.0.0\ntype: application\n"},
		{"v2 library chart", "apiVersion: v2\nname: demo\nversion: 1.0.0\ntype: library\n"},
		{"prerelease and build metadata", "apiVersion: v2\nname: demo\nversion: 1.0.0-rc.1+build.5\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chartData := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": tc.chartYAML})
			out := filepath.Join(t.TempDir(), "packaged")
			if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); err != nil {
				t.Fatalf("valid Chart.yaml rejected: %v", err)
			}
		})
	}
}

// SemVer places no length ceiling on a version string; helmChartTags must
// still produce tags the OCI tag grammar accepts (<=128 characters) instead
// of making every publication of such a chart fail at the tagging step.
func TestHelmChartTagsBoundsOverLengthVersion(t *testing.T) {
	t.Parallel()
	longVersion := "1.0.0-" + strings.Repeat("a", 200)
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: " + longVersion + "\n",
	}))
	discovery, content, err := helmChartTags(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.TagStr()) > maxOCITagLength {
		t.Fatalf("discovery tag length = %d, want <= %d", len(discovery.TagStr()), maxOCITagLength)
	}
	if len(content.TagStr()) > maxOCITagLength {
		t.Fatalf("content tag length = %d, want <= %d", len(content.TagStr()), maxOCITagLength)
	}
}

// tagHelmChart must push both tags on first publish, then skip re-pushing
// either tag on a rerun with identical content: both discovery and content
// are pure functions of the chart's own bytes and version, so a rerun that
// finds either tag already resolved to this manifest's digest is a no-op.
// This is what keeps a rerun working against a registry that forbids
// overwriting an already-existing tag.
func TestTagHelmChartIsIdempotentOnRerunWithIdenticalContent(t *testing.T) {
	ctx := context.Background()
	chartData := helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})
	out := filepath.Join(t.TempDir(), "packaged")
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); err != nil {
		t.Fatal(err)
	}
	descriptor, err := PayloadDescriptor(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := v1.NewHash(descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	img, err := layout.Path(out).Image(hash)
	if err != nil {
		t.Fatal(err)
	}

	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	var tagPuts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") && !strings.Contains(r.URL.Path, "/manifests/sha256:") {
			mu.Lock()
			tagPuts = append(tagPuts, r.URL.Path)
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	repository := strings.TrimPrefix(server.URL, "http://") + "/charts/demo"
	ref, err := name.NewDigest(repository+"@"+descriptor.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	remoteOptions := registryOptions(ctx, RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err := remote.Write(ref, img, remoteOptions...); err != nil {
		t.Fatal(err)
	}
	discovery, content, err := helmChartTags(ref, img, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if err := tagHelmChart(ctx, img, discovery, content, "demo", remoteOptions); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := len(tagPuts)
	mu.Unlock()
	if first != 2 {
		t.Fatalf("first publish: want 2 tag PUTs (discovery+content), got %d: %v", first, tagPuts)
	}

	if err := tagHelmChart(ctx, img, discovery, content, "demo", remoteOptions); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	second := len(tagPuts) - first
	mu.Unlock()
	if second != 0 {
		t.Fatalf("rerun: want 0 additional tag PUTs (both tags already current), got %d", second)
	}
}

// A discovery tag pointing at a stale manifest (a prior chart build under
// the same nominal version) must still move to the new manifest, matching
// real Helm's own mutable `<version>` tag semantics; only an already-current
// tag is skipped.
func TestTagHelmChartRepointsDiscoveryTagWhenStale(t *testing.T) {
	ctx := context.Background()
	buildImage := func(templateBody string) (v1.Image, Descriptor) {
		chartData := helmChartArchiveFixture(t, map[string]string{
			"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
			"demo/templates/deployment.yaml": templateBody,
		})
		out := filepath.Join(t.TempDir(), "packaged")
		if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nvkr.helm-chart.v1", chartData); err != nil {
			t.Fatal(err)
		}
		descriptor, err := PayloadDescriptor(ctx, out)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := v1.NewHash(descriptor.Digest)
		if err != nil {
			t.Fatal(err)
		}
		img, err := layout.Path(out).Image(hash)
		if err != nil {
			t.Fatal(err)
		}
		return img, descriptor
	}
	imgA, descriptorA := buildImage("kind: Deployment\nfirst\n")
	imgB, descriptorB := buildImage("kind: Deployment\nsecond\n")

	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(handler)
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/charts/demo"
	remoteOptions := registryOptions(ctx, RegistryOptions{PlainHTTP: true, Anonymous: true})

	refA, err := name.NewDigest(repository+"@"+descriptorA.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(refA, imgA, remoteOptions...); err != nil {
		t.Fatal(err)
	}
	discoveryA, contentA, err := helmChartTags(refA, imgA, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := tagHelmChart(ctx, imgA, discoveryA, contentA, "demo", remoteOptions); err != nil {
		t.Fatal(err)
	}

	refB, err := name.NewDigest(repository+"@"+descriptorB.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(refB, imgB, remoteOptions...); err != nil {
		t.Fatal(err)
	}
	discoveryB, contentB, err := helmChartTags(refB, imgB, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if discoveryB.TagStr() != discoveryA.TagStr() {
		t.Fatalf("discovery tag changed across builds under the same nominal version: %s vs %s", discoveryA.TagStr(), discoveryB.TagStr())
	}
	if err := tagHelmChart(ctx, imgB, discoveryB, contentB, "demo", remoteOptions); err != nil {
		t.Fatal(err)
	}

	moved, err := remote.Head(discoveryB, remoteOptions...)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := imgB.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if moved.Digest != wantDigest {
		t.Fatalf("discovery tag did not move to the newer manifest: got %s, want %s", moved.Digest, wantDigest)
	}
}

func TestHelmTagPublishErrorClassifiesPermanentVersusTransientFailure(t *testing.T) {
	t.Parallel()
	tag, err := name.NewTag("registry.example/charts/demo:1.2.3-abc", name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	permanent := helmTagPublishError(&transport.Error{StatusCode: http.StatusBadRequest}, tag, "demo")
	if code := e.ExitCode(permanent); code != e.ExitCode(e.New(e.CodeInvalidCatalog, "expected")) {
		t.Fatalf("4xx registry rejection classified as %v, want CodeInvalidCatalog", permanent)
	}
	transient := helmTagPublishError(&transport.Error{StatusCode: http.StatusServiceUnavailable}, tag, "demo")
	if code := e.ExitCode(transient); code != e.ExitCode(e.New(e.CodeUnavailable, "expected")) {
		t.Fatalf("5xx registry failure classified as %v, want CodeUnavailable", transient)
	}
}
