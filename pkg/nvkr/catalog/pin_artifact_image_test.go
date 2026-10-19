// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func resolveImageDigestsWith(ctx context.Context, ref string, arches []string, read pinImageReader) (map[string]string, error) {
	return resolveImageBindingWith(ctx, ref, arches, read, nil)
}

func imagePinFixture(t *testing.T, arch string, media types.MediaType) v1.Image {
	t.Helper()
	image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: arch})
	if err != nil {
		t.Fatal(err)
	}
	return mutate.MediaType(image, media)
}

func pinIndexFixture(media types.MediaType, adds ...mutate.IndexAddendum) v1.ImageIndex {
	return mutate.IndexMediaType(mutate.AppendManifests(empty.Index, adds...), media)
}

func TestImagePinResolutionSelectsRequestedLinuxManifests(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		t.Run(string(media), func(t *testing.T) {
			amd := imagePinFixture(t, "amd64", media)
			arm := imagePinFixture(t, "arm64", media)
			amdDigest, _ := amd.Digest()
			armDigest, _ := arm.Digest()
			indexMedia := types.OCIImageIndex
			if media == types.DockerManifestSchema2 {
				indexMedia = types.DockerManifestList
			}
			amdAdd := mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}}
			armAdd := mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}}}
			index := pinIndexFixture(indexMedia, amdAdd, armAdd)
			indexDigest, _ := index.Digest()
			for _, tc := range []struct {
				name                 string
				source               pinImageSource
				arches               []string
				ref                  string
				want                 map[string]string
				code                 e.Code
				canceled, readFailed bool
			}{
				{name: "both", source: pinImageSource{index: index}, arches: []string{"arm64", "amd64"}, want: map[string]string{"amd64": amdDigest.String(), "arm64": armDigest.String()}},
				{name: "subset", source: pinImageSource{index: index}, arches: []string{"arm64"}, want: map[string]string{"arm64": armDigest.String()}},
				{name: "digest-qualified index", ref: "example.invalid/image@" + indexDigest.String(), source: pinImageSource{index: index}, arches: []string{"arm64"}, want: map[string]string{"arm64": armDigest.String()}},
				{name: "digest-qualified native", ref: "example.invalid/image@" + armDigest.String(), source: pinImageSource{image: arm}, arches: []string{"arm64"}, want: map[string]string{"arm64": armDigest.String()}},
				{name: "substituted parent", ref: "example.invalid/image@sha256:" + strings.Repeat("a", 64), source: pinImageSource{index: index}, arches: []string{"arm64"}, code: e.CodeFetchFailed},
				{name: "native cannot cover both", source: pinImageSource{image: amd}, arches: []string{"amd64", "arm64"}, code: e.CodeFetchFailed},
				{name: "malformed native", source: pinImageSource{image: pinImageBytes{Image: amd, manifest: []byte("{")}}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "oversized native", source: pinImageSource{image: pinImageBytes{Image: amd, manifest: []byte(strings.Repeat("x", maxPinImageMetadataBytes+1))}}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "substituted config", source: pinImageSource{image: pinImageBytes{Image: amd, config: []byte(`{"architecture":"arm64","os":"linux"}`)}}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "oversized config", source: pinImageSource{image: pinImageBytes{Image: amd, config: []byte(strings.Repeat("x", maxPinImageMetadataBytes+1))}}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "non-image config", source: pinImageSource{image: mutate.ConfigMediaType(amd, types.MediaType("application/vnd.oci.empty.v1+json"))}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "missing architecture", source: pinImageSource{index: pinIndexFixture(indexMedia, amdAdd)}, arches: []string{"amd64", "arm64"}, code: e.CodeFetchFailed},
				{name: "unsupported architecture", source: pinImageSource{index: index}, arches: []string{"s390x"}, code: e.CodeUsage},
				{name: "duplicate native", source: pinImageSource{index: pinIndexFixture(indexMedia, amdAdd, amdAdd)}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "lying platform", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: arm, Descriptor: amdAdd.Descriptor})}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "wrong descriptor size", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: amdAdd.Platform, Size: 1}})}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "wrong descriptor media", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: amdAdd.Platform, MediaType: types.OCIImageIndex}})}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "nested native index", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: index, Descriptor: amdAdd.Descriptor})}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "variant not baseline", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v9"}}})}, arches: []string{"arm64"}, code: e.CodeFetchFailed},
				{name: "OS version constraint", source: pinImageSource{index: pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64", OSVersion: "custom"}}})}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "empty source", arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "ambiguous source", source: pinImageSource{image: amd, index: index}, arches: []string{"amd64"}, code: e.CodeFetchFailed},
				{name: "read failure", arches: []string{"amd64"}, readFailed: true, code: e.CodeFetchFailed},
				{name: "canceled", arches: []string{"amd64"}, canceled: true, code: e.CodeTimeout},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if tc.canceled {
						cancel()
					}
					if tc.ref == "" {
						tc.ref = "example.invalid/image:v1"
					}
					read := func(context.Context, name.Reference) (pinImageSource, error) {
						if tc.readFailed {
							return pinImageSource{}, errors.New("fixture registry failure")
						}
						return tc.source, nil
					}
					got, err := resolveImageDigestsWith(ctx, tc.ref, tc.arches, read)
					if tc.code != "" {
						assertCatalogContractCode(t, err, tc.code)
						if got != nil {
							t.Fatalf("failed resolution exposed partial pins: %v", got)
						}
						return
					}
					if err != nil || !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("pins=%v,%v; want %v", got, err, tc.want)
					}
				})
			}
		})
	}
}

