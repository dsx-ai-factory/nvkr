package delivery

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

func TestMetadataTransportExcludesPayloadAndPreservesConsumerRelease(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, arch)
			prepared := filepath.Join(t.TempDir(), "prepared")
			if _, err := BuildPublicationRoot(t.Context(), prepared, request); err != nil {
				t.Fatal(err)
			}
			full := filepath.Join(t.TempDir(), "full")
			result, err := AttestEvidence(t.Context(), prepared, full, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				return diskFixtureBundle(t, subject, document), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			inspected, err := Inspect(t.Context(), full)
			if err != nil {
				t.Fatal(err)
			}
			payload := request.Objects[0].Output.Disk.PayloadDigest
			var reads atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+payload) {
					reads.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			prefix := strings.TrimPrefix(server.URL, "http://") + "/candidate"
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := Push(t.Context(), full, prefix+"/releases-node-image", options)
			if err != nil {
				t.Fatal(err)
			}
			reads.Store(0)
			metadata := filepath.Join(t.TempDir(), "metadata")
			got, err := PullMetadata(t.Context(), ref, metadata, options)
			if err != nil {
				t.Fatal(err)
			}
			if got.RootDigest != result.RootDigest || reads.Load() != 0 {
				t.Fatal("metadata pull changed identity or downloaded payload")
			}
			if _, err := os.Stat(filepath.Join(metadata, "blobs", "sha256", strings.TrimPrefix(payload, "sha256:"))); !os.IsNotExist(err) {
				t.Fatalf("payload present: %v", err)
			}
			if _, err := Inspect(t.Context(), metadata); err == nil {
				t.Fatal("metadata issued full payload validation")
			}
			exported := filepath.Join(t.TempDir(), "exported")
			ex, err := ExportMetadata(t.Context(), full, exported)
			if err != nil || ex.RootDigest != inspected.RootDigest {
				t.Fatalf("export: %v", err)
			}
			release, err := PushMetadata(t.Context(), metadata, prefix+"/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 0 {
				t.Fatal("metadata push fetched payload")
			}
			recovered, err := Pull(t.Context(), release, filepath.Join(t.TempDir(), "consumer"), options)
			if err != nil || recovered.RootDigest != result.RootDigest {
				t.Fatalf("existing consumer: %v", err)
			}
			if reads.Load() == 0 {
				t.Fatal("consumer did not recover payload")
			}
		})
	}
}
