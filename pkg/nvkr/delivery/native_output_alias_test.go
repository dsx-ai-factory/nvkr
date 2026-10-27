// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func nativeOutputAliasRequest(t *testing.T, media string) PublicationRequest {
	t.Helper()
	fixture := newImagePullFixture(t, media)
	_, raw, err := readIndex(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	request := PublicationRequest{Release: &ReleaseMetadata{
		DefinitionDigest: digestBytes([]byte("definition")), EnvironmentDigest: digestBytes([]byte("environment")), PlanDigest: digestBytes([]byte("plan")),
	}}
	var outputs []Output
	for _, name := range []string{"init", "workload"} {
		build := digestBytes([]byte(name + " build"))
		image := fixture.object
		binding := &OutputBinding{Repository: name, Format: "oci", BuildDigest: build, Components: []string{name}, BuildSubjects: []BuildSubject{{Component: name, Digest: image.DescriptorDigest}}}
		request.Objects = append(request.Objects, ObjectInput{Name: name, Kind: image.Kind, Path: fixture.root, Layout: true,
			ArtifactType: image.ArtifactType, SemanticDigest: image.SemanticDigest, BlobDigest: digestBytes(raw), OS: image.OS, Arch: image.Arch, Output: binding})
		outputs = append(outputs, Output{Name: name, Kind: "oci", Digest: image.DescriptorDigest, BuildDigest: build, Components: []string{name}})
		bom, err := sbom.NewSubject(build, name, image.DescriptorDigest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, image.DescriptorDigest, "build-output", image.DescriptorDigest)
		if err != nil {
			t.Fatal(err)
		}
		document, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: document,
			Evidence: &EvidenceBinding{Outputs: []string{name}, Component: name, Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: image.DescriptorDigest}})
	}
	request.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestNativeOutputAliasesPreserveNamedBindings(t *testing.T) {
	for _, media := range []string{"native OCI", "native Docker"} {
		t.Run(media, func(t *testing.T) {
			request := nativeOutputAliasRequest(t, media)
			root := filepath.Join(t.TempDir(), "release")
			result, err := BuildPublicationRoot(t.Context(), root, request)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Root.Objects) != 4 {
				t.Fatal("logical output or evidence was dropped")
			}
			owner, err := OpenPublication(t.Context(), root, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			reference := "registry.example.test/product/releases@" + result.RootDigest
			view, err := owner.Description(reference)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Outputs) != 2 || len(view.Evidence) != 2 {
				t.Fatal("description lost named members")
			}
			if view.Outputs[0].Digest != request.Objects[0].SemanticDigest || view.Outputs[0].Digest != view.Outputs[1].Digest || view.Outputs[0].BuildDigest == view.Outputs[1].BuildDigest {
				t.Fatal("physical alias changed or logical build identity collapsed")
			}
			for i, name := range []string{"init", "workload"} {
				output := view.Outputs[i]
				if output.Name != name || !reflect.DeepEqual(output.Components, []string{name}) || len(output.BuildSubjects) != 1 || output.BuildSubjects[0].Component != name || output.BuildSubjects[0].Digest != output.Digest || output.Reference != "registry.example.test/product/"+name+"@"+output.Digest {
					t.Fatalf("lost named output binding: %+v", output)
				}
				found := false
				for _, evidence := range view.Evidence {
					if reflect.DeepEqual(evidence.Binding.Outputs, []string{name}) && evidence.Binding.Component == name {
						found = true
					}
				}
				if !found {
					t.Fatal("evidence crossed output memberships")
				}
			}
			metadata := filepath.Join(t.TempDir(), "metadata")
			if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
				t.Fatal(err)
			}
			if got, err := InspectMetadataDescription(t.Context(), metadata, reference); err != nil || !reflect.DeepEqual(got, view) {
				t.Fatalf("metadata lost aliases: %v", err)
			}
		})
	}
}

func TestNativeOutputAliasesRejectConflictingMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PublicationRoot)
	}{
		{"size", func(r *PublicationRoot) { r.Objects[3].Size++ }},
		{"platform", func(r *PublicationRoot) { r.Objects[3].Arch = "arm64" }},
		{"semantic digest", func(r *PublicationRoot) { r.Objects[3].SemanticDigest = digestBytes([]byte("other")) }},
		{"blob digest", func(r *PublicationRoot) { r.Objects[3].BlobDigest = digestBytes([]byte("other")) }},
		{"media type", func(r *PublicationRoot) {
			r.Objects[3].ArtifactType = "application/vnd.docker.distribution.manifest.v2+json"
		}},
		{"duplicate logical name", func(r *PublicationRoot) { r.Objects[3].Name = r.Objects[0].Name }},
		{"unsorted", func(r *PublicationRoot) { r.Objects[0], r.Objects[3] = r.Objects[3], r.Objects[0] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := nativeOutputAliasRequest(t, "native OCI")
			root := filepath.Join(t.TempDir(), "release")
			if _, err := BuildPublicationRoot(t.Context(), root, request); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			rewriteDiskPublicationRoot(t, root, inspection, tc.edit)
			_, err = Inspect(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestNativeOutputAliasesRequireEachEvidenceSlot(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PublicationRequest)
		code e.Code
	}{
		{"missing evidence", func(r *PublicationRequest) { r.Objects = r.Objects[:3] }, e.CodeProvenanceMissing},
		{"sibling evidence", func(r *PublicationRequest) { r.Objects[3].Evidence.Outputs = []string{"init"} }, e.CodeVerifyFailed},
		{"wrong component", func(r *PublicationRequest) { r.Objects[3].Evidence.Component = "init" }, e.CodeVerifyFailed},
		{"wrong subject", func(r *PublicationRequest) { r.Objects[3].Evidence.SubjectDigest = digestBytes([]byte("other")) }, e.CodeVerifyFailed},
		{"tampered init document", func(r *PublicationRequest) {
			r.Objects[1].Bytes = []byte(strings.ReplaceAll(string(r.Objects[1].Bytes), "init", "changed"))
		}, e.CodeVerifyFailed},
		{"tampered workload document", func(r *PublicationRequest) {
			r.Objects[3].Bytes = []byte(strings.ReplaceAll(string(r.Objects[3].Bytes), "workload", "changed"))
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := nativeOutputAliasRequest(t, "native OCI")
			tc.edit(&request)
			_, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "release"), request)
			assertRegistryCode(t, err, tc.code)
		})
	}
}

func TestNativeOutputAliasesDoNotAdmitDirectObjectAliases(t *testing.T) {
	request := nativeOutputAliasRequest(t, "native OCI")
	request.Release = nil
	request.Objects = []ObjectInput{request.Objects[0], request.Objects[2]}
	for i := range request.Objects {
		request.Objects[i].Output = nil
	}
	_, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "publication"), request)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

func TestNativeOutputAliasesDoNotAdmitOtherOutputFormats(t *testing.T) {
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	other := request.Objects[0]
	binding := *other.Output
	other.Name, binding.Repository, other.Output = "ztools", "ztools", &binding
	request.Objects = append(request.Objects, other)
	request.Objects[1].Evidence.Outputs = []string{"tools", "ztools"}
	descriptor, err := PayloadDescriptor(t.Context(), other.Path)
	if err != nil {
		t.Fatal(err)
	}
	request.Release.ContentSubject, err = ContentDigest([]Output{
		{Name: "tools", Kind: "executable", Digest: descriptor.Digest, BuildDigest: binding.BuildDigest, Components: binding.Components},
		{Name: "ztools", Kind: "executable", Digest: descriptor.Digest, BuildDigest: binding.BuildDigest, Components: binding.Components},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "release"), request)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if !strings.Contains(err.Error(), "sharing a descriptor") {
		t.Fatalf("unexpected alias rejection: %v", err)
	}
}

func TestNativeOutputAliasesRejectExtraPhysicalDescriptors(t *testing.T) {
	for _, extra := range []string{"duplicate root descriptor", "deliverable in root index", "extra blob"} {
		t.Run(extra, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "release")
			if _, err := BuildPublicationRoot(t.Context(), root, nativeOutputAliasRequest(t, "native OCI")); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			index, _, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			writer := layoutWriter{root: root}
			switch extra {
			case "duplicate root descriptor":
				index.Manifests = append(index.Manifests, index.Manifests[0])
			case "deliverable in root index":
				o := inspection.Root.Objects[0]
				index.Manifests = append(index.Manifests, Descriptor{MediaType: publishedManifestMediaType(o), ArtifactType: o.ArtifactType, Digest: o.DescriptorDigest, Size: o.Size})
			case "extra blob":
				if _, err := writer.addBlobBytes([]byte("unreachable"), MediaOctetStream); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := writer.writeIndex(index); err != nil {
				t.Fatal(err)
			}
			_, err = Inspect(t.Context(), root)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}
