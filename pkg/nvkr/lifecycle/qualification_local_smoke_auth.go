// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type localSignedSmoke struct {
	document string
	signed   sign.LocalSignedDocument
}

// localVerifiedSmoke authenticates the smoke key's complete observation claim.
// It is deliberately distinct from the process-local execution receipt.
type localVerifiedSmoke struct {
	signature string
	raw       string
	build     *localVerifiedBuild
	closed    *atomic.Bool
}

func (smoke *localVerifiedSmoke) Close() error {
	if smoke != nil && smoke.closed != nil {
		smoke.closed.Store(true)
	}
	return nil
}

func (smoke *localVerifiedSmoke) description(ctx context.Context) (localToolSmokeReport, error) {
	if smoke == nil || smoke.closed == nil || smoke.closed.Load() {
		return localToolSmokeReport{}, e.New(e.CodeGateUnmet, "authenticated local smoke is not live")
	}
	return validateLocalSmokeReport(ctx, smoke.build, []byte(smoke.raw))
}

func signLocalSmoke(ctx context.Context, receipt *localToolSmokeReceipt, binary string, privateKey []byte) (*localSignedSmoke, error) {
	if receipt == nil || receipt.build == nil {
		return nil, e.New(e.CodeGateUnmet, "smoke signing requires a live execution receipt")
	}
	signer, err := sign.NewLocalCosign(binary, receipt.build.policy.CosignDigest)
	if err != nil {
		return nil, err
	}
	return signLocalSmokeWith(ctx, receipt, privateKey, signer.SignDocument)
}

func signLocalSmokeWith(ctx context.Context, receipt *localToolSmokeReceipt, privateKey []byte, signDocument localBuildSignDocument) (*localSignedSmoke, error) {
	if receipt == nil || signDocument == nil {
		return nil, e.New(e.CodeGateUnmet, "smoke signing requires a live receipt and signer")
	}
	operation, finish, err := receipt.build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	report, err := receipt.description(operation)
	if err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(report)
	if err != nil {
		return nil, err
	}
	if _, err := validateLocalSmokeReport(operation, receipt.build, raw); err != nil {
		return nil, err
	}
	key := []byte(receipt.build.policy.SmokeKey)
	signed, err := signDocument(operation, raw, privateKey, key)
	if err != nil {
		return nil, err
	}
	if err := signed.RequireFor(operation, raw, key); err != nil {
		return nil, err
	}
	if _, err := receipt.description(operation); err != nil {
		return nil, err
	}
	return &localSignedSmoke{document: string(raw), signed: signed}, nil
}

func verifyLocalSmoke(ctx context.Context, build *localVerifiedBuild, binary string, document, signature []byte) (*localVerifiedSmoke, error) {
	operation, finish, err := build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if len(document) == 0 || len(document) > 4<<20 {
		return nil, e.New(e.CodeVerifyFailed, "local smoke report is missing or oversized")
	}
	if len(signature) == 0 || len(signature) > 64<<10 {
		return nil, e.New(e.CodeSignatureInvalid, "local smoke signature is missing or oversized")
	}
	document, signature = bytes.Clone(document), bytes.Clone(signature)
	if _, err := validateLocalSmokeReport(operation, build, document); err != nil {
		return nil, err
	}
	verifier, err := sign.NewLocalCosign(binary, build.policy.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := verifier.VerifyDocument(operation, document, signature, []byte(build.policy.SmokeKey)); err != nil {
		return nil, err
	}
	if err := build.requireLive(operation); err != nil {
		return nil, err
	}
	return &localVerifiedSmoke{raw: string(document), signature: string(signature), build: build, closed: new(atomic.Bool)}, nil
}

func validateLocalSmokeReport(ctx context.Context, build *localVerifiedBuild, raw []byte) (localToolSmokeReport, error) {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return localToolSmokeReport{}, e.New(e.CodeVerifyFailed, "local smoke report is missing or oversized")
	}
	members, err := localSmokeMembers(ctx, build)
	if err != nil {
		return localToolSmokeReport{}, err
	}
	var report localToolSmokeReport
	if err := canonical.UnmarshalExact(raw, &report); err != nil {
		return localToolSmokeReport{}, e.Wrap(e.CodeVerifyFailed, err, "decoding exact local smoke report")
	}
	digest, err := build.policy.Digest()
	if err != nil {
		return localToolSmokeReport{}, err
	}
	if report.SchemaVersion != "nvkr.dev/local-tool-smoke/v1" || report.Lane != factory.TierLocalExecution.String() || report.PolicyDigest != digest ||
		report.BuildEvidenceDigest != pipeline.FileDigest([]byte(build.document)) || report.Platform != build.policy.Platform || report.Image != build.policy.Smoke.Images[report.Platform] ||
		!canonical.IsDigest(report.RuntimeImage) || !canonical.IsDigest(report.RuntimeIdentity) {
		return localToolSmokeReport{}, e.New(e.CodeVerifyFailed, "local smoke report changes its source, policy, platform or runtime identity")
	}
	if len(members) == 0 || len(members) != len(report.Members) {
		return localToolSmokeReport{}, e.New(e.CodeReleaseIncomplete, "local smoke report changes complete tool membership")
	}
	for i, expected := range members {
		expected.ObservedVersion, expected.ObservedCommit = expected.ExpectedVersion, expected.ExpectedCommit
		if expected.Platform != report.Platform || expected != report.Members[i] {
			return localToolSmokeReport{}, e.New(e.CodeGateUnmet, "local smoke member differs from exact payload or expected observation")
		}
	}
	return report, nil
}
