// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestParseRunURLRequiresExactAttempt(t *testing.T) {
	valid := "https://github.com/example/nvkr/actions/runs/1234/attempts/2"
	for _, tc := range []struct {
		name string
		url  string
		pass bool
	}{
		{"exact attempt", valid, true},
		{"latest run", "https://github.com/example/nvkr/actions/runs/1234", false},
		{"http", strings.Replace(valid, "https:", "http:", 1), false},
		{"foreign host", strings.Replace(valid, "github.com", "github.example", 1), false},
		{"host suffix", strings.Replace(valid, "github.com", "github.com.attacker.example", 1), false},
		{"credentials", strings.Replace(valid, "github.com", "user@github.com", 1), false},
		{"port", strings.Replace(valid, "github.com", "github.com:443", 1), false},
		{"query", valid + "?attempt=3", false},
		{"empty query", valid + "?", false},
		{"fragment", valid + "#logs", false},
		{"empty fragment", valid + "#", false},
		{"escaped path", strings.Replace(valid, "example", "%65xample", 1), false},
		{"trailing slash", valid + "/", false},
		{"zero attempt", strings.TrimSuffix(valid, "2") + "0", false},
		{"leading zero", strings.TrimSuffix(valid, "2") + "02", false},
		{"traversal", strings.Replace(valid, "example", "..", 1), false},
		{"extra segment", valid + "/logs", false},
		{"surrounding space", " " + valid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRunURL(tc.url)
			if !tc.pass {
				assertCode(t, err, e.CodeUsage)
				if got != (RunLocator{}) {
					t.Fatal("invalid URL returned a usable locator")
				}
				return
			}
			if err != nil || got != (RunLocator{Repository: "example/nvkr", RunID: "1234", Attempt: "2"}) {
				t.Fatalf("locator=%+v, err=%v", got, err)
			}
		})
	}
}

