// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestOCIRepresentationAdmissionBeforeArtifactWork(t *testing.T) {
	for _, kind := range []string{"helm", "binary", "bundle", "debian", "root", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "output")
			var err error
			switch kind {
			case "helm":
				_, err = BuildHelmChartArtifactWithRepresentation(t.Context(), out, "", nil, "unknown")
			case "binary":
				_, err = BuildBinaryBundle(t.Context(), out, BinaryBundleRequest{OCIRepresentation: "unknown"})
			case "bundle":
				_, err = BuildBundleArtifactWithRepresentation(t.Context(), out, "", "absent", "", "unknown")
			case "debian":
				_, err = BuildDebianArtifactWithRepresentation(t.Context(), out, "", "absent", "", "unknown")
			case "root":
				_, err = BuildPublicationRoot(t.Context(), out, PublicationRequest{OCIRepresentation: "unknown"})
			case "metadata":
				_, err = BuildMetadataRootWithRepresentation(t.Context(), out, PublicationRoot{}, nil, "unknown")
			}
			assertRegistryCode(t, err, e.CodeEngineCapability)
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func TestOCIRepresentationDefaultPreservesArtifactConstruction(t *testing.T) {
	archive := helmChartArchiveFixture(t, map[string]string{"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n"})
	for _, kind := range []string{"helm", "binary", "bundle", "debian"} {
		t.Run(kind, func(t *testing.T) {
			var roots []string
			for _, representation := range []OCIRepresentation{"", OCIArtifactV1} {
				root := filepath.Join(t.TempDir(), "artifact")
				source := filepath.Join(t.TempDir(), "payload")
				if err := os.WriteFile(source, archive, 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "helm":
					_, err = BuildHelmChartArtifactWithRepresentation(t.Context(), root, "application/vnd.nvkr.helm-bundle.v1", archive, representation)
				case "binary":
					_, err = BuildBinaryBundle(t.Context(), root, BinaryBundleRequest{OCIRepresentation: representation, OS: "linux", Arch: "amd64", Purpose: "cli",
						Files: []BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o644}}})
				case "bundle":
					_, err = BuildBundleArtifactWithRepresentation(t.Context(), root, "bundle", source, digestBytes(archive), representation)
				case "debian":
					_, err = BuildDebianArtifactWithRepresentation(t.Context(), root, "package", source, digestBytes(archive), representation)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, manifest := fixtureManifest(t, root)
				if manifest.ArtifactType == "" || manifest.Annotations[artifactTypeAnnotation] != "" {
					t.Fatal("default construction stripped the artifact type")
				}
				roots = append(roots, root)
			}
			first, err := os.ReadFile(filepath.Join(roots[0], "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(filepath.Join(roots[1], "index.json"))
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("default differs from explicit artifact-v1: %v", err)
			}
		})
	}
}

func TestOCIRepresentationPreservesStandardSignedSBOMEnvelopes(t *testing.T) {
	request := releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)
	prepared := filepath.Join(t.TempDir(), "prepared")
	if _, err := BuildPublicationRoot(t.Context(), prepared, request); err != nil {
		t.Fatal(err)
	}
	attested := filepath.Join(t.TempDir(), "attested")
	if _, err := AttestPreparedEvidence(t.Context(), prepared, attested, fixtureMetadataAttester); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), attested)
	if err != nil || inspection.Root.OCIRepresentation() != OCINVKRCompatibleV1 {
		t.Fatalf("attestation changed representation intent: %v", err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		manifest, err := flatManifest(t.Context(), attested, object)
		if err != nil || manifest.ArtifactType != sbom.BundleMediaType || manifest.Config.MediaType != MediaEmptyConfig ||
			manifest.Subject == nil || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != sbom.BundleMediaType {
			t.Fatalf("standard signed profile changed: %+v %v", manifest, err)
		}
		if _, _, err := EvidenceDocument(t.Context(), attested, object.Name); err != nil {
			t.Fatal(err)
		}
	}
	assertSignedRepresentationDiscovery(t, attested, inspection)
	// Even valid bundle bytes cannot make an unsigned compatibility envelope a
	// supported signed profile. Exercise the metadata consumer, not just admission.
	changed := inspection.Root
	changed.Objects = append([]PublishedObject(nil), changed.Objects...)
	for i, object := range changed.Objects {
		if object.Evidence == nil {
			continue
		}
		manifest, err := flatManifest(t.Context(), attested, object)
		if err != nil {
			t.Fatal(err)
		}
		writer := layoutWriter{root: attested}
		manifest.ArtifactType = ""
		manifest.Annotations = artifactAnnotations(sbom.BundleMediaType, manifest.Annotations)
		manifest.Config, err = writer.addBlobBytes([]byte("{}"), MediaArtifactConfig)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
		if err != nil {
			t.Fatal(err)
		}
		changed.Objects[i].DescriptorDigest, changed.Objects[i].Size = descriptor.Digest, descriptor.Size
		break
	}
	sources := map[string]string{}
	for _, object := range changed.Objects {
		sources[object.Name] = attested
	}
	_, err = BuildMetadataRoot(t.Context(), filepath.Join(t.TempDir(), "unsupported-envelope"), changed, sources)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	// A compatibility selection does not authorize construction of a substitute
	// signed profile from an arbitrary caller-provided media type.
	out := filepath.Join(t.TempDir(), "rejected")
	_, err = BuildPublicationRoot(t.Context(), out, PublicationRequest{OCIRepresentation: OCINVKRCompatibleV1,
		Objects: []ObjectInput{{Name: "evidence", Kind: "sbom", ArtifactType: sbom.BundleMediaType, Bytes: []byte("{}"), SemanticDigest: digestBytes([]byte("{}"))}}})
	assertRegistryCode(t, err, e.CodeEngineCapability)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("unsupported signed-profile construction exposed output")
	}
}

