// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"cmp"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
)

// DescriptionSchemaVersion identifies the external JSON presentation contract.
// Description bytes are neither canonical identity input nor an authority receipt.
const DescriptionSchemaVersion = "nvkr.dev/release-description/v1"

// Description is the versioned, product-independent release inventory returned by
// JSON rendering. It reports the checked metadata closure and publisher claims
// separately from checks that this presentation does not establish. Consumers own
// role assignment, platform selection and application compatibility requirements.
//
// These explicit DTOs intentionally do not embed delivery's implementation types.
// Changing an internal publication record cannot silently extend this contract.
type Description struct {
	SchemaVersion         string            `json:"schemaVersion"`
	Inspection            string            `json:"inspection"`
	InspectionDetails     InspectionDetails `json:"inspectionDetails"`
	SelectedOutput        string            `json:"selectedOutput,omitempty"`
	TotalOutputs          int               `json:"totalOutputs"`
	Reference             string            `json:"reference"`
	ReferenceScope        string            `json:"referenceScope"`
	RootDigest            string            `json:"rootDigest"`
	DeclaredQualification string            `json:"declaredQualification"`
	Metadata              Metadata          `json:"metadata"`
	Outputs               []Output          `json:"outputs"`
	Evidence              []Evidence        `json:"evidence"`
}

// InspectionDetails describes the bounded operation that supplied this view.
// Filtering changes displayed outputs, never the metadata closure checked first.
type InspectionDetails struct {
	Scope           string           `json:"scope"`
	MetadataClosure string           `json:"metadataClosure"`
	OutputSelection string           `json:"outputSelection"`
	Checks          InspectionChecks `json:"checks"`
}

// InspectionChecks uses "checked" and "not-checked" as explicit statuses. In
// particular, checked metadata integrity is not authenticated publisher identity.
type InspectionChecks struct {
	MetadataIntegrity         string `json:"metadataIntegrity"`
	PayloadIntegrity          string `json:"payloadIntegrity"`
	Signatures                string `json:"signatures"`
	ConsumerPolicy            string `json:"consumerPolicy"`
	RegistryAvailability      string `json:"registryAvailability"`
	InstallationCompatibility string `json:"installationCompatibility"`
}

// Metadata retains publication-bound lifecycle identities without recomputing
// them or introducing a new release identity.
type Metadata struct {
	InvocationID      string `json:"invocationId,omitempty"`
	DefinitionDigest  string `json:"definitionDigest"`
	EnvironmentDigest string `json:"environmentDigest"`
	PlanDigest        string `json:"planDigest"`
	ContentSubject    string `json:"contentSubject"`
}

// Output describes one exact named artifact. OS/Arch describe a native artifact;
// ImageIndex retains the complete platform membership of an aggregate image.
// Missing platform labels assert neither portability nor incompatibility.
type Output struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Digest        string         `json:"digest"`
	Reference     string         `json:"reference,omitempty"`
	OS            string         `json:"os,omitempty"`
	Arch          string         `json:"arch,omitempty"`
	ChartVersion  string         `json:"chartVersion,omitempty"`
	BuildDigest   string         `json:"buildDigest"`
	Components    []string       `json:"components,omitempty"`
	Disk          *Disk          `json:"disk,omitempty"`
	ImageIndex    *ImageIndex    `json:"imageIndex,omitempty"`
	ArtifactType  string         `json:"artifactType"`
	BuildSubjects []BuildSubject `json:"buildSubjects"`
}

// Disk describes the publication's exact disk producer and payload binding.
// EvidenceMode is a publisher declaration, not applied consumer policy.
type Disk struct {
	EvidenceMode  string `json:"evidenceMode,omitempty"`
	PayloadDigest string `json:"payloadDigest"`
	Producer      string `json:"producer"`
	Component     string `json:"component"`
	Format        string `json:"format"`
	Encryption    string `json:"encryption"`
	Size          int64  `json:"size"`
	PlanDigest    string `json:"planDigest"`
	Outcome       string `json:"outcome"`
}

type ImageIndex struct {
	RequiredPlatforms []string          `json:"requiredPlatforms"`
	Children          []ImageIndexChild `json:"children"`
}

// ImageIndexChild preserves manifest, logical build and inspected export
// identities separately; the child's manifest never replaces the parent digest.
type ImageIndexChild struct {
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifestDigest"`
	Size           int64  `json:"size"`
	BuildDigest    string `json:"buildDigest"`
	Component      string `json:"component"`
	BuildSubject   string `json:"buildSubject"`
}

type BuildSubject struct {
	Component string `json:"component"`
	Digest    string `json:"digest"`
}

// Evidence preserves the exact subject, component, stage and output membership
// for each evidence document. Source describes this subject only. It must never
// be promoted to a single release-wide source or inferred from an artifact name.
type Evidence struct {
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	Reference      string          `json:"reference"`
	ArtifactType   string          `json:"artifactType"`
	ManifestDigest string          `json:"manifestDigest"`
	BlobDigest     string          `json:"blobDigest"`
	SemanticDigest string          `json:"semanticDigest"`
	Binding        EvidenceBinding `json:"binding"`
	Source         *Source         `json:"source,omitempty"`
}

type EvidenceBinding struct {
	Platform              string   `json:"platform,omitempty"`
	DocumentDigest        string   `json:"documentDigest"`
	Outputs               []string `json:"outputs"`
	Component             string   `json:"component"`
	Stage                 string   `json:"stage"`
	SubjectKind           string   `json:"subjectKind"`
	SubjectDigest         string   `json:"subjectDigest"`
	InspectionInputDigest string   `json:"inspectionInputDigest,omitempty"`
}

