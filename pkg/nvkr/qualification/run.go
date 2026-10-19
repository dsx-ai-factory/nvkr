// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

// PublishTriggeredRun selects its source from the admitted production event or
// explicit development dispatch and authenticates the exact provider attempt. The policy is independently selected by the
// qualification workflow; candidate inputs cannot override either trust role.
func PublishTriggeredRun(ctx context.Context, policyPath string, options lifecycle.QualificationRuntimeOptions) (*Publication, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification canceled")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "qualification signing requires Linux pinned cosign execution")
	}
	ctx, cancel := hostedRuntimeContext(ctx)
	defer cancel()
	policy, err := lifecycle.LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, err
	}
	if err := admitRunPolicy(policy); err != nil {
		return nil, err
	}
	if err := CheckGeneratedPolicy(ctx, ".", policy); err != nil {
		return nil, err
	}
	digest, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	newPublisher := NewPublisher
	if policy.Source.Development {
		newPublisher = NewDevelopmentPublisher
	}
	publisher, err := newPublisher(Options{CosignBinary: policy.Cosign.Path, CosignDigest: digest, Credential: options.Credential})
	if err != nil {
		return nil, err
	}
	storage := qualificationorigin.CIStorageCredential{Username: options.CIStorageUsername, Token: options.CIStorageToken}
	provider := qualificationorigin.NewGitHubWithCIStorageCredential(options.GitHubToken, storage)
	source, err := provider.AuthenticateTriggeredSource(ctx, policy.Source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	invocation, err := provider.AuthenticateInvocation(ctx, source, policy.QualificationWorkflow)
	if err != nil {
		return nil, err
	}
	defer func() { _ = invocation.Close() }()
	checks, report, err := checkTriggeredArtifacts(ctx, provider, source, policy, options)
	if err != nil {
		if report == nil {
			return nil, err
		}
		return &Publication{ArtifactChecks: report, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
	}
	defer func() { _ = checks.Close() }()
	smoke, err := lifecycle.AuthenticateHostedToolSmokeWithCIStorageCredential(ctx, checks, invocation, policy, options.GitHubToken, storage)
	if err != nil {
		return smokeFailurePublication(report, err)
	}
	defer func() { _ = smoke.Close() }()
	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	return publisher.Publish(ctx, checks, invocation, smoke, Policy{QualificationPolicyDigest: policyDigest, OutsideMVPSuites: policy.OutsideMVPSuites, RequiredSuites: policy.RequiredSuites})
}

// smokeFailurePublication preserves A's actual results and names the failed B
// authentication gate for every known output. A transport/signature refusal does
// not establish that the delivered executable itself failed during execution.
func smokeFailurePublication(report *lifecycle.ArtifactCheckDescription, err error) (*Publication, error) {
	if report == nil {
		return nil, err
	}
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(err, &coded) {
		code = coded.Code
	}
	verdicts := make([]Verdict, 0, len(report.Outputs))
	for _, output := range report.Outputs {
		verdicts = append(verdicts, Verdict{Output: output.Name, Subject: output.Subject,
			Check: "delivered-tool-smoke-authentication", Status: "fail",
			Reason: "complete delivered-tool smoke authentication failed (" + string(code) + "); no authenticated smoke result was accepted"})
	}
	return &Publication{ArtifactChecks: report, Verdicts: verdicts, PublishedSubjects: []string{}}, err
}

func admitRunPolicy(policy lifecycle.QualificationPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.QualificationWorkflow == "" {
		return e.New(e.CodeTrustPolicyMissing, "qualification policy requires an independent qualification workflow")
	}
	if len(policy.RequiredSuites) > 0 {
		return e.New(e.CodeEngineCapability, "required external suite result authentication is unavailable")
	}
	if policy.Smoke == nil {
		return e.New(e.CodeTrustPolicyMissing, "qualification requires the complete native smoke policy")
	}
	_, err := outsideSuites(policy.OutsideMVPSuites)
	return err
}

func checkTriggeredArtifacts(ctx context.Context, provider *qualificationorigin.GitHub, source *qualificationorigin.SourceRun, policy lifecycle.QualificationPolicy, options lifecycle.QualificationRuntimeOptions) (*lifecycle.ArtifactCheckReceipt, *lifecycle.ArtifactCheckDescription, error) {
	if policy.Source.Development {
		return lifecycle.CheckDevelopmentQualificationSource(ctx, provider, source, policy, options)
	}
	raw, err := provider.DownloadQualificationHandoff(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	return lifecycle.CheckQualificationHandoff(ctx, raw, source, policy, options)
}
