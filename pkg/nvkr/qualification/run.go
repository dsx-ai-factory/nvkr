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

// PublishTriggeredRun selects its source only from the authenticated workflow_run
// event and exact provider attempt. The policy is independently selected by the
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
	publisher, err := NewPublisher(Options{CosignBinary: policy.Cosign.Path, CosignDigest: digest, Credential: options.Credential})
	if err != nil {
		return nil, err
	}
	run, diagnostic, err := authenticateTriggeredRun(ctx, policy, options)
	if err != nil {
		return diagnostic, err
	}
	defer run.close()

	policyDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	return publisher.Publish(ctx, run.checks, run.invocation, run.smoke, Policy{QualificationPolicyDigest: policyDigest, OutsideMVPSuites: policy.OutsideMVPSuites, RequiredSuites: policy.RequiredSuites})
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

// triggeredRun owns the same authenticated source, invocation, artifact checks
// and native smoke for both predicate versions. It never signs an attachment.
type triggeredRun struct {
	source     *qualificationorigin.SourceRun
	invocation *qualificationorigin.Invocation
	checks     *lifecycle.ArtifactCheckReceipt
	smoke      *lifecycle.ToolSmokeReceipt
}

func (run *triggeredRun) close() {
	if run == nil {
		return
	}
	if run.smoke != nil {
		_ = run.smoke.Close()
	}
	if run.checks != nil {
		_ = run.checks.Close()
	}
	if run.invocation != nil {
		_ = run.invocation.Close()
	}
	if run.source != nil {
		_ = run.source.Close()
	}
}
func authenticateTriggeredRun(ctx context.Context, policy lifecycle.QualificationPolicy, options lifecycle.QualificationRuntimeOptions) (_ *triggeredRun, diagnostic *Publication, returned error) {
	run := &triggeredRun{}
	defer func() {
		if returned != nil {
			run.close()
		}
	}()
	provider := qualificationorigin.NewGitHub(options.GitHubToken)
	var err error
	run.source, err = provider.AuthenticateTriggeredSource(ctx, policy.Source)
	if err != nil {
		return nil, nil, err
	}
	run.invocation, err = provider.AuthenticateInvocation(ctx, run.source, policy.QualificationWorkflow)
	if err != nil {
		return nil, nil, err
	}
	raw, err := provider.DownloadQualificationHandoff(ctx, run.source)
	if err != nil {
		return nil, nil, err
	}
	var report *lifecycle.ArtifactCheckDescription
	run.checks, report, err = lifecycle.CheckQualificationHandoff(ctx, raw, run.source, policy, options)
	if err != nil {
		if report == nil {
			return nil, nil, err
		}
		return nil, &Publication{ArtifactChecks: report, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
	}
	run.smoke, err = lifecycle.AuthenticateHostedToolSmoke(ctx, run.checks, run.invocation, policy, options.GitHubToken)
	if err != nil {
		diagnostic, err = smokeFailurePublication(report, err)
		return nil, diagnostic, err
	}
	return run, nil, nil
}
