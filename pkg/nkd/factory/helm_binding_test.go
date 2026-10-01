package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
