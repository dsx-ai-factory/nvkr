// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Reuse the standard synthetic image/SBOM fixture and reconstruct ordinary
// public constructor inputs. No metadata mutation bypasses the builder's gate.
func imageIndexPublicationRequestFixture(t *testing.T) PublicationRequest {
	t.Helper()
	path, _, _ := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	request := PublicationRequest{Release: inspection.Root.Release}
	for _, object := range inspection.Root.Objects {
		input := ObjectInput{Name: object.Name, Kind: object.Kind, ArtifactType: object.ArtifactType,
			SemanticDigest: object.SemanticDigest, BlobDigest: object.BlobDigest, Output: object.Output, Evidence: object.Evidence}
		if object.Output != nil {
			held, err := openPublishedImageIndex(t.Context(), path, object)
			if err != nil {
				t.Fatal(err)
			}
			input.Path = writeIndexFixture(t, held)
			input.Layout = true
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			_, input.Bytes, err = ReadObject(t.Context(), path, object.Name)
			if err != nil {
				t.Fatal(err)
			}
			input.LayerMediaType = object.ArtifactType
		}
		request.Objects = append(request.Objects, input)
	}
	return request
}

func TestImageIndexPublicationRejectsIncompleteOrSubstitutedMembership(t *testing.T) {
	request := imageIndexPublicationRequestFixture(t)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*PublicationRequest)
		code   e.Code
	}{
		{"missing child", func(r *PublicationRequest) {
			r.Objects[0].Output.ImageIndex.Children = r.Objects[0].Output.ImageIndex.Children[:1]
		}, e.CodeVerifyFailed},
		{"duplicate platform", func(r *PublicationRequest) { r.Objects[0].Output.ImageIndex.Children[1].Platform = "linux/amd64" }, e.CodeVerifyFailed},
		{"wrong manifest", func(r *PublicationRequest) {
			r.Objects[0].Output.ImageIndex.Children[1].ManifestDigest = digestBytes([]byte("other"))
		}, e.CodeVerifyFailed},
		{"wrong size", func(r *PublicationRequest) { r.Objects[0].Output.ImageIndex.Children[1].Size++ }, e.CodeVerifyFailed},
		{"wrong native build", func(r *PublicationRequest) {
			r.Objects[0].Output.ImageIndex.Children[1].BuildDigest = digestBytes([]byte("other"))
		}, e.CodeVerifyFailed},
		{"wrong aggregate build", func(r *PublicationRequest) { r.Objects[0].Output.BuildDigest = digestBytes([]byte("other")) }, e.CodeVerifyFailed},
		{"single platform metadata", func(r *PublicationRequest) { r.Objects[0].OS, r.Objects[0].Arch = "linux", "amd64" }, e.CodeVerifyFailed},
		{"flat build subject", func(r *PublicationRequest) {
			r.Objects[0].Output.BuildSubjects = []BuildSubject{{Component: "controller", Digest: digestBytes(nil)}}
		}, e.CodeVerifyFailed},
		{"native media confusion", func(r *PublicationRequest) { r.Objects[0].ArtifactType = MediaOCIManifest }, e.CodeVerifyFailed},
		{"no release", func(r *PublicationRequest) { r.Release = nil }, e.CodeEngineCapability},
		{"missing platform evidence", func(r *PublicationRequest) { r.Objects = r.Objects[:2] }, e.CodeProvenanceMissing},
		{"missing evidence platform", func(r *PublicationRequest) { r.Objects[1].Evidence.Platform = "" }, e.CodeVerifyFailed},
		{"substituted evidence platform", func(r *PublicationRequest) { r.Objects[1].Evidence.Platform = r.Objects[2].Evidence.Platform }, e.CodeVerifyFailed},
		{"aggregate evidence subject", func(r *PublicationRequest) { r.Objects[1].Evidence.SubjectDigest = r.Objects[0].BlobDigest }, e.CodeVerifyFailed},
		{"wrong export archive", func(r *PublicationRequest) {
			r.Objects[1].Evidence.InspectionInputDigest = digestBytes([]byte("other"))
		}, e.CodeVerifyFailed},
		{"wrong evidence component", func(r *PublicationRequest) { r.Objects[1].Evidence.Component = "other" }, e.CodeVerifyFailed},
		{"wrong evidence stage", func(r *PublicationRequest) { r.Objects[1].Evidence.Stage = "packaging" }, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed PublicationRequest
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			test.mutate(&changed)
			destination := filepath.Join(t.TempDir(), "publication")
			_, err := BuildPublicationRoot(t.Context(), destination, changed)
			assertRegistryCode(t, err, test.code)
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("rejected aggregate exposed output: %v", err)
			}
		})
	}
}

