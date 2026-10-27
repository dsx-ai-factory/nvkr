// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

// PublishTriggeredRun selects its source from the admitted production event or
// explicit development dispatch and authenticates the exact provider attempt. The policy is independently selected by the
// qualification workflow; candidate inputs cannot override either trust role.
func PublishTriggeredRun(ctx context.Context, policyPath string, options lifecycle.QualificationRuntimeOptions) (result *Publication, err error) {
	if err := admitTriggeredContext(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := WithHostedRuntimeBudget(ctx)
	defer cancel()
	run, err := OpenTriggeredRun(ctx, policyPath, options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := run.Close(); closeErr != nil {
			if result != nil {
				result.Successful = false
			}
			err = errors.Join(err, e.Wrap(e.CodeGateUnmet, closeErr, "closing qualification operation"))
		}
	}()
	return run.Publish(ctx)
}

// OpenTriggeredRun admits the independently authored policy before returning a
// retained qualification operation. Provider and artifact work starts at Publish.
func OpenTriggeredRun(ctx context.Context, policyPath string, options lifecycle.QualificationRuntimeOptions) (*TriggeredRun, error) {
	if err := admitTriggeredContext(ctx); err != nil {
		return nil, err
	}
	policy, err := lifecycle.LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, err
	}
	return OpenTriggeredRunWithPolicy(ctx, policy, options)
}

// OpenTriggeredRunWithPolicy freezes one independently selected policy snapshot;
// it does not reopen a policy file or take trust configuration from artifacts.
func OpenTriggeredRunWithPolicy(ctx context.Context, policy lifecycle.QualificationPolicy, options lifecycle.QualificationRuntimeOptions) (*TriggeredRun, error) {
	if err := admitTriggeredContext(ctx); err != nil {
		return nil, err
	}
	if err := admitRunPolicy(policy); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "freezing qualification policy")
	}
	var frozen lifecycle.QualificationPolicy
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "reading frozen qualification policy")
	}
	if frozen.Cosign.Path == "" {
		frozen.Cosign.Path = "cosign"
	}
	if err := CheckGeneratedPolicy(ctx, ".", frozen); err != nil {
		return nil, err
	}
	digest, err := frozen.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	newPublisher := NewPublisher
	if frozen.Source.Development {
		newPublisher = NewDevelopmentPublisher
	}
	publisher, err := newPublisher(Options{CosignBinary: frozen.Cosign.Path, CosignDigest: digest, Credential: options.Credential})
	if err != nil {
		return nil, err
	}
	run := newTriggeredRun(ctx)
	run.state.publish = func(ctx context.Context) (*Publication, error) {
		return run.publishTriggered(ctx, frozen, options, publisher)
	}
	return run, nil
}

func admitTriggeredContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "qualification requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "qualification canceled")
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "qualification signing requires Linux pinned cosign execution")
	}
	return nil
}

func (run *TriggeredRun) publishTriggered(ctx context.Context, policy lifecycle.QualificationPolicy, options lifecycle.QualificationRuntimeOptions, publisher *Publisher) (*Publication, error) {
	storage := qualificationorigin.CIStorageCredential{Username: options.CIStorageUsername, Token: options.CIStorageToken}
	provider := qualificationorigin.NewGitHubWithCIStorageCredential(options.GitHubToken, storage)
	source, err := provider.AuthenticateTriggeredSource(ctx, policy.Source)
	if err != nil {
		return nil, err
	}
	run.state.closers = append(run.state.closers, source.Close)
	invocation, err := provider.AuthenticateInvocation(ctx, source, policy.QualificationWorkflow)
	if err != nil {
		return nil, err
	}
	run.state.closers = append(run.state.closers, invocation.Close)
	checks, report, err := checkTriggeredArtifacts(ctx, provider, source, policy, options)
	if err != nil {
		if report == nil {
			return nil, err
		}
		return &Publication{ArtifactChecks: report, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
	}
	run.state.checks = checks
	run.state.closers = append(run.state.closers, checks.Close)
	smoke, err := lifecycle.AuthenticateHostedToolSmokeWithCIStorageCredential(ctx, checks, invocation, policy, options.GitHubToken, storage)
	if err != nil {
		return smokeFailurePublication(report, err)
	}
	run.state.closers = append(run.state.closers, smoke.Close)
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
