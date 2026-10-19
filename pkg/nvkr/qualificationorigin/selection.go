// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"net/url"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// RunLocator selects one exact producing attempt. It is a locator, not evidence.
type RunLocator struct {
	Repository string `json:"repository"`
	RunID      string `json:"runId"`
	Attempt    string `json:"attempt"`
}

// SourcePolicy is selected independently of a candidate. Provider discovery can
// supply a commit, but cannot choose which repository, workflow or branch to trust.
type SourcePolicy struct {
	Development   bool   `json:"development,omitempty"`
	Repository    string `json:"repository"`
	Workflow      string `json:"workflow"`
	DefaultBranch string `json:"defaultBranch"`
}

func (p SourcePolicy) Validate() error {
	if !repositoryPattern.MatchString(p.Repository) || !workflowPattern.MatchString(p.Workflow) ||
		p.DefaultBranch == "" || strings.TrimSpace(p.DefaultBranch) != p.DefaultBranch ||
		strings.ContainsAny(p.DefaultBranch, " ~^:?*[\\\t\r\n") || strings.Contains(p.DefaultBranch, "..") ||
		strings.Contains(p.DefaultBranch, "@{") || strings.HasPrefix(p.DefaultBranch, "/") || strings.HasSuffix(p.DefaultBranch, "/") {
		return e.New(e.CodeTrustPolicyMissing, "qualification policy requires an exact approved repository, workflow and default branch")
	}
	return nil
}

func (r RunLocator) Validate() error {
	if !repositoryPattern.MatchString(r.Repository) || !numberPattern.MatchString(r.RunID) || !numberPattern.MatchString(r.Attempt) {
		return e.New(e.CodeUsage, "qualification requires an exact GitHub Run and attempt")
	}
	return nil
}

// ParseRunURL accepts only a canonical GitHub attempt URL. A Run URL without an
// attempt is ambiguous: retries must never silently select the latest attempt.
func ParseRunURL(raw string) (RunLocator, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return RunLocator{}, e.New(e.CodeUsage, "Run must be an exact https://github.com/OWNER/REPO/actions/runs/ID/attempts/N URL")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 8 || parts[0] != "" || parts[3] != "actions" || parts[4] != "runs" || parts[6] != "attempts" {
		return RunLocator{}, e.New(e.CodeUsage, "Run URL must select an explicit attempt")
	}
	locator := RunLocator{Repository: parts[1] + "/" + parts[2], RunID: parts[5], Attempt: parts[7]}
	if err := locator.Validate(); err != nil {
		return RunLocator{}, err
	}
	if raw != "https://github.com/"+locator.Repository+"/actions/runs/"+locator.RunID+"/attempts/"+locator.Attempt {
		return RunLocator{}, e.New(e.CodeUsage, "Run URL must use its canonical exact-attempt form")
	}
	return locator, nil
}

// AuthenticateRun discovers the source commit from the exact attempt endpoint,
// then applies the same successful-build checks as AuthenticateSource. The
// caller's independent policy remains authoritative for every trust decision.
func (g *GitHub) AuthenticateRun(ctx context.Context, locator RunLocator, policy SourcePolicy) (*SourceRun, error) {
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := locator.Validate(); err != nil {
		return nil, err
	}
	if locator.Repository != policy.Repository {
		return nil, e.New(e.CodeUntrustedSigner, "selected Run repository differs from the approved source policy")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	request := SourceRequest{Repository: policy.Repository, RunID: locator.RunID, Attempt: locator.Attempt,
		Workflow: policy.Workflow, DefaultBranch: policy.DefaultBranch}
	if policy.Development {
		_, err := developmentBranch(policy)
		if err != nil {
			return nil, err
		}
	}
	repository, run, err := g.lookup(ctx, request)
	if err != nil {
		return nil, err
	}
	if policy.Development {
		request.Branch = run.HeadBranch
		if request.Branch == "" || request.Branch == policy.DefaultBranch {
			return nil, e.New(e.CodeVerifyFailed, "development source requires a nondefault branch")
		}
	}
	request.Commit = run.HeadSHA
	if !commitPattern.MatchString(request.Commit) {
		return nil, e.New(e.CodeVerifyFailed, "GitHub source attempt does not identify an exact commit")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if policy.Development && run.Event != "workflow_dispatch" {
		return nil, e.New(e.CodeVerifyFailed, "development qualification requires a manually dispatched source")
	}
	return authenticateSourceResponse(ctx, request, repository, run)
}
