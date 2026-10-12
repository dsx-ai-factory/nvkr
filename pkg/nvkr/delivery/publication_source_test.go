// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestPublicationSourceDescriptionsBindExactEvidence(t *testing.T) {
	const repo = "https://example.test/product.git"
	commit := strings.Repeat("a", 40)
	for _, test := range []struct {
		name       string
		properties []sbom.Property
		want       *PublicationSource
	}{
		{"source", []sbom.Property{{Name: "nvkr:fromSource", Value: "true"}, {Name: "nvkr:repo", Value: repo}, {Name: "nvkr:commit", Value: commit}}, &PublicationSource{Repo: repo, Commit: commit}},
		{"redacted repository", []sbom.Property{{Name: "nvkr:fromSource", Value: "true"}, {Name: "nvkr:source", Value: "nvkr:source/cli"}, {Name: "nvkr:commit", Value: commit}}, &PublicationSource{Commit: commit}},
		{"missing commit", []sbom.Property{{Name: "nvkr:fromSource", Value: "true"}, {Name: "nvkr:repo", Value: repo}}, &PublicationSource{Repo: repo}},
		{"missing source fields", []sbom.Property{{Name: "nvkr:fromSource", Value: "true"}}, &PublicationSource{}},
		{"acquired", []sbom.Property{{Name: "nvkr:fromSource", Value: "false"}, {Name: "nvkr:repo", Value: repo}, {Name: "nvkr:commit", Value: commit}}, nil},
		{"no source declaration", []sbom.Property{{Name: "nvkr:repo", Value: repo}, {Name: "nvkr:commit", Value: commit}}, nil},
		{"undeclared", nil, nil},
	} {
		for _, attested := range []bool{false, true} {
			transport := "document"
			if attested {
				transport = "attestation"
			}
			t.Run(test.name+"/"+transport, func(t *testing.T) {
				request := publicationSourceRequest(t, test.properties, nil)
				root := filepath.Join(t.TempDir(), "publication")
				result, err := BuildPublicationRoot(t.Context(), root, request)
				if err != nil {
					t.Fatal(err)
				}
				if attested {
					destination := filepath.Join(t.TempDir(), "attested")
					result, err = AttestEvidence(t.Context(), root, destination, func(_ context.Context, subject string, document []byte) ([]byte, error) {
						return imageIndexFixtureAttestation(subject, document)
					})
					if err != nil {
						t.Fatal(err)
					}
					root = destination
				}
				publication, err := OpenPublication(t.Context(), root, result.RootDigest)
				if err != nil {
					t.Fatal(err)
				}
				defer publication.Close()
				reference := "registry.example.test/product/releases@" + result.RootDigest
				want, err := publication.Description(reference)
				if err != nil || len(want.Evidence) != 1 || !reflect.DeepEqual(want.Evidence[0].Source, test.want) {
					t.Fatalf("source projection differs: %+v, %v", want, err)
				}
				metadata := filepath.Join(t.TempDir(), "metadata")
				if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
					t.Fatal(err)
				}
				for _, layout := range []string{root, metadata} {
					got, err := InspectMetadataDescription(t.Context(), layout, reference)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("metadata source differs from held source: %+v, %v; want %+v", got, err, want)
					}
				}
				if want.Evidence[0].Source != nil {
					want.Evidence[0].Source.Repo = "caller mutation"
				}
				if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("source replaced"), 0o600); err != nil {
					t.Fatal(err)
				}
				again, err := publication.Description(reference)
				if err != nil || !reflect.DeepEqual(again.Evidence[0].Source, test.want) {
					t.Fatalf("held source changed after source or caller mutation: %+v, %v", again, err)
				}
			})
		}
	}
}

func TestPublicationSourceRejectsSubstitutedSubject(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*sbom.BOM)
	}{
		{"subject digest", func(bom *sbom.BOM) { bom.Metadata.Component.Hashes[0].Content = strings.Repeat("b", 64) }},
		{"subject component", func(bom *sbom.BOM) { bom.Metadata.Component.Name = "other-component" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := publicationSourceRequest(t, []sbom.Property{{Name: "nvkr:fromSource", Value: "true"}, {Name: "nvkr:repo", Value: "https://example.test/product"}}, test.edit)
			_, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "publication"), request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func publicationSourceRequest(t *testing.T, properties []sbom.Property, edit func(*sbom.BOM)) PublicationRequest {
	t.Helper()
	request := releasePublicationRequestFixture(t, "", "")
	for i := range request.Objects {
		object := &request.Objects[i]
		if object.Evidence == nil {
			continue
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(object.Bytes, []byte{'\n'}), &bom); err != nil {
			t.Fatal(err)
		}
		bom.Metadata.Component.Properties = append([]sbom.Property(nil), properties...)
		sort.Slice(bom.Metadata.Component.Properties, func(i, j int) bool {
			return bom.Metadata.Component.Properties[i].Name < bom.Metadata.Component.Properties[j].Name
		})
		// Top-level metadata properties must never be mistaken for the source
		// of this exact component subject.
		bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:fromSource", Value: "true"},
			sbom.Property{Name: "nvkr:repo", Value: "https://decoy.test/unrelated"})
		sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
		var err error
		if edit != nil {
			edit(&bom)
			// A deliberately invalid subject must reach the public publication
			// boundary; the fixture cannot use BOM.Marshal's earlier gate.
			object.Bytes, err = canonical.Marshal(bom)
			object.SemanticDigest, _ = canonical.Digest(canonical.KindSBOM, bom)
		} else {
			object.Bytes, err = bom.Marshal()
			object.SemanticDigest, _ = bom.Digest()
		}
		if err != nil {
			t.Fatal(err)
		}
		object.Name = "sbom-" + strings.TrimPrefix(object.SemanticDigest, "sha256:")
	}
	return request
}
