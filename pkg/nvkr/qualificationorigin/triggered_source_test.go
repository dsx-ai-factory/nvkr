// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func triggeredEnvironment(t *testing.T) (SourcePolicy, runResponse) {
	t.Helper()
	request, _, run := sourceFixture()
	for key, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_EVENT_NAME": "workflow_run",
		"GITHUB_REPOSITORY": request.Repository, "GITHUB_REF": "refs/heads/" + request.DefaultBranch,
		"GITHUB_SHA": strings.Repeat("b", 40), "GITHUB_RUN_ID": "9876", "GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_EVENT_PATH": filepath.Join(t.TempDir(), "event.json"),
	} {
		t.Setenv(key, value)
	}
	return SourcePolicy{Repository: request.Repository, Workflow: request.Workflow, DefaultBranch: request.DefaultBranch}, run
}

func writeTriggeredEvent(t *testing.T, action string, run runResponse) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Action string      `json:"action"`
		Run    runResponse `json:"workflow_run"`
	}{action, run})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("GITHUB_EVENT_PATH"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func noTriggeredProviderIO(t *testing.T) *GitHub {
	t.Helper()
	g := NewGitHub("")
	g.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("refused triggered source performed provider I/O")
		return nil, nil
	})
	return g
}

func TestTriggeredSourceUsesEventCommitUnderIndependentPolicy(t *testing.T) {
	for _, event := range []string{"push", "workflow_dispatch"} {
		t.Run(event, func(t *testing.T) {
			policy, run := triggeredEnvironment(t)
			request, repository, _ := sourceFixture()
			run.Event = event
			// Workflow event nested repository objects may omit this API field.
			run.Repository.DefaultBranch, run.HeadRepository.DefaultBranch = "", ""
			writeTriggeredEvent(t, "completed", run)
			source, err := fakeGitHub(t, repository, run).AuthenticateTriggeredSource(t.Context(), policy)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			description, err := source.Description()
			if err != nil || description != describe(request) || description.Commit == os.Getenv("GITHUB_SHA") {
				t.Fatalf("source confused qualifier identity: %+v, %v", description, err)
			}
		})
	}
}

func TestTriggeredSourceAdmissionPrecedesEventAndProviderIO(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		policy           func(*SourcePolicy)
	}{
		{name: "missing policy", policy: func(p *SourcePolicy) { *p = SourcePolicy{} }},
		{name: "unapproved workflow", policy: func(p *SourcePolicy) { p.Workflow = "../build.yml" }},
		{name: "missing actions", key: "GITHUB_ACTIONS", value: ""},
		{name: "false actions", key: "GITHUB_ACTIONS", value: "false"},
		{name: "foreign server", key: "GITHUB_SERVER_URL", value: "https://github.example"},
		{name: "manual qualifier", key: "GITHUB_EVENT_NAME", value: "workflow_dispatch"},
		{name: "pull request", key: "GITHUB_EVENT_NAME", value: "pull_request"},
		{name: "foreign repository", key: "GITHUB_REPOSITORY", value: "fork/nvkr"},
		{name: "nondefault ref", key: "GITHUB_REF", value: "refs/heads/feature"},
		{name: "tag ref", key: "GITHUB_REF", value: "refs/tags/main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, _ := triggeredEnvironment(t) // The selected event file does not exist.
			if test.policy != nil {
				test.policy(&policy)
			} else {
				t.Setenv(test.key, test.value)
			}
			source, err := noTriggeredProviderIO(t).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, e.CodeTrustPolicyMissing)
			if source != nil {
				t.Fatal("refused admission issued source")
			}
		})
	}
	policy, _ := triggeredEnvironment(t)
	g := noTriggeredProviderIO(t)
	_, err := g.AuthenticateTriggeredSource(nil, policy) //nolint:staticcheck // Nil context must fail before event I/O.
	assertCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = g.AuthenticateTriggeredSource(ctx, policy)
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestTriggeredSourceRejectsInvalidEventBeforeProviderIO(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*string, *runResponse)
	}{
		{"unfinished action", func(a *string, _ *runResponse) { *a = "requested" }},
		{"missing run", func(_ *string, r *runResponse) { r.ID = "" }},
		{"zero attempt", func(_ *string, r *runResponse) { r.Attempt = "0" }},
		{"noncommit", func(_ *string, r *runResponse) { r.HeadSHA = "main" }},
		{"workflow", func(_ *string, r *runResponse) { r.Path = ".github/workflows/other.yml" }},
		{"branch", func(_ *string, r *runResponse) { r.HeadBranch = "feature" }},
		{"repository", func(_ *string, r *runResponse) { r.Repository.FullName = "other/nvkr" }},
		{"missing repository id", func(_ *string, r *runResponse) { r.Repository.ID = "" }},
		{"fork identity", func(_ *string, r *runResponse) { r.HeadRepository.ID = "456" }},
		{"fork name", func(_ *string, r *runResponse) { r.HeadRepository.FullName = "fork/nvkr" }},
		{"failed", func(_ *string, r *runResponse) { r.Conclusion = "failure" }},
		{"not completed", func(_ *string, r *runResponse) { r.Status = "in_progress" }},
		{"pull request", func(_ *string, r *runResponse) { r.Event = "pull_request" }},
		{"workflow call", func(_ *string, r *runResponse) { r.Event = "workflow_call" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, run := triggeredEnvironment(t)
			action := "completed"
			test.edit(&action, &run)
			writeTriggeredEvent(t, action, run)
			source, err := noTriggeredProviderIO(t).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, e.CodeVerifyFailed)
			if source != nil {
				t.Fatal("invalid event issued source")
			}
		})
	}
}

