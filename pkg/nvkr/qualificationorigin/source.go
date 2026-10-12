// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qualificationorigin authenticates GitHub workflow identities independently
// of artifact descriptions and of the workflow requesting qualification.
package qualificationorigin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const maxResponseBytes = 2 << 20

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	numberPattern     = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	workflowPattern   = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml$`)
)

// SourceRequest is an externally selected expectation, never evidence.
type SourceRequest struct {
	Repository    string `json:"repository"`
	RunID         string `json:"runId"`
	Attempt       string `json:"attempt"`
	Commit        string `json:"commit"`
	Workflow      string `json:"workflow"`
	DefaultBranch string `json:"defaultBranch"`
}

// Description is a copy of authenticated facts, with no signing authority.
type Description struct {
	Repository    string `json:"repository"`
	RunID         string `json:"runId"`
	Attempt       string `json:"attempt"`
	Commit        string `json:"commit"`
	Workflow      string `json:"workflow"`
	DefaultBranch string `json:"defaultBranch"`
	Ref           string `json:"ref"`
	InvocationID  string `json:"invocationId"`
	BuilderID     string `json:"builderId"`
}

// SourceRun is issued only after the provider authenticates a successful exact
// attempt. Copies share revocation. JSON and caller-provided booleans cannot mint it.
type SourceRun struct {
	description Description
	closed      *atomic.Bool
}

func (s *SourceRun) Require() error {
	if s == nil || s.closed == nil || s.closed.Load() {
		return e.New(e.CodeProvenanceMissing, "qualification requires an authenticated producing Run")
	}
	return nil
}

func (s *SourceRun) Description() (Description, error) {
	if err := s.Require(); err != nil {
		return Description{}, err
	}
	return s.description, nil
}

func (s *SourceRun) Close() error {
	if s != nil && s.closed != nil {
		s.closed.Store(true)
	}
	return nil
}

// GitHub authenticates provider facts from the fixed GitHub API origin. Its HTTP
// transport is deliberately not a public receipt-minting injection point.
type GitHub struct {
	token  string
	client *http.Client
	api    string
}

// NewGitHub uses an explicit read credential; empty permits public repository
// reads. Redirects are refused, including repository renames and token forwarding.
func NewGitHub(token string) *GitHub {
	return &GitHub{token: token, api: "https://api.github.com", client: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}}
}

type repositoryResponse struct {
	FullName      string      `json:"full_name"`
	DefaultBranch string      `json:"default_branch"`
	ID            json.Number `json:"id"`
}

type runResponse struct {
	ID             json.Number        `json:"id"`
	Attempt        json.Number        `json:"run_attempt"`
	HeadSHA        string             `json:"head_sha"`
	HeadBranch     string             `json:"head_branch"`
	Path           string             `json:"path"`
	Status         string             `json:"status"`
	Conclusion     string             `json:"conclusion"`
	Event          string             `json:"event"`
	Repository     repositoryResponse `json:"repository"`
	HeadRepository repositoryResponse `json:"head_repository"`
}

// Validate admits only the request's exact-identity shape. It does not
// authenticate provider facts or grant source Run authority.
func (r SourceRequest) Validate() error {
	if !repositoryPattern.MatchString(r.Repository) || !numberPattern.MatchString(r.RunID) ||
		!numberPattern.MatchString(r.Attempt) || !commitPattern.MatchString(r.Commit) || !workflowPattern.MatchString(r.Workflow) ||
		r.DefaultBranch == "" || strings.TrimSpace(r.DefaultBranch) != r.DefaultBranch ||
		strings.ContainsAny(r.DefaultBranch, " ~^:?*[\\\t\r\n") || strings.Contains(r.DefaultBranch, "..") ||
		strings.Contains(r.DefaultBranch, "@{") || strings.HasPrefix(r.DefaultBranch, "/") || strings.HasSuffix(r.DefaultBranch, "/") {
		return e.New(e.CodeTrustPolicyMissing, "qualification source requires an exact repository, Run, attempt, commit, workflow and default branch")
	}
	return nil
}

// AuthenticateSource accepts only an approved, successful push/manual build on
// the repository's actual default branch. It never uses the current invocation's
// environment to invent the producing Run's identity.
func (g *GitHub) AuthenticateSource(ctx context.Context, request SourceRequest) (*SourceRun, error) {
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	repository, run, err := g.lookup(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := matchRun(request, repository, run); err != nil {
		return nil, err
	}
	if run.Status != "completed" || run.Conclusion != "success" || (run.Event != "push" && run.Event != "workflow_dispatch") {
		return nil, e.New(e.CodeVerifyFailed, "qualification source is not a successful approved default-branch build")
	}
	return &SourceRun{description: describe(request), closed: new(atomic.Bool)}, nil
}

func describe(r SourceRequest) Description {
	return Description{Repository: r.Repository, RunID: r.RunID, Attempt: r.Attempt, Commit: r.Commit, Workflow: r.Workflow,
		DefaultBranch: r.DefaultBranch, Ref: "refs/heads/" + r.DefaultBranch,
		InvocationID: "https://github.com/" + r.Repository + "/actions/runs/" + r.RunID + "/attempts/" + r.Attempt,
		BuilderID:    "https://github.com/" + r.Repository + "/" + r.Workflow + "@refs/heads/" + r.DefaultBranch}
}

func (g *GitHub) lookup(ctx context.Context, request SourceRequest) (repositoryResponse, runResponse, error) {
	var repository repositoryResponse
	var run runResponse
	if err := g.get(ctx, "/repos/"+request.Repository, &repository); err != nil {
		return repository, run, err
	}
	if err := g.get(ctx, "/repos/"+request.Repository+"/actions/runs/"+request.RunID+"/attempts/"+request.Attempt, &run); err != nil {
		return repository, run, err
	}
	return repository, run, nil
}

func matchRun(request SourceRequest, repository repositoryResponse, run runResponse) error {
	if repository.FullName != request.Repository || !numberPattern.MatchString(string(repository.ID)) ||
		repository.DefaultBranch != request.DefaultBranch || run.Repository.ID != repository.ID || run.HeadRepository.ID != repository.ID ||
		run.Repository.FullName != request.Repository || run.HeadRepository.FullName != request.Repository ||
		string(run.ID) != request.RunID || string(run.Attempt) != request.Attempt || run.HeadSHA != request.Commit ||
		run.HeadBranch != request.DefaultBranch || run.Path != request.Workflow {
		return e.New(e.CodeVerifyFailed, "GitHub Run does not match the exact approved repository, attempt, commit, workflow and default branch")
	}
	return nil
}

func admitContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitHub origin authentication requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "GitHub origin authentication canceled")
	}
	return nil
}

func (g *GitHub) get(ctx context.Context, path string, out any) error {
	if g == nil || g.client == nil || g.api == "" {
		return e.New(e.CodeEngineCapability, "GitHub origin authenticator is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+path, nil)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating GitHub origin request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "authenticating GitHub origin")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return e.New(e.CodeFetchFailed, "GitHub origin returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "reading GitHub origin")
	}
	if len(raw) > maxResponseBytes {
		return e.New(e.CodeVerifyFailed, "GitHub origin response exceeds size limit")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid GitHub origin response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding GitHub origin response")
	}
	return nil
}
