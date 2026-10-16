// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func developmentOriginFixture(t *testing.T) (SourcePolicy, repositoryResponse, runResponse) {
	t.Helper()
	policy, run := triggeredEnvironment(t)
	_, repo, _ := sourceFixture()
	policy.Development = true
	policy.Workflow = development.ProducerWorkflow
	run.Path = policy.Workflow
	run.HeadBranch = "feature/qualification"
	t.Setenv("GITHUB_EVENT_NAME", "workflow_dispatch")
	t.Setenv("GITHUB_REF", "refs/heads/"+run.HeadBranch)
	t.Setenv("GITHUB_WORKFLOW_REF", policy.Repository+"/"+development.QualificationWorkflow+"@refs/heads/"+run.HeadBranch)
	raw := []byte(`{"inputs":{"source_run_url":"https://github.com/example/nvkr/actions/runs/1234/attempts/2"}}`)
	if err := os.WriteFile(os.Getenv("GITHUB_EVENT_PATH"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return policy, repo, run
}

func TestDevelopmentSourceExactAttempt(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*repositoryResponse, *runResponse)
		code e.Code
	}{
		{name: "successful manual source"},
		{"default branch", func(_ *repositoryResponse, r *runResponse) { r.HeadBranch = "main" }, e.CodeVerifyFailed},
		{"other development source branch", func(_ *repositoryResponse, r *runResponse) { r.HeadBranch = "feature/other" }, ""},
		{"production workflow", func(_ *repositoryResponse, r *runResponse) { r.Path = ".github/workflows/nvkr-run.yml" }, e.CodeVerifyFailed},
		{"push", func(_ *repositoryResponse, r *runResponse) { r.Event = "push" }, e.CodeVerifyFailed},
		{"fork", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.ID = "456" }, e.CodeVerifyFailed},
		{"retry", func(_ *repositoryResponse, r *runResponse) { r.Attempt = "3" }, e.CodeVerifyFailed},
		{"failed", func(_ *repositoryResponse, r *runResponse) { r.Conclusion = "failure" }, e.CodeVerifyFailed},
		{"renamed default", func(r *repositoryResponse, _ *runResponse) { r.DefaultBranch = "trunk" }, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, repo, run := developmentOriginFixture(t)
			if test.edit != nil {
				test.edit(&repo, &run)
			}
			source, err := fakeGitHub(t, repo, run).AuthenticateTriggeredSource(t.Context(), policy)
			if test.code != "" {
				assertCode(t, err, test.code)
				if source != nil {
					t.Fatal("refused source issued receipt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			description, err := source.Description()
			if err != nil {
				t.Fatal(err)
			}
			if description.Branch != run.HeadBranch || description.DefaultBranch != "main" || description.Commit != run.HeadSHA || description.Ref != "refs/heads/"+run.HeadBranch || description.BuilderID != "https://github.com/example/nvkr/"+development.ProducerWorkflow+"@"+description.Ref {
				t.Fatalf("incorrect source: %+v", description)
			}
			if err := description.Validate(); err != nil {
				t.Fatal(err)
			}
			copied := *source
			_ = source.Close()
			assertCode(t, copied.Require(), e.CodeProvenanceMissing)
		})
	}
}

func TestDevelopmentSourceAdmissionBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		edit             func(*SourcePolicy)
	}{
		{name: "production policy", edit: func(p *SourcePolicy) { p.Development = false }},
		{name: "production source", edit: func(p *SourcePolicy) { p.Workflow = ".github/workflows/nvkr-run.yml" }},
		{name: "main", key: "GITHUB_REF", value: "refs/heads/main"},
		{name: "tag", key: "GITHUB_REF", value: "refs/tags/v1"},
		{name: "production qualifier", key: "GITHUB_WORKFLOW_REF", value: "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/feature/qualification"},
		{name: "workflow run", key: "GITHUB_EVENT_NAME", value: "workflow_run"},
		{name: "fork repo", key: "GITHUB_REPOSITORY", value: "fork/nvkr"},
		{name: "not actions", key: "GITHUB_ACTIONS", value: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, _, _ := developmentOriginFixture(t)
			if test.edit != nil {
				test.edit(&policy)
			} else {
				t.Setenv(test.key, test.value)
			}
			t.Setenv("GITHUB_EVENT_PATH", "missing-event")
			source, err := noTriggeredProviderIO(t).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, e.CodeTrustPolicyMissing)
			if source != nil {
				t.Fatal("refused admission issued source")
			}
		})
	}
	request, _, _ := sourceFixture()
	request.Branch = "feature/qualification"
	_, err := noTriggeredProviderIO(t).AuthenticateSource(t.Context(), request)
	assertCode(t, err, e.CodeEngineCapability)
}