// Synthetic attestations establish transport/discovery only, never cryptographic
// validity or acceptance by an external registry.
func assertSignedRepresentationDiscovery(t *testing.T, path string, inspection Inspection) {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := Push(t.Context(), path, strings.TrimPrefix(server.URL, "http://")+"/product/release", options)
	if err != nil {
		t.Fatal(err)
	}
	root, err := name.NewDigest(reference, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	references, err := publicationReferences(root, inspection.Root, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		owner, err := evidenceOwner(inspection.Root.Objects, object.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		remoteOptions := append(registryOptions(t.Context(), options), remote.WithFilter("artifactType", sbom.BundleMediaType))
		index, err := remote.Referrers(references[owner.Name], remoteOptions...)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := index.IndexManifest()
		if err != nil || len(manifest.Manifests) != 1 || manifest.Manifests[0].Digest.String() != object.DescriptorDigest ||
			manifest.Manifests[0].ArtifactType != sbom.BundleMediaType || manifest.Manifests[0].Annotations["dev.sigstore.bundle.predicateType"] != sbom.CycloneDXPredicateType {
			t.Fatalf("standard signed evidence discovery changed: %+v %v", manifest, err)
		}
	}
}

func TestOCIRepresentationCannotChangeDuringAttestation(t *testing.T) {
	root := PublicationRoot{ociRepresentation: OCINVKRCompatibleV1}
	other := root
	other.ociRepresentation = ""
	assertRegistryCode(t, unchangedAttestationBindings(root, other), e.CodeVerifyFailed)
	for _, value := range []string{"unknown", ""} {
		index := ociIndex{Annotations: map[string]string{artifactTypeAnnotation: MediaPublicationRoot, ociRepresentationAnnotation: value}}
		_, err := inspectOCIRepresentation(index)
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
}

func TestOCIRepresentationMetadataCachePreservesExactBootstrap(t *testing.T) {
	for _, representation := range []OCIRepresentation{"", OCINVKRCompatibleV1} {
		for _, whitespace := range []bool{false, true} {
			t.Run(string(representation)+map[bool]string{false: "/canonical", true: "/retained-whitespace"}[whitespace], func(t *testing.T) {
				full := filepath.Join(t.TempDir(), "full")
				request := releasePublicationRequestWithRepresentation(t, "", "", representation)
				if _, err := BuildPublicationRoot(t.Context(), full, request); err != nil {
					t.Fatal(err)
				}
				source := filepath.Join(t.TempDir(), "metadata")
				if _, err := ExportMetadata(t.Context(), full, source); err != nil {
					t.Fatal(err)
				}
				indexPath := filepath.Join(source, "index.json")
				want, err := os.ReadFile(indexPath)
				if err != nil {
					t.Fatal(err)
				}
				if whitespace {
					want = append(want, '\n')
					if err := os.WriteFile(indexPath, want, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				inspected, err := InspectMetadata(t.Context(), source)
				if err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(t.TempDir(), "copy")
				if err := copyMetadataCache(t.Context(), source, out, inspected); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(out, "index.json"))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("metadata cache rebuilt identified bootstrap: %v", err)
				}
				copied, err := InspectMetadata(t.Context(), out)
				if err != nil || copied.RootDigest != inspected.RootDigest || copied.Root.OCIRepresentation() != representation {
					t.Fatalf("metadata cache changed identity or representation: %v", err)
				}
			})
		}
	}
}
