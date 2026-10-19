// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// RunResult reports qualification and manifest outcomes independently. It grants
// no tag or qualified-lane authority. Earlier attachments may survive failure.
type RunResult struct {
	Qualification   *qualification.Publication `json:"qualification,omitempty"`
	Manifest        json.RawMessage            `json:"manifest,omitempty"`
	ManifestDigest  string                     `json:"manifestDigest,omitempty"`
	ManifestSubject sign.Subject               `json:"manifestSubject"`
	Successful      bool                       `json:"successful"`
}

type triggeredQualification interface {
	Publish(context.Context) (*qualification.Publication, error)
	PublishedEvidence() (*lifecycle.ArtifactCheckReceipt, []byte, error)
	Close() error
}

// PublishTriggeredRun publishes complete production M3 qualification, then
// authenticates those exact attachments and issues its release manifest while
// the same artifact-check receipt remains live. Development stays qualification-only.
func PublishTriggeredRun(ctx context.Context, policyPath string, options lifecycle.QualificationRuntimeOptions) (*RunResult, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "release manifest publication requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release manifest publication canceled")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "release manifest publication requires Linux pinned cosign execution")
	}
	ctx, cancel := qualification.WithHostedRuntimeBudget(ctx)
	defer cancel()
	selected, err := lifecycle.LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, err
	}
	policy, err := triggeredManifestPolicy(selected)
	if err != nil {
		return nil, err
	}
	digest, err := selected.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	issuer, err := NewIssuer(Options{CosignBinary: selected.Cosign.Path, CosignDigest: digest, Credential: options.Credential})
	if err != nil {
		return nil, err
	}
	run, err := qualification.OpenTriggeredRunWithPolicy(ctx, selected, options)
	if err != nil {
		return nil, err
	}
	return publishTriggeredManifest(ctx, run, policy, issuer.Issue)
}

// The selected policy is independent authored input, not the predicate's signer
// claim. qualificationorigin authenticates this same default-branch identity
// projection in AuthenticateInvocation and describe.
func triggeredManifestPolicy(selected lifecycle.QualificationPolicy) (Policy, error) {
	if err := selected.Validate(); err != nil {
		return Policy{}, err
	}
	if selected.Source.Development {
		return Policy{}, e.New(e.CodeEngineCapability, "development qualification cannot issue a production release manifest")
	}
	if selected.QualificationWorkflow == "" {
		return Policy{}, e.New(e.CodeTrustPolicyMissing, "release manifest requires an independent qualification workflow")
	}
	digest, err := selected.Digest()
	if err != nil {
		return Policy{}, err
	}
	return Policy{QualificationIdentity: "https://github.com/" + selected.Source.Repository + "/" + selected.QualificationWorkflow + "@refs/heads/" + selected.Source.DefaultBranch,
		QualificationPolicyDigest: digest}, nil
}

func publishTriggeredManifest(ctx context.Context, run triggeredQualification, policy Policy, issue func(context.Context, *lifecycle.ArtifactCheckReceipt, []byte, Policy) (*Receipt, error)) (result *RunResult, err error) {
	ctx, cancel := qualification.WithHostedRuntimeBudget(ctx)
	defer cancel()
	defer func() {
		if closeErr := run.Close(); closeErr != nil {
			if result != nil {
				result.Successful = false
			}
			err = errors.Join(err, e.Wrap(e.CodeGateUnmet, closeErr, "closing release manifest qualification operation"))
		}
	}()
	publication, err := run.Publish(ctx)
	result = &RunResult{Qualification: publication}
	if err != nil {
		return result, err
	}
	checks, evidence, err := run.PublishedEvidence()
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "release manifest issuance canceled after qualification")
	}
	receipt, err := issue(ctx, checks, evidence, policy)
	if err != nil {
		return result, err
	}
	raw, err := receipt.Canonical()
	if err != nil {
		return result, err
	}
	digest, err := receipt.Digest()
	if err != nil {
		return result, err
	}
	subject, err := receipt.Subject()
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "release manifest issuance interrupted; attachments may remain")
	}
	result.Manifest, result.ManifestDigest, result.ManifestSubject = raw, digest, subject
	result.Successful = true
	return result, nil
}