type pinImageBytes struct {
	v1.Image
	manifest, config []byte
}

func (image pinImageBytes) RawManifest() ([]byte, error) {
	if image.manifest != nil {
		return image.manifest, nil
	}
	return image.Image.RawManifest()
}

func (image pinImageBytes) RawConfigFile() ([]byte, error) {
	if image.config != nil {
		return image.config, nil
	}
	return image.Image.RawConfigFile()
}

func TestImagePinCancellationAfterReadReturnsNoPins(t *testing.T) {
	image := imagePinFixture(t, "amd64", types.OCIManifestSchema1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	got, err := resolveImageDigestsWith(ctx, "temporalio/ui:2.48.1", []string{"amd64"}, func(_ context.Context, ref name.Reference) (pinImageSource, error) {
		if ref.Name() != "index.docker.io/temporalio/ui:2.48.1" {
			t.Fatalf("normalized ref %q", ref.Name())
		}
		cancel()
		return pinImageSource{image: image}, nil
	})
	assertCatalogContractCode(t, err, e.CodeTimeout)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pins=%v,error=%v", got, err)
	}
}

type imagePinRoundTripper func(*http.Request) (*http.Response, error)

func (f imagePinRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestImagePinTransportBoundsMetadataBeforeParsing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int64
		body     string
		insecure bool
		message  string
	}{
		{name: "oversized declared response", length: maxPinImageMetadataBytes + 1, message: "image pin metadata response exceeds size bound"},
		{name: "oversized streaming response", length: -1, body: strings.Repeat("x", maxPinImageMetadataBytes+1), message: "image pin metadata response exceeds size bound"},
		{name: "plain HTTP rejected", insecure: true, message: "must use HTTPS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := "https"
			if tc.insecure {
				scheme = "http"
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, scheme+"://example.invalid/v2/image/manifests/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			transport := pinImageTransport{RoundTripper: imagePinRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			response, err := transport.RoundTrip(request)
			if err == nil {
				defer response.Body.Close()
				_, err = io.ReadAll(response.Body)
			}
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong metadata/transport guard: %v, want %q", err, tc.message)
			}
		})
	}
}
