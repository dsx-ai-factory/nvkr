// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func developmentBranch(policy SourcePolicy) (string, error) {
	branch, ok := strings.CutPrefix(os.Getenv("GITHUB_REF"), "refs/heads/")
	if !policy.Development || policy.Workflow != development.ProducerWorkflow || !ok || branch == policy.DefaultBranch ||
		os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" ||
		os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" || os.Getenv("GITHUB_REPOSITORY") != policy.Repository ||
		os.Getenv("GITHUB_WORKFLOW_REF") != policy.Repository+"/"+development.QualificationWorkflow+"@refs/heads/"+branch {
		return "", e.New(e.CodeTrustPolicyMissing, "development qualification requires its separate manually dispatched nondefault-branch workflow")
	}
	if err := (SourcePolicy{Repository: policy.Repository, Workflow: policy.Workflow, DefaultBranch: branch}).Validate(); err != nil {
		return "", err
	}
	return branch, nil
}

func dispatchedDevelopmentLocator(ctx context.Context) (RunLocator, error) {
	held, err := heldfile.OpenContext(ctx, os.Getenv("GITHUB_EVENT_PATH"), heldfile.Options{Label: "development qualification event", MaxBytes: maxResponseBytes})
	if err != nil {
		return RunLocator{}, e.Wrap(e.CodeProvenanceMissing, err, "opening development qualification event")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxResponseBytes)
	if err != nil {
		return RunLocator{}, e.Wrap(e.CodeProvenanceMissing, err, "reading development qualification event")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return RunLocator{}, e.Wrap(e.CodeVerifyFailed, err, "invalid development qualification event")
	}
	var event struct {
		Inputs struct {
			SourceRunURL string `json:"source_run_url"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return RunLocator{}, e.Wrap(e.CodeVerifyFailed, err, "decoding development qualification event")
	}
	return ParseRunURL(event.Inputs.SourceRunURL)
}

func (g *GitHub) authenticateDispatchedDevelopmentSource(ctx context.Context, policy SourcePolicy) (*SourceRun, error) {
	if _, err := developmentBranch(policy); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	locator, err := dispatchedDevelopmentLocator(ctx)
	if err != nil {
		return nil, err
	}
	return g.AuthenticateRun(ctx, locator, policy)
}

func (g *GitHub) authenticateDevelopmentInvocation(ctx context.Context, source *SourceRun, workflow string) (*Invocation, error) {
	origin, err := source.Description()
	if err != nil {
		return nil, err
	}
	branch, err := developmentBranch(SourcePolicy{Development: true, Repository: origin.Repository, Workflow: origin.Workflow, DefaultBranch: origin.DefaultBranch})
	if err != nil {
		return nil, err
	}
	request := SourceRequest{Branch: branch, Repository: os.Getenv("GITHUB_REPOSITORY"), RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT"), Commit: os.Getenv("GITHUB_SHA"), Workflow: workflow, DefaultBranch: origin.DefaultBranch}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if workflow != development.QualificationWorkflow || request.RunID == origin.RunID {
		return nil, e.New(e.CodeTrustPolicyMissing, "development qualification requires a separate invocation of the admitted development workflow")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	locator, err := dispatchedDevelopmentLocator(ctx)
	if err != nil {
		return nil, err
	}
	if locator.Repository != origin.Repository || locator.RunID != origin.RunID || locator.Attempt != origin.Attempt {
		return nil, e.New(e.CodeVerifyFailed, "development dispatch differs from the authenticated source attempt")
	}
	repo, run, err := g.lookup(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := matchRun(request, repo, run); err != nil {
		return nil, err
	}
	if run.Event != "workflow_dispatch" || run.Status != "in_progress" || run.Conclusion != "" {
		return nil, e.New(e.CodeVerifyFailed, "development qualification invocation is not an active manual dispatch")
	}
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	if err := source.Require(); err != nil {
		return nil, err
	}
	return &Invocation{description: describe(request), source: source, closed: new(atomic.Bool)}, nil
}
