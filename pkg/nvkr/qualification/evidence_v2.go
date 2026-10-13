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

const SchemaVersionV2 = "nvkr.dev/qualification-evidence/v2"
const GateVersionV2 = "nvkr.dev/qualification-gate/v2"
const maxEvidenceV2Bytes = 4 << 20

// SuiteEvidenceV2 records a qualifier's authenticated coverage claim. The exact
// observation digest retains the external suite's independent signer boundary.
// Origin is an attester declaration matched against authenticated source facts,
// never an authenticated claim about installed cluster bytes.
type SuiteEvidenceV2 struct {
	Reference   string                 `json:"reference"`
	Name        string                 `json:"name"`
	Requirement SuiteRequirement       `json:"requirement"`
	Origin      testobservation.Origin `json:"origin"`
	Role        string                 `json:"role"`
	Status      string                 `json:"status"`
}

func (s SuiteEvidenceV2) validate(checks lifecycle.ArtifactCheckDescription) error {
	if err := (testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: s.Reference, ObservationDigest: s.Requirement.Expected.ObservationDigest}).Validate(); err != nil {
		return err
	}
	if err := s.Requirement.Validate(); err != nil {
		return err
	}
	if s.Name != s.Requirement.Expected.Suite.Name || s.Status != "pass" || s.Role != s.Requirement.RequiredRole || !originMatchesV2(s.Origin, checks) {
		return e.New(e.CodeGateUnmet, "suite origin, role or verdict differs from qualification policy")
	}
	return validateSuiteSubjectsV2(s.Requirement.Expected, checks)
}

// EvidenceV2 has a distinct schema and predicate; v1 cannot consume it.
type EvidenceV2 struct {
	SchemaVersion  string                             `json:"schemaVersion"`
	GateVersion    string                             `json:"gateVersion"`
	ArtifactChecks lifecycle.ArtifactCheckDescription `json:"artifactChecks"`
	Invocation     qualificationorigin.Description    `json:"invocation"`
	Smoke          []Verdict                          `json:"smoke"`
	ExternalSuites []ExternalSuite                    `json:"externalSuites"`
	Suites         []SuiteEvidenceV2                  `json:"suites"`
}

func (v EvidenceV2) Validate() error {
	if v.SchemaVersion != SchemaVersionV2 || v.GateVersion != GateVersionV2 {
		return e.New(e.CodeVerifyFailed, "unknown required-suite qualification schema or gate")
	}
	// Reuse v1's unchanged artifact membership, input, origin and smoke contracts.
	base := Evidence{SchemaVersion: SchemaVersion, GateVersion: GateVersion, ArtifactChecks: v.ArtifactChecks, Invocation: v.Invocation, Smoke: v.Smoke, ExternalSuites: v.ExternalSuites}
	if err := base.Validate(); err != nil {
		return err
	}
	if len(v.Suites) == 0 || len(v.Suites) > 256 {
		return e.New(e.CodeGateUnmet, "v2 requires a bounded nonempty authenticated suite set")
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
		for _, outside := range v.ExternalSuites {
			if outside.Name == suite.Name {
				return e.New(e.CodeGateUnmet, "required suite also declared outside scope")
			}
		}
	}
	return nil
}
func (v EvidenceV2) Canonical() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxEvidenceV2Bytes {
		return nil, e.New(e.CodeVerifyFailed, "v2 qualification evidence exceeds byte budget")
	}
	return raw, nil
}
func (v EvidenceV2) Digest() (string, error) {
	if _, err := v.Canonical(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualificationEvidence, v)
}
func DecodeV2(raw []byte) (EvidenceV2, error) {
	var v EvidenceV2
	if len(raw) > maxEvidenceV2Bytes {
		return v, e.New(e.CodeVerifyFailed, "v2 qualification evidence exceeds byte budget")
	}
	if err := canonical.UnmarshalExact(raw, &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
