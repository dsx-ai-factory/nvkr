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
	// Recorded by constructing these exact fixtures at immutable pre-change base
	// 664905a46cb0b0be2ce111da24d833cf318efd96, not by comparing two new paths.
	golden := map[string]string{
		"helm":   "sha256:e8ce745191ac66401bd512dfaa5736868fb9716c94e1f634233c16ff3a4e3e64",
		"binary": "sha256:f36975e7bf7166d16f565df2f4ff879cf20e10f5722e6ffdbbf442cd1498b691",
		"bundle": "sha256:282a42e0a538bdfd09ea1a972aa0a2c0a05a76e2cee597a078f91ed8da8795d2",
		"debian": "sha256:0a51cb600e038a5cd1ff24e2a8ea8b9f3b676e1cced1ba6c9ecba658feddbcc1",
	}
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
				var result Result
				switch kind {
				case "helm":
					result, err = BuildHelmChartArtifactWithRepresentation(t.Context(), root, "application/vnd.nvkr.helm-bundle.v1", archive, representation)
				case "binary":
					result, err = BuildBinaryBundle(t.Context(), root, BinaryBundleRequest{OCIRepresentation: representation, OS: "linux", Arch: "amd64", Purpose: "cli",
						Files: []BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o644}}})
				case "bundle":
					result, err = BuildBundleArtifactWithRepresentation(t.Context(), root, "bundle", source, digestBytes(archive), representation)
				case "debian":
					result, err = BuildDebianArtifactWithRepresentation(t.Context(), root, "package", source, digestBytes(archive), representation)
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.RootDigest != golden[kind] {
					t.Fatalf("default %s differs from pre-change bytes: %s", kind, result.RootDigest)
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

func TestOCIRepresentationDefaultReleaseRootGolden(t *testing.T) {
	// Same pre-change base and releasePublicationRequestFixture as the artifact
	// goldens above; this binds the aggregate bootstrap as well as its members.
	result, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "release"), releasePublicationRequestFixture(t, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if result.RootDigest != "sha256:74ff392f37a525b570e24906b257a229f0c79a169edece8208923c0fce29dc01" {
		t.Fatalf("default release differs from pre-change bytes: %s", result.RootDigest)
	}
}

func TestOCIRepresentationMetadataRejectsUntypedEmptyMemberConfig(t *testing.T) {
	full := filepath.Join(t.TempDir(), "full")
	if _, err := BuildPublicationRoot(t.Context(), full, releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for i, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		manifest, err := flatManifest(t.Context(), full, object)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Config.MediaType != MediaArtifactConfig {
			continue
		}
		manifest.Config.MediaType = MediaEmptyConfig
		descriptor, err := (layoutWriter{root: full}).addJSONBlob(manifest, MediaOCIManifest)
		if err != nil {
			t.Fatal(err)
		}
		inspection.Root.Objects[i].DescriptorDigest, inspection.Root.Objects[i].Size = descriptor.Digest, descriptor.Size
		changed = true
		break
	}
	if !changed {
		t.Fatal("fixture has no compatible generic evidence member")
	}
	sources := map[string]string{}
	for _, object := range inspection.Root.Objects {
		sources[object.Name] = full
	}
	out := filepath.Join(t.TempDir(), "invalid-metadata")
	_, err = BuildMetadataRoot(t.Context(), out, inspection.Root, sources)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if !strings.Contains(err.Error(), "empty config requires") {
		t.Fatalf("metadata rejected another invariant: %v", err)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("untyped empty config exposed metadata output")
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
				verified := filepath.Join(t.TempDir(), "verified-copy")
				if _, err := CopyMetadataWithExpectedRoot(t.Context(), source, verified, inspected.RootDigest); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(verified, "index.json"))
				if err != nil || !bytes.Equal(raw, want) {
					t.Fatalf("verified metadata copy rebuilt the signed root: %v", err)
				}
				rejected := filepath.Join(t.TempDir(), "rejected")
				_, err = CopyMetadataWithExpectedRoot(t.Context(), source, rejected, digestBytes([]byte("other root")))
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Lstat(rejected); !os.IsNotExist(err) {
					t.Fatal("wrong expected root exposed metadata output")
				}
			})
		}
	}
}
