// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"regexp"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// TektonOptions selects a cluster key independently from downloaded evidence.
// PrivateKeyPath names the qualifier's projected Secret, never a provider token.
type TektonOptions struct {
	Options
	Trust          qualificationpolicy.Tekton
	PrivateKeyPath string
	PlainHTTP      bool
}

func NewTektonPublisher(options TektonOptions) (*Publisher, error) {
	if err := options.Trust.Validate(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "Tekton qualification signing requires Linux")
	}
	if !canonical.IsDigest(options.CosignDigest) || options.CosignBinary == "" || options.PrivateKeyPath == "" {
		return nil, e.New(e.CodeUnpinnedInput, "Tekton qualification requires an exact cosign executable and private key mount")
	}
	identity, err := options.Trust.Identity("qualifier")
	if err != nil {
		return nil, err
	}
	trust := options.Trust
	return &Publisher{tekton: &trust, cosignDigest: options.CosignDigest, newAttester: func(actual string) (qualificationAttester, error) {
		if actual != identity {
			return nil, e.New(e.CodeUntrustedSigner, "Tekton invocation does not match the selected qualifier")
		}
		return sign.NewCosignOCISigner(sign.CosignOCIOptions{Binary: options.CosignBinary, BinaryDigest: options.CosignDigest,
			Identity: "^" + regexp.QuoteMeta(identity) + "$", Issuer: trust.APIServer, Credential: options.Credential,
			KeyTrust: &sign.OCIKeyTrust{PublicKey: []byte(trust.QualifierKey), Identity: identity}, PrivateKeyPath: options.PrivateKeyPath, PlainHTTP: options.PlainHTTP})
	}}, nil
}

// PublishTekton consumes live lifecycle receipts; decoded evidence cannot enter
// this path, and each attachment rechecks the task and artifact receipt.
func (p *Publisher) PublishTekton(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, invocation *lifecycle.TektonInvocation, smoke *SmokeReceipt, policy Policy) (*Publication, error) {
	if p == nil || p.tekton == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton publisher trust is missing")
	}
	if err := invocation.RequireSigning(); err != nil {
		return nil, err
	}
	return p.publish(ctx, checks, invocation, smoke, policy)
}
