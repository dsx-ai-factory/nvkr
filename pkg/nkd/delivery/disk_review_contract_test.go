// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestReleaseDiskTypeAdmissionRejectsOtherArtifactFamilies(t *testing.T) {
	for _, artifactType := range []string{MediaDiskImage, testDiskType, MediaOCIManifest, MediaPublicationRoot, "application/vnd.cyclonedx+json"} {
		t.Run(artifactType, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			request.Objects[0].ArtifactType = artifactType
			// The release constructor must reject unsupported types before opening
			// the source layout, even when the MIME grammar admits them.
			request.Objects[0].Path = "missing-layout"
			output := filepath.Join(t.TempDir(), "never-created")
			_, err := BuildPublicationRoot(t.Context(), output, request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if artifactType != MediaDiskImage && !strings.Contains(err.Error(), "disk release input requires") {
				t.Fatalf("unsupported artifact type reached layout I/O: %v", err)
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal exposed output: %v", err)
			}
		})
	}
}

func TestReleaseSBOMSubjectGateRejectsSelfPinnedChanges(t *testing.T) {
	for _, scenario := range []string{"semantic digest", "object name", "subject hash", "no subject hash", "two subject hashes", "component name", "invalid non-disk document", "subject hash missing provenance", "material subject hash missing provenance"} {
		t.Run(scenario, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			component := "image"
			if scenario == "material subject hash missing provenance" {
				component = "agent"
			}
			if scenario == "invalid non-disk document" {
				request = releasePublicationRequestFixture(t, "linux", "amd64")
				component = "cli"
			}
			root := filepath.Join(t.TempDir(), "publication")
			if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			for index := range inspection.Root.Objects {
				object := &inspection.Root.Objects[index]
				if object.Evidence == nil || object.Evidence.Component != component {
					continue
				}
				switch scenario {
				case "semantic digest":
					object.SemanticDigest = digestBytes([]byte("sibling semantic identity"))
				case "object name":
					object.Name += "x"
				default:
					manifest, err := flatManifest(t.Context(), root, *object)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := readBlob(t.Context(), root, manifest.Layers[0], sbom.MaxDocumentBytes)
					if err != nil {
						t.Fatal(err)
					}
					var bom sbom.BOM
					if err := canonical.UnmarshalExact([]byte(strings.TrimSuffix(string(raw), "\n")), &bom); err != nil {
						t.Fatal(err)
					}
					switch scenario {
					case "subject hash", "subject hash missing provenance", "material subject hash missing provenance":
						changed, err := sbom.ForSubject(&bom, digestBytes([]byte("sibling bytes")), "build-output", "")
						if err != nil {
							t.Fatal(err)
						}
						bom = *changed
						if strings.HasSuffix(scenario, "missing provenance") {
							for index, property := range bom.Metadata.Props {
								if property.Name == "nkd:dependencyScannerDigest" {
									bom.Metadata.Props = append(bom.Metadata.Props[:index], bom.Metadata.Props[index+1:]...)
									break
								}
							}
						}
					case "no subject hash":
						bom.Metadata.Component.Hashes = nil
					case "two subject hashes":
						bom.Metadata.Component.Hashes = append(bom.Metadata.Component.Hashes, bom.Metadata.Component.Hashes[0])
					case "component name":
						bom.Metadata.Component.Name = "sibling"
					case "invalid non-disk document":
						bom.SpecVersion = "unsupported"
					}
					// Bypass only validated construction in this hostile wire fixture.
					// Every byte, document and outer-root identity is recomputed.
					encoded, err := canonical.Marshal(bom)
					if err != nil {
						t.Fatal(err)
					}
					semantic, err := canonical.Digest(canonical.KindSBOM, bom)
					if err != nil {
						t.Fatal(err)
					}
					writer := layoutWriter{root: root}
					payload, err := writer.addBlobBytes(encoded, manifest.Layers[0].MediaType)
					if err != nil {
						t.Fatal(err)
					}
					oldPayload, oldManifest := manifest.Layers[0].Digest, object.DescriptorDigest
					manifest.Layers[0] = payload
					descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
					if err != nil {
						t.Fatal(err)
					}
					object.DescriptorDigest, object.Size, object.BlobDigest = descriptor.Digest, descriptor.Size, payload.Digest
					object.SemanticDigest, object.Evidence.DocumentDigest = semantic, payload.Digest
					object.Name = "sbom-" + strings.TrimPrefix(semantic, "sha256:")
					for _, pin := range []string{oldPayload, oldManifest} {
						if err := os.Remove(diskBlobPath(root, pin)); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			// Recomputed SBOM identities change names. Keep the outer envelope
			// canonical so the selected semantic or subject defect reaches its gate.
			sort.Slice(inspection.Root.Objects, func(i, j int) bool {
				left, right := inspection.Root.Objects[i], inspection.Root.Objects[j]
				return left.Name+"\x00"+left.Kind < right.Name+"\x00"+right.Kind
			})
			pin := rewriteDiskPublicationRoot(t, root, inspection, func(*PublicationRoot) {})
			owner, err := openDiskPublicationFixture(t.Context(), root, pin)
			code, diagnostic := e.CodeVerifyFailed, "SBOM document differs from its exact release subject"
			if strings.HasSuffix(scenario, "missing provenance") {
				code, diagnostic = e.CodeProvenanceMissing, "disk inventory lacks an exact nkd:dependencyScannerDigest"
			}
			assertRegistryCode(t, err, code)
			if owner != nil || !strings.Contains(err.Error(), diagnostic) {
				t.Fatalf("self-pinned substitution missed subject gate: %v, %v", owner, err)
			}
		})
	}
}

func TestInspectionRejectsPreseededDiskObservationsBeforeIO(t *testing.T) {
	observations := map[string]DiskArtifactInspection{digestBytes([]byte("forged")): {Size: 1}}
	_, err := inspectPublicationWithDisks(t.Context(), "missing-publication", nil, observations)
	assertRegistryCode(t, err, e.CodeUsage)
	if !strings.Contains(err.Error(), "empty disk observation map") || len(observations) != 1 {
		t.Fatalf("inspection did not reject pre-seeding before I/O: %v", err)
	}
}

// This writes hostile metadata beneath the producer constructor. All outer
// content identities are recomputed so OpenPublication must reject the actual
// inconsistent disk binding, not merely a stale metadata hash.
func rewriteDiskPublicationRoot(t *testing.T, root string, inspection Inspection, edit func(*PublicationRoot)) string {
	t.Helper()
	publication := inspection.Root
	edit(&publication)
	raw, err := canonical.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	writer := layoutWriter{root: root}
	config, err := writer.addBlobBytes(raw, MediaPublicationRootConfig)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := writer.addJSONBlob(ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest,
		ArtifactType: MediaPublicationRoot, Config: config, Layers: []Descriptor{}}, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.ArtifactType = MediaPublicationRoot
	semantic, err := publication.Digest()
	if err != nil {
		// Invalid release metadata is intentionally encoded without validation.
		semantic, err = canonical.Digest(canonical.KindPublicationRoot, publication)
		if err != nil {
			t.Fatal(err)
		}
	}
	index, _, err := readIndex(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range index.Manifests {
		if err := os.Remove(diskBlobPath(root, original.Digest)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(diskBlobPath(root, inspection.RootConfig.Digest)); err != nil {
		t.Fatal(err)
	}
	pin, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex,
		ArtifactType: MediaPublicationRoot, Manifests: []Descriptor{descriptor},
		Annotations: map[string]string{"dev.nkd.semantic.digest": semantic}})
	if err != nil {
		t.Fatal(err)
	}
	return pin
}

func TestOpenPublicationRejectsSelfPinnedDiskBindingChanges(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name, diagnostic string
			edit             func(*PublishedObject)
		}{
			{"size", "disk wrapper differs", func(o *PublishedObject) { o.Output.Disk.Size++ }},
			{"payload", "published disk requires", func(o *PublishedObject) { o.Output.Disk.PayloadDigest = digestBytes([]byte("sibling")) }},
			{"missing binding", "published disk requires", func(o *PublishedObject) { o.Output.Disk = nil }},
			{"non-disk binding", "non-disk release output", func(o *PublishedObject) {
				o.Kind, o.ArtifactType, o.Output.Format = "container-image", MediaOCIManifest, "oci"
			}},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				request, _ := diskPublicationRequest(t, arch)
				root := filepath.Join(t.TempDir(), "publication")
				if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
					t.Fatal(err)
				}
				inspection, err := Inspect(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				pin := rewriteDiskPublicationRoot(t, root, inspection, func(publication *PublicationRoot) {
					for index := range publication.Objects {
						if publication.Objects[index].Output != nil {
							test.edit(&publication.Objects[index])
						}
					}
				})
				owner, err := openDiskPublicationFixture(t.Context(), root, pin)
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if owner != nil || !strings.Contains(err.Error(), test.diagnostic) {
					t.Fatalf("changed binding reached the wrong gate: owner=%v, %v", owner, err)
				}
			})
		}
	}
}

