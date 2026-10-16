// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package releasemanifest issues the pipeline-attested M4 release manifest: one
// signed document naming the complete qualified artifact set of one producing
// Run. It grants no qualified-lane clearance, tag, channel or revocation state.
package releasemanifest

import (
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const (
	SchemaVersion = "nvkr.dev/release-manifest/v1"
	// Lane is the only evidence lane this manifest can state (ADR-16 §5).
	Lane = "pipeline-attested"
	// DiskScope keeps M3's producer-only disk evidence explicit for consumers.
	DiskScope = "producer-only; content inspection not performed"
)

// Release names the immutable publication root that carries the manifest.
type Release struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

// Inputs are the exact authored and compiled identities the Run realized.
type Inputs struct {
	CatalogDigest            string `json:"catalogDigest"`
	ContentSubject           string `json:"contentSubject"`
	DefinitionDigest         string `json:"definitionDigest"`
	EnvironmentDigest        string `json:"environmentDigest"`
	PlanDigest               string `json:"planDigest"`
	VerificationPolicyDigest string `json:"verificationPolicyDigest"`
}

// Qualification binds the exact M3 evidence attached to every member.
type Qualification struct {
	EvidenceDigest string                          `json:"evidenceDigest"`
	GateVersion    string                          `json:"gateVersion"`
	Invocation     qualificationorigin.Description `json:"invocation"`
	PolicyDigest   string                          `json:"policyDigest"`
	PredicateType  string                          `json:"predicateType"`
}

// Provenance binds the exact verified production predicate bytes of a member.
type Provenance struct {
	Digest        string `json:"digest"`
	PredicateType string `json:"predicateType"`
}

// Member is one named output. Digest is the published OCI subject; for an
// aggregate image it is the index, whose native children the referenced
// qualification evidence enumerates.
type Member struct {
	BuildDigest   string                  `json:"buildDigest"`
	BuildSubjects []delivery.BuildSubject `json:"buildSubjects"`
	Digest        string                  `json:"digest"`
	DiskScope     string                  `json:"diskScope,omitempty"`
	Kind          string                  `json:"kind"`
	Name          string                  `json:"name"`
	Platform      string                  `json:"platform,omitempty"`
	Provenance    Provenance              `json:"provenance"`
	Reference     string                  `json:"reference"`
	SourceLane    string                  `json:"sourceLane"`
}

// Manifest is the strict wire document. Decoding one describes a claim; only
// its qualification-identity attestation on Release authenticates it.
type Manifest struct {
	Inputs        Inputs                          `json:"inputs"`
	Lane          string                          `json:"lane"`
	Members       []Member                        `json:"members"`
	Qualification Qualification                   `json:"qualification"`
	Release       Release                         `json:"release"`
	SchemaVersion string                          `json:"schemaVersion"`
	Source        qualificationorigin.Description `json:"source"`
}

var (
	memberNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	memberKinds       = map[string]bool{lifecycle.OutputExecutable: true, lifecycle.OutputOCI: true,
		lifecycle.OutputAPT: true, lifecycle.OutputHelm: true, lifecycle.OutputDiskImage: true}
	sourceLanes = map[string]bool{lifecycle.SourceLaneHermeticBuild: true,
		lifecycle.SourceLaneWrappedBuild: true, lifecycle.SourceLaneAcquired: true}
)

func (manifest Manifest) validate() error {
	if manifest.SchemaVersion != SchemaVersion || manifest.Lane != Lane {
		return e.New(e.CodeVerifyFailed, "unknown release manifest schema or lane")
	}
	if err := validateRelease(manifest.Release); err != nil {
		return err
	}
	inputs := manifest.Inputs
	for _, digest := range []string{inputs.CatalogDigest, inputs.ContentSubject, inputs.DefinitionDigest,
		inputs.EnvironmentDigest, inputs.PlanDigest, inputs.VerificationPolicyDigest} {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "release manifest omits exact input identities")
		}
	}
	source, qualifier := manifest.Source, manifest.Qualification.Invocation
	if err := source.Validate(); err != nil {
		return err
	}
	if err := qualifier.Validate(); err != nil {
		return err
	}
	if source.Commit == "" || source.BuilderID == "" || source.InvocationID == "" || qualifier.BuilderID == "" ||
		qualifier.InvocationID == "" || qualifier.Repository != source.Repository || qualifier.Ref != source.Ref ||
		qualifier.BuilderID == source.BuilderID || qualifier.InvocationID == source.InvocationID {
		return e.New(e.CodeVerifyFailed, "release manifest lacks separate exact build and qualification identities")
	}
	q := manifest.Qualification
	if q.PredicateType != sign.QualificationPredicateType || q.GateVersion != qualification.GateVersion ||
		!canonical.IsDigest(q.EvidenceDigest) || !canonical.IsDigest(q.PolicyDigest) {
		return e.New(e.CodeVerifyFailed, "release manifest does not bind exact qualification evidence")
	}
	if len(manifest.Members) == 0 {
		return e.New(e.CodeReleaseIncomplete, "release manifest requires the complete member set")
	}
	previous := ""
	for _, member := range manifest.Members {
		if member.Name <= previous {
			return e.New(e.CodeVerifyFailed, "release manifest members must be sorted and unique")
		}
		previous = member.Name
		if err := validateMember(member); err != nil {
			return err
		}
	}
	return nil
}

func validateRelease(release Release) error {
	if err := delivery.ValidatePublicationReference(release.Reference); err != nil {
		return err
	}
	if strings.Contains(release.Reference, "://") {
		return e.New(e.CodeVerifyFailed, "release manifest root must be a canonical immutable registry reference")
	}
	return (sign.Subject{Ref: release.Reference, Digest: release.Digest}).Validate()
}

