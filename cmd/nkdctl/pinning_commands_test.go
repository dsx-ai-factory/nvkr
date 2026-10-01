package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestCatalogSourcePinProjection(t *testing.T) {
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")
	unpinned := t.TempDir()
	registry, err := os.ReadFile(filepath.Join(data, catalog.RegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpinned, catalog.RegistryFile), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpinned, catalog.LockFile),
		[]byte("apiVersion: nkd.dev/lock/v1\nkind: PinLock\npins: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		args     []string
		wantCode e.Code
		commit   string
		tree     string
	}{
		{
			name:   "validated source",
			args:   []string{"nkdctl", "catalog", "source-pin", "--data", data, "kubernetes"},
			commit: "70d3cc986aa8221cd1dfb1121852688902d3bf53",
			tree:   "195abcc8a74b362b88fc76b0ee676a3354e4937e",
		},
		{
			name:     "unknown source",
			args:     []string{"nkdctl", "catalog", "source-pin", "--data", data, "missing"},
			wantCode: e.CodeInvalidCatalog,
		},
		{
			name:     "unvalidated missing pin",
			args:     []string{"nkdctl", "catalog", "source-pin", "--data", unpinned, "kubernetes"},
			wantCode: e.CodeUnpinnedSource,
		},
		{
			name:     "missing source argument",
			args:     []string{"nkdctl", "catalog", "source-pin", "--data", data},
			wantCode: e.CodeUsage,
		},
		{
			name:     "too many source arguments",
			args:     []string{"nkdctl", "catalog", "source-pin", "--data", data, "kubernetes", "extra"},
			wantCode: e.CodeUsage,
		},
		{
			name:     "catalog load failure",
			args:     []string{"nkdctl", "catalog", "source-pin", "--data", filepath.Join(t.TempDir(), "missing"), "kubernetes"},
			wantCode: e.CodeInvalidCatalog,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runCLIWithCapturedStdout(t, newApp(), test.args...)
			if test.wantCode != "" {
				assertLifecycleCommandCode(t, err, test.wantCode)
				if len(output) != 0 {
					t.Fatalf("failed source-pin produced output: %s", output)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Commit   string `json:"commit"`
				TreeHash string `json:"treeHash"`
			}
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("source-pin output is not JSON: %v: %s", err, output)
			}
			if got.Commit != test.commit || got.TreeHash != test.tree {
				t.Fatalf("source pin = %+v, want commit %q tree %q", got, test.commit, test.tree)
			}
		})
	}
}

func TestCatalogSourcePinCommandOutput(t *testing.T) {
	repo, data := t.TempDir(), t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.invalid/pin-source")
	t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Pin Test")
	git("config", "user.email", "pin@example.invalid")
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	git("tag", "v1.0.0")
	git("tag", "v1.1.0")
	registry := "apiVersion: nkd.dev/catalog/v3\nkind: SourceBuildRegistry\nsources:\n  source: {repo: https://example.invalid/pin-source, constraint: 1.x}\ncomponents:\n  excluded: {platforms: [linux/amd64], fromSource: false, exclusionReason: fixture}\n"
	if err := os.WriteFile(filepath.Join(data, catalog.RegistryFile), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		return runCLIWithCapturedStdout(t, newApp(), append([]string{"nkdctl", "catalog"}, args...)...)
	}
	readLock := func() []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(data, catalog.LockFile))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err := invoke("pin", "--dir", data, "--tag", "v1.0.0", "source"); err != nil {
		t.Fatal(err)
	}
	before := readLock()
	out, err := invoke("bump", "--dir", data, "--all", "--check")
	assertLifecycleCommandCode(t, err, e.CodeDiff)
	if !bytes.Equal(before, readLock()) || !strings.Contains(string(out), "v1.1.0") {
		t.Fatal("dry run changed lock or failed to report newer tag")
	}
	if _, err := invoke("bump", "--dir", data, "source"); err != nil {
		t.Fatal(err)
	}
	before = readLock()
	out, err = invoke("pin", "--dir", data, "source")
	if err != nil || !strings.Contains(string(out), "no changes") || !bytes.Equal(before, readLock()) {
		t.Fatalf("unchanged pin: %s, %v", out, err)
	}
}

func TestCatalogArtifactPinCommandOutput(t *testing.T) {
	const payload = "new binary payload"
	want := pipeline.FileDigest([]byte(payload))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(strings.TrimPrefix(want, "sha256:")))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	data := acquisitionCatalogFixture(t, server.URL+"/tool-{{.Arch}}", "sha256:"+strings.Repeat("a", 64))
	for _, selection := range [][]string{{"--all"}, {"tool"}} {
		out, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nkdctl", "catalog", "pin-artifacts", "--dir", data}, selection...)...)
		if err != nil {
			t.Fatal(err)
		}
		if selection[0] == "tool" && !strings.Contains(string(out), "no changes") {
			t.Fatalf("unchanged pin: %s", out)
		}
	}
	_, err := runCLIWithCapturedStdout(t, newApp(), "nkdctl", "catalog", "pin-artifacts", "--dir", data)
	assertLifecycleCommandCode(t, err, e.CodeUnsupported)
}

func TestCatalogArtifactPinCommandRejectsArchitectureSubset(t *testing.T) {
	data := acquisitionCatalogFixture(t, "https://must-not-be-contacted.invalid/tool-{{.Arch}}", "sha256:"+strings.Repeat("a", 64))
	before, err := os.ReadFile(filepath.Join(data, catalog.LockFile))
	if err != nil {
		t.Fatal(err)
	}
	output, err := runCLIWithCapturedStdout(t, newApp(), "nkdctl", "catalog", "pin-artifacts", "--dir", data, "--arch", "amd64", "tool")
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	after, err := os.ReadFile(filepath.Join(data, catalog.LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 || !bytes.Equal(before, after) {
		t.Fatal("invalid command produced output or changed lock")
	}
}
