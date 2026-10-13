// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// SuiteRequirement is independently selected consumer policy, never evidence.
type SuiteRequirement struct {
	Expected                       testobservation.Expected `json:"expected"`
	Identity                       string                   `json:"identity"`
	Issuer                         string                   `json:"issuer"`
	RequiredRole                   string                   `json:"requiredRole"`
	RequireAuthenticatedDeployment bool                     `json:"requireAuthenticatedDeployment"`
}

func (r SuiteRequirement) Validate() error {
	if r.RequireAuthenticatedDeployment {
		return e.New(e.CodeEngineCapability, "authenticated deployment binding is unavailable")
	}
	if err := r.Expected.Validate(); err != nil {
		return err
	}
	if !safetext.Valid(r.Identity, 4096, false) || !safetext.Valid(r.Issuer, 4096, false) || strings.TrimSpace(r.Identity) != r.Identity || strings.TrimSpace(r.Issuer) != r.Issuer {
		return e.New(e.CodeTrustPolicyMissing, "suite requires an exact independent signer identity and issuer")
	}
	if r.RequiredRole != "site" && r.RequiredRole != "tenant" {
		return e.New(e.CodeTrustPolicyMissing, "suite requires an exact site or tenant role")
	}
	if len(r.Expected.Suite.RequiredCases) == 0 {
		return e.New(e.CodeGateUnmet, "suite requires explicit nonempty coverage")
	}
	return nil
}

type suiteObservation interface {
	Observation(context.Context) (testobservation.Observation, error)
	Description(context.Context) (testobservation.Verification, error)
	Signer(context.Context) (string, string, error)
	Reference(context.Context) (string, error)
}
type suiteChecks interface {
	Description() (lifecycle.ArtifactCheckDescription, error)
}

// SuiteReceipt borrows authenticated observation/source lifetimes. Serialization
// cannot recreate it; copies share revocation. It asserts no installed-byte proof.
type SuiteReceipt struct {
	observation suiteObservation
	checks      suiteChecks
	requirement SuiteRequirement
	binding     string
	closed      *atomic.Bool
	lifetime    context.Context
}

func (r *SuiteReceipt) Close() error {
	if r != nil && r.closed != nil {
		r.closed.Store(true)
	}
	return nil
}