func TestImageIndexPublicationHeldViewBindsChildrenAndEvidence(t *testing.T) {
	path, result, index := registryImageIndexFixture(t)
	publication, err := OpenPublication(t.Context(), path, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	reference := "registry.example.test/product/releases@" + result.RootDigest
	view, err := publication.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Outputs) != 1 || len(view.Evidence) != 2 || view.Outputs[0].OS != "" || view.Outputs[0].Arch != "" || view.Outputs[0].ImageIndex == nil {
		t.Fatalf("aggregate projection = %+v", view)
	}
	output := view.Outputs[0]
	if output.Digest != index.Digest || output.ArtifactType != MediaOCIIndex || len(output.ImageIndex.Children) != 2 {
		t.Fatalf("wrong aggregate output: %+v", output)
	}
	for i, child := range output.ImageIndex.Children {
		if child.Platform != index.Children[i].Platform || child.ManifestDigest != index.Children[i].ManifestDigest {
			t.Fatal("lost exact native member")
		}
		matched := false
		for _, evidence := range view.Evidence {
			if evidence.Binding.Platform == child.Platform && evidence.Binding.SubjectDigest == child.ManifestDigest && evidence.Binding.InspectionInputDigest == child.BuildSubject {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("missing evidence for %s", child.Platform)
		}
	}
	// A descriptive projection and the source pathname cannot mutate held state.
	view.Outputs[0].ImageIndex.Children[0].ManifestDigest = digestBytes([]byte("replacement"))
	view.Outputs[0].ImageIndex.RequiredPlatforms[0] = "other"
	if err := os.Rename(path, path+"-replaced"); err != nil {
		t.Fatal(err)
	}
	again, err := publication.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Outputs[0].ImageIndex.RequiredPlatforms, imageIndexPlatforms) || again.Outputs[0].ImageIndex.Children[0].ManifestDigest != index.Children[0].ManifestDigest {
		t.Fatal("caller or path replacement mutated held aggregate")
	}
	artifacts, subject, err := publication.Artifacts(reference)
	if err != nil || len(artifacts) != 1 || subject != again.Metadata.ContentSubject {
		t.Fatalf("held artifacts: %+v, %v", artifacts, err)
	}
	artifacts[0].ImageIndex.Children = nil
	fresh, _, err := publication.Artifacts(reference)
	if err != nil || len(fresh[0].ImageIndex.Children) != 2 {
		t.Fatal("Artifacts leaked mutable aggregate children")
	}
	assertRegistryCode(t, publication.RequireAttestedEvidence(), e.CodeProvenanceMissing)
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = publication.Description(reference)
	assertRegistryCode(t, err, e.CodeGateUnmet)
}

func TestImageIndexPublicationAdmissionPrecedesInputAccess(t *testing.T) {
	request := imageIndexPublicationRequestFixture(t)
	request.Objects[0].Path = filepath.Join(t.TempDir(), "missing-input")
	request.Objects[0].Output.BuildDigest = digestBytes([]byte("substituted build"))
	parent := filepath.Join(t.TempDir(), "missing-parent")
	_, err := BuildPublicationRoot(t.Context(), filepath.Join(parent, "publication"), request)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("invalid aggregate created output parent: %v", err)
	}
}

func TestPublicationRejectsPreviousMembershipSchema(t *testing.T) {
	path, _, _ := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"nvkr.dev/publication-root/v1", "nvkr.dev/publication-root/v2", "nvkr.dev/publication-root/v3"} {
		t.Run(version, func(t *testing.T) {
			previous := inspection.Root
			previous.SchemaVersion = version
			assertRegistryCode(t, previous.Validate(), e.CodeVerifyFailed)
		})
	}
}

// OCI index bytes are identity-bearing but need not use NVKR's canonical codec.
// Storage preflight must preserve the format admitted by the image-index owner.
func TestImageIndexPublicationPreservesNoncanonicalOCIIndex(t *testing.T) {
	request := imageIndexPublicationRequestFixture(t)
	var outputs []Output
	for i := range request.Objects {
		input := &request.Objects[i]
		if input.Output == nil {
			continue
		}
		path := filepath.Join(input.Path, "index.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, formatted.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		input.BlobDigest = digestBytes(formatted.Bytes())
		input.SemanticDigest = input.BlobDigest
		outputs = append(outputs, Output{Name: input.Name, Kind: input.Output.Format, Digest: input.BlobDigest,
			BuildDigest: input.Output.BuildDigest, Components: input.Output.Components, ImageIndex: input.Output.ImageIndex})
	}
	var err error
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	held, err := OpenPublication(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
}