// Source contains available source-built subject properties from exact evidence.
// Empty fields preserve unavailable or redacted data; no tag is reconstructed.
type Source struct {
	Repo   string `json:"repo,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Describe returns the same detached, sorted description as JSON rendering,
// optionally selecting one exact output after the owner's complete inspection.
// Like Render, it describes that prerequisite; it does not authenticate inputs.
func Describe(publication delivery.PublicationDescription, output string) (Description, error) {
	selected, err := selectView(publication, Options{Output: output})
	if err != nil {
		return Description{}, err
	}
	return describe(selected), nil
}

func describe(selected view) Description {
	selection := "all"
	if selected.SelectedOutput != "" {
		selection = "filtered"
	}
	result := Description{
		SchemaVersion: DescriptionSchemaVersion, Inspection: selected.Inspection,
		InspectionDetails: InspectionDetails{
			Scope: "metadata-only", MetadataClosure: "complete", OutputSelection: selection,
			Checks: InspectionChecks{
				MetadataIntegrity: "checked", PayloadIntegrity: "not-checked", Signatures: "not-checked",
				ConsumerPolicy: "not-checked", RegistryAvailability: "not-checked", InstallationCompatibility: "not-checked",
			},
		},
		SelectedOutput: selected.SelectedOutput, TotalOutputs: selected.TotalOutputs,
		Reference: selected.Reference, ReferenceScope: selected.ReferenceScope,
		RootDigest: selected.RootDigest, DeclaredQualification: selected.DeclaredQualification,
		Metadata: Metadata{
			InvocationID: selected.Metadata.InvocationID, DefinitionDigest: selected.Metadata.DefinitionDigest,
			EnvironmentDigest: selected.Metadata.EnvironmentDigest, PlanDigest: selected.Metadata.PlanDigest,
			ContentSubject: selected.Metadata.ContentSubject,
		},
		Outputs: make([]Output, 0, len(selected.Outputs)), Evidence: make([]Evidence, 0, len(selected.Evidence)),
	}
	for _, output := range selected.Outputs {
		projected := Output{
			Name: output.Name, Kind: output.Kind, Digest: output.Digest, Reference: output.Reference,
			OS: output.OS, Arch: output.Arch, ChartVersion: output.ChartVersion,
			BuildDigest: output.BuildDigest, Components: slices.Clone(output.Components),
			ArtifactType: output.ArtifactType, BuildSubjects: make([]BuildSubject, 0, len(output.BuildSubjects)),
		}
		slices.Sort(projected.Components)
		for _, subject := range output.BuildSubjects {
			projected.BuildSubjects = append(projected.BuildSubjects, BuildSubject{Component: subject.Component, Digest: subject.Digest})
		}
		slices.SortFunc(projected.BuildSubjects, func(a, b BuildSubject) int {
			return cmp.Or(cmp.Compare(a.Component, b.Component), cmp.Compare(a.Digest, b.Digest))
		})
		if disk := output.Disk; disk != nil {
			projected.Disk = &Disk{
				EvidenceMode: disk.EvidenceMode, PayloadDigest: disk.PayloadDigest, Producer: disk.Producer,
				Component: disk.Component, Format: disk.Format, Encryption: disk.Encryption, Size: disk.Size,
				PlanDigest: disk.PlanDigest, Outcome: disk.Outcome,
			}
		}
		if index := output.ImageIndex; index != nil {
			projected.ImageIndex = &ImageIndex{
				RequiredPlatforms: append([]string{}, index.RequiredPlatforms...),
				Children:          make([]ImageIndexChild, 0, len(index.Children)),
			}
			slices.Sort(projected.ImageIndex.RequiredPlatforms)
			for _, child := range index.Children {
				projected.ImageIndex.Children = append(projected.ImageIndex.Children, ImageIndexChild{
					Platform: child.Platform, ManifestDigest: child.ManifestDigest, Size: child.Size,
					BuildDigest: child.BuildDigest, Component: child.Component, BuildSubject: child.BuildSubject,
				})
			}
			slices.SortFunc(projected.ImageIndex.Children, func(a, b ImageIndexChild) int {
				return cmp.Compare(a.Platform, b.Platform)
			})
		}
		result.Outputs = append(result.Outputs, projected)
	}
	slices.SortFunc(result.Outputs, func(a, b Output) int { return cmp.Compare(a.Name, b.Name) })
	for _, evidence := range selected.Evidence {
		binding := evidence.Binding
		projected := Evidence{
			Name: evidence.Name, Kind: evidence.Kind, Reference: evidence.Reference, ArtifactType: evidence.ArtifactType,
			ManifestDigest: evidence.ManifestDigest, BlobDigest: evidence.BlobDigest, SemanticDigest: evidence.SemanticDigest,
			Binding: EvidenceBinding{
				Platform: binding.Platform, DocumentDigest: binding.DocumentDigest, Outputs: append([]string{}, binding.Outputs...),
				Component: binding.Component, Stage: binding.Stage, SubjectKind: binding.SubjectKind,
				SubjectDigest: binding.SubjectDigest, InspectionInputDigest: binding.InspectionInputDigest,
			},
		}
		slices.Sort(projected.Binding.Outputs)
		if evidence.Source != nil {
			projected.Source = &Source{Repo: evidence.Source.Repo, Commit: evidence.Source.Commit}
		}
		result.Evidence = append(result.Evidence, projected)
	}
	slices.SortFunc(result.Evidence, func(a, b Evidence) int { return cmp.Compare(a.Name, b.Name) })
	return result
}