func TestAuthenticateRunDiscoversCommitUnderIndependentPolicy(t *testing.T) {
	request, repo, run := sourceFixture()
	locator := RunLocator{Repository: request.Repository, RunID: request.RunID, Attempt: request.Attempt}
	policy := SourcePolicy{Repository: request.Repository, Workflow: request.Workflow, DefaultBranch: request.DefaultBranch}
	for _, tc := range []struct {
		name string
		edit func(*repositoryResponse, *runResponse)
		pass bool
	}{
		{"selected attempt", func(*repositoryResponse, *runResponse) {}, true},
		{"provider commit", func(_ *repositoryResponse, r *runResponse) { r.HeadSHA = strings.Repeat("b", 40) }, true},
		{"noncommit", func(_ *repositoryResponse, r *runResponse) { r.HeadSHA = "main" }, false},
		{"different attempt", func(_ *repositoryResponse, r *runResponse) { r.Attempt = "1" }, false},
		{"different run", func(_ *repositoryResponse, r *runResponse) { r.ID = "1235" }, false},
		{"unapproved workflow", func(_ *repositoryResponse, r *runResponse) { r.Path = ".github/workflows/other.yml" }, false},
		{"nondefault branch", func(_ *repositoryResponse, r *runResponse) { r.HeadBranch = "feature" }, false},
		{"changed default branch", func(r *repositoryResponse, _ *runResponse) { r.DefaultBranch = "trunk" }, false},
		{"fork", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.ID = "456" }, false},
		{"fork name", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.FullName = "other/nvkr" }, false},
		{"repository substitution", func(r *repositoryResponse, _ *runResponse) { r.FullName = "other/nvkr" }, false},
		{"failed build", func(_ *repositoryResponse, r *runResponse) { r.Conclusion = "failure" }, false},
		{"incomplete build", func(_ *repositoryResponse, r *runResponse) { r.Status = "in_progress" }, false},
		{"pull request", func(_ *repositoryResponse, r *runResponse) { r.Event = "pull_request" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, build := repo, run
			tc.edit(&r, &build)
			receipt, err := fakeGitHub(t, r, build).AuthenticateRun(t.Context(), locator, policy)
			if !tc.pass {
				assertCode(t, err, e.CodeVerifyFailed)
				if receipt != nil {
					t.Fatal("refused attempt returned authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = receipt.Close() }()
			description, err := receipt.Description()
			if err != nil || description.Commit != build.HeadSHA || description.Workflow != policy.Workflow || description.Attempt != locator.Attempt {
				t.Fatalf("source=%+v, err=%v", description, err)
			}
		})
	}
}

func TestAuthenticateRunAdmissionPrecedesProviderIO(t *testing.T) {
	request, _, _ := sourceFixture()
	locator := RunLocator{Repository: request.Repository, RunID: request.RunID, Attempt: request.Attempt}
	policy := SourcePolicy{Repository: request.Repository, Workflow: request.Workflow, DefaultBranch: request.DefaultBranch}
	g := NewGitHub("")
	g.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider I/O"); return nil, nil })
	for _, tc := range []struct {
		name string
		edit func(*RunLocator, *SourcePolicy)
		code e.Code
	}{
		{"unapproved repository", func(r *RunLocator, _ *SourcePolicy) { r.Repository = "other/nvkr" }, e.CodeUntrustedSigner},
		{"missing attempt", func(r *RunLocator, _ *SourcePolicy) { r.Attempt = "" }, e.CodeUsage},
		{"missing policy", func(_ *RunLocator, p *SourcePolicy) { *p = SourcePolicy{} }, e.CodeTrustPolicyMissing},
		{"bad policy workflow", func(_ *RunLocator, p *SourcePolicy) { p.Workflow = "../build.yml" }, e.CodeTrustPolicyMissing},
		{"bad policy branch", func(_ *RunLocator, p *SourcePolicy) { p.DefaultBranch = "main\n" }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, p := locator, policy
			tc.edit(&l, &p)
			receipt, err := g.AuthenticateRun(t.Context(), l, p)
			assertCode(t, err, tc.code)
			if receipt != nil {
				t.Fatal("admission failure issued receipt")
			}
		})
	}
	_, err := g.AuthenticateRun(nil, locator, policy) //nolint:staticcheck // Nil-context admission is part of the public contract.
	assertCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = g.AuthenticateRun(ctx, locator, policy)
	assertCode(t, err, e.CodeVerifyFailed)
}

// The final response may finish reading before its Close cancels the caller.
// Neither API may mint source authority after that cancellation.
func TestSourceAuthenticationRejectsFinalResponseCloseCancellation(t *testing.T) {
	for _, method := range []string{"AuthenticateSource", "AuthenticateRun"} {
		t.Run(method, func(t *testing.T) {
			request, repo, run := sourceFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider := fakeGitHub(t, repo, run)
			transport := provider.client.Transport
			closes := 0
			provider.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if err == nil && strings.Contains(request.URL.Path, "/attempts/") {
					response.Body = &sourceCancelOnCloseBody{ReadCloser: response.Body, cancel: cancel, closes: &closes}
				}
				return response, err
			})
			var receipt *SourceRun
			var err error
			if method == "AuthenticateSource" {
				receipt, err = provider.AuthenticateSource(ctx, request)
			} else {
				receipt, err = provider.AuthenticateRun(ctx,
					RunLocator{Repository: request.Repository, RunID: request.RunID, Attempt: request.Attempt},
					SourcePolicy{Repository: request.Repository, Workflow: request.Workflow, DefaultBranch: request.DefaultBranch})
			}
			assertCode(t, err, e.CodeVerifyFailed)
			if !errors.Is(err, context.Canceled) || receipt != nil || closes != 1 {
				t.Fatalf("receipt=%v closes=%d cancellation=%v", receipt, closes, err)
			}
		})
	}
}

type sourceCancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	closes *int
}

func (body *sourceCancelOnCloseBody) Close() error {
	err := body.ReadCloser.Close()
	*body.closes++
	body.cancel()
	return err
}
