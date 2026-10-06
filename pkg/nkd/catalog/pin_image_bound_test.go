package catalog

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestSelectedLayerMaxInt64Bound(t *testing.T) {
	image := imagePinFixture(t, "amd64", types.OCIManifestSchema1)
	manifest, err := image.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	layer := v1.Descriptor{MediaType: types.OCILayer, Size: math.MaxInt64, Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}}
	manifest.Layers = []v1.Descriptor{layer}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	selected := pinImageBytes{Image: image, manifest: raw}
	if _, _, _, err := authenticatePinImage(selected, "amd64"); err != nil {
		t.Fatal(err)
	}
	transport := pinImageTransport{RoundTripper: imagePinRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})}
	if err := transport.admitImageLayers(selected); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.invalid/v2/runtime/blobs/"+layer.Digest.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	// No enormous stream or allocation: exercise the actual descriptor-selected
	// transport and first-byte arithmetic, not complete-closure acceptance.
	buffer := make([]byte, 1)
	if n, err := response.Body.Read(buffer); n != 1 || err != nil || buffer[0] != 'x' {
		t.Fatalf("representable bound read = %d, %q, %v", n, buffer, err)
	}
}

func TestBoundedBodyRepeatedReadAfterLimit(t *testing.T) {
	for _, limit := range []int64{0, 4} {
		for _, label := range []string{"", "authenticated image layer"} {
			t.Run(strings.Repeat("x", int(limit))+"/"+label, func(t *testing.T) {
				body := &helmBoundedBody{ReadCloser: io.NopCloser(strings.NewReader("123456789")), remaining: limit, label: label}
				defer body.Close()
				for attempt := range 3 {
					if n, err := body.Read(make([]byte, 8)); n != 0 || err == nil || !strings.Contains(err.Error(), "response exceeds size bound") {
						t.Fatalf("attempt %d after limit: %d, %v", attempt, n, err)
					}
				}
			})
		}
	}
}
