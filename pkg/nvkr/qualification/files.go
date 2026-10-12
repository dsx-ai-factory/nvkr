// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

type FilesOptions struct {
	ArtifactFiles         lifecycle.QualificationFilesOptions
	QualificationWorkflow string
	Signer                Options
	Policy                Policy
}

// PublishFiles authenticates the source and current qualification invocation
// before opening artifact inputs. It then reuses A's exact-input gate and C's
// receipt-only publisher. Required smoke cannot be supplied as a file or a
// caller-provided verdict while the M3-B issuer remains unavailable.
func PublishFiles(ctx context.Context, options FilesOptions) (*Publication, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification canceled")
	}
	publisher, err := NewPublisher(options.Signer)
	if err != nil {
		return nil, err
	}
	if err := admitFiles(options); err != nil {
		return nil, err
	}
	provider := qualificationorigin.NewGitHub(options.ArtifactFiles.GitHubToken)
	source, err := provider.AuthenticateSource(ctx, options.ArtifactFiles.Source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	invocation, err := provider.AuthenticateInvocation(ctx, source, options.QualificationWorkflow)
	if err != nil {
		return nil, err
	}
	defer func() { _ = invocation.Close() }()
	checks, report, err := lifecycle.CheckQualificationFiles(ctx, options.ArtifactFiles)
	if err != nil {
		if report == nil {
			return nil, err
		}
		return &Publication{ArtifactChecks: report, Verdicts: []Verdict{}, PublishedSubjects: []string{}}, err
	}
	defer func() { _ = checks.Close() }()
	return publisher.Publish(ctx, checks, invocation, nil, options.Policy)
}

func admitFiles(options FilesOptions) error {
	if len(options.Policy.RequiredSuites) > 0 {
		return e.New(e.CodeEngineCapability, "required external suite result authentication is unavailable")
	}
	if _, err := outsideSuites(options.Policy.OutsideMVPSuites); err != nil {
		return err
	}
	files := options.ArtifactFiles
	if options.QualificationWorkflow == "" || options.QualificationWorkflow == files.Source.Workflow {
		return e.New(e.CodeTrustPolicyMissing, "qualification requires a distinct approved workflow path")
	}
	if files.DefinitionPath == "" || files.EnvironmentPath == "" || files.PlanPath == "" || files.CandidatePath == "" {
		return e.New(e.CodeUsage, "qualification requires the original Definition, Environment, Plan and candidate records")
	}
	// This adapter uses one independently pinned executable for both evidence
	// verification and qualification signing; mismatched adapter settings fail
	// before provider or artifact I/O.
	if files.Consumer.CosignBinary != options.Signer.CosignBinary || files.Consumer.CosignDigest != options.Signer.CosignDigest {
		return e.New(e.CodeUnpinnedInput, "qualification signer differs from the admitted verifier executable pin")
	}
	return nil
}
