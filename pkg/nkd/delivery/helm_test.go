package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
	result, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nkd.helm-chart.v1", chartData)
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
	if manifest.ArtifactType != "application/vnd.nkd.helm-chart.v1" {
		t.Fatalf("artifact type: %s", manifest.ArtifactType)
	}

	// Read back via the updated delivery.ReadArtifact path.
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
			"home: https://example.test/demo\nsources:\n  - https://example.test/demo.git\n" +
			"maintainers:\n  - name: someone\ndependencies:\n  - name: sub\n    repository: https://example.test\n    version: 1.0.0\n",
	})
	out := filepath.Join(t.TempDir(), "packaged")
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nkd.helm-chart.v1", chartData); err != nil {
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
		"org.opencontainers.image.source":      "https://example.test/demo.git",
	}
	for key, value := range want {
		if manifest.Annotations[key] != value {
			t.Fatalf("annotation %s = %q, want %q (all: %#v)", key, manifest.Annotations[key], value, manifest.Annotations)
		}
	}
	if _, ok := manifest.Annotations["org.opencontainers.image.created"]; ok {
		t.Fatalf("manifest carries a non-deterministic created annotation: %#v", manifest.Annotations)
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
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nkd.helm-chart.v1", chartData); err != nil {
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

func TestHelmChartVersionTagDerivesFromChartConfig(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	}))
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	tag, err := helmChartVersionTag(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	want := "1.2.3-" + digest.Hex
	if tag.TagStr() != want || tag.Context().Name() != ref.Context().Name() {
		t.Fatalf("tag = %s, want %s", tag.Name(), want)
	}
}

// A SemVer build-metadata suffix is legal in Chart.yaml but illegal in an OCI
// tag; Helm's own OCI client substitutes "_" for "+", so the derived discovery
// tag follows that spelling rather than failing closed on a valid chart.
func TestHelmChartVersionTagSubstitutesBuildMetadata(t *testing.T) {
	t.Parallel()
	ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3+build.5\n",
	}))
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	tag, err := helmChartVersionTag(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	want := "1.2.3_build.5-" + digest.Hex
	if tag.TagStr() != want || tag.Context().Name() != ref.Context().Name() {
		t.Fatalf("tag = %s, want %s", tag.Name(), want)
	}
}

// Two chart builds published under the same nominal version must never
// collide on the same mutable tag: the tag is a deterministic, pure function
// of the version and the exact published manifest, so different content
// always yields a different tag.
func TestHelmChartVersionTagIsCollisionFreeAcrossContent(t *testing.T) {
	t.Parallel()
	refA, imgA := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"demo/templates/deployment.yaml": "kind: Deployment\nfirst\n",
	}))
	refB, imgB := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
		"demo/Chart.yaml":                "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"demo/templates/deployment.yaml": "kind: Deployment\nsecond\n",
	}))
	tagA, err := helmChartVersionTag(refA, imgA)
	if err != nil {
		t.Fatal(err)
	}
	tagB, err := helmChartVersionTag(refB, imgB)
	if err != nil {
		t.Fatal(err)
	}
	if tagA.TagStr() == tagB.TagStr() {
		t.Fatalf("distinct chart content produced the same tag %s", tagA.TagStr())
	}
	if !strings.HasPrefix(tagA.TagStr(), "1.2.3-") || !strings.HasPrefix(tagB.TagStr(), "1.2.3-") {
		t.Fatalf("tags = %s, %s", tagA.TagStr(), tagB.TagStr())
	}
}

func TestHelmChartVersionTagRejectsAnUntaggableVersion(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.2.3 dev", "1.2.3/4"} {
		ref, img := helmChartImage(t, helmChartArchiveFixture(t, map[string]string{
			"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: " + version + "\n",
		}))
		if _, err := helmChartVersionTag(ref, img); e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
			t.Fatalf("version %q: expected a fail-closed error, got %v", version, err)
		}
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
		{"not gzip", "application/vnd.nkd.helm-chart.v1", []byte("not a tarball")},
		{"missing Chart.yaml", "application/vnd.nkd.helm-chart.v1",
			helmChartArchiveFixture(t, map[string]string{"demo/values.yaml": "{}\n"})},
		{"incomplete Chart.yaml", "application/vnd.nkd.helm-chart.v1",
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
