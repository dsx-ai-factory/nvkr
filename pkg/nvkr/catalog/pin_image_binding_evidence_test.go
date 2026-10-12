// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"sigs.k8s.io/yaml"
)

// The current metadata owner already authenticates digest-requested parent bytes
// independently of mutable tag lookup. This is not runtime materialization proof.
func TestImagePinExactParentSurvivesTagMovement(t *testing.T) {
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
				mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}})
			ref, err := name.NewTag("example.invalid/runtime:v1")
			if err != nil {
				t.Fatal(err)
			}
			options := []remote.Option{remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport)}
			if err := remote.WriteIndex(ref, index, options...); err != nil {
				t.Fatal(err)
			}
			parent, err := index.Digest()
			if err != nil {
				t.Fatal(err)
			}
			// Move the same tag to a different, incomplete native set. The old
			// immutable parent remains the only accepted acquisition description.
			if err := remote.WriteIndex(ref, pinIndexFixture(media,
				mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}}), options...); err != nil {
				t.Fatal(err)
			}
			wanted := map[string]string{}
			for arch, image := range map[string]v1.Image{"amd64": amd, "arm64": arm} {
				digest, err := image.Digest()
				if err != nil {
					t.Fatal(err)
				}
				wanted[arch] = digest.String()
			}
			parentRef := ref.Context().Name() + "@" + parent.String()
			pins, err := resolveImageDigestsWith(t.Context(), parentRef, []string{"amd64", "arm64"},
				func(ctx context.Context, requested name.Reference) (pinImageSource, error) {
					if requested.Name() != parentRef {
						t.Fatalf("mutable tag substituted for parent: %s", requested.Name())
					}
					return readPinImageWithTransport(ctx, requested, transport)
				})
			if err != nil || !reflect.DeepEqual(pins, wanted) {
				t.Fatalf("exact parent native subjects: pins=%v error=%v", pins, err)
			}
		})
	}
}

// Opt-in external metadata read-back uses the existing anonymous HTTPS owner.
// It never writes a lock, fetches image layers, or grants source/release approval.
func TestMaintainedM2ImageParentReadback(t *testing.T) {
	if os.Getenv("NVKR_TEST_M2_IMAGE_PARENT_READBACK") != "1" {
		t.Skip("explicit external metadata read-back was not requested")
	}
	provider := Dir(filepath.Join("..", "..", "..", "definitions", "nvke-m2", "image-catalog")).(ContextDataProvider)
	raw, err := provider.ReadFileContext(t.Context(), RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	var registryDocument struct {
		Components map[string]struct {
			Prebuilt struct{ Image string }
		}
	}
	if err := yaml.Unmarshal(raw, &registryDocument); err != nil {
		t.Fatal(err)
	}
	raw, err = provider.ReadFileContext(t.Context(), LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var lock PinLock
	if err := yaml.UnmarshalStrict(raw, &lock); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"postgresql-image", "spire-bash-image"} {
		t.Run(component, func(t *testing.T) {
			ref := registryDocument.Components[component].Prebuilt.Image
			parsed, err := name.ParseReference(ref)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := parsed.(name.Digest); !ok {
				t.Fatalf("maintained parent is not exact: %s", ref)
			}
			pins, err := resolveImageDigestsWith(t.Context(), ref, []string{"amd64", "arm64"}, readPinImage)
			if err != nil || !reflect.DeepEqual(pins, lock.Artifacts[component].Digests) {
				t.Fatalf("upstream metadata differs from original locked native subjects; no repin: pins=%v error=%v", pins, err)
			}
			observed, err := json.Marshal(struct {
				Parent string            `json:"parent"`
				Native map[string]string `json:"native"`
			}{ref, pins})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("authenticated anonymous metadata read-back: %s", observed)
		})
	}
}