func validateMember(member Member) error {
	if !memberNamePattern.MatchString(member.Name) || !memberKinds[member.Kind] {
		return e.New(e.CodeVerifyFailed, "release manifest member name or kind is not canonical")
	}
	if err := delivery.ValidatePublicationReference(member.Reference); err != nil {
		return err
	}
	if err := (sign.Subject{Ref: member.Reference, Digest: member.Digest}).Validate(); err != nil {
		return err
	}
	if !canonical.IsDigest(member.BuildDigest) {
		return e.New(e.CodeVerifyFailed, "release manifest member %q omits its Build Digest", member.Name)
	}
	if !sourceLanes[member.SourceLane] {
		return e.New(e.CodeVerifyFailed, "release manifest member %q has an unknown source lane", member.Name)
	}
	if member.Provenance.PredicateType != sign.ProvenancePredicateType || !canonical.IsDigest(member.Provenance.Digest) {
		return e.New(e.CodeProvenanceMissing, "release manifest member %q omits exact production provenance", member.Name)
	}
	if member.Platform != "" && member.Platform != "linux/amd64" && member.Platform != "linux/arm64" {
		return e.New(e.CodeVerifyFailed, "release manifest member %q has an unsupported native platform", member.Name)
	}
	if (member.Kind == lifecycle.OutputDiskImage) != (member.DiskScope == DiskScope) {
		return e.New(e.CodeVerifyFailed, "release manifest member %q misstates disk evidence scope", member.Name)
	}
	if member.BuildSubjects == nil {
		return e.New(e.CodeVerifyFailed, "release manifest member %q omits its producer subject list", member.Name)
	}
	previous := ""
	for _, subject := range member.BuildSubjects {
		if !memberNamePattern.MatchString(subject.Component) || subject.Component <= previous || !canonical.IsDigest(subject.Digest) {
			return e.New(e.CodeVerifyFailed, "release manifest build subjects must be canonical, sorted, unique and exact")
		}
		previous = subject.Component
	}
	return nil
}

// Validate admits the complete document and the exact signing transport bound.
func (manifest Manifest) Validate() error {
	_, err := manifest.Canonical()
	return err
}

// Canonical returns the exact bytes that are signed and attested.
func (manifest Manifest) Canonical() ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > sign.MaxQualificationPredicateBytes {
		return nil, e.New(e.CodeEngineCapability, "release manifest exceeds the signing transport limit")
	}
	return raw, nil
}

// Digest is the domain-separated release manifest identity (ADR-01).
func (manifest Manifest) Digest() (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindReleaseManifest, manifest)
}

// Decode strictly parses canonical manifest bytes. The result is a description,
// not authority; callers must verify its attestation before relying on it.
func Decode(raw []byte) (Manifest, error) {
	if int64(len(raw)) > sign.MaxQualificationPredicateBytes {
		return Manifest{}, e.New(e.CodeVerifyFailed, "release manifest exceeds the signing transport limit")
	}
	var manifest Manifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		return Manifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding release manifest")
	}
	exact, err := manifest.Canonical()
	if err != nil {
		return Manifest{}, err
	}
	if string(exact) != string(raw) {
		return Manifest{}, e.New(e.CodeVerifyFailed, "release manifest bytes are not canonical")
	}
	return manifest, nil
}

// fromEvidence projects complete validated evidence and its authenticated
// per-output provenance into the manifest. It never adds or drops a member.
func fromEvidence(evidence qualification.Evidence, members []lifecycle.QualificationReleaseMember) (Manifest, error) {
	evidenceDigest, err := evidence.Digest()
	if err != nil {
		return Manifest{}, err
	}
	checks := evidence.ArtifactChecks
	if len(members) != len(checks.Outputs) {
		return Manifest{}, e.New(e.CodeReleaseIncomplete, "release provenance does not cover the exact qualified output set")
	}
	release := checks.Inputs.ReleaseSubject
	manifest := Manifest{SchemaVersion: SchemaVersion, Lane: Lane,
		Release: Release{Reference: release, Digest: release[strings.LastIndex(release, "@")+1:]},
		Source:  checks.Origin,
		Inputs: Inputs{CatalogDigest: checks.CatalogDigest, ContentSubject: checks.Inputs.ContentSubject,
			DefinitionDigest: checks.Inputs.DefinitionDigest, EnvironmentDigest: checks.Inputs.EnvironmentDigest,
			PlanDigest: checks.Inputs.PlanDigest, VerificationPolicyDigest: checks.Inputs.PolicyDigest},
		Qualification: Qualification{PredicateType: sign.QualificationPredicateType, EvidenceDigest: evidenceDigest,
			GateVersion: evidence.GateVersion, PolicyDigest: evidence.ToolSmoke.PolicyDigest, Invocation: evidence.Invocation},
		Members: make([]Member, 0, len(checks.Outputs))}
	for i, output := range checks.Outputs {
		if members[i].Output != output.Name {
			return Manifest{}, e.New(e.CodeReleaseIncomplete, "release provenance differs from the qualified output %q", output.Name)
		}
		member := Member{Name: output.Name, Kind: output.Kind, Reference: output.Reference, Digest: output.Subject,
			BuildDigest: output.BuildDigest, Platform: output.Platform, SourceLane: members[i].SourceLane,
			BuildSubjects: append([]delivery.BuildSubject{}, output.BuildSubjects...),
			Provenance:    Provenance{PredicateType: sign.ProvenancePredicateType, Digest: members[i].ProvenanceDigest}}
		if output.Kind == lifecycle.OutputDiskImage {
			member.DiskScope = DiskScope
		}
		manifest.Members = append(manifest.Members, member)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
