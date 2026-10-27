// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qualification joins authenticated artifact checks and smoke results
// before permitting independent qualification signing. It grants no runtime,
// reproduction, full-disk inspection or release authorization.
package qualification

import (
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const SchemaVersion = "nvkr.dev/qualification-evidence/v2"
const GateVersion = "nvkr.dev/qualification-gate/v2"
const DevelopmentSchemaVersion = "nvkr.dev/development-qualification-evidence/v1"
const DevelopmentGateVersion = "nvkr.dev/development-qualification-gate/v1"

// Verdict is diagnostic data. Only owner-issued receipts authorize publication.
type Verdict struct {
	Output  string `json:"output"`
	Subject string `json:"subject"`
	Check   string `json:"check"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

type ExternalSuite struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Evidence is the strict wire predicate. Decoding it describes a claim; the
// separate qualification workflow signature authenticates that claim.
type Evidence struct {
	ToolSmoke      lifecycle.ToolSmokeDescription     `json:"toolSmoke"`
	SchemaVersion  string                             `json:"schemaVersion"`
	GateVersion    string                             `json:"gateVersion"`
	ArtifactChecks lifecycle.ArtifactCheckDescription `json:"artifactChecks"`
	Invocation     qualificationorigin.Description    `json:"invocation"`
	Smoke          []Verdict                          `json:"smoke"`
	ExternalSuites []ExternalSuite                    `json:"externalSuites"`
}

var requiredArtifactChecks = []string{"build-signature-and-provenance", "embedded-declarations", "payload-pins", "run-and-inputs", "schema-and-membership", "signed-evidence"}

var subjectNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

func (evidence Evidence) validate() error {
	dev := evidence.SchemaVersion == DevelopmentSchemaVersion && evidence.GateVersion == DevelopmentGateVersion
	if (!dev && (evidence.SchemaVersion != SchemaVersion || evidence.GateVersion != GateVersion)) || evidence.ArtifactChecks.GateVersion != lifecycle.ArtifactCheckGateVersion {
		return e.New(e.CodeVerifyFailed, "unknown qualification schema or gate version")
	}
	checks := evidence.ArtifactChecks
	if dev != (checks.Origin.Branch != "") || dev != (evidence.Invocation.Branch != "") || dev != evidence.ToolSmoke.Policy.Source.Development {
		return e.New(e.CodeVerifyFailed, "production qualification rejects development evidence")
	}
	if err := checks.Origin.Validate(); err != nil {
		return err
	}
	if err := evidence.Invocation.Validate(); err != nil {
		return err
	}
	if checks.Origin.Repository == "" || checks.Origin.Commit == "" || evidence.Invocation.Repository != checks.Origin.Repository ||
		evidence.Invocation.BuilderID == checks.Origin.BuilderID || evidence.Invocation.InvocationID == checks.Origin.InvocationID ||
		(!dev && evidence.Invocation.Ref != checks.Origin.Ref) || evidence.Invocation.BuilderID == "" || evidence.Invocation.InvocationID == "" {
		return e.New(e.CodeVerifyFailed, "qualification evidence lacks separate exact build and qualification identities")
	}
	for _, digest := range []string{checks.CatalogDigest, checks.Inputs.PolicyDigest, checks.Inputs.DefinitionDigest, checks.Inputs.EnvironmentDigest, checks.Inputs.PlanDigest, checks.Inputs.ContentSubject} {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "qualification evidence omits exact input identities")
		}
	}
	if err := delivery.ValidatePublicationReference(checks.Inputs.ReleaseSubject); err != nil {
		return err
	}
	if strings.Contains(checks.Inputs.ReleaseSubject, "://") {
		return e.New(e.CodeVerifyFailed, "qualification release subject must be a canonical immutable registry reference")
	}
	if len(checks.Outputs) == 0 || len(evidence.Smoke) != len(checks.Outputs) {
		return e.New(e.CodeReleaseIncomplete, "qualification evidence requires the complete output and smoke set")
	}
	if dev {
		namespace, err := development.ParseNamespace("ghcr.io/" + strings.ToLower(checks.Origin.Repository) + "/development")
		if err != nil {
			return err
		}
		refs := []string{checks.Inputs.ReleaseSubject}
		for _, output := range checks.Outputs {
			refs = append(refs, output.Reference)
		}
		for _, ref := range refs {
			repository, _, ok := strings.Cut(ref, "@")
			if !ok {
				return e.New(e.CodeVerifyFailed, "development evidence requires immutable subjects")
			}
			parsed, err := namespace.ParseReference(repository)
			if err != nil {
				return err
			}
			invocation := "https://github.com/" + namespace.Repository() + "/actions/runs/" + parsed.RunID() + "/attempts/" + parsed.Attempt()
			if err := factory.ValidatePublicationInvocation(invocation, checks.Origin.InvocationID); err != nil {
				return err
			}
		}
	}
	previous := ""
	for _, output := range checks.Outputs {
		if output.Name <= previous || output.Overall != "pass" || !canonical.IsDigest(output.BuildDigest) {
			return e.New(e.CodeGateUnmet, "qualification outputs must be sorted, unique and passing")
		}
		previous = output.Name
		if err := (sign.Subject{Ref: output.Reference, Digest: output.Subject}).Validate(); err != nil {
			return err
		}
		if err := validateOutputSubjects(output); err != nil {
			return err
		}
		if len(output.Checks) != len(requiredArtifactChecks) {
			return e.New(e.CodeGateUnmet, "qualification omits artifact checks")
		}
		for i, check := range output.Checks {
			allowed := check.Status == "pass" || ((check.Name == "embedded-declarations" || check.Name == "payload-pins") && check.Status == "not-applicable")
			if check.Name != requiredArtifactChecks[i] || !allowed || check.Code != "" {
				return e.New(e.CodeGateUnmet, "qualification contains missing or failing artifact checks")
			}
		}
		if output.Kind == lifecycle.OutputDiskImage && output.DiskEvidence != "producer-only; content inspection not performed" {
			return e.New(e.CodeVerifyFailed, "qualification cannot claim disk inspection")
		}
		if output.Kind != lifecycle.OutputDiskImage && output.DiskEvidence != "" {
			return e.New(e.CodeVerifyFailed, "non-disk output claims disk evidence")
		}
	}
	if err := evidence.ToolSmoke.Validate(); err != nil {
		return err
	}
	bound, err := canonical.Marshal(evidence.ToolSmoke.ArtifactChecks)
	if err != nil {
		return err
	}
	actual, err := canonical.Marshal(checks)
	if err != nil {
		return err
	}
	if string(bound) != string(actual) || evidence.ToolSmoke.Invocation != evidence.Invocation {
		return e.New(e.CodeGateUnmet, "qualification smoke differs from exact artifact checks or invocation")
	}
	expected, err := smokeVerdicts(evidence.ToolSmoke)
	if err != nil {
		return err
	}
	for i, verdict := range evidence.Smoke {
		if verdict != expected[i] {
			return e.New(e.CodeGateUnmet, "qualification smoke verdict differs from authenticated worker evidence")
		}
	}
	suites, err := outsideSuites(evidence.ToolSmoke.Policy.OutsideMVPSuites)
	if err != nil {
		return err
	}
	if len(evidence.ExternalSuites) != len(suites) {
		return e.New(e.CodeGateUnmet, "qualification external suite scope differs from independent policy")
	}
	for i, suite := range evidence.ExternalSuites {
		if suite != suites[i] {
			return e.New(e.CodeGateUnmet, "qualification external suite status differs from independent policy")
		}
	}

	previous = ""
	for _, suite := range evidence.ExternalSuites {
		if suite.Name == "" || suite.Name <= previous || strings.TrimSpace(suite.Name) != suite.Name || suite.Status != "not-evaluated" || suite.Reason != "outside MVP scope" {
			return e.New(e.CodeGateUnmet, "external suites require authenticated results before claiming evaluation")
		}
		previous = suite.Name
	}
	return nil
}

func validateOutputSubjects(output lifecycle.ArtifactCheckOutput) error {
	if !subjectNamePattern.MatchString(output.Name) {
		return e.New(e.CodeVerifyFailed, "qualification output name is not canonical")
	}
	if err := delivery.ValidatePublicationReference(output.Reference); err != nil {
		return err
	}
	if output.ImageIndex != nil {
		if output.Kind != lifecycle.OutputOCI || output.Platform != "" || len(output.BuildSubjects) != 0 {
			return e.New(e.CodeVerifyFailed, "aggregate image cannot carry a single platform or flat build subjects")
		}
		if err := output.ImageIndex.Validate(); err != nil {
			return err
		}
		// Delivery owns native child completeness and aggregate logical Build
		// identity; this strict evidence projection reuses that same validator.
		return (delivery.Output{Name: output.Name, Kind: output.Kind, Digest: output.Subject,
			BuildDigest: output.BuildDigest, Components: []string{output.ImageIndex.Children[0].Component},
			ImageIndex: output.ImageIndex}).Validate()
	}
	if output.Platform != "" && output.Platform != "linux/amd64" && output.Platform != "linux/arm64" {
		return e.New(e.CodeVerifyFailed, "qualification output has an unsupported native platform")
	}
	if (output.Kind != lifecycle.OutputHelm && len(output.BuildSubjects) == 0) || (output.Kind == lifecycle.OutputOCI && len(output.BuildSubjects) != 1) {
		return e.New(e.CodeVerifyFailed, "qualification output omits its exact producer subjects")
	}
	previous := ""
	for _, subject := range output.BuildSubjects {
		if !subjectNamePattern.MatchString(subject.Component) || subject.Component <= previous || !canonical.IsDigest(subject.Digest) {
			return e.New(e.CodeVerifyFailed, "qualification build subjects must be canonical, sorted, unique and exact")
		}
		previous = subject.Component
	}
	return nil
}

// Validate admits both the complete claim and the exact signing transport bound.
func (evidence Evidence) Validate() error {
	_, err := evidence.Canonical()
	return err
}

func (evidence Evidence) Canonical() ([]byte, error) {
	if err := evidence.validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > sign.MaxQualificationPredicateBytes {
		return nil, e.New(e.CodeEngineCapability, "complete qualification evidence exceeds the signing transport limit")
	}
	return raw, nil
}
func (evidence Evidence) Digest() (string, error) {
	if err := evidence.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualificationEvidence, evidence)
}
func Decode(raw []byte) (Evidence, error) {
	if int64(len(raw)) > sign.MaxQualificationPredicateBytes {
		return Evidence{}, e.New(e.CodeVerifyFailed, "qualification evidence exceeds the signing transport limit")
	}
	var evidence Evidence
	if err := canonical.UnmarshalExact(raw, &evidence); err != nil {
		return Evidence{}, e.Wrap(e.CodeVerifyFailed, err, "decoding qualification evidence")
	}
	if evidence.SchemaVersion != SchemaVersion {
		return Evidence{}, e.New(e.CodeVerifyFailed, "production qualification decoder rejects development evidence")
	}
	if err := evidence.Validate(); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func outsideSuites(names []string) ([]ExternalSuite, error) {
	names = append([]string(nil), names...)
	sort.Strings(names)
	result := make([]ExternalSuite, 0, len(names))
	previous := ""
	for _, name := range names {
		if name == "" || name == previous || strings.TrimSpace(name) != name {
			return nil, e.New(e.CodeTrustPolicyMissing, "external suite names must be canonical and unique")
		}
		previous = name
		result = append(result, ExternalSuite{Name: name, Status: "not-evaluated", Reason: "outside MVP scope"})
	}
	return result, nil
}
