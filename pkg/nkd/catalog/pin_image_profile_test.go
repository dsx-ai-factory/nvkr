package catalog

import (
	"context"
	"encoding/json"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type pinIndexInterface interface{ v1.ImageIndex }

type pinIndexBytes struct {
	pinIndexInterface
	manifest []byte
}

func (index pinIndexBytes) RawManifest() ([]byte, error) { return index.manifest, nil }

func TestImagePinNativeProfileMarkersAndBaselineAmbiguity(t *testing.T) {
	image := imagePinFixture(t, "arm64", types.OCIManifestSchema1)
	add := mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}}
	index := pinIndexFixture(types.OCIImageIndex, add)
	for _, tc := range []struct {
		name   string
		native bool
		modify func(map[string]any)
	}{
		{name: "index subject", modify: func(m map[string]any) { m["subject"] = map[string]any{} }},
		{name: "index artifact type", modify: func(m map[string]any) { m["artifactType"] = "application/vnd.example.artifact" }},
		{name: "index body media differs", modify: func(m map[string]any) { m["mediaType"] = string(types.DockerManifestList) }},
		{name: "native subject", native: true, modify: func(m map[string]any) { m["subject"] = map[string]any{} }},
		{name: "native artifact type", native: true, modify: func(m map[string]any) { m["artifactType"] = "application/vnd.example.artifact" }},
		{name: "native body media differs", native: true, modify: func(m map[string]any) { m["mediaType"] = string(types.DockerManifestSchema2) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := index.RawManifest()
			if tc.native {
				raw, err = image.RawManifest()
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			tc.modify(body)
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			source := pinImageSource{index: pinIndexBytes{pinIndexInterface: index, manifest: raw}}
			if tc.native {
				source = pinImageSource{image: pinImageBytes{Image: image, manifest: raw}}
			}
			pins, err := resolveImageDigestsWith(t.Context(), "example.invalid/runtime:v1", []string{"arm64"}, func(context.Context, name.Reference) (pinImageSource, error) { return source, nil })
			assertCatalogContractCode(t, err, e.CodeFetchFailed)
			if pins != nil {
				t.Fatal("unsupported profile exposed pins")
			}
		})
	}
	t.Run("empty and v8 both select baseline", func(t *testing.T) {
		v8 := add
		v8.Platform = &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
		pins, err := resolveImageDigestsWith(t.Context(), "example.invalid/runtime:v1", []string{"arm64"}, func(context.Context, name.Reference) (pinImageSource, error) {
			return pinImageSource{index: pinIndexFixture(types.OCIImageIndex, add, v8)}, nil
		})
		assertCatalogContractCode(t, err, e.CodeFetchFailed)
		if pins != nil {
			t.Fatal("ambiguous baseline exposed pins")
		}
	})
}
