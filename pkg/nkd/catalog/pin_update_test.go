package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func pinFixtureDigest(value string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func sourceUpdateFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	registry := "apiVersion: nkd.dev/catalog/v3\nkind: SourceBuildRegistry\nsources:\n  source: {repo: https://example.invalid/source, constraint: 1.x}\n  second: {repo: https://example.invalid/second, constraint: 1.x}\ncomponents:\n  excluded: {platforms: [linux/amd64], fromSource: false, exclusionReason: fixture}\n"
	if err := os.WriteFile(filepath.Join(dir, RegistryFile), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source", "second"} {
		lock.Pins[name] = sourceUpdatePin("v1.0.0")
	}
	if err := WriteLockContext(t.Context(), dir, lock); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sourceUpdatePin(tag string) Pin {
	return Pin{Tag: tag, Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1733939955}
}

func readPinFixtureLock(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSourcePinUpdateTransaction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request SourcePinRequest
		code    e.Code
		changed int
		write   bool
	}{
		{name: "explicit unchanged", request: SourcePinRequest{Sources: []string{"source"}, Tag: "v1.0.0"}},
		{name: "unchanged check", request: SourcePinRequest{Sources: []string{"source"}, Tag: "v1.0.0", Check: true}},
		{name: "explicit changed", request: SourcePinRequest{Sources: []string{"source"}, Tag: "v1.1.0"}, changed: 1, write: true},
		{name: "newest default", request: SourcePinRequest{Sources: []string{"source"}}, changed: 1, write: true},
		{name: "all check", request: SourcePinRequest{Bump: true, All: true, Check: true}, changed: 2, code: e.CodeDiff},
		{name: "selected overrides all", request: SourcePinRequest{Sources: []string{"source"}, Bump: true, All: true}, changed: 1, write: true},
		{name: "missing pin selection", code: e.CodeUnsupported},
		{name: "missing bump selection", request: SourcePinRequest{Bump: true}, code: e.CodeUnsupported},
		{name: "unknown source", request: SourcePinRequest{Sources: []string{"absent"}}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			before := readPinFixtureLock(t, dir)
			request := tc.request
			request.Directory = dir
			update, err := updateSourcePins(t.Context(), request,
				func(context.Context, string) ([]string, error) { return []string{"v1.0.0", "v1.1.0"}, nil },
				func(_ context.Context, _ Source, tag string) (Pin, error) { return sourceUpdatePin(tag), nil })
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if update.Changed != tc.changed || bytes.Equal(before, readPinFixtureLock(t, dir)) == tc.write {
				t.Fatalf("update=%+v, lock changed=%v", update, !bytes.Equal(before, readPinFixtureLock(t, dir)))
			}
			if tc.request.All && len(tc.request.Sources) == 0 && len(update.Pins) == 2 && update.Pins[0].Name != "second" {
				t.Fatal("all-source order is not sorted")
			}
		})
	}
}

func TestSourcePinUpdatePreservesSequentialFailureWithoutPartialCommit(t *testing.T) {
	for _, names := range [][]string{{"source", "absent"}, {"absent", "source"}, {"source", "second"}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			before := readPinFixtureLock(t, dir)
			var calls []string
			update, err := updateSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: names, Bump: true},
				func(_ context.Context, repo string) ([]string, error) {
					calls = append(calls, repo)
					if strings.HasSuffix(repo, "second") {
						return nil, e.New(e.CodeFetchFailed, "upstream unavailable")
					}
					return []string{"v1.1.0"}, nil
				},
				func(_ context.Context, _ Source, tag string) (Pin, error) { return sourceUpdatePin(tag), nil })
			code := e.CodeInvalidCatalog
			if names[1] == "second" {
				code = e.CodeFetchFailed
			}
			assertCatalogContractCode(t, err, code)
			wantCalls := []string(nil)
			wantChanges := 0
			if names[0] == "source" {
				wantCalls = []string{"https://example.invalid/source"}
				wantChanges = 1
			}
			if names[1] == "second" {
				wantCalls = append(wantCalls, "https://example.invalid/second")
			}
			if !reflect.DeepEqual(calls, wantCalls) || update.Changed != wantChanges || !bytes.Equal(before, readPinFixtureLock(t, dir)) {
				t.Fatalf("calls=%v update=%+v", calls, update)
			}
		})
	}
}

func TestSourcePinUpdateRetainsSnapshotAcrossResolution(t *testing.T) {
	dir := sourceUpdateFixture(t)
	_, err := updateSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: []string{"source"}, Tag: "v1.1.0"}, nil,
		func(_ context.Context, _ Source, tag string) (Pin, error) {
			concurrent, loadErr := LoadLockContext(t.Context(), dir)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			concurrent.Pins["second"] = sourceUpdatePin("v1.2.0")
			if writeErr := WriteLockContext(t.Context(), dir, concurrent); writeErr != nil {
				t.Fatal(writeErr)
			}
			return sourceUpdatePin(tag), nil
		})
	assertCatalogContractCode(t, err, e.CodeWriteConflict)
	lock, err := LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Pins["source"].Tag != "v1.0.0" || lock.Pins["second"].Tag != "v1.2.0" {
		t.Fatalf("lock=%+v", lock.Pins)
	}
}

