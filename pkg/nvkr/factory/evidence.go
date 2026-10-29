// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// EvidenceTier is the exact authority a factory plan, a generated pipeline, an
// emitted observation, or a local run claims for itself. Tokens are spelled only
// here: a retyped copy elsewhere is one typo from a document no gate recognizes.
//
// The tiers are deliberately NOT ordered or comparable: `pipeline-attested` is
// not "more" than `static-unqualified` on the same scale, it is a different claim
// (an attestation was produced by a pipeline that can carry one).
type EvidenceTier string

const (
	// TierStaticUnqualified is the legacy, default tier and the zero value's
	// meaning: the artifacts are real, the plan is exact, and NOTHING here is a
	// producer receipt, validation vector, clearance, or release authority. Every
	// document written before this type existed carries exactly this tier.
	TierStaticUnqualified EvidenceTier = "static-unqualified"

	// TierPipelineAttested is claimed ONLY when the delivery profile signs in
	// cosign mode: a real signature and an SLSA production attestation are bound
	// to the published digest by a pipeline identity. A dry-run signing profile
	// never reaches this tier — its record explicitly states nothing was signed.
	TierPipelineAttested EvidenceTier = "pipeline-attested"

	// TierTerminalObservation is the artifact kind of a terminal observation: a
	// deterministic digest listing of what a build target actually materialized.
	// It gates packaging without claiming a qualified terminal receipt.
	TierTerminalObservation EvidenceTier = "unqualified-terminal-observation"

	// TierLocalExecution is the qualification of a `factory run` record on an
	// operator workstation. It names an unattested local execution so a consumer
	// can refuse it by identity rather than by inspecting what it happens to hold.
	TierLocalExecution EvidenceTier = "unqualified-local-execution"
)

// Normalize maps the zero value onto the legacy tier. A document written before
// the tier was typed omits nothing and means static-unqualified; reading it must
// not silently produce an empty, unrecognized claim.
func (tier EvidenceTier) Normalize() EvidenceTier {
	if tier == "" {
		return TierStaticUnqualified
	}
	return tier
}

// String renders the exact wire token.
func (tier EvidenceTier) String() string { return string(tier.Normalize()) }

// ValidatePlanTier admits only the two tiers a compiled plan may claim, and binds
// the attested tier to the one signing mode that can actually produce evidence.
func (tier EvidenceTier) ValidatePlanTier(signing *definition.Signing) error {
	switch tier.Normalize() {
	case TierStaticUnqualified:
		return nil
	case TierPipelineAttested:
		if signing == nil || signing.Mode != SigningModeCosign {
			return e.New(e.CodeInvalidCatalog,
				"factory plan claims %s without a %s signing profile", TierPipelineAttested, SigningModeCosign)
		}
		return nil
	default:
		return e.New(e.CodeInvalidCatalog,
			"factory plan qualification %q is not %s or %s", string(tier), TierStaticUnqualified, TierPipelineAttested)
	}
}

// planTier is the tier a plan compiled against this delivery profile may claim.
// Only cosign-mode signing produces an attestation, so a dry-run (or absent)
// signing profile keeps the plan explicitly unqualified.
func planTier(signing *definition.Signing) EvidenceTier {
	if signing != nil && signing.Mode == SigningModeCosign {
		return TierPipelineAttested
	}
	return TierStaticUnqualified
}
