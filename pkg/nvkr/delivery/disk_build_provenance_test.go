// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestDiskBuildProvenanceReleaseContract(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []string{"valid", "missing evidence", "strict mode", "unknown mode", "wrong subject", "false inspection", "missing scope"} {
			t.Run(arch+"/"+test, func(t *testing.T) {
				request, _ := diskPublicationRequest(t, arch)
				output := request.Objects[0].Output
				output.Disk.EvidenceMode = sbom.DiskBuildProvenance
				request.Objects = request.Objects[:1]
				for _, subject := range output.BuildSubjects {
					bom, err := sbom.NewSubject(output.BuildDigest, subject.Component, subject.Digest)
					if err != nil {
						t.Fatal(err)
					}
					digestSubject := subject.Digest
					if test == "wrong subject" {
						digestSubject = "sha256:" + strings.Repeat("a", 64)
					}
					bom, err = sbom.ForDiskBuildProvenance(bom, digestSubject)
					if err != nil {
						t.Fatal(err)
					}
					if test == "missing scope" {
						for i, prop := range bom.Metadata.Props {
							if prop.Name == "nvkr:diskInspection" {
								bom.Metadata.Props = append(bom.Metadata.Props[:i], bom.Metadata.Props[i+1:]...)
								break
							}
						}
					}
					if test == "false inspection" {
						bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:diskInspectorDigest", Value: output.BuildDigest})
					}
					sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
					digest, err := bom.Digest()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := bom.Marshal()
					if err != nil {
						t.Fatal(err)
					}
					request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(digest, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: digest, Bytes: raw,
						Evidence: &EvidenceBinding{Outputs: []string{"node-image"}, Component: subject.Component, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject.Digest}})
				}
				switch test {
				case "missing evidence":
					request.Objects = request.Objects[:2]
				case "strict mode":
					output.Disk.EvidenceMode = ""
				case "unknown mode":
					output.Disk.EvidenceMode = "skip-everything"
				}
				path := filepath.Join(t.TempDir(), "publication")
				_, err := BuildPublicationRoot(t.Context(), path, request)
				if test != "valid" {
					want := e.CodeVerifyFailed
					if test == "missing evidence" || test == "strict mode" || test == "missing scope" {
						want = e.CodeProvenanceMissing
					}
					assertRegistryCode(t, err, want)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Inspect(t.Context(), path); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestQualifiedPublicationRejectsDiskBuildProvenance(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, test := range []struct{ name, mode string }{
			{"strict", ""},
			{"producer provenance", sbom.DiskBuildProvenance},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				request, _ := diskPublicationRequest(t, arch)
				path := filepath.Join(t.TempDir(), "publication")
				if _, err := BuildPublicationRoot(t.Context(), path, request); err != nil {
					t.Fatal(err)
				}
				inspection, err := Inspect(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				root := inspection.Root
				// These describe a structurally complete qualified root; they do
				// not create clearance, signatures or qualified publication proof.
				pin := request.Release.DefinitionDigest
				root.Qualification = QualificationQualified
				root.PublicationIdentity, root.PublicationManifest = pin, pin
				root.ReleaseSetDigest, root.InventoryDigest, root.ClearanceDigest = pin, pin, pin
				if err := root.Validate(); err != nil {
					t.Fatalf("complete strict qualified description rejected before mode mutation: %v", err)
				}
				changed := false
				for index := range root.Objects {
					object := &root.Objects[index]
					if object.Output != nil && object.Output.Disk != nil {
						object.Output.Disk.EvidenceMode = test.mode
						changed = true
					}
				}
				if !changed {
					t.Fatal("fixture has no disk output to exercise qualification admission")
				}
				err = root.Validate()
				if test.mode == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				assertRegistryCode(t, err, e.CodeGateUnmet)
				if !strings.Contains(err.Error(), "producer-provenance-only disk output") {
					t.Fatalf("qualified rejection missed disk evidence gate: %v", err)
				}
				_, err = root.Digest()
				assertRegistryCode(t, err, e.CodeGateUnmet)
			})
		}
	}
}
