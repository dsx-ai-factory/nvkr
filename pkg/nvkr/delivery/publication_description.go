// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// ValidatePublicationReference admits an exact canonical OCI locator without I/O.
// A tag, URL scheme or implicit repository expansion is not an exact locator.
func ValidatePublicationReference(reference string) error {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || strings.Count(reference, "@") != 1 || ref.Name() != reference || !isDigest(ref.DigestStr()) {
		return e.New(e.CodeUnverifiedRef, "release access requires a canonical repository@sha256:digest reference")
	}
	return nil
}

// PublicationDescription is a detached projection of checked metadata or a held
// publication. It carries no payload, signature, policy or execution authority.
type PublicationDescription struct {
	Reference             string                `json:"reference"`
	ReferenceScope        string                `json:"referenceScope"`
	RootDigest            string                `json:"rootDigest"`
	DeclaredQualification string                `json:"declaredQualification"`
	Metadata              ReleaseMetadata       `json:"metadata"`
	Outputs               []PublicationOutput   `json:"outputs"`
	Evidence              []PublicationEvidence `json:"evidence"`
}

type PublicationOutput struct {
	Output
	Publication   *PublicationOrigin `json:"publication,omitempty"`
	ArtifactType  string             `json:"artifactType"`
	BuildSubjects []BuildSubject     `json:"buildSubjects"`
}

type PublicationEvidence struct {
	Name           string             `json:"name"`
	Kind           string             `json:"kind"`
	Reference      string             `json:"reference"`
	ArtifactType   string             `json:"artifactType"`
	ManifestDigest string             `json:"manifestDigest"`
	BlobDigest     string             `json:"blobDigest"`
	SemanticDigest string             `json:"semanticDigest"`
	Binding        EvidenceBinding    `json:"binding"`
	Source         *PublicationSource `json:"source,omitempty"`
}

// PublicationSource describes the source-built subject of exact release evidence.
// Empty fields preserve missing or redacted properties; this is not a source or
// signer authorization. The original tag cannot be reconstructed from an SBOM.
type PublicationSource struct {
	Repo   string `json:"repo,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Description joins outputs and evidence from this exact held publication.
// Returned slices are independent of both the source layout and the live owner.
// A claimed qualification is reported as metadata, never authenticated here.
func (publication *Publication) Description(reference string) (PublicationDescription, error) {
	if publication == nil || publication.publicationState == nil {
		return PublicationDescription{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || publication.inspection.Root.Release == nil {
		return PublicationDescription{}, e.New(e.CodeGateUnmet, "publication is not open")
	}
	return publicationDescription(reference, publication.inspection, publication.outputs, publication.platforms)
}

func publicationDescription(reference string, inspection Inspection, outputs []Output, platforms map[string]publicationPlatform) (PublicationDescription, error) {
	if _, digest, ok := strings.Cut(reference, "@"); ok && isDigest(digest) && digest != inspection.RootDigest {
		return PublicationDescription{}, e.New(e.CodeVerifyFailed, "checked publication root differs from requested subject")
	}
	outputRefs, err := OutputReferences(reference, inspection)
	if err != nil {
		return PublicationDescription{}, err
	}
	evidenceRefs, err := EvidenceReferences(reference, inspection)
	if err != nil {
		return PublicationDescription{}, err
	}
	root := inspection.Root
	view := PublicationDescription{
		Reference: reference, RootDigest: inspection.RootDigest,
		ReferenceScope:        "derived from supplied root reference; no registry availability claim",
		DeclaredQualification: root.Qualification, Metadata: *root.Release,
		Outputs: make([]PublicationOutput, 0, len(outputs)), Evidence: []PublicationEvidence{},
	}
	objects := make(map[string]PublishedObject, len(root.Objects))
	for _, object := range root.Objects {
		objects[object.Name] = object
		if object.Evidence == nil {
			continue
		}
		binding := *object.Evidence
		binding.Outputs = append([]string{}, binding.Outputs...)
		var source *PublicationSource
		if value, ok := inspection.sources[object.Name]; ok {
			source = &value
		}
		view.Evidence = append(view.Evidence, PublicationEvidence{
			Name: object.Name, Kind: object.Kind, Reference: evidenceRefs[object.Name],
			ArtifactType: object.ArtifactType, ManifestDigest: object.DescriptorDigest,
			BlobDigest: object.BlobDigest, SemanticDigest: object.SemanticDigest, Binding: binding, Source: source,
		})
	}
	for _, output := range cloneOutputs(outputs) {
		object, ok := objects[output.Name]
		if !ok || object.Output == nil || outputRefs[output.Name] == "" {
			return PublicationDescription{}, e.New(e.CodeVerifyFailed, "publication output has no bound repository")
		}
		output.Reference = outputRefs[output.Name]
		platform := platforms[output.Name]
		output.OS, output.Arch = platform.OS, platform.Arch
		var origin *PublicationOrigin
		if object.Output.Publication != nil {
			value := *object.Output.Publication
			origin = &value
		}
		view.Outputs = append(view.Outputs, PublicationOutput{
			Output: output, Publication: origin, ArtifactType: object.ArtifactType,
			BuildSubjects: append([]BuildSubject{}, object.Output.BuildSubjects...),
		})
	}
	sort.Slice(view.Evidence, func(i, j int) bool { return view.Evidence[i].Name < view.Evidence[j].Name })
	return view, nil
}
