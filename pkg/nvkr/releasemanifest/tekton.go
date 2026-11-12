// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"context"
	"regexp"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// TektonOptions binds the signer to independently selected cluster trust.
// An empty PrivateKeyPath constructs a verification-only owner.
type TektonOptions struct {
	Options
	Trust          qualificationpolicy.Tekton
	PrivateKeyPath string
	PlainHTTP      bool
}

func NewTektonIssuer(options TektonOptions) (*Issuer, error) {
	if err := options.Trust.Validate(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "Tekton manifest verification requires Linux")
	}
	if options.CosignBinary == "" || !canonical.IsDigest(options.CosignDigest) {
		return nil, e.New(e.CodeUnpinnedInput, "Tekton manifest requires an exact cosign executable")
	}
	identity, err := options.Trust.Identity("qualifier")
	if err != nil {
		return nil, err
	}
	trust := options.Trust
	return &Issuer{tekton: &trust, readOnly: options.PrivateKeyPath == "", newSigner: func(actual string) (manifestSigner, error) {
		if actual != identity {
			return nil, e.New(e.CodeUntrustedSigner, "manifest signer differs from the selected Tekton qualifier")
		}
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest,
			Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: trust.APIServer, Credential: options.Credential,
			KeyTrust: &sign.OCIKeyTrust{PublicKey: []byte(trust.QualifierKey), Identity: identity}, PrivateKeyPath: options.PrivateKeyPath, PlainHTTP: options.PlainHTTP})
	}}, nil
}

// IssueTekton adds live provider admission to the shared issuance gate.
func (issuer *Issuer) IssueTekton(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, invocation *lifecycle.TektonInvocation, evidence []byte, policy Policy) (*Receipt, error) {
	if issuer == nil || issuer.tekton == nil || issuer.readOnly {
		return nil, e.New(e.CodeEngineCapability, "Tekton manifest signer is unavailable")
	}
	if err := invocation.RequireSigning(); err != nil {
		return nil, err
	}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, err
	}
	current, err := invocation.Description()
	if err != nil {
		return nil, err
	}
	claim, err := qualification.Decode(evidence)
	if err != nil {
		return nil, err
	}
	if claim.Invocation != current {
		return nil, e.New(e.CodeUntrustedSigner, "release evidence differs from the active qualifier task")
	}
	return issuer.issue(ctx, checks, evidence, policy)
}

// VerifyTekton independently verifies every member's qualification attachment
// and the exact complete manifest using a fresh artifact-check receipt. It has
// no signing side effect and requires no private key.
func (issuer *Issuer) VerifyTekton(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, evidence, manifest []byte, policy Policy) (*Receipt, error) {
	if issuer == nil || issuer.tekton == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton manifest verifier trust is missing")
	}
	if len(manifest) == 0 {
		return nil, e.New(e.CodeReleaseIncomplete, "Tekton release manifest is missing")
	}
	if checks == nil {
		return nil, e.New(e.CodeGateUnmet, "manifest verification requires live artifact checks")
	}
	return issuer.authenticate(ctx, checks, evidence, policy, manifest)
}
