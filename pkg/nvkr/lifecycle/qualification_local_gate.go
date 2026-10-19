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

// localQualificationEvidence attests the local gate only. Publication/read-back
// must still complete before the rehearsal can report end-to-end success.
type localQualificationEvidence struct {
	SchemaVersion  string              `json:"schemaVersion"`
	Lane           string              `json:"lane"`
	GateVersion    string              `json:"gateVersion"`
	PolicyDigest   string              `json:"policyDigest"`
	Attempt        string              `json:"attempt"`
	ContentSubject string              `json:"contentSubject"`
	Evidence       []localEvidenceBlob `json:"evidence"`
}

type localSignedQualification struct {
	document string
	signed   sign.LocalSignedDocument
}

type localVerifiedQualification struct {
	raw       string
	signature string
	build     *localVerifiedBuild
	smoke     *localVerifiedSmoke
	closed    *atomic.Bool
}

func (qualified *localVerifiedQualification) Close() error {
	if qualified != nil && qualified.closed != nil {
		qualified.closed.Store(true)
	}
	return nil
}

func (qualified *localVerifiedQualification) requireLive(ctx context.Context) error {
	if qualified == nil || qualified.closed == nil || qualified.closed.Load() {
		return e.New(e.CodeGateUnmet, "local qualification verification is not live")
	}
	expected, err := prepareLocalQualification(ctx, qualified.build, qualified.smoke)
	if err != nil {
		return err
	}
	if string(expected) != qualified.raw {
		return e.New(e.CodeVerifyFailed, "local qualification binding changed")
	}
	return nil
}

func prepareLocalQualification(ctx context.Context, build *localVerifiedBuild, smoke *localVerifiedSmoke) ([]byte, error) {
	if err := build.requireLive(ctx); err != nil {
		return nil, err
	}
	if smoke == nil || smoke.build != build {
		return nil, e.New(e.CodeGateUnmet, "local qualification requires smoke authenticated for this exact build owner")
	}
	if _, err := smoke.description(ctx); err != nil {
		return nil, err
	}
	if build.signature == "" || smoke.signature == "" {
		return nil, e.New(e.CodeProvenanceMissing, "local qualification requires retained builder and smoke signatures")
	}
	digest, err := build.policy.Digest()
	if err != nil {
		return nil, err
	}
	evidence := localQualificationEvidence{SchemaVersion: "nvkr.dev/local-qualification-evidence/v1", Lane: factory.TierLocalExecution.String(), GateVersion: "local-m3-v1",
		PolicyDigest: digest, Attempt: build.inputs.run.Attempt, ContentSubject: build.inputs.run.ContentSubject, Evidence: []localEvidenceBlob{}}
	for _, blob := range []struct{ name, raw string }{{"build-evidence.json", build.document}, {"build-evidence.sig", build.signature}, {"tool-smoke.json", smoke.raw}, {"tool-smoke.sig", smoke.signature}} {
		evidence.Evidence = append(evidence.Evidence, localEvidenceBlob{Name: blob.name, Digest: pipeline.FileDigest([]byte(blob.raw)), Size: int64(len(blob.raw))})
	}
	return canonical.Marshal(evidence)
}

func signLocalQualification(ctx context.Context, build *localVerifiedBuild, smoke *localVerifiedSmoke, binary string, privateKey []byte) (*localSignedQualification, error) {
	if build == nil {
		return nil, e.New(e.CodeGateUnmet, "local qualification requires verified build evidence")
	}
	adapter, err := sign.NewLocalCosign(binary, build.policy.CosignDigest)
	if err != nil {
		return nil, err
	}
	return signLocalQualificationWith(ctx, build, smoke, privateKey, adapter.SignDocument)
}

func signLocalQualificationWith(ctx context.Context, build *localVerifiedBuild, smoke *localVerifiedSmoke, privateKey []byte, signDocument localBuildSignDocument) (*localSignedQualification, error) {
	operation, finish, err := build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	document, err := prepareLocalQualification(operation, build, smoke)
	if err != nil {
		return nil, err
	}
	if signDocument == nil {
		return nil, e.New(e.CodeUsage, "local qualification requires a signer")
	}
	key := []byte(build.policy.QualifierKey)
	signed, err := signDocument(operation, document, privateKey, key)
	if err != nil {
		return nil, err
	}
	if err := signed.RequireFor(operation, document, key); err != nil {
		return nil, err
	}
	current, err := prepareLocalQualification(operation, build, smoke)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(current, document) {
		return nil, e.New(e.CodeVerifyFailed, "local qualification changed while signing")
	}
	return &localSignedQualification{document: string(document), signed: signed}, nil
}

func verifyLocalQualification(ctx context.Context, build *localVerifiedBuild, smoke *localVerifiedSmoke, binary string, document, signature []byte) (*localVerifiedQualification, error) {
	operation, finish, err := build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if len(document) == 0 || len(document) > 4<<20 {
		return nil, e.New(e.CodeVerifyFailed, "local qualification document is missing or oversized")
	}
	if len(signature) == 0 || len(signature) > 64<<10 {
		return nil, e.New(e.CodeSignatureInvalid, "local qualification signature is missing or oversized")
	}
	document, signature = bytes.Clone(document), bytes.Clone(signature)
	expected, err := prepareLocalQualification(operation, build, smoke)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(document, expected) {
		return nil, e.New(e.CodeVerifyFailed, "qualification document differs from independently authenticated evidence")
	}
	adapter, err := sign.NewLocalCosign(binary, build.policy.CosignDigest)
	if err != nil {
		return nil, err
	}
	if err := adapter.VerifyDocument(operation, document, signature, []byte(build.policy.QualifierKey)); err != nil {
		return nil, err
	}
	current, err := prepareLocalQualification(operation, build, smoke)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(current, document) {
		return nil, e.New(e.CodeVerifyFailed, "qualification binding changed during verification")
	}
	return &localVerifiedQualification{raw: string(document), signature: string(signature), build: build, smoke: smoke, closed: new(atomic.Bool)}, nil
}
