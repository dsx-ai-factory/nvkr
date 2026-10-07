// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestMetadataDescriptionExcludesPayloadVerification(t *testing.T) {
	for _, kind := range []string{"executable", "disk-image"} {
		t.Run(kind, func(t *testing.T) {
			root, pin := metadataDescriptionFixture(t, kind)
			reference := "registry.example.test/product/releases@" + pin
			open := OpenPublication
			if kind == "disk-image" {
				open = openDiskPublicationFixture
			}
			publication, err := open(t.Context(), root, pin)
			if err != nil {
				t.Fatal(err)
			}
			want, err := publication.Description(reference)
			if err != nil {
				t.Fatal(err)
			}
			if err := publication.Close(); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(t.TempDir(), "metadata")
			if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
				t.Fatal(err)
			}
			for _, layout := range []string{root, metadata} {
				got, err := InspectMetadataDescription(t.Context(), layout, reference)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("metadata projection differs from full publication: %+v, %v; want %+v", got, err, want)
				}
			}
			// The strict metadata transport still refuses a full layout's extra
			// payload blobs; only descriptive browsing permits their presence.
			_, err = InspectMetadata(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			missing, err := open(t.Context(), metadata, pin)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if missing != nil {
				_ = missing.Close()
				t.Fatal("metadata-only layout issued a full publication")
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var payload string
			for _, object := range inspection.Root.Objects {
				if object.Output != nil {
					manifest, err := flatManifest(t.Context(), root, object)
					if err != nil {
						t.Fatal(err)
					}
					payload = diskBlobPath(root, manifest.Layers[0].Digest)
				}
			}
			for _, state := range []string{"corrupt", "missing"} {
				t.Run(state, func(t *testing.T) {
					if state == "corrupt" {
						err = os.WriteFile(payload, []byte("changed artifact payload"), 0o600)
					} else {
						err = os.Remove(payload)
					}
					if err != nil {
						t.Fatal(err)
					}
					got, err := InspectMetadataDescription(t.Context(), root, reference)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("metadata browsing depended on %s payload: %+v, %v", state, got, err)
					}
					held, err := open(t.Context(), root, pin)
					assertRegistryCode(t, err, e.CodeVerifyFailed)
					if held != nil {
						_ = held.Close()
						t.Fatal("full publication accepted damaged payload")
					}
				})
			}
		})
	}
}

func TestMetadataDescriptionRejectsInvalidReferenceAndContext(t *testing.T) {
	root, pin := releasePublicationFixture(t)
	reference := "registry.example.test/releases@" + pin
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name, reference string
		ctx             context.Context
		code            e.Code
	}{
		{"tag", "registry.example.test/releases:latest", t.Context(), e.CodeUnverifiedRef},
		{"wrong root", "registry.example.test/releases@sha256:" + strings.Repeat("f", 64), t.Context(), e.CodeVerifyFailed},
		{"nil context", reference, nil, e.CodeUsage},
		{"canceled", reference, canceled, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := InspectMetadataDescription(test.ctx, root, test.reference)
			assertEmptyMetadataDescription(t, got, err, test.code)
		})
	}
}

func TestMetadataDescriptionRejectsRerootedMembershipChanges(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		edit       func(*PublicationRoot)
		code       e.Code
	}{
		{"root schema", "executable", func(root *PublicationRoot) { root.SchemaVersion = "nkd.dev/publication-root/v2" }, e.CodeVerifyFailed},
		{"content subject", "executable", func(root *PublicationRoot) { root.Release.ContentSubject = digestBytes([]byte("other")) }, e.CodeVerifyFailed},
		{"missing evidence", "executable", func(root *PublicationRoot) {
			for i, object := range root.Objects {
				if object.Evidence != nil {
					root.Objects = append(root.Objects[:i], root.Objects[i+1:]...)
					return
				}
			}
		}, e.CodeProvenanceMissing},
		{"unbound evidence", "executable", func(root *PublicationRoot) {
			for i := range root.Objects {
				if root.Objects[i].Evidence != nil {
					root.Objects[i].Evidence.Outputs = []string{"unrelated"}
				}
			}
		}, e.CodeVerifyFailed},
		{"missing build subject", "executable", func(root *PublicationRoot) {
			for i := range root.Objects {
				if root.Objects[i].Output != nil {
					root.Objects[i].Output.BuildSubjects = nil
				}
			}
		}, e.CodeVerifyFailed},
		{"contradictory executable platform", "executable", func(root *PublicationRoot) {
			for i := range root.Objects {
				if root.Objects[i].Output != nil {
					root.Objects[i].OS, root.Objects[i].Arch = "linux", "arm64"
				}
			}
		}, e.CodeVerifyFailed},
		{"contradictory disk size", "disk-image", func(root *PublicationRoot) {
			for i := range root.Objects {
				if root.Objects[i].Output != nil {
					root.Objects[i].Output.Disk.Size++
				}
			}
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := metadataDescriptionFixture(t, test.kind)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "missing evidence" {
				for _, object := range inspection.Root.Objects {
					if object.Evidence == nil {
						continue
					}
					manifest, err := flatManifest(t.Context(), root, object)
					if err != nil {
						t.Fatal(err)
					}
					for _, digest := range []string{object.DescriptorDigest, manifest.Config.Digest, manifest.Layers[0].Digest} {
						if err := os.Remove(diskBlobPath(root, digest)); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			pin := rewriteDiskPublicationRoot(t, root, inspection, test.edit)
			got, err := InspectMetadataDescription(t.Context(), root, "registry.example.test/releases@"+pin)
			assertEmptyMetadataDescription(t, got, err, test.code)
		})
	}
}

func TestMetadataDescriptionRejectsMetadataWireChanges(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ociManifest, *[]byte)
	}{
		{"root manifest schema", func(manifest *ociManifest, _ *[]byte) { manifest.SchemaVersion = 1 }},
		{"unknown root field", func(_ *ociManifest, config *[]byte) {
			*config = append([]byte(`{"unexpected":true,`), (*config)[1:]...)
		}},
		{"duplicate root field", func(_ *ociManifest, config *[]byte) {
			*config = append([]byte(`{"schemaVersion":"ignored",`), (*config)[1:]...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := releasePublicationFixture(t)
			index, _, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := flatManifest(t.Context(), root, PublishedObject{DescriptorDigest: index.Manifests[0].Digest, Size: index.Manifests[0].Size})
			if err != nil {
				t.Fatal(err)
			}
			config, err := readBlob(t.Context(), root, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), config...)
			oldConfig := manifest.Config.Digest
			test.edit(&manifest, &config)
			writer := layoutWriter{root: root}
			if !bytes.Equal(original, config) {
				manifest.Config, err = writer.addBlobBytes(config, MediaPublicationRootConfig)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(diskBlobPath(root, oldConfig)); err != nil {
					t.Fatal(err)
				}
			}
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			descriptor.ArtifactType = MediaPublicationRoot
			if err := os.Remove(diskBlobPath(root, index.Manifests[0].Digest)); err != nil {
				t.Fatal(err)
			}
			index.Manifests[0] = descriptor
			pin, err := writer.writeIndex(index)
			if err != nil {
				t.Fatal(err)
			}
			got, err := InspectMetadataDescription(t.Context(), root, "registry.example.test/releases@"+pin)
			assertEmptyMetadataDescription(t, got, err, e.CodeVerifyFailed)
		})
	}
}

