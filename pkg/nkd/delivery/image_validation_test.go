package delivery

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Rebind deliberately malformed JSON to its exact blob digest, removing the old
// blob so rejection cannot be attributed to an unrelated unreachable member.
func replaceImageFixtureManifest(t *testing.T, root string, index ociIndex, manifest ociManifest, raw []byte) string {
	t.Helper()
	writer := layoutWriter{root: root}
	old := index.Manifests[0].Digest
	var descriptor Descriptor
	var err error
	if raw == nil {
		descriptor, err = writer.addJSONBlob(manifest, index.Manifests[0].MediaType)
	} else {
		descriptor, err = writer.addBlobBytes(raw, index.Manifests[0].MediaType)
	}
	if err != nil {
		t.Fatal(err)
	}
	index.Manifests[0] = descriptor
	if _, err := writer.writeIndex(index); err != nil {
		t.Fatal(err)
	}
	if old != descriptor.Digest {
		if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))); err != nil {
			t.Fatal(err)
		}
	}
	return descriptor.Digest
}

func TestAcquiredImageRejectsAmbiguousJSONAndMediaBeforeOutput(t *testing.T) {
	t.Parallel()
	for _, docker := range []bool{false, true} {
		format := "OCI"
		if docker {
			format = "Docker"
		}
		for _, condition := range []string{"duplicate index", "duplicate marker", "duplicate manifest", "duplicate config", "mixed config", "descriptor body mismatch", "schema 1", "schema 1 media"} {
			t.Run(format+"/"+condition, func(t *testing.T) {
				root := imageFixture(t)
				if docker {
					root = nativeDockerLayerFixture(t, types.DockerLayer)
				}
				index, manifest := fixtureManifest(t, root)
				pin := index.Manifests[0].Digest
				writer := layoutWriter{root: root}
				switch condition {
				case "duplicate index":
					_, raw, err := readIndex(t.Context(), root)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "index.json"), append([]byte(`{"schemaVersion":1,`), raw[1:]...), 0o600); err != nil {
						t.Fatal(err)
					}
				case "duplicate marker":
					if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte(`{"imageLayoutVersion":"unsupported","imageLayoutVersion":"1.0.0"}`), 0o600); err != nil {
						t.Fatal(err)
					}
				case "duplicate manifest":
					raw, err := readBlob(t.Context(), root, index.Manifests[0], 8<<20)
					if err != nil {
						t.Fatal(err)
					}
					pin = replaceImageFixtureManifest(t, root, index, manifest, append([]byte(`{"schemaVersion":1,`), raw[1:]...))
				case "duplicate config":
					raw, err := readBlob(t.Context(), root, manifest.Config, 8<<20)
					if err != nil {
						t.Fatal(err)
					}
					old := manifest.Config.Digest
					manifest.Config, err = writer.addBlobBytes(append([]byte(`{"os":"windows",`), raw[1:]...), manifest.Config.MediaType)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(old, "sha256:"))); err != nil {
						t.Fatal(err)
					}
					pin = replaceImageFixtureManifest(t, root, index, manifest, nil)
				case "mixed config":
					manifest.Config.MediaType = string(types.DockerConfigJSON)
					if docker {
						manifest.Config.MediaType = MediaImageConfig
					}
					pin = replaceImageFixtureManifest(t, root, index, manifest, nil)
				case "descriptor body mismatch":
					manifest.MediaType = MediaDockerManifest
					if docker {
						manifest.MediaType = MediaOCIManifest
					}
					pin = replaceImageFixtureManifest(t, root, index, manifest, nil)
				case "schema 1":
					manifest.SchemaVersion = 1
					pin = replaceImageFixtureManifest(t, root, index, manifest, nil)
				case "schema 1 media":
					index.Manifests[0].MediaType = string(types.DockerManifestSchema1)
					manifest.MediaType = string(types.DockerManifestSchema1)
					pin = replaceImageFixtureManifest(t, root, index, manifest, nil)
				}
				out := filepath.Join(t.TempDir(), "never-created")
				_, err := ImportAcquiredContainerImage(t.Context(), root, pin, "linux/amd64", out)
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("invalid native image admitted: %v", err)
				}
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatalf("invalid native image exposed output: %v", err)
				}
			})
		}
	}
}

func TestAcquiredImageNormalizesOnlyIndexScaffolding(t *testing.T) {
	for _, docker := range []bool{false, true} {
		root := imageFixture(t)
		if docker {
			root = nativeDockerLayerFixture(t, types.DockerLayer)
		}
		index, manifest := fixtureManifest(t, root)
		pin := index.Manifests[0].Digest
		index.Annotations = map[string]string{"untrusted-transport": "attempt-specific metadata"}
		index.Manifests[0].Annotations = map[string]string{"untrusted-transport": "attempt-specific descriptor metadata"}
		if _, err := (layoutWriter{root: root}).writeIndex(index); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "normalized")
		if _, err := ImportAcquiredContainerImage(t.Context(), root, pin, "linux/amd64", out); err != nil {
			t.Fatal(err)
		}
		actual, _, err := readIndex(t.Context(), out)
		if err != nil || len(actual.Annotations) != 0 || len(actual.Manifests) != 1 || len(actual.Manifests[0].Annotations) != 0 || actual.Manifests[0].Digest != pin {
			t.Fatalf("index scaffolding not normalized: %+v, %v", actual, err)
		}
		members := append([]Descriptor{{Digest: pin}, manifest.Config}, manifest.Layers...)
		for _, member := range members {
			path := filepath.Join("blobs", "sha256", strings.TrimPrefix(member.Digest, "sha256:"))
			before, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(out, path))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("normalization changed native bytes: %v", err)
			}
		}
	}
}

func TestPublishedNativeImageRejectsDescriptorBodyMismatch(t *testing.T) {
	for _, docker := range []bool{false, true} {
		root := imageFixture(t)
		wrongMedia := MediaDockerManifest
		if docker {
			root = nativeDockerLayerFixture(t, types.DockerLayer)
			wrongMedia = MediaOCIManifest
		}
		index, _ := fixtureManifest(t, root)
		descriptor := index.Manifests[0]
		publication := Inspection{Root: PublicationRoot{Objects: []PublishedObject{{Name: "native", Kind: "oci-image",
			ArtifactType: wrongMedia, DescriptorDigest: descriptor.Digest, BlobDigest: descriptor.Digest, Size: descriptor.Size, OS: "linux", Arch: "amd64"}}}}
		_, err := inspectPublishedContainerImage(t.Context(), root, publication, "native")
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatalf("native descriptor/body mismatch accepted: %v", err)
		}
	}
}
