// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestQualificationInvocationAuthenticatesIndependentRunAndExactSourceEvent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		eventEdit        func(*runResponse)
		runEdit          func(*runResponse)
		envKey, envValue string
		code             e.Code
		closeAction      string
	}{
		{name: "independent invocation"},
		{name: "canceled after provider response", closeAction: "cancel", code: e.CodeVerifyFailed},
		{name: "source revoked after provider response", closeAction: "revoke", code: e.CodeProvenanceMissing},
		{name: "wrong source attempt", eventEdit: func(r *runResponse) { r.Attempt = "1" }, code: e.CodeVerifyFailed},
		{name: "wrong source commit", eventEdit: func(r *runResponse) { r.HeadSHA = strings.Repeat("b", 40) }, code: e.CodeVerifyFailed},
		{name: "failed source", eventEdit: func(r *runResponse) { r.Conclusion = "failure" }, code: e.CodeVerifyFailed},
		{name: "wrong qualification SHA", runEdit: func(r *runResponse) { r.HeadSHA = strings.Repeat("c", 40) }, code: e.CodeVerifyFailed},
		{name: "completed qualification", runEdit: func(r *runResponse) { r.Status = "completed"; r.Conclusion = "success" }, code: e.CodeVerifyFailed},
		{name: "manual qualification", envKey: "GITHUB_EVENT_NAME", envValue: "workflow_dispatch", code: e.CodeTrustPolicyMissing},
		{name: "source run reused", envKey: "GITHUB_RUN_ID", envValue: "1234", code: e.CodeTrustPolicyMissing},
		{name: "untrusted ref", envKey: "GITHUB_REF", envValue: "refs/pull/2/merge", code: e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, repo, sourceEvent := sourceFixture()
			source := &SourceRun{description: describe(request), closed: new(atomic.Bool)}
			const workflow = ".github/workflows/qualification.yml"
			for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": request.Repository, "GITHUB_EVENT_NAME": "workflow_run", "GITHUB_REF": "refs/heads/main", "GITHUB_RUN_ID": "2345", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": strings.Repeat("d", 40), "GITHUB_WORKFLOW_REF": request.Repository + "/" + workflow + "@refs/heads/main"} {
				t.Setenv(key, value)
			}
			qualifier := sourceEvent
			qualifier.ID = "2345"
			qualifier.Attempt = "1"
			qualifier.Path = workflow
			qualifier.HeadSHA = strings.Repeat("d", 40)
			qualifier.Event = "workflow_run"
			qualifier.Status = "in_progress"
			qualifier.Conclusion = ""
			if tc.eventEdit != nil {
				tc.eventEdit(&sourceEvent)
			}
			if tc.runEdit != nil {
				tc.runEdit(&qualifier)
			}
			if tc.envKey != "" {
				t.Setenv(tc.envKey, tc.envValue)
			}
			raw, err := json.Marshal(map[string]any{"action": "completed", "workflow_run": sourceEvent})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_EVENT_PATH", path)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			g := NewGitHub("")
			g.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var value any
				switch r.URL.Path {
				case "/repos/example/nvkr":
					value = repo
				case "/repos/example/nvkr/actions/runs/2345/attempts/1":
					value = qualifier
				default:
					t.Fatalf("wrong lookup: %s", r.URL)
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				body := io.NopCloser(strings.NewReader(string(raw)))
				if strings.Contains(r.URL.Path, "/actions/runs/") && tc.closeAction != "" {
					body = invocationCloseBody{ReadCloser: body, after: func() {
						if tc.closeAction == "cancel" {
							cancel()
						} else {
							_ = source.Close()
						}
					}}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
			})
			receipt, err := g.AuthenticateInvocation(ctx, source, workflow)
			if tc.code != "" {
				if receipt != nil {
					t.Fatal("invalid invocation has receipt")
				}
				assertCode(t, err, tc.code)
				if tc.closeAction == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation cause: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := receipt.RequireSource(source.description); err != nil {
				t.Fatal(err)
			}
			wrong := source.description
			wrong.Attempt = "3"
			assertCode(t, receipt.RequireSource(wrong), e.CodeVerifyFailed)
			copy := *receipt
			_ = receipt.Close()
			_, err = copy.Description()
			assertCode(t, err, e.CodeProvenanceMissing)
		})
	}
}

func TestSourceAuthenticatorOwnsTransport(t *testing.T) {
	g := NewGitHub("")
	transport, ok := g.client.Transport.(*http.Transport)
	if !ok || transport == http.DefaultTransport || transport.Proxy != nil || transport.DialContext == nil || transport.ResponseHeaderTimeout == 0 {
		t.Fatal("source authority inherits replaceable global transport or unbounded IO")
	}
}

type invocationCloseBody struct {
	io.ReadCloser
	after func()
}

func (body invocationCloseBody) Close() error {
	err := body.ReadCloser.Close()
	body.after()
	return err
}
