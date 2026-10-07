package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestImageIndexRejectsChildMediaMismatch(t *testing.T) {
	for _, route := range []string{"assemble", "open"} {
		t.Run(route, func(t *testing.T) {
			inputs := imageIndexInputs(t)
			if route == "assemble" {
				rewriteImageChild(t, &inputs[0], func(m *ociManifest, _ map[string]any) {
					m.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
					m.Config.MediaType = "application/vnd.docker.container.image.v1+json"
				})
				got, err := AssembleImageIndex(t.Context(), imageIndexPlatforms, inputs)
				if got != nil {
					defer got.Close()
					t.Error("mismatched child admitted")
				}
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			root := writeIndexFixture(t, assembledImageIndex(t, inputs))
			var index runnableIndex
			if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "index.json")), &index); err != nil {
				t.Fatal(err)
			}
			old := index.Manifests[0].Digest
			var manifest ociManifest
			if err := json.Unmarshal(mustReadImageFile(t, filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
			manifest.Config.MediaType = "application/vnd.docker.container.image.v1+json"
			descriptor, err := (layoutWriter{root: root}).addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			index.Manifests[0].Descriptor = descriptor
			if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))); err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			writeImageTestFile(t, filepath.Join(root, "index.json"), raw)
			got, err := OpenImageIndex(t.Context(), root, digestBytes(raw), imageIndexPlatforms)
			if got != nil {
				defer got.Close()
				t.Error("mismatched child reopened")
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestRegistryImageIndexPullBlobFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"interrupted", e.CodeUnavailable}, {"corrupt", e.CodeVerifyFailed}, {"oversized", e.CodeVerifyFailed}, {"canceled", e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publication, _, _ := registryImageIndexFixture(t)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var broken atomic.Bool
			var hits atomic.Int32
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if broken.Load() && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/product/cwe/blobs/") {
					hits.Add(1)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					if rec.Code != http.StatusOK {
						t.Errorf("fixture blob status %d", rec.Code)
						w.WriteHeader(rec.Code)
						return
					}
					body := append([]byte(nil), rec.Body.Bytes()...)
					switch tc.name {
					case "interrupted":
						w.Header().Set("Content-Length", strconv.Itoa(len(body)))
						body = body[:3]
					case "corrupt":
						body[0] ^= 1
					case "oversized":
						body = append(body, 0)
					case "canceled":
						cancel()
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(t.Context(), publication, strings.TrimPrefix(server.URL, "http://")+"/product/release", options)
			if err != nil {
				t.Fatal(err)
			}
			broken.Store(true)
			out := filepath.Join(t.TempDir(), "absent")
			_, err = Pull(ctx, ref, out, options)
			assertRegistryCode(t, err, tc.code)
			if tc.name == "interrupted" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("lost interrupted read cause: %v", err)
			}
			if hits.Load() == 0 {
				t.Fatal("aggregate blob route not exercised")
			}
			entries, readErr := os.ReadDir(filepath.Dir(out))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("partial pull exposed output: %v/%v", entries, readErr)
			}
		})
	}
}
