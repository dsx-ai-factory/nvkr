package lifecycle

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestSeparateRepositoryPublicationAndConsumption(t *testing.T) {
	layout, digest := testPublicationLayout(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	outputDigest := ""
	for _, object := range inspection.Root.Objects {
		if object.Output != nil {
			outputDigest = object.DescriptorDigest
		}
	}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	mode := ""
	writes := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
			writes[r.URL.Path] = true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/product/tools/manifests/"+outputDigest {
			switch mode {
			case "missing":
				w.WriteHeader(http.StatusNotFound)
				return
			case "denied":
				w.WriteHeader(http.StatusForbidden)
				return
			case "substituted":
				w.Header().Set("Content-Type", delivery.MediaOCIManifest)
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	target := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	options := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), layout, target, options)
	if err != nil {
		t.Fatal(err)
	}
	if reference != target+"@"+digest {
		t.Fatal("release identity changed", reference)
	}
	mu.Lock()
	if !writes["/v2/product/tools/manifests/"+outputDigest] || writes["/v2/product/releases/manifests/"+outputDigest] {
		t.Error("deliverable was not isolated in its own repository", writes)
	}
	if !writes["/v2/product/releases/manifests/"+digest] {
		t.Error("missing release entry point")
	}
	mu.Unlock()
	for _, tc := range []struct {
		mode string
		code e.Code
	}{{"", ""}, {"missing", e.CodeUnavailable}, {"denied", e.CodeUnavailable}, {"substituted", e.CodeUnavailable}} {
		t.Run(tc.mode, func(t *testing.T) {
			mu.Lock()
			mode = tc.mode
			mu.Unlock()
			out := filepath.Join(t.TempDir(), "pulled")
			actual, err := delivery.Pull(t.Context(), reference, out, options)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("failed pull exposed partial closure", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			artifacts, _, _, err := artifactsFromBinding(t.Context(), out, actual)
			if err != nil {
				t.Fatal(err)
			}
			if len(artifacts) != 1 || artifacts[0].Digest != outputDigest {
				t.Fatal("wrong reconstructed deliverable", artifacts)
			}
		})
	}
}

func TestRepositoryCollisionFailsBeforePublishing(t *testing.T) {
	layout, _ := testPublicationLayout(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
	defer server.Close()
	_, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/tools", delivery.RegistryOptions{Anonymous: true, PlainHTTP: true})
	assertCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("repository collision reached registry")
	}
}