func TestDevelopmentInvocationIndependentProviderAuthentication(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*runResponse)
		code e.Code
	}{
		{name: "active manual qualifier"},
		{"production workflow", func(r *runResponse) { r.Path = ".github/workflows/nvkr-qualify.yml" }, e.CodeVerifyFailed},
		{"other branch", func(r *runResponse) { r.HeadBranch = "feature/other" }, e.CodeVerifyFailed},
		{"workflow run", func(r *runResponse) { r.Event = "workflow_run" }, e.CodeVerifyFailed},
		{"completed", func(r *runResponse) { r.Status = "completed"; r.Conclusion = "success" }, e.CodeVerifyFailed},
		{"other commit", func(r *runResponse) { r.HeadSHA = strings.Repeat("c", 40) }, e.CodeVerifyFailed},
		{"fork", func(r *runResponse) { r.HeadRepository.ID = "456" }, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, repo, run := developmentOriginFixture(t)
			source, err := fakeGitHub(t, repo, run).AuthenticateTriggeredSource(t.Context(), policy)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			run.ID = "9876"
			run.Attempt = "1"
			run.HeadSHA = os.Getenv("GITHUB_SHA")
			run.Path = development.QualificationWorkflow
			run.Status = "in_progress"
			run.Conclusion = ""
			if test.edit != nil {
				test.edit(&run)
			}
			g := NewGitHub("")
			g.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if _, ok := request.Context().Deadline(); !ok {
					t.Fatal("unbounded provider request")
				}
				var value any
				switch request.URL.Path {
				case "/repos/example/nvkr":
					value = repo
				case "/repos/example/nvkr/actions/runs/9876/attempts/1":
					value = run
				default:
					t.Fatalf("wrong endpoint %s", request.URL.Path)
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})
			invocation, err := g.AuthenticateInvocation(t.Context(), source, development.QualificationWorkflow)
			if test.code != "" {
				assertCode(t, err, test.code)
				if invocation != nil {
					t.Fatal("refused invocation issued receipt")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = invocation.Close() }()
			description, err := invocation.Description()
			if err != nil {
				t.Fatal(err)
			}
			if description.Branch != run.HeadBranch || description.Workflow != development.QualificationWorkflow || description.RunID != "9876" {
				t.Fatalf("wrong invocation %+v", description)
			}
			origin, _ := source.Description()
			if err := invocation.RequireSource(origin); err != nil {
				t.Fatal(err)
			}
			_ = source.Close()
			_, err = invocation.Description()
			assertCode(t, err, e.CodeProvenanceMissing)
		})
	}
}

func TestDevelopmentDispatchRequiresExactCanonicalLocator(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		code      e.Code
	}{
		{"missing attempt", `{"inputs":{"source_run_url":"https://github.com/example/nvkr/actions/runs/1234"}}`, e.CodeUsage},
		{"foreign repository", `{"inputs":{"source_run_url":"https://github.com/fork/nvkr/actions/runs/1234/attempts/2"}}`, e.CodeUntrustedSigner},
		{"duplicate inputs", `{"inputs":{},"inputs":{}}`, e.CodeVerifyFailed},
		{"missing input", `{"inputs":{}}`, e.CodeUsage},
		{"extra document", `{} {}`, e.CodeVerifyFailed},
		{"wrong input type", `{"inputs":{"source_run_url":1234}}`, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, _, _ := developmentOriginFixture(t)
			if err := os.WriteFile(os.Getenv("GITHUB_EVENT_PATH"), []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := noTriggeredProviderIO(t).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, test.code)
			if source != nil {
				t.Fatal("invalid dispatch issued source authority")
			}
		})
	}
}

func TestDevelopmentInvocationRejectsChangedSelectionBeforeProviderIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T)
		code   e.Code
	}{
		{"changed source attempt", func(t *testing.T) {
			t.Helper()
			raw := []byte(`{"inputs":{"source_run_url":"https://github.com/example/nvkr/actions/runs/1234/attempts/3"}}`)
			if err := os.WriteFile(os.Getenv("GITHUB_EVENT_PATH"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"same invocation", func(t *testing.T) { t.Setenv("GITHUB_RUN_ID", "1234") }, e.CodeTrustPolicyMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, repo, run := developmentOriginFixture(t)
			source, err := fakeGitHub(t, repo, run).AuthenticateTriggeredSource(t.Context(), policy)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			test.change(t)
			invocation, err := noTriggeredProviderIO(t).AuthenticateInvocation(t.Context(), source, development.QualificationWorkflow)
			assertCode(t, err, test.code)
			if invocation != nil {
				t.Fatal("changed selection issued invocation authority")
			}
		})
	}
}
