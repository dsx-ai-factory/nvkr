// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

const SchemaVersionV3 = "nvkr.dev/qualification-evidence/v3"
const GateVersionV3 = "nvkr.dev/qualification-gate/v3"
const maxEvidenceV3Bytes = 4 << 20

// SuiteEvidenceV3 records a qualifier's authenticated coverage claim. The exact
// observation digest retains the external suite's independent signer boundary.
// Origin is an attester declaration matched against authenticated source facts,
// never an authenticated claim about installed cluster bytes.
type SuiteEvidenceV3 struct {
	Reference   string                 `json:"reference"`
	Name        string                 `json:"name"`
	Requirement SuiteRequirement       `json:"requirement"`
	Origin      testobservation.Origin `json:"origin"`
	Role        string                 `json:"role"`
	Status      string                 `json:"status"`
}

func (s SuiteEvidenceV3) validate(checks lifecycle.ArtifactCheckDescription) error {
	if err := (testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: s.Reference, ObservationDigest: s.Requirement.Expected.ObservationDigest}).Validate(); err != nil {
		return err
	}
	if err := s.Requirement.Validate(); err != nil {
		return err
	}
	if s.Name != s.Requirement.Expected.Suite.Name || s.Status != "pass" || s.Role != s.Requirement.RequiredRole || !originMatchesV3(s.Origin, checks) {
		return e.New(e.CodeGateUnmet, "suite origin, role or verdict differs from qualification policy")
	}
	return validateSuiteSubjectsV3(s.Requirement.Expected, checks)
}

// EvidenceV3 extends native v2 with independent required-suite authentication. Native v2 cannot consume it.
type EvidenceV3 struct {
	ToolSmoke      lifecycle.ToolSmokeDescription     `json:"toolSmoke"`
	Policy         PolicyV3                           `json:"policy"`
	PolicyDigest   string                             `json:"policyDigest"`
	SchemaVersion  string                             `json:"schemaVersion"`
	GateVersion    string                             `json:"gateVersion"`
	ArtifactChecks lifecycle.ArtifactCheckDescription `json:"artifactChecks"`
	Invocation     qualificationorigin.Description    `json:"invocation"`
	Smoke          []Verdict                          `json:"smoke"`
	ExternalSuites []ExternalSuite                    `json:"externalSuites"`
	Suites         []SuiteEvidenceV3                  `json:"suites"`
}

func (v EvidenceV3) Validate() error {
	if v.SchemaVersion != SchemaVersionV3 || v.GateVersion != GateVersionV3 {
		return e.New(e.CodeVerifyFailed, "unknown required-suite qualification schema or gate")
	}
	// Reuse the native v2's unchanged artifact membership, input, origin and smoke contracts.
	base := Evidence{ToolSmoke: v.ToolSmoke, SchemaVersion: SchemaVersion, GateVersion: GateVersion, ArtifactChecks: v.ArtifactChecks, Invocation: v.Invocation, Smoke: v.Smoke, ExternalSuites: v.ExternalSuites}
	if err := base.Validate(); err != nil {
		return err
	}
	if err := v.Policy.Validate(); err != nil {
		return err
	}
	digest, err := v.Policy.Digest()
	if err != nil {
		return err
	}
	if digest != v.PolicyDigest || v.Policy.NativePolicyDigest != v.ToolSmoke.PolicyDigest {
		return e.New(e.CodeGateUnmet, "required-suite policy differs from complete native policy or selected digest")
	}
	if len(v.Suites) != len(v.Policy.RequiredSuites) {
		return e.New(e.CodeGateUnmet, "required suite set differs from independent policy")
	}
	if len(v.Suites) == 0 || len(v.Suites) > 256 {
		return e.New(e.CodeGateUnmet, "v3 requires a bounded nonempty authenticated suite set")
	}
	previous := ""
	for _, suite := range v.Suites {
		if suite.Name <= previous {
			return e.New(e.CodeGateUnmet, "required suites must be unique and sorted")
		}
		previous = suite.Name
		if err := suite.validate(v.ArtifactChecks); err != nil {
			return err
		}
		matched := false
		for _, requirement := range v.Policy.RequiredSuites {
			if requirement.Expected.Suite.Name != suite.Name {
				continue
			}
			expected, err := canonical.Marshal(requirement)
			if err != nil {
				return err
			}
			actual, err := canonical.Marshal(suite.Requirement)
			if err != nil {
				return err
			}
			if string(expected) != string(actual) {
				return e.New(e.CodeGateUnmet, "suite evidence differs from independently selected required policy")
			}
			matched = true
		}
		if !matched {
			return e.New(e.CodeGateUnmet, "suite evidence has no independent requirement")
		}
		for _, outside := range v.ExternalSuites {
			if outside.Name == suite.Name {
				return e.New(e.CodeGateUnmet, "required suite also declared outside scope")
			}
		}
	}
	return nil
}
func (v EvidenceV3) Canonical() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxEvidenceV3Bytes {
		return nil, e.New(e.CodeVerifyFailed, "v3 qualification evidence exceeds byte budget")
	}
	return raw, nil
}
func (v EvidenceV3) Digest() (string, error) {
	if _, err := v.Canonical(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualificationEvidence, v)
}
func DecodeV3(raw []byte) (EvidenceV3, error) {
	var v EvidenceV3
	if len(raw) > maxEvidenceV3Bytes {
		return v, e.New(e.CodeVerifyFailed, "v3 qualification evidence exceeds byte budget")
	}
	if err := canonical.UnmarshalExact(raw, &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
