// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestPullImageOutputPreservesSelectedClosure(t *testing.T) {
	for _, format := range []string{"native OCI", "native Docker", "image index"} {
		t.Run(format, func(t *testing.T) {
			fixture := newImagePullFixture(t, format)
			var active, corrupt atomic.Bool
			var unexpected, payloadGets atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active.Load() && r.Method == http.MethodGet && r.URL.Path != "/v2/" {
					digest := filepath.Base(r.URL.Path)
					if !strings.HasPrefix(r.URL.Path, "/v2/product/"+fixture.object.Name+"/") ||
						(fixture.blobs[digest] == nil && digest != fixture.object.DescriptorDigest) {
						unexpected.Add(1)
						http.Error(w, "unselected artifact is inaccessible", http.StatusForbidden)
						return
					}
					if fixture.payloads[digest] {
						payloadGets.Add(1)
						if corrupt.Load() {
							raw := append([]byte(nil), fixture.blobs[digest]...)
							raw[0] ^= 1
							_, _ = w.Write(raw)
							return
						}
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference := pushImagePullFixture(t, fixture, strings.TrimPrefix(server.URL, "http://"), options)
			active.Store(true)
			for _, test := range []struct {
				name, failure string
				code          e.Code
			}{
				{name: "complete selected output"},
				{name: "corrupt selected layer", failure: "corrupt", code: e.CodeVerifyFailed},
				{name: "existing destination", failure: "existing", code: e.CodeImmutableViolation},
			} {
				t.Run(test.name, func(t *testing.T) {
					corrupt.Store(test.failure == "corrupt")
					payloadGets.Store(0)
					parent := t.TempDir()
					out := filepath.Join(parent, "image")
					if test.failure == "existing" {
						if err := os.Mkdir(out, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(out, "sentinel"), []byte("keep"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					err := pullImageOutput(t.Context(), reference, out, fixture.object, options)
					if unexpected.Load() != 0 || payloadGets.Load() == 0 {
						t.Fatalf("incorrect image selection: unselected requests=%d, selected payload requests=%d", unexpected.Load(), payloadGets.Load())
					}
					if test.code != "" {
						assertRegistryCode(t, err, test.code)
						if test.failure == "existing" {
							entries, err := os.ReadDir(out)
							if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
								t.Fatalf("existing image destination changed: %v, %v", entries, err)
							}
							if raw, err := os.ReadFile(filepath.Join(out, "sentinel")); err != nil || string(raw) != "keep" {
								t.Fatalf("existing bytes changed: %q, %v", raw, err)
							}
						} else {
							assertEmptyPullDirectory(t, parent)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					assertPulledImageFixture(t, fixture, out)
				})
			}
		})
	}
}

func TestPullImageOutputRejoinsNativePlatformBeforeLayers(t *testing.T) {
	fixture := newImagePullFixture(t, "native OCI")
	var active atomic.Bool
	var layerGets atomic.Int64
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Load() && r.Method == http.MethodGet && fixture.payloads[filepath.Base(r.URL.Path)] {
			layerGets.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference := pushImagePullFixture(t, fixture, strings.TrimPrefix(server.URL, "http://"), options)
	active.Store(true)
	for _, test := range []struct {
		name string
		edit func(*PublishedObject)
	}{
		{"wrong platform", func(object *PublishedObject) { object.Arch = "arm64" }},
		{"wrong native identity", func(object *PublishedObject) { object.SemanticDigest = digestBytes([]byte("different")) }},
		{"wrong requested descriptor", func(object *PublishedObject) { object.DescriptorDigest = digestBytes([]byte("different")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := fixture.object
			test.edit(&object)
			parent := t.TempDir()
			err := pullImageOutput(t.Context(), reference, filepath.Join(parent, "image"), object, options)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if layerGets.Load() != 0 {
				t.Fatalf("invalid native binding downloaded %d payload layers", layerGets.Load())
			}
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func TestPullOutputOCIValidatesWholeReleaseWithoutSiblingPayloads(t *testing.T) {
	for _, format := range []string{"native OCI", "native Docker", "image index"} {
		t.Run(format, func(t *testing.T) {
			fixture := newImagePullFixture(t, format)
			root := imagePullReleaseFixture(t, fixture)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var siblingConfig, siblingLayer string
			var siblingConfigRaw []byte
			for _, object := range inspection.Root.Objects {
				if object.Name != "tools" {
					continue
				}
				manifest, err := flatManifest(t.Context(), root, object)
				if err != nil {
					t.Fatal(err)
				}
				siblingConfig, siblingLayer = manifest.Config.Digest, manifest.Layers[0].Digest
				siblingConfigRaw, err = readBlob(t.Context(), root, manifest.Config, 8<<20)
				if err != nil {
					t.Fatal(err)
				}
			}
			var active, corruptMetadata atomic.Bool
			var siblingGets, selectedGets atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active.Load() && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
					digest := filepath.Base(r.URL.Path)
					if digest == siblingLayer {
						siblingGets.Add(1)
						http.Error(w, "unselected executable layer is inaccessible", http.StatusForbidden)
						return
					}
					if corruptMetadata.Load() && digest == siblingConfig {
						raw := append([]byte(nil), siblingConfigRaw...)
						raw[0] ^= 1
						_, _ = w.Write(raw)
						return
					}
					if fixture.payloads[digest] {
						selectedGets.Add(1)
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			active.Store(true)
			for _, corrupt := range []bool{false, true} {
				label := "selected image"
				if corrupt {
					label = "corrupt unselected metadata"
				}
				t.Run(label, func(t *testing.T) {
					corruptMetadata.Store(corrupt)
					selectedGets.Store(0)
					parent, workspace := t.TempDir(), t.TempDir()
					out := filepath.Join(parent, "image")
					result, err := PullOutput(t.Context(), reference, fixture.object.Name, out, workspace, options)
					assertEmptyPullDirectory(t, workspace)
					if siblingGets.Load() != 0 {
						t.Fatalf("pull downloaded %d unselected payloads", siblingGets.Load())
					}
					if corrupt {
						assertRegistryCode(t, err, e.CodeFetchFailed)
						if selectedGets.Load() != 0 || !reflect.DeepEqual(result, PulledOutput{}) {
							t.Fatalf("invalid release metadata exposed output or retrieved image payloads: %+v, %d", result, selectedGets.Load())
						}
						assertEmptyPullDirectory(t, parent)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if result.Output.Name != fixture.object.Name || result.Output.Kind != "oci" || len(result.Files) != 0 || selectedGets.Load() == 0 {
						t.Fatalf("wrong selected output result: %+v, payload requests=%d", result, selectedGets.Load())
					}
					assertPulledImageFixture(t, fixture, out)
				})
			}
		})
	}
}
func imagePullReleaseFixture(t *testing.T, fixture imagePullFixture) string {
	t.Helper()
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	binaryDescriptor, err := PayloadDescriptor(t.Context(), request.Objects[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	build := digestBytes([]byte("image pull fixture build"))
	image := fixture.object
	imageRoot := fixture.root
	binding := &OutputBinding{Repository: image.Name, Format: "oci", BuildDigest: build, Components: []string{"worker"},
		BuildSubjects: []BuildSubject{{Component: "worker", Digest: image.DescriptorDigest}}}
	if image.Output.ImageIndex != nil {
		binding = image.Output
		index, err := openPublishedImageIndex(t.Context(), fixture.root, image)
		if err != nil {
			t.Fatal(err)
		}
		imageRoot = filepath.Join(t.TempDir(), "selected-index")
		if err := index.WriteLayout(t.Context(), imageRoot); err != nil {
			_ = index.Close()
			t.Fatal(err)
		}
		if err := index.Close(); err != nil {
			t.Fatal(err)
		}
	}
	output := Output{Name: image.Name, Kind: "oci", Digest: image.DescriptorDigest, BuildDigest: binding.BuildDigest,
		Components: binding.Components, ImageIndex: binding.ImageIndex}
	request.Release.ContentSubject, err = ContentDigest([]Output{output, {
		Name: "tools", Kind: "executable", Digest: binaryDescriptor.Digest, BuildDigest: request.Objects[0].Output.BuildDigest, Components: []string{"cli"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, indexRaw, err := readIndex(t.Context(), imageRoot)
	if err != nil {
		t.Fatal(err)
	}
	request.Objects = append(request.Objects, ObjectInput{Name: image.Name, Kind: image.Kind, Path: imageRoot, Layout: true,
		ArtifactType: image.ArtifactType, SemanticDigest: image.SemanticDigest, BlobDigest: digestBytes(indexRaw), OS: image.OS, Arch: image.Arch,
		Output: binding})
	if binding.ImageIndex != nil {
		inspection, err := Inspect(t.Context(), fixture.root)
		if err != nil {
			t.Fatal(err)
		}
		for _, evidence := range inspection.Root.Objects {
			if evidence.Evidence == nil {
				continue
			}
			raw, err := metadataEvidence(t.Context(), fixture.root, evidence)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := flatManifest(t.Context(), fixture.root, evidence)
			if err != nil {
				t.Fatal(err)
			}
			request.Objects = append(request.Objects, ObjectInput{Name: evidence.Name, Kind: evidence.Kind, ArtifactType: evidence.ArtifactType,
				LayerMediaType: manifest.Layers[0].MediaType, SemanticDigest: evidence.SemanticDigest, Bytes: raw, Evidence: evidence.Evidence})
		}
	} else {
		request.Objects = append(request.Objects, imagePullNativeEvidenceFixture(t, image, build))
	}
	root := filepath.Join(t.TempDir(), "release")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	return root
}

func imagePullNativeEvidenceFixture(t *testing.T, image PublishedObject, build string) ObjectInput {
	t.Helper()
	bom, err := sbom.NewSubject(build, "worker", image.DescriptorDigest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, image.DescriptorDigest, "build-output", image.DescriptorDigest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
		ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
		Evidence: &EvidenceBinding{Outputs: []string{image.Name}, Component: "worker", Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: image.DescriptorDigest}}
}

type imagePullFixture struct {
	root        string
	object      PublishedObject
	blobs       map[string][]byte
	payloads    map[string]bool
	indexDigest string
}

func newImagePullFixture(t *testing.T, format string) imagePullFixture {
	t.Helper()
	fixture := imagePullFixture{blobs: make(map[string][]byte), payloads: make(map[string]bool)}
	if format == "image index" {
		fixture.root, _, _ = registryImageIndexFixture(t)
		inspection, err := Inspect(t.Context(), fixture.root)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range inspection.Root.Objects {
			if object.Output != nil {
				fixture.object = object
				break
			}
		}
		fixture.indexDigest = fixture.object.DescriptorDigest
		for _, child := range fixture.object.Output.ImageIndex.Children {
			fixture.addNativeBlobs(t, Descriptor{MediaType: MediaOCIManifest, Digest: child.ManifestDigest, Size: child.Size})
		}
		return fixture
	}
	fixture.root = imageFixture(t)
	if format == "native Docker" {
		fixture.root = nativeDockerLayerFixture(t, types.DockerLayer)
	}
	image, err := InspectContainerImage(t.Context(), fixture.root, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := PayloadDescriptor(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.object = PublishedObject{Name: "image", Kind: "container-image", ArtifactType: image.MediaType,
		DescriptorDigest: descriptor.Digest, Size: descriptor.Size, BlobDigest: descriptor.Digest, SemanticDigest: descriptor.Digest,
		OS: image.OS, Arch: image.Arch, Output: &OutputBinding{Format: "oci", Components: []string{"worker"}, Repository: "image"}}
	fixture.addNativeBlobs(t, descriptor)
	return fixture
}

func (fixture *imagePullFixture) addNativeBlobs(t *testing.T, descriptor Descriptor) {
	t.Helper()
	raw, err := readBlob(t.Context(), fixture.root, descriptor, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	fixture.blobs[descriptor.Digest] = raw
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, blob := range append([]Descriptor{manifest.Config}, manifest.Layers...) {
		raw, err := readBlob(t.Context(), fixture.root, blob, maxDeliveryFileBytes)
		if err != nil {
			t.Fatal(err)
		}
		fixture.blobs[blob.Digest] = raw
	}
	for _, layer := range manifest.Layers {
		fixture.payloads[layer.Digest] = true
	}
}

func pushImagePullFixture(t *testing.T, fixture imagePullFixture, host string, options RegistryOptions) string {
	t.Helper()
	reference := host + "/product/" + fixture.object.Name + "@" + fixture.object.DescriptorDigest
	if fixture.object.Output.ImageIndex != nil {
		if _, err := Push(t.Context(), fixture.root, host+"/product/releases", options); err != nil {
			t.Fatal(err)
		}
		return reference
	}
	index, err := layout.Path(fixture.root).ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	image, err := index.Image(indexManifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewDigest(reference, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image, registryOptions(t.Context(), options)...); err != nil {
		t.Fatal(err)
	}
	return reference
}

func assertPulledImageFixture(t *testing.T, fixture imagePullFixture, out string) {
	t.Helper()
	if fixture.object.Output.ImageIndex != nil {
		index, err := OpenImageIndex(t.Context(), out, fixture.indexDigest, fixture.object.Output.ImageIndex.RequiredPlatforms)
		if err != nil {
			t.Fatal(err)
		}
		defer index.Close()
		description, err := index.Description()
		if err != nil {
			t.Fatal(err)
		}
		if err := validatePublishedImageIndex(fixture.object, description); err != nil {
			t.Fatal(err)
		}
		var platforms []string
		for _, child := range description.Children {
			platforms = append(platforms, child.Platform)
		}
		if !reflect.DeepEqual(platforms, imageIndexPlatforms) {
			t.Fatalf("pull changed required platform closure: %v", platforms)
		}
	} else {
		observed, err := InspectContainerImage(t.Context(), out, fixture.object.OS+"/"+fixture.object.Arch)
		if err != nil || observed.ManifestDigest != fixture.object.DescriptorDigest || observed.MediaType != fixture.object.ArtifactType {
			t.Fatalf("pull changed native manifest: %+v, %v", observed, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(out, "blobs", "sha256"))
	if err != nil || len(entries) != len(fixture.blobs) {
		t.Fatalf("pulled layout has extra or missing blobs: %v, %v", entries, err)
	}
	for digest, original := range fixture.blobs {
		raw, err := os.ReadFile(filepath.Join(out, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("pull changed exact image blob %s: %v", digest, err)
		}
	}
}
