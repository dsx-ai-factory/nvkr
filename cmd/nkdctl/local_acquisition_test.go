package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func acquisitionCatalogFixture(t *testing.T, url, digest string) string {
	t.Helper()
	root := t.TempDir()
	registry := fmt.Sprintf(`apiVersion: nkd.dev/catalog/v1
kind: SourceBuildRegistry
sources:
  source: {repo: https://example.invalid/source}
recipes:
  - {id: tools, criteria: {category: kubernetes}, components: [tool]}
components:
  tool:
    source: source
    roles: [worker]
    outputType: raw
    prebuilt:
      url: %s
      evidence: {none: true}
`, url)
	lock := fmt.Sprintf(`apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  source: {tag: v1.2.3, commit: %s, treeHash: %s, sourceDateEpoch: 1733939955}
artifacts:
  tool: {tag: v1.2.3, digests: {amd64: %s, arm64: %s}}
`, strings.Repeat("a", 40), strings.Repeat("b", 40), digest, digest)
	for name, text := range map[string]string{"registry.yaml": registry, "pins.lock.yaml": lock} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLocalAcquisitionCommandsUsePinnedPrebuiltBytes(t *testing.T) {
	const payload = "fixture executable bytes"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) }))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	digest := pipeline.FileDigest([]byte(payload))
	catalog := acquisitionCatalogFixture(t, server.URL+"/tool", digest)
	for _, tc := range []struct {
		name string
		args []string
		want string
		code e.Code
	}{
		{"fetch", []string{"fetch"}, digest, ""},
		{"fetch explicit", []string{"fetch", "--only", "tool"}, digest, ""},
		{"fetch dry run", []string{"fetch", "--dry-run"}, "dry-run", ""},
		{"build", []string{"build"}, digest, ""},
		{"build dry run", []string{"build", "--dry-run"}, "dry-run", ""},
		{"build role", []string{"build", "--role", "worker"}, digest, ""},
		{"build explicit", []string{"build", "--only", "tool"}, digest, ""},
		{"unknown fetch component", []string{"fetch", "--only", "absent"}, "", e.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"nkdctl"}, tc.args...)
			args = append(args, "--data", catalog, "--category", "kubernetes", "--k8s", "1.32.0", "--os", "ubuntu", "--arch", "amd64", "--source-root", filepath.Join(t.TempDir(), "src"))
			out, err := runCLIWithCapturedStdout(t, newApp(), args...)
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("output omitted %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestResolveOutputFormsShareRecipeIdentity(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	invoke := func(format string) []byte {
		t.Helper()
		out, err := runCLIWithCapturedStdout(t, newApp(), "nkdctl", "resolve", "--data", data, "--category", "kubernetes", "--k8s", "1.32.0", "--os", "ubuntu", "--arch", "amd64", "-o", format)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	digest := strings.TrimSpace(string(invoke("digest")))
	if !canonical.IsDigest(digest) {
		t.Fatalf("digest = %q", digest)
	}
	var graph ir.Graph
	if err := json.Unmarshal(invoke("nodes"), &graph); err != nil {
		t.Fatal(err)
	}
	computed, err := graph.Digest()
	if err != nil || computed != digest {
		t.Fatalf("graph digest = %s, %v", computed, err)
	}
	view, err := graph.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := canonical.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(invoke("canonical"))) != string(expected) {
		t.Fatal("canonical output differs from recipe identity view")
	}
	for _, format := range []string{"ir", "components"} {
		out := string(invoke(format))
		if !strings.Contains(out, digest) || !strings.Contains(out, "containerd") {
			t.Fatalf("%s output lacks identity or members: %s", format, out)
		}
	}
}
