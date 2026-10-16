// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// localSignedBuild retains a builder-key-authenticated description. This is not
// a qualification receipt and cannot be passed to a hosted publisher.
type localSignedBuild struct {
	document string
	signed   sign.LocalSignedDocument
}

type localBuildSignDocument func(context.Context, []byte, []byte, []byte) (sign.LocalSignedDocument, error)

func signLocalBuildEvidence(ctx context.Context, inputs *localQualificationInputs, capture *localQualificationCapture, policy LocalQualificationPolicy, binary string, privateKey []byte) (*localSignedBuild, error) {
	frozen, err := policy.freeze()
	if err != nil {
		return nil, err
	}
	signer, err := sign.NewLocalCosign(binary, frozen.CosignDigest)
	if err != nil {
		return nil, err
	}
	return signLocalBuildEvidenceWith(ctx, inputs, capture, frozen, privateKey, signer.SignDocument)
}

// The private seam may return only a genuine exact-byte signing receipt. Even
// an injected test callback cannot manufacture successful verification authority.
func signLocalBuildEvidenceWith(ctx context.Context, inputs *localQualificationInputs, capture *localQualificationCapture, policy LocalQualificationPolicy, privateKey []byte, signDocument localBuildSignDocument) (*localSignedBuild, error) {
	if signDocument == nil {
		return nil, e.New(e.CodeUsage, "local builder requires a signing adapter")
	}
	frozen, err := policy.freeze()
	if err != nil {
		return nil, err
	}
	document, err := prepareLocalBuildEvidence(ctx, inputs, capture, frozen)
	if err != nil {
		return nil, err
	}
	receipt, err := signDocument(ctx, document, privateKey, []byte(frozen.BuilderKey))
	if err != nil {
		return nil, err
	}
	if err := receipt.RequireFor(ctx, document, []byte(frozen.BuilderKey)); err != nil {
		return nil, err
	}
	// Signing can block. Refuse to return a usable result if the capture closes
	// or its bindings change during that interval. No attachment is published here.
	current, err := prepareLocalBuildEvidence(ctx, inputs, capture, frozen)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(document, current) {
		return nil, e.New(e.CodeVerifyFailed, "local build evidence changed while signing")
	}
	return &localSignedBuild{document: string(document), signed: receipt}, nil
}
