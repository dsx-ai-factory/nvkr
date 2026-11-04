// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestDebianCarrierRejectsForeignMetadataBeforePull(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ociManifest)
		config string
	}{
		{name: "subject", mutate: func(m *ociManifest) { m.Subject = &m.Layers[0] }},
		{name: "annotations", mutate: func(m *ociManifest) { m.Annotations = map[string]string{"foreign": "metadata"} }},
		{name: "schema", mutate: func(m *ociManifest) { m.SchemaVersion = 1 }},
		{name: "media", mutate: func(m *ociManifest) { m.MediaType = MediaDockerManifest }},
		{name: "layer media", mutate: func(m *ociManifest) { m.Layers[0].MediaType = "application/foreign" }},
		{name: "raw digest", mutate: func(m *ociManifest) { m.Layers[0].Digest = digestBytes([]byte("foreign")) }},
		{name: "config", config: `{"foreign":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writer := layoutWriter{root: root, plain: true}
			config := test.config
			if config == "" {
				config = "{}"
			}
			configuration, err := writer.addBlobBytes([]byte(config), MediaEmptyConfig)
			if err != nil {
				t.Fatal(err)
			}
			layer, err := writer.addBlobBytes([]byte("original raw Debian package"), MediaOctetStream)
			if err != nil {
				t.Fatal(err)
			}
			manifest := ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: MediaDeliveryObject, Config: configuration, Layers: []Descriptor{layer}}
			if test.mutate != nil {
				test.mutate(&manifest)
			}
			descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			descriptor.ArtifactType = MediaDeliveryObject
			if _, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}); err != nil {
				t.Fatal(err)
			}
			_, err = InspectDebianArtifact(t.Context(), root, layer.Digest)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			out := filepath.Join(t.TempDir(), "refused")
			_, err = pullDebianOutput(t.Context(), "registry.example.test/agent@"+descriptor.Digest, out, root, t.TempDir(), PublishedObject{Name: "agent", DescriptorDigest: descriptor.Digest, Size: descriptor.Size, BlobDigest: layer.Digest, SemanticDigest: layer.Digest, Output: &OutputBinding{Format: outputFormatDebian}}, RegistryOptions{})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("foreign carrier exposed output")
			}
		})
	}
}

func TestDebianReleaseEvidenceRejectsForeignRawSubject(t *testing.T) {
	raw := digestBytes([]byte("raw Debian package"))
	foreign := digestBytes([]byte("other package"))
	for _, field := range []string{"build subject", "semantic subject"} {
		t.Run(field, func(t *testing.T) {
			object := PublishedObject{Name: "agent", BlobDigest: raw, SemanticDigest: raw, Output: &OutputBinding{Format: outputFormatDebian, Components: []string{"agent"}, BuildSubjects: []BuildSubject{{Component: "agent", Digest: raw}}}}
			if field == "build subject" {
				object.Output.BuildSubjects[0].Digest = foreign
			} else {
				object.SemanticDigest = foreign
			}
			err := verifyReleaseEvidence(t.Context(), t.TempDir(), PublicationRoot{Release: &ReleaseMetadata{}, Objects: []PublishedObject{object}})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "preserved raw package") {
				t.Fatalf("wrong refusal boundary: %v", err)
			}
		})
	}
}

func TestDebianPublicationPreservesRawSubjectAndOrdinaryCarrier(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "original+package.deb")
	raw := []byte("opaque original package bytes; transport does not install or infer compatibility")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := digestBytes(raw)
	carrier, err := BuildDebianArtifact(t.Context(), filepath.Join(root, "carrier"), "agent", source, digest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := InspectDebianArtifact(t.Context(), carrier.LayoutPath, digest)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest == digest {
		t.Fatal("ordinary OCI wrapper replaced raw producer identity")
	}
	build := digestBytes([]byte("logical build"))
	bom, err := sbom.NewSubject(build, "agent", digest)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, digest, "build-output", digest)
	if err != nil {
		t.Fatal(err)
	}
	bomRaw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	bomDigest, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	content, err := ContentDigest([]Output{{Name: "agent", Kind: outputFormatDebian, Digest: digest, BuildDigest: build, Components: []string{"agent"}}})
	if err != nil {
		t.Fatal(err)
	}
	request := PublicationRequest{Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content}, Objects: []ObjectInput{
		{Name: "agent", Kind: outputFormatDebian, Path: carrier.LayoutPath, Layout: true, ArtifactType: MediaDeliveryObject, BlobDigest: carrier.RootDigest, SemanticDigest: digest, OS: "linux", Arch: "amd64", Output: &OutputBinding{Repository: "agent", Format: outputFormatDebian, BuildDigest: build, Components: []string{"agent"}, BuildSubjects: []BuildSubject{{Component: "agent", Digest: digest}}}},
		{Name: "sbom-" + strings.TrimPrefix(bomDigest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: bomDigest, Bytes: bomRaw, Evidence: &EvidenceBinding{Outputs: []string{"agent"}, Component: "agent", Stage: "build-output", SubjectKind: "file", SubjectDigest: digest}},
	}}
	result, err := BuildPublicationRoot(t.Context(), filepath.Join(root, "publication"), request)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := OpenPublication(t.Context(), result.LayoutPath, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	reference := "registry.example.test/raw-debian@" + result.RootDigest
	description, err := publication.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Outputs) != 1 || description.Outputs[0].Digest != digest || description.Outputs[0].DescriptorDigest != descriptor.Digest {
		t.Fatal("raw Debian and wrapper descriptor identities were conflated")
	}
	store := &mirrorMemoryStore{}
	verifier := mirrorFixtureSigner(t)
	locator, err := MirrorPublications(t.Context(), store, MirrorRequest{RootReference: reference, Publications: []MirrorPublication{{Reference: reference, Publication: publication}}, Proofs: []*sign.VerifiedOCIEvidence{mirrorFixtureProof(t, verifier, reference), mirrorFixtureProof(t, verifier, "registry.example.test/agent@"+descriptor.Digest)}, Limits: mirrorFixtureLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.data[mirrorBlobKey(digest)], raw) {
		t.Fatal("mirror did not preserve original raw Debian bytes")
	}
	restored, err := RestoreMirror(t.Context(), store, locator, filepath.Join(root, "restored"), mirrorFixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	checked, err := OpenPublication(t.Context(), restored.Publications[0].Path, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer checked.Close()
	outputs, err := checked.Outputs()
	if err != nil || len(outputs) != 1 || outputs[0].Digest != digest {
		t.Fatalf("restored raw Debian identity changed: %+v %v", outputs, err)
	}
}

func TestDebianCarrierRefusesSubstitutedOriginalBeforeOutput(t *testing.T) {
	for _, scenario := range []string{"wrong-digest", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "original.deb")
			raw := []byte("original producer package")
			var expected string
			if scenario == "empty" {
				raw = nil
				expected = digestBytes(nil)
			} else {
				expected = digestBytes([]byte("other producer"))
			}
			if err := os.WriteFile(source, raw, 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "refused")
			_, err := BuildDebianArtifact(t.Context(), out, "agent", source, expected)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("refused original package exposed output")
			}
		})
	}
}