func TestTriggeredSourceReauthenticatesEventAgainstExactProviderAttempt(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*repositoryResponse, *runResponse)
	}{
		{"wrong run", func(_ *repositoryResponse, r *runResponse) { r.ID = "1235" }},
		{"wrong attempt", func(_ *repositoryResponse, r *runResponse) { r.Attempt = "1" }},
		{"wrong commit", func(_ *repositoryResponse, r *runResponse) { r.HeadSHA = strings.Repeat("b", 40) }},
		{"wrong workflow", func(_ *repositoryResponse, r *runResponse) { r.Path = ".github/workflows/other.yml" }},
		{"wrong default branch", func(r *repositoryResponse, _ *runResponse) { r.DefaultBranch = "trunk" }},
		{"fork", func(_ *repositoryResponse, r *runResponse) { r.HeadRepository.ID = "456" }},
		{"failed", func(_ *repositoryResponse, r *runResponse) { r.Conclusion = "failure" }},
		{"pull request", func(_ *repositoryResponse, r *runResponse) { r.Event = "pull_request" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, event := triggeredEnvironment(t)
			_, repository, run := sourceFixture()
			writeTriggeredEvent(t, "completed", event)
			test.edit(&repository, &run)
			source, err := fakeGitHub(t, repository, run).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, e.CodeVerifyFailed)
			if source != nil {
				t.Fatal("provider mismatch issued source")
			}
		})
	}
}

func TestTriggeredSourceEventFileIsBoundedAndStrict(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		missing   bool
		code      e.Code
	}{
		{name: "missing file", missing: true, code: e.CodeProvenanceMissing},
		{name: "empty file", raw: "", code: e.CodeVerifyFailed},
		{name: "duplicate JSON", raw: `{"action":"completed","action":"requested"}`, code: e.CodeVerifyFailed},
		{name: "extra document", raw: `{} {}`, code: e.CodeVerifyFailed},
		{name: "wrong type", raw: `{"workflow_run":[]}`, code: e.CodeVerifyFailed},
		{name: "oversized", raw: strings.Repeat(" ", maxResponseBytes+1), code: e.CodeProvenanceMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, _ := triggeredEnvironment(t)
			if !test.missing {
				if err := os.WriteFile(os.Getenv("GITHUB_EVENT_PATH"), []byte(test.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			source, err := noTriggeredProviderIO(t).AuthenticateTriggeredSource(t.Context(), policy)
			assertCode(t, err, test.code)
			if source != nil {
				t.Fatal("invalid event file issued source")
			}
		})
	}
}

type triggeredCancelBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (body triggeredCancelBody) Close() error { body.cancel(); return nil }

func TestTriggeredSourceCancellationAtProviderCompletionIssuesNoReceipt(t *testing.T) {
	policy, run := triggeredEnvironment(t)
	_, repository, _ := sourceFixture()
	writeTriggeredEvent(t, "completed", run)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g := NewGitHub("")
	g.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var value any = repository
		if strings.Contains(request.URL.Path, "/actions/runs/") {
			value = run
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body := io.NopCloser(strings.NewReader(string(raw)))
		if strings.Contains(request.URL.Path, "/actions/runs/") {
			body = triggeredCancelBody{Reader: strings.NewReader(string(raw)), cancel: cancel}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	source, err := g.AuthenticateTriggeredSource(ctx, policy)
	assertCode(t, err, e.CodeVerifyFailed)
	if source != nil {
		t.Fatal("canceled provider authentication returned source authority")
	}
}
