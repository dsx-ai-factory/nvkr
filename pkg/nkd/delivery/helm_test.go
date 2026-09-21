package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"path/filepath"
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

func TestHelmChartNameReadsTheChartsOwnName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chartData := helmChartArchiveFixture(t, map[string]string{
		"postgresql/Chart.yaml": "apiVersion: v2\nname: postgresql\nversion: 17.1.2\n",
	})
	out := filepath.Join(t.TempDir(), "packaged")
	if _, err := BuildHelmChartArtifact(ctx, out, "application/vnd.nkd.helm-chart.v1", chartData); err != nil {
		t.Fatal(err)
	}
	got, err := HelmChartName(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgresql" {
		t.Fatalf("got %q, want %q", got, "postgresql")
	}
}

func TestHelmChartNameRejectsNonHelmArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	image := imageFixture(t)
	if _, err := HelmChartName(ctx, image); e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("expected a fail-closed error, got %v", err)
	}
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
	tag, err := helmChartVersionTag(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if tag.TagStr() != "1.2.3" || tag.Context().Name() != ref.Context().Name() {
		t.Fatalf("tag = %s", tag.Name())
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
	tag, err := helmChartVersionTag(ref, img)
	if err != nil {
		t.Fatal(err)
	}
	if tag.TagStr() != "1.2.3_build.5" || tag.Context().Name() != ref.Context().Name() {
		t.Fatalf("tag = %s", tag.Name())
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
