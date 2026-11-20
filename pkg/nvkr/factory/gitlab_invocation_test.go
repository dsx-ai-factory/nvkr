// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	provider "github.com/dsx-ai-factory/nvkr/pkg/nvkr/gitlab"
	yaml "go.yaml.in/yaml/v3"
)

func TestGitLabWorkflowInvocationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		refs, sources []string
		publication   bool
		condition     string
		deduplicate   bool
	}{
		{name: "default branch and default sources", condition: `($CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH) && ($CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "web")`},
		{name: "exact extra ref and api source", refs: []string{"release/next"}, sources: []string{"api"}, condition: `($CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH || $CI_COMMIT_REF_NAME == "release/next") && ($CI_PIPELINE_SOURCE == "api")`},
		{name: "explicit merge requests deduplicate push", sources: []string{"merge_request_event", "push"}, deduplicate: true, condition: `($CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH) && ($CI_PIPELINE_SOURCE == "merge_request_event" || $CI_PIPELINE_SOURCE == "push")`},
		{name: "all publication requires protected branch", publication: true, condition: `($CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH) && ($CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "web")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, plan := gitlabFixture(t, 1)
			policy := plan.Environment.GitLab
			policy.AdditionalRefs = tc.refs
			policy.PipelineSources = tc.sources
			if tc.publication {
				plan.Environment.Publication = &authored.Publication{RepositoryPrefix: "registry.example.com/project", Anonymous: true}
			}
			rules := gitlabWorkflowRules(plan)
			if rules[0]["when"] != "never" || rules[1]["if"] != `$CI_COMMIT_TAG` || rules[1]["when"] != "never" {
				t.Fatal("missing debug or tag denial")
			}
			index := 2
			if tc.publication {
				if !reflect.DeepEqual(rules[index], map[string]any{"if": `$CI_COMMIT_REF_PROTECTED != "true"`, "when": "never"}) {
					t.Fatal("publication was not protected before allow rule")
				}
				index++
			}
			if tc.deduplicate {
				if !reflect.DeepEqual(rules[index], map[string]any{"if": `$CI_PIPELINE_SOURCE == "push" && $CI_OPEN_MERGE_REQUESTS`, "when": "never"}) {
					t.Fatal("missing explicitly selected MR deduplication")
				}
				index++
			}
			if !reflect.DeepEqual(rules[index], map[string]any{"if": tc.condition, "when": "always"}) || !reflect.DeepEqual(rules[index+1], map[string]any{"when": "never"}) || len(rules) != index+2 {
				t.Fatalf("unexpected allow/final rule: %#v", rules)
			}
			// The exact native projection, not an independently authored policy file,
			// must carry the policy used by runtime admission.
			if !tc.publication {
				writeSnapshotFixtureDocument(t, root, snapshotEnvironment, plan.Environment)
				captured, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, plan.GeneratorVersion)
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := RenderGitLab(t.Context(), captured)
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					Workflow struct {
						Rules []map[string]any `yaml:"rules"`
					} `yaml:"workflow"`
				}
				if err := yaml.Unmarshal(bundle.Files[GitLabWorkflowPath], &document); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(document.Workflow.Rules, rules) {
					t.Fatal("renderer dropped invocation rules")
				}
			}
		})
	}
}

func TestGitLabInvocationAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, ref, source, defaultBranch string
		refs, sources                    []string
		allowed                          bool
	}{
		{name: "default push", ref: "main", source: "push", defaultBranch: "main", sources: []string{"push", "web"}, allowed: true},
		{name: "default web", ref: "main", source: "web", defaultBranch: "main", sources: []string{"push", "web"}, allowed: true},
		{name: "unlisted ref", ref: "feature/x", source: "push", defaultBranch: "main", sources: []string{"push", "web"}},
		{name: "explicit ref and source", ref: "feature/x", source: "api", defaultBranch: "main", refs: []string{"feature/x"}, sources: []string{"api"}, allowed: true},
		{name: "unlisted source", ref: "main", source: "merge_request_event", defaultBranch: "main", sources: []string{"push", "web"}},
		{name: "unsafe default", ref: "main", source: "push", defaultBranch: "refs/heads/main", refs: []string{"main"}, sources: []string{"push", "web"}},
		{name: "unsafe selected ref", ref: "feature$REF", source: "push", defaultBranch: "main", refs: []string{"feature$REF"}, sources: []string{"push", "web"}},
		{name: "missing default", ref: "main", source: "push", sources: []string{"push", "web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitGitLabInvocation(provider.Identity{Ref: tc.ref, Source: tc.source}, tc.defaultBranch, tc.refs, tc.sources)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			}
		})
	}
}

func TestGitLabJobRequiresInvocationBeforeGeneratedWork(t *testing.T) {
	_, plan := gitlabFixture(t, 1)
	for _, tc := range []struct{ ref, source string }{{"", "push"}, {"main", ""}, {"main\n", "push"}} {
		owner, err := AdmitGitLabFinalizer(t.Context(), plan, GitLabJobContext{ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40), Ref: tc.ref, Source: tc.source})
		assertFactoryErrorCode(t, err, e.CodeUsage)
		if owner != nil {
			t.Fatal("invalid invocation issued an admission")
		}
	}
}

func TestGitLabInvocationRejectsRevokedOwners(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, owner := range []*GitLabFinalizer{nil, {}, {state: &gitlabAdmission{closed: true}}, {state: &gitlabAdmission{lifetime: canceled}}} {
		identity, err := owner.Invocation()
		assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
		if identity != (provider.Identity{}) {
			t.Fatal("revoked owner exposed an invocation")
		}
	}
}

func TestGitLabInvocationKeepsAuthenticatedPolicyAcrossRefresh(t *testing.T) {
	var changed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipeline := map[string]any{"id": 2, "project_id": 123, "sha": strings.Repeat("a", 40), "ref": "main", "source": "push"}
		self := map[string]any{"id": 3, "name": gitlabFinalizerJob, "status": "running", "tag": false, "ref": "main", "commit": map[string]any{"id": strings.Repeat("a", 40)}, "pipeline": pipeline}
		switch r.URL.Path {
		case "/api/v4/projects/123":
			branch := "main"
			if changed.Load() {
				branch = "release"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "ci_config_path": nil, "default_branch": branch})
		case "/api/v4/projects/123/pipelines/2":
			_ = json.NewEncoder(w).Encode(pipeline)
		case "/api/v4/job":
			_ = json.NewEncoder(w).Encode(self)
		case "/api/v4/projects/123/pipelines/2/jobs":
			w.Header().Set("X-Next-Page", "")
			_ = json.NewEncoder(w).Encode([]any{self})
		default:
			t.Errorf("unexpected provider work: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	config := provider.Config{APIURL: server.URL + "/api/v4", ProjectID: 123, JobToken: "synthetic-job", ReadCredential: "synthetic-read", ReadCredentialKind: "private-token", CertificateAuthority: ca}
	client, err := provider.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	identity := provider.Identity{APIURL: config.APIURL, ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40), Ref: "main", Source: "push"}
	receipt, err := client.Current(t.Context(), identity, []string{gitlabFinalizerJob})
	if err != nil {
		t.Fatal(err)
	}
	state := &gitlabAdmission{lifetime: t.Context(), client: client, identity: receipt.CurrentIdentity(), receipt: receipt, names: []string{gitlabFinalizerJob}, defaultBranch: receipt.DefaultBranch(), pipelineSources: []string{"push", "web"}}
	owner := &GitLabFinalizer{state: state}
	t.Cleanup(func() { _ = owner.Close() })
	got, err := owner.Invocation()
	if err != nil || got != identity {
		t.Fatalf("authenticated invocation copy: %+v,%v", got, err)
	}
	got.Ref = "caller-mutated"
	again, err := owner.Invocation()
	if err != nil || again.Ref != "main" {
		t.Fatal("caller changed owner through descriptive copy")
	}
	state.mu.Lock()
	_, err = state.current(t.Context())
	state.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	state.mu.Lock()
	_, err = state.current(t.Context())
	state.mu.Unlock()
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = owner.Invocation()
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	if got != (provider.Identity{}) {
		t.Fatal("closed owner retained an invocation")
	}
}

func TestGitLabRequiresNativeTokenFreeCheckoutBeforeGeneratedWork(t *testing.T) {
	_, plan := gitlabFixture(t, 1)
	for _, value := range []string{"", "false", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("FF_GIT_URLS_WITHOUT_TOKENS", value)
			owner, err := AdmitGitLabFinalizer(t.Context(), plan, GitLabJobContext{ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40), Ref: "main", Source: "push"})
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if owner != nil {
				t.Fatal("override issued an admission before checkout guard")
			}
		})
	}
}

func TestGitLabCompileSnapshotRequiresLiveContext(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil", nil, e.CodeUsage}, {"canceled", canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFactoryErrorCode(t, VerifyGitLabCompileSnapshot(tc.ctx, strings.Repeat("a", 40), InputSnapshot{}), tc.code)
		})
	}
}
