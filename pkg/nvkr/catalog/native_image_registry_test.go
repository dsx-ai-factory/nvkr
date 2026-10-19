// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Controlled TLS registry protocol proof, not an upstream production build or
// signature claim. The layer intentionally exceeds the metadata admission bound.
func TestNativeImageSelectionMaterializesOriginalLargeLayer(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(string(media)+"/"+arch, func(t *testing.T) {
				server := httptest.NewTLSServer(registry.New())
				defer server.Close()
				transport := pinRegistryTransport(t, server)
				layerMedia := types.OCIUncompressedLayer
				if media == types.DockerManifestSchema2 {
					layerMedia = types.DockerUncompressedLayer
				}
				var payload bytes.Buffer
				writer := tar.NewWriter(&payload)
				data := bytes.Repeat([]byte("x"), maxPinImageMetadataBytes+1024)
				if err := writer.WriteHeader(&tar.Header{Name: "fixture", Mode: 0600, Size: int64(len(data))}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				image, err := mutate.AppendLayers(imagePinFixture(t, arch, media), static.NewLayer(payload.Bytes(), layerMedia))
				if err != nil {
					t.Fatal(err)
				}
				indexMedia := types.OCIImageIndex
				if media == types.DockerManifestSchema2 {
					indexMedia = types.DockerManifestList
				}
				index := pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}}})
				parent, err := index.Digest()
				if err != nil {
					t.Fatal(err)
				}
				child, err := image.Digest()
				if err != nil {
					t.Fatal(err)
				}
				ref, err := name.NewTag("example.invalid/runtime:v1")
				if err != nil {
					t.Fatal(err)
				}
				if err := remote.WriteIndex(ref, index, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport)); err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(t.TempDir(), "native")
				err = withNativeImage(t.Context(), ref.Context().Digest(parent.String()).Name(), child.String(), "linux/"+arch,
					func(ctx context.Context, reference name.Reference) (pinImageSource, error) {
						return readPinImageWithTransport(ctx, reference, transport)
					},
					func(ctx context.Context, selection *NativeImageSelection) error {
						return selection.Consume(ctx, func(_ context.Context, selected v1.Image) error {
							root, err := layout.Write(destination, empty.Index)
							if err != nil {
								return err
							}
							return root.AppendImage(selected)
						})
					})
				if err != nil {
					t.Fatal(err)
				}
				root, err := layout.FromPath(destination)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := root.Image(child)
				if err != nil {
					t.Fatal(err)
				}
				actualRaw, err := actual.RawManifest()
				if err != nil {
					t.Fatal(err)
				}
				originalRaw, err := image.RawManifest()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(actualRaw, originalRaw) {
					t.Fatal("materialization changed authenticated native manifest bytes")
				}
				layers, err := actual.Layers()
				if err != nil {
					t.Fatal(err)
				}
				got, err := layers[len(layers)-1].Digest()
				if err != nil {
					t.Fatal(err)
				}
				expectedLayers, err := image.Layers()
				if err != nil {
					t.Fatal(err)
				}
				want, err := expectedLayers[len(expectedLayers)-1].Digest()
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatal("materialization replaced selected native layer")
				}
			})
		}
	}
}
