// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func pinRegistryTransport(t *testing.T, server *httptest.Server) http.RoundTripper {
	t.Helper()
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return imagePinRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "example.invalid" && request.URL.Host != address.Host {
			t.Fatalf("unexpected registry fixture host %s", request.URL.Host)
		}
		copy := request.Clone(request.Context())
		copy.URL.Host = address.Host
		return server.Client().Transport.RoundTrip(copy)
	})
}

func TestImagePinRealTLSRegistryNativeIndexes(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIImageIndex, types.DockerManifestList} {
		t.Run(string(media), func(t *testing.T) {
			server := httptest.NewTLSServer(registry.New())
			defer server.Close()
			transport := pinRegistryTransport(t, server)
			nativeMedia := types.OCIManifestSchema1
			if media == types.DockerManifestList {
				nativeMedia = types.DockerManifestSchema2
			}
			amd, arm := imagePinFixture(t, "amd64", nativeMedia), imagePinFixture(t, "arm64", nativeMedia)
			index := pinIndexFixture(media,
				mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
				mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}}})
			ref, err := name.NewTag("example.invalid/runtime:v1")
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.WriteIndex(ref, index, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport)); err != nil {
				t.Fatal(err)
			}
			amdDigest, err := amd.Digest()
			if err != nil {
				t.Fatal(err)
			}
			armDigest, err := arm.Digest()
			if err != nil {
				t.Fatal(err)
			}
			pins, err := resolveImageDigestsWith(t.Context(), ref.Name(), []string{"amd64", "arm64"}, func(ctx context.Context, reference name.Reference) (pinImageSource, error) {
				return readPinImageWithTransport(ctx, reference, transport)
			})
			if err != nil || !reflect.DeepEqual(pins, map[string]string{"amd64": amdDigest.String(), "arm64": armDigest.String()}) {
				t.Fatalf("native index pins: %v, %v", pins, err)
			}
		})
	}
}

func TestImagePinRealTLSRegistryUnsupportedMedia(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.example.unsupported+json")
		_, _ = w.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer server.Close()
	transport := pinRegistryTransport(t, server)
	pins, err := resolveImageDigestsWith(t.Context(), "example.invalid/runtime:v1", []string{"amd64"}, func(ctx context.Context, ref name.Reference) (pinImageSource, error) {
		return readPinImageWithTransport(ctx, ref, transport)
	})
	assertCatalogContractCode(t, err, e.CodeFetchFailed)
	if pins != nil {
		t.Fatal("unsupported media exposed pins")
	}
}
