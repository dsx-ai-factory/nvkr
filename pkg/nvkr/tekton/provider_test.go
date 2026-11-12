// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func providerFixture(t *testing.T, edit func(*pipelineRun, *taskRun), responses ...func(*pipelineRun)) (*Provider, TaskExpectation, []byte) {
	t.Helper()
	const runUID = "11111111-1111-1111-1111-111111111111"
	const taskUID = "22222222-2222-2222-2222-222222222222"
	plan := "sha256:" + strings.Repeat("a", 64)
	spec := json.RawMessage(`{"steps":[{"name":"execute","image":"example.invalid/runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","command":["nvkr","factory","tekton","execute"]}]}`)
	pipeline := json.RawMessage(`{"tasks":[{"name":"build","taskSpec":` + string(spec) + `}]}`)
	metadata := func(name, uid string) objectMetadata {
		return objectMetadata{Name: name, UID: uid, Namespace: "fixture", Annotations: map[string]string{PlanAnnotation: plan}}
	}
	run := pipelineRun{APIVersion: "tekton.dev/v1", Kind: "PipelineRun", Metadata: metadata("run", runUID)}
	run.Spec.PipelineSpec = pipeline
	run.Spec.TaskRunTemplate = json.RawMessage(`{"serviceAccountName":"builder"}`)
	runSpec, err := json.Marshal(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	run.Status.ChildReferences = append(run.Status.ChildReferences, childReference{
		"tekton.dev/v1", "TaskRun", "run-build", "build",
	})
	task := taskRun{APIVersion: "tekton.dev/v1", Kind: "TaskRun", Metadata: metadata("run-build", taskUID)}
	task.Metadata.Labels = map[string]string{"tekton.dev/pipelineTask": "build", "tekton.dev/pipelineRun": "run"}
	task.Metadata.OwnerReferences = append(task.Metadata.OwnerReferences, controllerOwner{"tekton.dev/v1", "PipelineRun", "run", runUID, true})
	task.Spec.TaskSpec, task.Spec.ServiceAccountName = spec, "builder"
	task.Spec.Params = []nameValue{{"run-name", "run"}, {"run-uid", runUID}}
	task.Status.Conditions = append(task.Status.Conditions, condition{"Succeeded", "True", "Succeeded"})
	task.Status.CompletionTime = "2026-11-09T20:00:00Z"
	carrier := []byte("synthetic exact carrier")
	digest := sha256.Sum256(carrier)
	task.Status.Results = append(task.Status.Results, nameValue{"carrier", "sha256:" + hex.EncodeToString(digest[:])})
	if edit != nil {
		edit(&run, &task)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("request did not use the explicitly scoped credential and read method")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/tekton.dev/v1/namespaces/fixture/pipelineruns/run":
			response := run
			response.Status.ChildReferences = append([]childReference(nil), run.Status.ChildReferences...)
			for _, respond := range responses {
				respond(&response)
			}
			_ = json.NewEncoder(w).Encode(response)
		case "/apis/tekton.dev/v1/namespaces/fixture/taskruns/run-build":
			_ = json.NewEncoder(w).Encode(task)
		default:
			t.Errorf("unexpected request scope %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(ProviderPolicy{Server: server.URL, Namespace: "fixture", RunName: "run", RunUID: runUID,
		PlanDigest: plan, RunSpec: runSpec, Token: "synthetic-test-token",
		CertificateAuthority: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	return provider, TaskExpectation{Name: "build", RunName: "run-build", RunUID: taskUID, ServiceAccount: "builder", Spec: spec}, carrier
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestProviderAuthenticatesExactTaskAndCarrier(t *testing.T) {
	provider, task, carrier := providerFixture(t, nil)
	receipt, err := provider.AuthenticateTask(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt.Invocation(), "/pipelineruns/run?uid="+provider.policy.RunUID) {
		t.Fatal("invocation omitted exact Run identity")
	}
	for _, tc := range []struct {
		name    string
		receipt *TaskReceipt
		result  string
		bytes   []byte
		code    e.Code
	}{
		{"exact carrier", receipt, "carrier", carrier, ""},
		{"corruption", receipt, "carrier", []byte("corrupted"), e.CodeVerifyFailed},
		{"omission", receipt, "carrier", nil, e.CodeVerifyFailed},
		{"wrong result", receipt, "other", carrier, e.CodeVerifyFailed},
		{"zero receipt", &TaskReceipt{}, "carrier", carrier, e.CodeVerifyFailed},
		{"nil receipt", nil, "carrier", carrier, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.receipt.RequireBytes(t.Context(), tc.result, tc.bytes)
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProviderExecutionStateDoesNotGrantArtifactAuthority(t *testing.T) {
	for _, state := range []string{"Unknown", "False"} {
		t.Run(state, func(t *testing.T) {
			provider, expected, carrier := providerFixture(t, func(_ *pipelineRun, task *taskRun) {
				task.Status.Conditions[0].Status = state
				if state == "Unknown" {
					task.Status.CompletionTime = ""
				}
			})
			receipt, err := provider.AuthenticateExecution(t.Context(), expected)
			if err != nil {
				t.Fatal(err)
			}
			want := "failure"
			if state == "Unknown" {
				want = "running"
			}
			if receipt.State() != want {
				t.Fatalf("execution state = %s, want %s", receipt.State(), want)
			}
			// Early or stale result bytes never authorize consumption.
			requireCode(t, receipt.RequireBytes(t.Context(), "carrier", carrier), e.CodeVerifyFailed)
		})
	}
}

func TestProviderRejectsSubstitutedExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*pipelineRun, *taskRun)
		code e.Code
	}{
		{"foreign run", func(p *pipelineRun, _ *taskRun) { p.Metadata.UID = "33333333-3333-3333-3333-333333333333" }, e.CodeVerifyFailed},
		{"wrong plan", func(p *pipelineRun, _ *taskRun) {
			p.Metadata.Annotations[PlanAnnotation] = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeVerifyFailed},
		{"canceled pipeline", func(p *pipelineRun, _ *taskRun) { p.Spec.Status = "CancelledRunFinally" }, e.CodeVerifyFailed},
		{"deleting run", func(p *pipelineRun, _ *taskRun) { p.Metadata.DeletionTimestamp = "2026-11-09T20:00:00Z" }, e.CodeVerifyFailed},
		{"changed pipeline", func(p *pipelineRun, _ *taskRun) { p.Spec.PipelineSpec = json.RawMessage(`{"tasks":[]}`) }, e.CodePipelineDrift},
		{"foreign owner", func(_ *pipelineRun, r *taskRun) {
			r.Metadata.OwnerReferences[0].UID = "33333333-3333-3333-3333-333333333333"
		}, e.CodeVerifyFailed},
		{"wrong namespace", func(_ *pipelineRun, r *taskRun) { r.Metadata.Namespace = "foreign" }, e.CodeVerifyFailed},
		{"wrong account", func(_ *pipelineRun, r *taskRun) { r.Spec.ServiceAccountName = "signer" }, e.CodeVerifyFailed},
		{"wrong task label", func(_ *pipelineRun, r *taskRun) { r.Metadata.Labels["tekton.dev/pipelineTask"] = "foreign" }, e.CodeVerifyFailed},
		{"missing controller child", func(p *pipelineRun, _ *taskRun) { p.Status.ChildReferences = nil }, e.CodeVerifyFailed},
		{"ambiguous controller child", func(p *pipelineRun, _ *taskRun) {
			p.Status.ChildReferences = append(p.Status.ChildReferences, p.Status.ChildReferences[0])
		}, e.CodeVerifyFailed},
		{"substituted Run parameter", func(_ *pipelineRun, r *taskRun) { r.Spec.Params[0].Value = "other" }, e.CodePipelineDrift},
		{"duplicate Run parameter", func(_ *pipelineRun, r *taskRun) { r.Spec.Params[1] = r.Spec.Params[0] }, e.CodePipelineDrift},
		{"substituted command", func(_ *pipelineRun, r *taskRun) { r.Spec.TaskSpec = json.RawMessage(`{"steps":[]}`) }, e.CodePipelineDrift},
		{"injected pod volume", func(_ *pipelineRun, r *taskRun) {
			r.Spec.PodTemplate = json.RawMessage(`{"volumes":[{"name":"foreign","hostPath":{"path":"/"}}]}`)
		}, e.CodePipelineDrift},
		{"overridden timeout", func(_ *pipelineRun, r *taskRun) { r.Spec.Timeout = "24h0m0s" }, e.CodePipelineDrift},
		{"injected workspace", func(_ *pipelineRun, r *taskRun) {
			r.Spec.Workspaces = json.RawMessage(`[{"name":"foreign","emptyDir":{}}]`)
		}, e.CodePipelineDrift},
		{"failure", func(_ *pipelineRun, r *taskRun) { r.Status.Conditions[0].Status = "False" }, e.CodeVerifyFailed},
		{"pending", func(_ *pipelineRun, r *taskRun) { r.Status.Conditions[0].Status = "Unknown" }, e.CodeVerifyFailed},
		{"missing condition", func(_ *pipelineRun, r *taskRun) { r.Status.Conditions = nil }, e.CodeVerifyFailed},
		{"ambiguous condition", func(_ *pipelineRun, r *taskRun) {
			r.Status.Conditions = append(r.Status.Conditions, r.Status.Conditions[0])
		}, e.CodeVerifyFailed},
		{"missing completion", func(_ *pipelineRun, r *taskRun) { r.Status.CompletionTime = "" }, e.CodeVerifyFailed},
		{"retry", func(_ *pipelineRun, r *taskRun) { r.Status.Retries = []json.RawMessage{json.RawMessage(`{}`)} }, e.CodeVerifyFailed},
		{"duplicate result", func(_ *pipelineRun, r *taskRun) { r.Status.Results = append(r.Status.Results, r.Status.Results[0]) }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, expected, _ := providerFixture(t, tc.edit)
			receipt, err := provider.AuthenticateTask(t.Context(), expected)
			requireCode(t, err, tc.code)
			if receipt != nil {
				t.Fatal("failed authentication returned a receipt")
			}
		})
	}
}

func TestProviderCancellationAndAdmission(t *testing.T) {
	provider, expected, carrier := providerFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := provider.AuthenticateTask(ctx, expected)
	requireCode(t, err, e.CodeTimeout)
	requireCode(t, (&TaskReceipt{}).RequireBytes(ctx, "carrier", carrier), e.CodeTimeout)
	for _, tc := range []struct {
		name string
		edit func(*ProviderPolicy)
		code e.Code
	}{
		{"http", func(p *ProviderPolicy) { p.Server = "http://example.invalid" }, e.CodeTrustPolicyMissing},
		{"userinfo", func(p *ProviderPolicy) { p.Server = "https://user@example.invalid" }, e.CodeTrustPolicyMissing},
		{"path", func(p *ProviderPolicy) { p.Server += "/api" }, e.CodeTrustPolicyMissing},
		{"empty query", func(p *ProviderPolicy) { p.Server += "?" }, e.CodeTrustPolicyMissing},
		{"no roots", func(p *ProviderPolicy) { p.CertificateAuthority = nil }, e.CodeTrustPolicyMissing},
		{"no credential", func(p *ProviderPolicy) { p.Token = "" }, e.CodeTrustPolicyMissing},
		{"header injection", func(p *ProviderPolicy) { p.Token = "synthetic\nheader" }, e.CodeTrustPolicyMissing},
		{"path traversal", func(p *ProviderPolicy) { p.Namespace = "../other" }, e.CodeTrustPolicyMissing},
		{"name-only run", func(p *ProviderPolicy) { p.RunUID = "" }, e.CodeTrustPolicyMissing},
		{"duplicate specification", func(p *ProviderPolicy) { p.RunSpec = json.RawMessage(`{"tasks":[],"tasks":[]}`) }, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := provider.policy
			tc.edit(&policy)
			got, err := NewProvider(policy)
			requireCode(t, err, tc.code)
			if got != nil {
				got.Close()
				t.Fatal("invalid trust policy returned a provider")
			}
		})
	}
}

func TestProviderAwaitsOnlyInitialControllerReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"recorded after startup", ""},
		{"never recorded", e.CodeTimeout},
		{"duplicate", e.CodeVerifyFailed},
		{"foreign Run", e.CodeVerifyFailed},
		{"canceled while waiting", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			var revoked atomic.Bool
			provider, expected, _ := providerFixture(t, func(_ *pipelineRun, task *taskRun) {
				task.Status.Conditions[0].Status = "Unknown"
				task.Status.CompletionTime = ""
			}, func(run *pipelineRun) {
				n := reads.Add(1)
				switch tc.name {
				case "duplicate":
					run.Status.ChildReferences = append(run.Status.ChildReferences, run.Status.ChildReferences[0])
				case "foreign Run":
					run.Metadata.UID = "33333333-3333-3333-3333-333333333333"
				default:
					if n == 1 || tc.name == "never recorded" || revoked.Load() {
						run.Status.ChildReferences = nil
					}
					if n > 1 && tc.name == "canceled while waiting" {
						run.Spec.Status = "CancelledRunFinally"
					}
				}
			})
			ctx := t.Context()
			if tc.name == "never recorded" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			receipt, err := provider.AwaitExecution(ctx, expected)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				if receipt != nil {
					t.Fatal("incomplete or invalid binding issued a receipt")
				}
				if (tc.name == "duplicate" || tc.name == "foreign Run") && reads.Load() != 1 {
					t.Fatal("startup retried an authentication failure")
				}
				return
			}
			if err != nil || receipt.State() != "running" || reads.Load() != 2 {
				t.Fatalf("startup did not authenticate the recorded child: %v", err)
			}
			// Once admitted, losing the binding revokes authority immediately.
			revoked.Store(true)
			receipt, err = provider.AuthenticateExecution(t.Context(), expected)
			requireCode(t, err, e.CodeVerifyFailed)
			if receipt != nil || reads.Load() != 3 {
				t.Fatal("active execution delayed or ignored a missing binding")
			}
		})
	}
}