func TestMetadataObjectPlatformChecksImageSchemaWithoutPayloads(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ociManifest, *[]byte)
	}{
		{"exact", nil},
		{"manifest schema", func(manifest *ociManifest, _ *[]byte) { manifest.SchemaVersion = 1 }},
		{"artifact type", func(manifest *ociManifest, _ *[]byte) { manifest.ArtifactType = MediaDeliveryObject }},
		{"layer media type", func(manifest *ociManifest, _ *[]byte) { manifest.Layers[0].MediaType = MediaQCOW2 }},
		{"config schema spelling", func(_ *ociManifest, raw *[]byte) {
			*raw = bytes.Replace(*raw, []byte(`"architecture"`), []byte(`"Architecture"`), 1)
		}},
		{"config rootfs", func(_ *ociManifest, raw *[]byte) {
			*raw = bytes.Replace(*raw, []byte(`"type":"layers"`), []byte(`"type":"other"`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := imageFixture(t)
			_, manifest := fixtureManifest(t, root)
			config, err := readBlob(t.Context(), root, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			for _, layer := range manifest.Layers {
				if err := os.Remove(diskBlobPath(root, layer.Digest)); err != nil {
					t.Fatal(err)
				}
			}
			if test.edit != nil {
				test.edit(&manifest, &config)
			}
			writer := layoutWriter{root: root}
			manifest.Config, err = writer.addBlobBytes(config, manifest.Config.MediaType)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := writer.addJSONBlob(manifest, manifest.MediaType)
			if err != nil {
				t.Fatal(err)
			}
			object := PublishedObject{Name: "worker", Kind: "container-image", ArtifactType: MediaOCIManifest,
				DescriptorDigest: descriptor.Digest, BlobDigest: descriptor.Digest, Size: descriptor.Size, OS: "linux", Arch: "amd64",
				Output: &OutputBinding{Format: "oci"}}
			got, err := metadataObjectPlatform(t.Context(), root, object)
			if test.edit == nil {
				if err != nil || got != (publicationPlatform{OS: "linux", Arch: "amd64"}) {
					t.Fatalf("image metadata required payloads: %+v, %v", got, err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func metadataDescriptionFixture(t *testing.T, kind string) (string, string) {
	t.Helper()
	if kind == "executable" {
		return releasePublicationFixture(t)
	}
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

func TestMetadataManifestRejectsAmbiguousImageFields(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"case alias", func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"mediaType"`), []byte(`"MediaType"`), 1)
		}},
		{"duplicate field", func(raw []byte) []byte {
			return append([]byte(`{"schemaVersion":2,`), raw[1:]...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := imageFixture(t)
			index, _ := fixtureManifest(t, root)
			descriptor := index.Manifests[0]
			raw, err := readBlob(t.Context(), root, descriptor, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err = (layoutWriter{root: root}).addBlobBytes(test.edit(raw), MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = metadataManifest(t.Context(), root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size, ArtifactType: MediaOCIManifest})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func assertEmptyMetadataDescription(t *testing.T, description PublicationDescription, err error, code e.Code) {
	t.Helper()
	assertRegistryCode(t, err, code)
	if !reflect.DeepEqual(description, PublicationDescription{}) {
		t.Fatalf("rejected metadata returned a description: %+v", description)
	}
}
