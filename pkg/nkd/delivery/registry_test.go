package delivery

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func registryPublicationFixture(t *testing.T) (string, Result) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "tool")
	if err := os.WriteFile(file, []byte("exact executable bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(context.Background(), bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tool", Epoch: 1, Files: []BinaryFileInput{{Name: "tool", Path: file, InstallPath: "/usr/local/bin/tool", Mode: 0755}}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "publication")
	result, err := BuildPublicationRoot(context.Background(), out, PublicationRequest{Objects: []ObjectInput{{Name: "tools", Kind: "binary-bundle", Path: bundlePath, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, OS: "linux", Arch: "amd64", Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return out, result
}

func TestRegistryRoundTripPreservesRecursiveClosure(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "basic authentication"}[private], func(t *testing.T) {
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if private {
					user, password, ok := r.BasicAuth()
					if !ok || user != "fixture" || password != "fixture-password" {
						w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: !private}
			if private {
				options.Username = "fixture"
				options.Password = "fixture-password"
			}
			layout, result := registryPublicationFixture(t)
			tag := strings.TrimPrefix(server.URL, "http://") + "/release:test"
			ref, err := Push(context.Background(), layout, tag, options)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(ref, "@"+result.RootDigest) {
				t.Fatalf("published identity = %s", ref)
			}
			served, err := ResolveDigest(context.Background(), ref, options)
			if err != nil || served != result.RootDigest {
				t.Fatalf("served digest=%s %v", served, err)
			}
			destination := filepath.Join(t.TempDir(), "pulled")
			inspection, err := Pull(context.Background(), ref, destination, options)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.RootDigest != result.RootDigest {
				t.Fatal("pull changed root identity")
			}
			extracted := filepath.Join(t.TempDir(), "extracted")
			if _, err := ExtractBinaryBundle(context.Background(), destination, "tools", extracted); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(extracted, "tool"))
			if err != nil || string(raw) != "exact executable bytes" {
				t.Fatalf("recovered bytes=%q %v", raw, err)
			}
			_, err = Pull(context.Background(), ref, destination, options)
			assertRegistryCode(t, err, e.CodeImmutableViolation)
			if private {
				_, err = ResolveDigest(context.Background(), ref, RegistryOptions{PlainHTTP: true, Anonymous: true})
				assertRegistryCode(t, err, e.CodeUnavailable)
			}
		})
	}
}

func TestRegistryRejectsInvalidRequestsWithoutOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name, ref string
		options   RegistryOptions
		ctx       context.Context
		code      e.Code
	}{
		{name: "mutable tag", ref: "registry.invalid/release:tag", ctx: context.Background(), code: e.CodeUnpinnedSource},
		{name: "invalid digest", ref: "registry.invalid/release@sha256:bad", ctx: context.Background(), code: e.CodeUnpinnedSource},
		{name: "conflicting authentication", options: RegistryOptions{Anonymous: true, Username: "fixture", Password: "secret"}, ctx: context.Background(), code: e.CodeUsage},
		{name: "missing password", options: RegistryOptions{Username: "fixture"}, ctx: context.Background(), code: e.CodeUsage},
		{name: "missing username", options: RegistryOptions{Password: "fixture"}, ctx: context.Background(), code: e.CodeUsage},
		{name: "canceled", ctx: ctx, code: e.CodeTimeout},
		{name: "missing context", code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "must-not-exist")
			_, err := Pull(tc.ctx, tc.ref, out, tc.options)
			assertRegistryCode(t, err, tc.code)
			_, err = ResolveDigest(tc.ctx, tc.ref, tc.options)
			assertRegistryCode(t, err, tc.code)
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected request wrote output: %v", err)
			}
		})
	}
}

func TestRegistryUnavailableDoesNotExposePartialLayout(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	options := RegistryOptions{Anonymous: true, PlainHTTP: true}
	ref := strings.TrimPrefix(server.URL, "http://") + "/missing@sha256:" + strings.Repeat("1", 64)
	destination := filepath.Join(t.TempDir(), "missing")
	_, err := Pull(context.Background(), ref, destination, options)
	assertRegistryCode(t, err, e.CodeUnavailable)
	_, err = ResolveDigest(context.Background(), ref, options)
	assertRegistryCode(t, err, e.CodeUnavailable)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pull exposed output: %v", err)
	}
	layout, _ := registryPublicationFixture(t)
	_, err = Push(context.Background(), layout, strings.TrimPrefix(server.URL, "http://")+"/missing:tag", options)
	assertRegistryCode(t, err, e.CodeUnavailable)
}

func assertRegistryCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v, want %s", err, want)
	}
}

func TestDigestOnlyPublicationNeverWritesATag(t *testing.T) {
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	var tagWrites []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") && !strings.Contains(r.URL.Path, "/manifests/sha256:") {
			mu.Lock()
			tagWrites = append(tagWrites, r.URL.Path)
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	layout, result := registryPublicationFixture(t)
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/tool"
	ref, err := Push(t.Context(), layout, repository, RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ref != repository+"@"+result.RootDigest || len(tagWrites) != 0 {
		t.Fatalf("ref=%s tag writes=%v", ref, tagWrites)
	}
	inspection, err := Pull(t.Context(), ref, filepath.Join(t.TempDir(), "pulled"), RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err != nil || inspection.RootDigest != result.RootDigest {
		t.Fatalf("digest-only round trip: %v", err)
	}
}