func artifactUpdateFixture(t *testing.T, url, digest string, platforms ...string) string {
	t.Helper()
	dir := t.TempDir()
	if len(platforms) == 0 {
		platforms = []string{"linux/amd64", "linux/arm64"}
	}
	registry := fmt.Sprintf("apiVersion: nkd.dev/catalog/v3\nkind: SourceBuildRegistry\nsources:\n  source: {repo: https://example.invalid/source}\ncomponents:\n  tool:\n    platforms: [%s]\n    source: source\n    outputType: raw\n    prebuilt:\n      url: %s\n      evidence: {none: true}\n", strings.Join(platforms, ", "), url)
	lock := fmt.Sprintf("apiVersion: nkd.dev/lock/v1\nkind: PinLock\npins:\n  source: {tag: v1.2.3, commit: %s, treeHash: %s, sourceDateEpoch: 1733939955}\nartifacts:\n  tool: {tag: v1.2.3, digests: {amd64: %s, arm64: %s}}\n", strings.Repeat("a", 40), strings.Repeat("b", 40), digest, digest)
	for name, raw := range map[string]string{RegistryFile: registry, LockFile: lock} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestArtifactPinUpdatePreservesNoOpBytes(t *testing.T) {
	want := pinFixtureDigest("artifact payload")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.TrimPrefix(want, "sha256:")))
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	dir := artifactUpdateFixture(t, server.URL+"/tool-{{.Arch}}", "sha256:"+strings.Repeat("a", 64))
	for _, tc := range []struct {
		name    string
		request ArtifactPinRequest
		changed int
	}{{"all", ArtifactPinRequest{All: true}, 1}, {"selected no-op", ArtifactPinRequest{Components: []string{"tool"}}, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			before := readPinFixtureLock(t, dir)
			request := tc.request
			request.Directory = dir
			update, err := UpdateArtifactPins(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if update.Changed != tc.changed || len(update.Pins) != 1 || update.Pins[0].After.Digests["amd64"] != want || update.Pins[0].After.Digests["arm64"] != want {
				t.Fatalf("update=%+v", update)
			}
			if tc.changed == 0 && !bytes.Equal(before, readPinFixtureLock(t, dir)) {
				t.Fatal("no-op changed lock bytes")
			}
		})
	}
}

func TestArtifactPinUpdateAdmitsSelectionBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request ArtifactPinRequest
		code    e.Code
	}{
		{"architecture subset", ArtifactPinRequest{Components: []string{"tool"}, Architectures: []string{"amd64"}}, e.CodeUsage},
		{"duplicate architecture", ArtifactPinRequest{Components: []string{"tool"}, Architectures: []string{"amd64", "arm64", "arm64"}}, e.CodeUsage},
		{"unsupported architecture", ArtifactPinRequest{Components: []string{"tool"}, Architectures: []string{"amd64", "s390x"}}, e.CodeUsage},
		{"unknown component", ArtifactPinRequest{Components: []string{"tool", "absent"}}, e.CodeInvalidCatalog},
		{"unknown with all", ArtifactPinRequest{All: true, Components: []string{"absent"}}, e.CodeInvalidCatalog},
		{"duplicate component", ArtifactPinRequest{Components: []string{"tool", "tool"}}, e.CodeUsage},
		{"no selection", ArtifactPinRequest{}, e.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer server.Close()
			original := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			t.Cleanup(func() { http.DefaultTransport = original })
			dir := artifactUpdateFixture(t, server.URL+"/tool-{{.Arch}}", "sha256:"+strings.Repeat("a", 64))
			before := readPinFixtureLock(t, dir)
			request := tc.request
			request.Directory = dir
			update, err := UpdateArtifactPins(t.Context(), request)
			assertCatalogContractCode(t, err, tc.code)
			if calls != 0 || len(update.Pins) != 0 || !bytes.Equal(before, readPinFixtureLock(t, dir)) {
				t.Fatal("invalid request caused work")
			}
		})
	}
}

func TestPinUpdateCanceledBeforeCatalogIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := UpdateSourcePins(ctx, SourcePinRequest{Directory: "does-not-exist", Sources: []string{"source"}})
	assertCatalogContractCode(t, err, e.CodeTimeout)
	_, err = UpdateArtifactPins(ctx, ArtifactPinRequest{Directory: "does-not-exist", All: true})
	assertCatalogContractCode(t, err, e.CodeTimeout)
}

func TestArtifactPinUpdateArchitectureOrder(t *testing.T) {
	var calls atomic.Int32
	want := pinFixtureDigest("artifact payload")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(strings.TrimPrefix(want, "sha256:")))
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, platforms := range [][]string{{"linux/amd64", "linux/arm64"}, {"linux/arm64", "linux/amd64"}} {
		for _, arches := range [][]string{{"amd64", "arm64"}, {"arm64", "amd64"}} {
			t.Run(strings.Join(platforms, "-")+"/"+strings.Join(arches, "-"), func(t *testing.T) {
				dir := artifactUpdateFixture(t, server.URL+"/tool-{{.Arch}}", "sha256:"+strings.Repeat("a", 64), platforms...)
				lockBefore := readPinFixtureLock(t, dir)
				calls.Store(0)
				before := append([]string(nil), arches...)
				update, err := UpdateArtifactPins(t.Context(), ArtifactPinRequest{Directory: dir, Components: []string{"tool"}, Architectures: arches})
				if platforms[0] == "linux/arm64" {
					assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
					if calls.Load() != 0 || len(update.Pins) != 0 || !bytes.Equal(lockBefore, readPinFixtureLock(t, dir)) {
						t.Fatal("unsorted catalog caused network or lock mutation")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				lock, err := LoadLockContext(t.Context(), dir)
				if err != nil {
					t.Fatal(err)
				}
				if update.Changed != 1 || !reflect.DeepEqual(lock.Artifacts["tool"].Digests, map[string]string{"amd64": want, "arm64": want}) {
					t.Fatalf("update=%+v, persisted pins=%+v", update, lock.Artifacts)
				}
				if !reflect.DeepEqual(arches, before) {
					t.Fatal("request architecture order mutated")
				}
			})
		}
	}
}
