// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"regexp"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ReleaseMetadata is the release-wide context stored in the sole release index
// metadata document. Output identities and evidence membership live on Objects.
type ReleaseMetadata struct {
	InvocationID      string `json:"invocationId,omitempty"`
	DefinitionDigest  string `json:"definitionDigest"`
	EnvironmentDigest string `json:"environmentDigest"`
	PlanDigest        string `json:"planDigest"`
	ContentSubject    string `json:"contentSubject"`
}

// OutputBinding adds meaning to the object's one descriptor; it does not repeat
// the name or artifact digest already owned by the release index object.
type OutputBinding struct {
	// Publication retains the authenticated output-local handoff selected by assembly.
	Publication *PublicationOrigin `json:"publication,omitempty"`
	// Repository is a sibling repository name relative to the release repository namespace.
	Repository    string             `json:"repository"`
	Format        string             `json:"format"`
	BuildDigest   string             `json:"buildDigest"`
	Components    []string           `json:"components"`
	TransportPath string             `json:"transportPath,omitempty"`
	BuildSubjects []BuildSubject     `json:"buildSubjects,omitempty"`
	Disk          *DiskOutput        `json:"disk,omitempty"`
	ImageIndex    *ImageIndexBinding `json:"imageIndex,omitempty"`
}

// PublicationOrigin preserves preparation and signing identities across retries.
// These fields are descriptions until the containing release is authenticated.
type PublicationOrigin struct {
	Ref                  string `json:"ref"`
	PreparedInvocationID string `json:"preparedInvocationId"`
	SignedInvocationID   string `json:"signedInvocationId"`
}

type BuildSubject struct {
	Component string `json:"component"`
	Digest    string `json:"digest"`
}

// EvidenceBinding describes exactly which stage and bytes a document inventories.
// DescriptorDigest and BlobDigest on the enclosing object bind the document.
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

func validateReleaseMetadata(root PublicationRoot) error {
	if root.Release == nil {
		for _, object := range root.Objects {
			if object.Output != nil && object.Output.ImageIndex != nil {
				return e.New(e.CodeVerifyFailed, "aggregate image requires exact release membership")
			}
		}
		return nil
	}
	m := root.Release
	if len(m.InvocationID) > 2048 {
		return e.New(e.CodeVerifyFailed, "release invocation identity exceeds the maximum length")
	}
	if !canonical.IsDigest(m.DefinitionDigest) || !canonical.IsDigest(m.EnvironmentDigest) ||
		!canonical.IsDigest(m.PlanDigest) || !canonical.IsDigest(m.ContentSubject) {
		return e.New(e.CodeVerifyFailed, "release metadata has incomplete identities")
	}
	outputs := map[string]PublishedObject{}
	repositories := map[string]bool{}
	names := map[string]bool{}
	for _, o := range root.Objects {
		if names[o.Name] {
			return e.New(e.CodeVerifyFailed, "duplicate release object name")
		}
		names[o.Name] = true
		if o.Output == nil && o.Evidence == nil {
			return e.New(e.CodeVerifyFailed, "release object has no output or evidence binding")
		}

		if o.Output == nil {
			continue
		}
		if origin := o.Output.Publication; origin != nil {
			if ValidatePublicationReference(origin.Ref) != nil || len(origin.Ref) > 2048 || m.InvocationID == "" ||
				origin.PreparedInvocationID == "" || len(origin.PreparedInvocationID) > 2048 ||
				origin.SignedInvocationID == "" || len(origin.SignedInvocationID) > 2048 {
				return e.New(e.CodeVerifyFailed, "release output has an invalid publication origin")
			}
		}
		if o.Evidence != nil || !canonical.IsDigest(o.Output.BuildDigest) {
			return e.New(e.CodeVerifyFailed, "release object has conflicting output/evidence bindings")
		}
		if _, ok := outputs[o.Name]; ok {
			return e.New(e.CodeVerifyFailed, "duplicate release output")
		}
		if !siblingRepositoryPattern.MatchString(o.Output.Repository) || repositories[o.Output.Repository] {
			return e.New(e.CodeVerifyFailed, "output requires a unique canonical sibling repository")
		}
		repositories[o.Output.Repository] = true
		if o.Output.ImageIndex != nil {
			if err := validateImageIndexObject(o); err != nil {
				return err
			}
		}
		var kind, media string
		switch o.Output.Format {
		case "executable":
			kind, media = "binary-bundle", MediaBinaryBundle
		case "oci":
			kind, media = "container-image", o.ArtifactType
			if o.Output.ImageIndex != nil {
				media = MediaOCIIndex
			} else if !types.MediaType(media).IsImage() {
				return e.New(e.CodeVerifyFailed, "container output requires a native OCI or Docker schema-2 image manifest")
			}
		case outputFormatHelm:
			kind, media = outputFormatHelm, "application/vnd.nvkr.helm-bundle.v1"
		case "apt-repository":
			kind, media = "apt-repository", "application/vnd.nvkr.apt-repository.v1"
		case "disk-image":
			kind, media = "disk-image", o.ArtifactType
			if media != MediaDiskImage || o.Output.Disk == nil || !isDigest(o.Output.Disk.PayloadDigest) ||
				o.Output.Disk.PayloadDigest != o.BlobDigest || o.Output.TransportPath != "" {
				return e.New(e.CodeVerifyFailed, "published disk requires an exact payload binding")
			}
			if err := o.Output.Disk.validatePublicationEncryption(); err != nil {
				return err
			}
			if err := (Output{Name: o.Name, Kind: kind, Digest: o.DescriptorDigest, BuildDigest: o.Output.BuildDigest,
				Components: o.Output.Components, Disk: o.Output.Disk}).Validate(); err != nil {
				return err
			}
		default:
			return e.New(e.CodeVerifyFailed, "unsupported release output format")
		}
		if o.Kind != kind || o.ArtifactType != media {
			return e.New(e.CodeVerifyFailed, "release output type differs from format")
		}
		if kind != "disk-image" && o.Output.Disk != nil {
			return e.New(e.CodeVerifyFailed, "non-disk release output carries disk binding")
		}
		previous := ""
		for _, component := range o.Output.Components {
			if !canonicalToken(component) || component <= previous {
				return e.New(e.CodeVerifyFailed, "output components must be sorted and unique")
			}
			previous = component
		}
		if o.Output.Format != outputFormatHelm && len(o.Output.Components) == 0 {
			return e.New(e.CodeProvenanceMissing, "release output has no build subjects")
		}
		if o.Output.Format == "oci" && len(o.Output.Components) != 1 {
			return e.New(e.CodeVerifyFailed, "image requires one component")
		}
		outputs[o.Name] = o
	}
	if len(outputs) == 0 {
		return e.New(e.CodeReleaseIncomplete, "release has no deliverables")
	}
	return nil
}

var siblingRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
