package factory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
)

const helmDeploymentFixture = `# License retained
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller
spec:
  template:
    spec:
      containers:
        - name: controller
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}@{{ .Values.image.digest }}"
`

func testHelmBinding(from, chart string) *HelmImageBinding {
	return &HelmImageBinding{Chart: chart, Images: map[string]HelmImage{"controller": {From: from, Values: catalog.HelmImageValues{Digest: "image.digest"}}}}
}

func TestHelmImageBindingUpdatesChecksums(t *testing.T) {
	root := t.TempDir()
	chart := filepath.Join(root, "007-controller")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"007-controller/templates/controller.yaml": helmDeploymentFixture,
		"007-controller/values.yaml":               "image:\n  repository: old\n  tag: dev\n  digest: ''\n",
		"checksums.txt":                            "old\n",
	}
	for path, raw := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := bindHelmImages(context.Background(), root, *testHelmBinding("package/cwe", "007-controller"), map[string]string{"controller": digest}); err != nil {
		t.Fatal(err)
	}
	checksums, err := os.ReadFile(filepath.Join(root, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for path := range files {
		if path == "checksums.txt" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(checksums), fmt.Sprintf("%x  %s\n", sha256.Sum256(raw), path)) {
			t.Fatalf("unbound checksum: %s", checksums)
		}
	}
	values, err := os.ReadFile(filepath.Join(chart, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "tag: dev") || !strings.Contains(string(values), "repository: old") || !strings.Contains(string(values), digest) {
		t.Fatalf("values: %s", values)
	}
}

// Binding must not create or replace chart-owned checksum files. Wrapper
// bookkeeping is excluded from the final chart, including for nested charts.
func TestHelmBindingPreservesChartChecksums(t *testing.T) {
	for _, chart := range []string{".", "controller"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("chart=%s/existing=%t", chart, existing), func(t *testing.T) {
				stage := t.TempDir()
				root := filepath.Join(stage, "source")
				chartDir := filepath.Join(root, chart)
				if err := os.MkdirAll(chartDir, 0o755); err != nil {
					t.Fatal(err)
				}
				files := map[string]string{"Chart.yaml": "apiVersion: v2\nname: controller\nversion: 1.0.0\n", "values.yaml": "image:\n  digest: ''\n"}
				const upstream = "upstream-owned checksum manifest\n"
				if existing {
					files["checksums.txt"] = upstream
				}
				for name, content := range files {
					if err := os.WriteFile(filepath.Join(chartDir, name), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				digest := "sha256:" + strings.Repeat("a", 64)
				if err := bindHelmImages(t.Context(), root, *testHelmBinding("package/cwe", chart), map[string]string{"controller": digest}); err != nil {
					t.Fatal(err)
				}
				archive := filepath.Join(stage, "source.tar")
				if err := transportarchive.Pack(t.Context(), archive, stage, []string{"source"}); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(stage, "chart.tgz")
				if err := rerootHelmChartLayer(archive, dest, "source", filepath.ToSlash(filepath.Join("source", chart)), "controller"); err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(dest)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				gz, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				tr := tar.NewReader(gz)
				found := false
				for {
					h, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if h.Name != "controller/checksums.txt" {
						continue
					}
					found = true
					raw, err := io.ReadAll(tr)
					if err != nil {
						t.Fatal(err)
					}
					if string(raw) != upstream {
						t.Fatalf("chart checksum file overwritten: %q", raw)
					}
				}
				if found != existing {
					t.Fatalf("published checksum presence = %t, want %t", found, existing)
				}
			})
		}
	}
}