func TestDiskPayloadCopyRechecksExistingBlob(t *testing.T) {
	for _, condition := range []string{"identical", "changed", "symlink"} {
		t.Run(condition, func(t *testing.T) {
			root, payload, manifest := diskFixture(t, "linux/amd64")
			disk, err := OpenDiskArtifact(t.Context(), root, manifest, "linux/amd64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = disk.Close() }()
			writer := layoutWriter{root: t.TempDir()}
			if err := writer.copyDiskPayload(t.Context(), disk); err != nil {
				t.Fatal(err)
			}
			path := diskBlobPath(writer.root, digestBytes(payload))
			switch condition {
			case "changed":
				if err := os.WriteFile(path, []byte("different"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(diskBlobPath(root, digestBytes(payload)), path); err != nil {
					t.Fatal(err)
				}
			}
			err = writer.copyDiskPayload(t.Context(), disk)
			if condition == "identical" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
			if _, err := disk.WriteToContext(t.Context(), io.Discard); err != nil {
				t.Fatalf("destination change corrupted source owner: %v", err)
			}
		})
	}
}

func TestPrivateDiskOutputProjectionRequiresCurrentWalk(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "publication")
	if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
		t.Fatal(err)
	}
	disks := map[string]DiskArtifactInspection{}
	inspection, err := inspectPublicationWithDisks(t.Context(), root, nil, disks)
	if err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{"valid", "missing", "size", "manifest"} {
		t.Run(condition, func(t *testing.T) {
			observations := make(map[string]DiskArtifactInspection, len(disks))
			for pin, observation := range disks {
				if condition == "missing" {
					continue
				}
				if condition == "size" {
					observation.Size++
				}
				if condition == "manifest" {
					observation.ManifestDigest = digestBytes([]byte("sibling"))
				}
				observations[pin] = observation
			}
			outputs, err := inspectOutputs(t.Context(), root, inspection, t.TempDir(), map[string]publicationPlatform{}, observations)
			if condition == "valid" {
				if err != nil || len(outputs) != 1 {
					t.Fatalf("checked private projection: %v, %v", outputs, err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if outputs != nil {
					t.Fatal("failed projection returned partial outputs")
				}
			}
		})
	}
}