// AuthenticateSuite only accepts package-issued authenticated production receipts.
func AuthenticateSuite(ctx context.Context, observation *testobservation.Verified, checks *lifecycle.ArtifactCheckReceipt, requirement SuiteRequirement) (*SuiteReceipt, error) {
	return authenticateSuiteV2(ctx, observation, checks, requirement)
}
func authenticateSuiteV2(ctx context.Context, observation suiteObservation, checks suiteChecks, requirement SuiteRequirement) (*SuiteReceipt, error) {
	if err := suiteContext(ctx); err != nil {
		return nil, err
	}
	if err := requirement.Validate(); err != nil {
		return nil, err
	}
	if observation == nil || checks == nil {
		return nil, e.New(e.CodeGateUnmet, "authenticated suite and source receipts are required")
	}
	// Canonical round trip detaches nested caller policy slices before retaining.
	raw, err := canonical.Marshal(requirement)
	if err != nil {
		return nil, err
	}
	var frozen SuiteRequirement
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, err
	}
	receipt := &SuiteReceipt{observation: observation, checks: checks, requirement: frozen, closed: new(atomic.Bool), lifetime: ctx}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	binding, err := canonical.Marshal(description)
	if err != nil {
		return nil, err
	}
	receipt.binding = string(binding)
	if _, err := receipt.require(ctx, description); err != nil {
		return nil, err
	}
	return receipt, nil
}
func suiteContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "suite requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "suite verification canceled")
	}
	return nil
}
func (r *SuiteReceipt) require(ctx context.Context, checks lifecycle.ArtifactCheckDescription) (SuiteEvidenceV2, error) {
	if err := suiteContext(ctx); err != nil {
		return SuiteEvidenceV2{}, err
	}
	if r == nil || r.closed == nil || r.closed.Load() || r.observation == nil || r.checks == nil {
		return SuiteEvidenceV2{}, e.New(e.CodeGateUnmet, "suite receipt is missing or closed")
	}
	if err := suiteContext(r.lifetime); err != nil {
		return SuiteEvidenceV2{}, err
	}
	current, err := r.checks.Description()
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	raw, err := canonical.Marshal(current)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	target, err := canonical.Marshal(checks)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	if string(raw) != r.binding || string(target) != r.binding {
		return SuiteEvidenceV2{}, e.New(e.CodeGateUnmet, "suite receipt source changed or differs from qualification")
	}
	if err := r.requirement.Validate(); err != nil {
		return SuiteEvidenceV2{}, err
	}
	identity, issuer, err := r.observation.Signer(ctx)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	if identity != r.requirement.Identity || issuer != r.requirement.Issuer {
		return SuiteEvidenceV2{}, e.New(e.CodeUntrustedSigner, "suite observation signer differs from independent policy")
	}
	observation, err := r.observation.Observation(ctx)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	if err := r.requirement.Expected.Check(observation); err != nil {
		return SuiteEvidenceV2{}, err
	}
	verified, err := r.observation.Description(ctx)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	if !verified.SignerAuthenticated || !verified.SelfConsistent || !verified.Passing || verified.ObservationDigest != r.requirement.Expected.ObservationDigest || observation.Summary.Total == 0 || observation.Summary.Failed != 0 || observation.Summary.Errors != 0 || observation.Summary.Passed < len(r.requirement.Expected.Suite.RequiredCases) || observation.Execution.Status != "completed" || observation.Execution.ExitCode != 0 || observation.Cleanup.Status != "completed" || observation.Cleanup.ExitCode != 0 {
		return SuiteEvidenceV2{}, e.New(e.CodeGateUnmet, "suite requires complete authenticated passing execution and cleanup with complete required-case coverage")
	}
	reference, err := r.observation.Reference(ctx)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	detached, err := canonical.Marshal(r.requirement)
	if err != nil {
		return SuiteEvidenceV2{}, err
	}
	var requirement SuiteRequirement
	if err := canonical.UnmarshalExact(detached, &requirement); err != nil {
		return SuiteEvidenceV2{}, err
	}
	evidence := SuiteEvidenceV2{Reference: reference, Name: observation.Suite.Name, Requirement: requirement, Origin: observation.Origin, Role: observation.Environment.Role, Status: "pass"}
	if err := evidence.validate(checks); err != nil {
		return SuiteEvidenceV2{}, err
	}
	if err := suiteContext(ctx); err != nil {
		return SuiteEvidenceV2{}, err
	}
	if r.closed.Load() {
		return SuiteEvidenceV2{}, e.New(e.CodeGateUnmet, "suite receipt was revoked during admission")
	}
	if _, err := r.checks.Description(); err != nil {
		return SuiteEvidenceV2{}, err
	}
	if _, _, err := r.observation.Signer(ctx); err != nil {
		return SuiteEvidenceV2{}, err
	}
	return evidence, nil
}

func validateSuiteSubjectsV2(expected testobservation.Expected, checks lifecycle.ArtifactCheckDescription) error {
	if expected.Inputs.DefinitionDigest != checks.Inputs.DefinitionDigest || expected.Inputs.EnvironmentDigest != checks.Inputs.EnvironmentDigest || expected.Inputs.PlanDigest != checks.Inputs.PlanDigest || expected.Inputs.PublicationDigest != checks.Inputs.ReleaseSubject {
		return e.New(e.CodeGateUnmet, "suite input bindings differ from authenticated publication")
	}
	for _, subject := range expected.Subjects {
		matched := false
		for _, output := range checks.Outputs {
			if subject.Output != output.Name {
				continue
			}
			if err := validateOutputSubjects(output); err != nil {
				return err
			}
			if output.ImageIndex != nil {
				for _, child := range output.ImageIndex.Children {
					if child.Platform == subject.Platform && child.ManifestDigest == subject.ArtifactDigest && child.BuildDigest == subject.BuildDigest && child.BuildSubject == subject.PayloadDigest {
						matched = true
					}
				}
				if !matched {
					return e.New(e.CodeGateUnmet, "suite native subject differs from authenticated image-index child")
				}
				continue
			}
			if subject.ArtifactDigest != output.Subject || subject.BuildDigest != output.BuildDigest || output.Platform != "" && subject.Platform != output.Platform {
				return e.New(e.CodeGateUnmet, "suite subject differs from exact named artifact")
			}

			for _, payload := range output.BuildSubjects {
				if payload.Digest == subject.PayloadDigest {
					matched = true
				}
			}
		}
		if !matched {
			return e.New(e.CodeGateUnmet, "suite subject has no matching exact acquired payload")
		}
	}
	return nil
}
func originMatchesV2(origin testobservation.Origin, checks lifecycle.ArtifactCheckDescription) bool {
	source := checks.Origin
	return origin.Status == "declared" && origin.Repository == source.Repository && origin.Commit == source.Commit && origin.Run == source.RunID && strconv.Itoa(origin.Attempt) == source.Attempt && origin.Workflow == source.Workflow
}
