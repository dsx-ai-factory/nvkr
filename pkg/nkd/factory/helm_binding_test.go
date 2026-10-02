package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
          image: "registry.example.test/old:dev"
`

func TestHelmDeploymentImageBinding(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, raw string
		reject    bool
	}{
		{"one deployment", helmDeploymentFixture, false},
		{"missing deployment", "kind: Service\n", true},
		{"two deployments", helmDeploymentFixture + "\n---\n" + helmDeploymentFixture, true},
		{"commented separator", helmDeploymentFixture + "\n--- # another document\n" + helmDeploymentFixture, true},
		{"two containers", helmDeploymentFixture + "        - name: sidecar\n          image: other:dev\n", true},
		{"template input", strings.ReplaceAll(helmDeploymentFixture, "registry.example.test/old:dev", "{{ .Values.image }}"), true},
		{"duplicate YAML key", helmDeploymentFixture + "          image: other:dev\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, err := bindHelmDeployment([]byte(tc.raw), digest)
			if tc.reject {
				if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
					t.Fatalf("invalid binding: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(bound), digest) || !strings.Contains(string(bound), "# License retained") || strings.Contains(string(bound), "old:dev") || !strings.Contains(string(bound), "required") {
				t.Fatalf("binding: %s", bound)
			}
		})
	}
}

func TestHelmImageBindingUpdatesChecksums(t *testing.T) {
	root := t.TempDir()
	chart := filepath.Join(root, "007-controller")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"007-controller/templates/controller.yaml": helmDeploymentFixture,
		"007-controller/values.yaml":               "image:\n  repository: old\n  tag: dev\n  digest: ''\npullPolicy: Always\n",
		"checksums.txt":                            "old\n",
	}
	for path, raw := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := bindHelmImage(context.Background(), root, "007-controller", digest, nil); err != nil {
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
	if strings.Contains(string(values), "tag:") || !strings.Contains(string(values), `repository: ""`) || !strings.Contains(string(values), digest) {
		t.Fatalf("values: %s", values)
	}
}

func TestHelmImageBindingAppliesValuesBeforeChecksums(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "007-controller", "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string]string{
		"007-controller/templates/controller.yaml": helmDeploymentFixture,
		"007-controller/values.yaml":               "image:\n  repository: old\n  tag: dev\n  digest: ''\npullPolicy: Always\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	overrides := []HelmValueOverride{{Path: "pullPolicy", Value: "IfNotPresent"}}
	if err := bindHelmImage(context.Background(), root, "007-controller", digest, overrides); err != nil {
		t.Fatal(err)
	}
	values, err := os.ReadFile(filepath.Join(root, "007-controller", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	checksums, err := os.ReadFile(filepath.Join(root, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "pullPolicy: IfNotPresent") || !strings.Contains(string(values), `repository: ""`) || !strings.Contains(string(values), digest) ||
		!strings.Contains(string(checksums), fmt.Sprintf("%x  007-controller/values.yaml\n", sha256.Sum256(values))) {
		t.Fatalf("values %s checksums %s", values, checksums)
	}
	for _, path := range []string{"image.digest", "image.tag", "image.repository"} {
		err := bindHelmImage(context.Background(), root, "007-controller", digest, []HelmValueOverride{{Path: path, Value: "x"}})
		assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		assertErrorContains(t, err, "verified image binding")
	}
	before, err := os.ReadFile(filepath.Join(root, "007-controller", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "007-controller", "templates", "controller.yaml"), []byte(helmDeploymentFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	err = bindHelmImage(context.Background(), root, "007-controller", digest, []HelmValueOverride{{Path: "unknown", Value: "x"}})
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	assertErrorContains(t, err, "does not exist")
	after, err := os.ReadFile(filepath.Join(root, "007-controller", "values.yaml"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("values changed on rejected binding: %v\n%s\n%s", err, before, after)
	}
}
