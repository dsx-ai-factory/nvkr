// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sourceFixture() (SourceRequest, repositoryResponse, runResponse) {
	r := SourceRequest{Repository: "example/nvkr", RunID: "1234", Attempt: "2", Commit: strings.Repeat("a", 40), Workflow: ".github/workflows/build.yml", DefaultBranch: "main"}
	repo := repositoryResponse{FullName: r.Repository, DefaultBranch: r.DefaultBranch, ID: json.Number("123")}
	run := runResponse{ID: json.Number(r.RunID), Attempt: json.Number(r.Attempt), HeadSHA: r.Commit, HeadBranch: r.DefaultBranch, Path: r.Workflow, Status: "completed", Conclusion: "success", Event: "workflow_dispatch", Repository: repo, HeadRepository: repo}
	return r, repo, run
}

func fakeGitHub(t *testing.T, repo repositoryResponse, run runResponse) *GitHub {
	t.Helper()
	g := NewGitHub("read-token")
	g.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != "GET" || request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.Header.Get("Authorization") != "Bearer read-token" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
		}
		if _, ok := request.Context().Deadline(); !ok {
			t.Fatal("unbounded provider request")
		}
		var value any
		switch request.URL.Path {
		case "/repos/example/nvkr":
			value = repo
		case "/repos/example/nvkr/actions/runs/1234/attempts/2":
			value = run
		default:
			t.Fatalf("wrong request %s", request.URL.Path)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	return g
}

func assertCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestSourceRunRequiresExactProviderFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*repositoryResponse, *runResponse)
	}{
		{"wrong repository", func(r *repositoryResponse, _ *runResponse) { r.FullName = "other/nvkr" }},
		{"renamed branch", func(r *repositoryResponse, _ *runResponse) { r.DefaultBranch = "trunk" }},
		{"wrong run", func(_ *repositoryResponse, r *runResponse) { r.ID = "1235" }},
		{"wrong attempt", func(_ *repositoryResponse, r *runResponse) { r.Attempt = "1" }},
		{"wrong commit", func(_ *repositoryResponse, r *runResponse) { r.HeadSHA = strings.Repeat("b", 40) }},
		{"wrong workflow", func(_ *repositoryResponse, r *runResponse) { r.Path = ".github/workflows/other.yml" }},
		{"wrong branch", func(_ *repositoryResponse, r *runResponse) { r.HeadBranch = "feature" }},
		{"wrong repo id", func(_ *repositoryResponse, r *runResponse) { r.Repository.ID = "456" }},
		{"fork", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.ID = "456" }},
		{"fork name", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.FullName = "fork/nvkr" }},
		{"unfinished", func(_ *repositoryResponse, r *runResponse) { r.Status = "in_progress" }},
		{"failed", func(_ *repositoryResponse, r *runResponse) { r.Conclusion = "failure" }},
		{"pull request", func(_ *repositoryResponse, r *runResponse) { r.Event = "pull_request" }},
		{"reusable invocation", func(_ *repositoryResponse, r *runResponse) { r.Event = "workflow_call" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, repo, run := sourceFixture()
			tc.edit(&repo, &run)
			receipt, err := fakeGitHub(t, repo, run).AuthenticateSource(context.Background(), request)
			if receipt != nil {
				t.Fatal("failed source returned receipt")
			}
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestSourceRunReceiptAndRevocation(t *testing.T) {
	for _, event := range []string{"push", "workflow_dispatch"} {
		t.Run(event, func(t *testing.T) {
			request, repo, run := sourceFixture()
			run.Event = event
			receipt, err := fakeGitHub(t, repo, run).AuthenticateSource(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			description, err := receipt.Description()
			if err != nil {
				t.Fatal(err)
			}
			if description.Commit != request.Commit || description.InvocationID != "https://github.com/example/nvkr/actions/runs/1234/attempts/2" || description.BuilderID != "https://github.com/example/nvkr/.github/workflows/build.yml@refs/heads/main" {
				t.Fatalf("wrong source: %+v", description)
			}
			description.Commit = "mutated"
			again, _ := receipt.Description()
			if again.Commit != request.Commit {
				t.Fatal("description altered receipt")
			}
			copied := *receipt
			if err := receipt.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copied.Description()
			assertCode(t, err, e.CodeProvenanceMissing)
		})
	}
	var empty SourceRun
	assertCode(t, empty.Require(), e.CodeProvenanceMissing)
}

func TestSourceAdmissionPrecedesProviderIO(t *testing.T) {
	request, _, _ := sourceFixture()
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	g := NewGitHub("")
	g.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected IO"); return nil, nil })
	for _, tc := range []struct {
		name string
		edit func(*SourceRequest)
	}{
		{"repository", func(r *SourceRequest) { r.Repository = "../other" }},
		{"run", func(r *SourceRequest) { r.RunID = "01" }},
		{"attempt", func(r *SourceRequest) { r.Attempt = "0" }},
		{"commit", func(r *SourceRequest) { r.Commit = "main" }},
		{"workflow", func(r *SourceRequest) { r.Workflow = ".github/workflows/../malicious.yml" }},
		{"branch", func(r *SourceRequest) { r.DefaultBranch = "main\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			tc.edit(&candidate)
			assertCode(t, candidate.Validate(), e.CodeTrustPolicyMissing)
			_, err := g.AuthenticateSource(context.Background(), candidate)
			assertCode(t, err, e.CodeTrustPolicyMissing)
		})
	}
	_, err := g.AuthenticateSource(nil, request) //nolint:staticcheck // Contract explicitly rejects nil contexts before I/O.
	assertCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = g.AuthenticateSource(ctx, request)
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestProviderResponseFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       e.Code
	}{
		{"redirect", "", http.StatusMovedPermanently, e.CodeFetchFailed},
		{"not found", "", http.StatusNotFound, e.CodeFetchFailed},
		{"duplicate key", `{"id":1,"id":2}`, http.StatusOK, e.CodeVerifyFailed},
		{"extra document", `{} {}`, http.StatusOK, e.CodeVerifyFailed},
		{"oversize", strings.Repeat(" ", maxResponseBytes+1), http.StatusOK, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, _, _ := sourceFixture()
			g := NewGitHub("")
			g.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err := g.AuthenticateSource(context.Background(), request)
			assertCode(t, err, tc.code)
		})
	}
}
