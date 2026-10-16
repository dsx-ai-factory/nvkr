// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// localVerifiedBuild owns independently captured payloads whose complete build
// description verifies under the selected builder key. It authenticates that
// local assertion only, not smoke, qualification or original live execution.
type localVerifiedBuild struct {
	lifetime  *localBuildLifetime
	inputs    *localQualificationInputs
	capture   *localQualificationCapture
	document  string
	signature string
	policy    LocalQualificationPolicy
}

type localBuildLifetime struct {
	mu         sync.Mutex
	closing    bool
	ctx        context.Context
	stop       context.CancelFunc
	operations sync.WaitGroup
	once       sync.Once
	closeErr   error
}

func newLocalVerifiedBuild(ctx context.Context, inputs *localQualificationInputs, capture *localQualificationCapture, document string, policy LocalQualificationPolicy) *localVerifiedBuild {
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &localVerifiedBuild{inputs: inputs, capture: capture, document: document, policy: policy, lifetime: &localBuildLifetime{ctx: lifetime, stop: stop}}
}

func (build *localVerifiedBuild) Close() error {
	if build == nil || build.lifetime == nil {
		return nil
	}
	state := build.lifetime
	state.once.Do(func() {
		state.mu.Lock()
		state.closing = true
		state.stop()
		state.mu.Unlock()
		state.operations.Wait()
		state.closeErr = build.capture.Close()
	})
	return state.closeErr
}

func (build *localVerifiedBuild) beginOperation(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "local build operation requires context")
	}
	if build == nil || build.lifetime == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "local build owner is not issued")
	}
	state := build.lifetime
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closing {
		return nil, nil, e.New(e.CodeGateUnmet, "local build owner is closing")
	}
	state.operations.Add(1)
	operation, stop := context.WithCancel(ctx)
	detach := context.AfterFunc(state.ctx, stop)
	return operation, func() { detach(); stop(); state.operations.Done() }, nil
}

// openLocalVerifiedBuild consumes only retained files, detached signature bytes
// and independently selected public trust. It never accepts producer handles or
// private keys. Registry download and qualification evidence remain separate.
func openLocalVerifiedBuild(ctx context.Context, workspace string, policy LocalQualificationPolicy, binary string, document, signature []byte) (result *localVerifiedBuild, err error) {
	if ctx == nil || workspace == "" {
		return nil, e.New(e.CodeUsage, "local build verification requires context and retained workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local build verification canceled")
	}
	frozen, err := policy.freeze()
	if err != nil {
		return nil, err
	}
	if len(document) == 0 || len(document) > 4<<20 {
		return nil, e.New(e.CodeVerifyFailed, "local build evidence is missing or oversized")
	}
	if len(signature) == 0 || len(signature) > 64<<10 {
		return nil, e.New(e.CodeSignatureInvalid, "local builder signature is missing or oversized")
	}
	document, signature = bytes.Clone(document), bytes.Clone(signature)
	if err := validateLocalBuildDocument(document, frozen); err != nil {
		return nil, err
	}
	verifier, err := sign.NewLocalCosign(binary, frozen.CosignDigest)
	if err != nil {
		return nil, err
	}
	// Authenticate the statement before opening its potentially large payload set.
	if err := verifier.VerifyDocument(ctx, document, signature, []byte(frozen.BuilderKey)); err != nil {
		return nil, err
	}
	inputs, err := loadLocalQualificationInputs(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if err := frozen.RequirePlan(inputs.plan); err != nil {
		return nil, err
	}
	capture, err := captureLocalQualificationPayloads(ctx, workspace, inputs)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, capture.Close())
			result = nil
		}
	}()
	expected, err := prepareLocalBuildEvidence(ctx, inputs, capture, frozen)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(document, expected) {
		return nil, e.New(e.CodeVerifyFailed, "signed local build evidence differs from independently replayed inputs and captured outputs")
	}
	verified := newLocalVerifiedBuild(ctx, inputs, capture, string(document), frozen)
	verified.signature = string(signature)
	return verified, nil
}

func validateLocalBuildDocument(raw []byte, policy LocalQualificationPolicy) error {
	if len(raw) == 0 || len(raw) > 4<<20 {
		return e.New(e.CodeVerifyFailed, "local build evidence is missing or oversized")
	}
	var document localBuildEvidence
	if err := canonical.UnmarshalExact(raw, &document); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding canonical local build evidence")
	}
	digest, err := policy.Digest()
	if err != nil {
		return err
	}
	if document.SchemaVersion != "nvkr.dev/local-build-evidence/v1" || document.Lane != factory.TierLocalExecution.String() || document.Origin != "retained-local-run" ||
		document.PolicyDigest != digest || document.PlanDigest != policy.PlanDigest || document.DefinitionDigest != policy.DefinitionDigest || document.EnvironmentDigest != policy.EnvironmentDigest {
		return e.New(e.CodeTrustPolicyMissing, "local build evidence differs from the independently selected policy or origin")
	}
	return nil
}
