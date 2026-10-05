package delivery

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Construct actual Docker layer encodings through the pinned upstream owner,
// rather than merely relabeling an OCI image's manifest and config.
func nativeDockerLayerFixture(t *testing.T, media types.MediaType) string {
	t.Helper()
	source := imageFixture(t)
	index, err := layout.Path(source).ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	image, err := index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	adds := make([]mutate.Addendum, 0, len(layers))
	for _, original := range layers {
		var layer v1.Layer
		if media == types.DockerUncompressedLayer {
			reader, err := original.Uncompressed()
			if err != nil {
				t.Fatal(err)
			}
			raw, readErr := io.ReadAll(reader)
			if err := errors.Join(readErr, reader.Close()); err != nil {
				t.Fatal(err)
			}
			layer = static.NewLayer(raw, media)
		} else {
			layer, err = tarball.LayerFromOpener(original.Uncompressed, tarball.WithMediaType(media))
			if err != nil {
				t.Fatal(err)
			}
		}
		adds = append(adds, mutate.Addendum{Layer: layer, MediaType: media})
	}
	image, err = mutate.Append(empty.Image, adds...)
	if err != nil {
		t.Fatal(err)
	}
	image, err = mutate.ConfigFile(image, configuration)
	if err != nil {
		t.Fatal(err)
	}
	image = mutate.MediaType(image, types.DockerManifestSchema2)
	image = mutate.ConfigMediaType(image, types.DockerConfigJSON)
	out := filepath.Join(t.TempDir(), "docker")
	writer, err := layout.Write(out, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNativeDockerLayerImportAndPublicationPreserveExactBytes(t *testing.T) {
	t.Parallel()
	for _, media := range []types.MediaType{types.DockerLayer, types.DockerUncompressedLayer} {
		t.Run(string(media), func(t *testing.T) {
			source := nativeDockerLayerFixture(t, media)
			index, manifest := fixtureManifest(t, source)
			if manifest.MediaType != MediaDockerManifest || manifest.Config.MediaType != string(types.DockerConfigJSON) || len(manifest.Layers) == 0 {
				t.Fatal("fixture is not a native Docker image")
			}
			for _, layer := range manifest.Layers {
				if layer.MediaType != string(media) {
					t.Fatalf("fixture retained an OCI layer: %+v", layer)
				}
			}
			pin := index.Manifests[0].Digest
			imported := filepath.Join(t.TempDir(), "imported")
			image, err := ImportAcquiredContainerImage(t.Context(), source, pin, "linux/amd64", imported)
			if err != nil || image.ManifestDigest != pin || image.MediaType != MediaDockerManifest {
				t.Fatalf("native import changed subject: %+v, %v", image, err)
			}
			publication := filepath.Join(t.TempDir(), "publication")
			_, err = BuildPublicationRoot(t.Context(), publication, PublicationRequest{Objects: []ObjectInput{{
				Name: "native-docker", Kind: "oci-image", Path: imported, Layout: true, ArtifactType: MediaDockerManifest,
				SemanticDigest: pin, BlobDigest: image.RootDigest, OS: "linux", Arch: "amd64",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			observed, err := Inspect(t.Context(), publication)
			if err != nil || len(observed.Root.Objects) != 1 || observed.Root.Objects[0].SemanticDigest != pin {
				t.Fatalf("publication changed native subject: %+v, %v", observed, err)
			}
			flat := filepath.Join(t.TempDir(), "flat")
			writer := layoutWriter{root: flat, plain: true}
			descriptor, blob, err := writer.importFlatLayout(t.Context(), ObjectInput{Path: imported, BlobDigest: image.RootDigest})
			if err != nil || descriptor.Digest != pin || descriptor.MediaType != MediaDockerManifest || blob != pin {
				t.Fatalf("flat import changed native subject: %+v, %q, %v", descriptor, blob, err)
			}
			if _, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
				t.Fatal(err)
			}
			flatImage, err := InspectContainerImage(t.Context(), flat, "linux/amd64")
			if err != nil || flatImage.ManifestDigest != pin || flatImage.MediaType != MediaDockerManifest {
				t.Fatalf("flat native image readback changed subject: %+v, %v", flatImage, err)
			}
			members := append([]Descriptor{{Digest: pin}, manifest.Config}, manifest.Layers...)
			for _, member := range members {
				path := filepath.Join("blobs", "sha256", strings.TrimPrefix(member.Digest, "sha256:"))
				want, err := os.ReadFile(filepath.Join(source, path))
				if err != nil {
					t.Fatal(err)
				}
				for _, root := range []string{imported, publication, flat} {
					got, err := os.ReadFile(filepath.Join(root, path))
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("native member changed at %s: %v", root, err)
					}
				}
			}
		})
	}
}

func TestNativeDockerLayerCorruptionRejectsImportBeforeOutput(t *testing.T) {
	t.Parallel()
	for _, media := range []types.MediaType{types.DockerLayer, types.DockerUncompressedLayer} {
		t.Run(string(media), func(t *testing.T) {
			source := nativeDockerLayerFixture(t, media)
			index, manifest := fixtureManifest(t, source)
			layer := filepath.Join(source, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))
			file, err := os.OpenFile(layer, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := io.WriteString(file, "not the held native layer")
			if err := errors.Join(writeErr, file.Close()); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "never-created")
			_, err = ImportAcquiredContainerImage(t.Context(), source, index.Manifests[0].Digest, "linux/amd64", out)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("corrupt native layer was accepted: %v", err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected import exposed output: %v", err)
			}
		})
	}
}
