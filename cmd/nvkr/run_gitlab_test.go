// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestGitLabGenerateRefusesGitHubOptionsBeforePluginOrInputWork(t *testing.T) {
	previous := selectProjectionPilot
	selected := 0
	selectProjectionPilot = func(ctx context.Context, _ string, _ string) (context.Context, error) {
		selected++
		return ctx, e.New(e.CodeInternal, "GitLab must not select a GitHub projection")
	}
	t.Cleanup(func() { selectProjectionPilot = previous })
	for _, engine := range []string{"gitlab", " GitLab "} {
		for _, check := range []bool{false, true} {
			for _, option := range []string{
				"--runtime", "--source-commit=" + strings.Repeat("a", 40),
				"--projection-pilot=in-process", "--projection-pilot=out-of-process",
				"--qualification-policy=missing-policy.yaml",
			} {
				t.Run(engine+"/"+option+"/check="+strconv.FormatBool(check), func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "must-not-exist")
					argv := []string{"run", "generate", "--engine", engine,
						"--definition", "missing-definition.yaml", "--environment", "missing-environment.yaml", "--root", root, option}
					if check {
						argv = append(argv, "--check")
					}
					_, err := invokeLifecycleCLI(t, argv...)
					assertLifecycleCommandCode(t, err, e.CodeEngineCapability)
					if !strings.Contains(err.Error(), "GitLab generation does not support --") {
						t.Fatalf("unexpected admission owner: %v", err)
					}
					if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
						t.Fatalf("unsupported GitLab selection touched output: %v", statErr)
					}
				})
			}
		}
	}
	if selected != 0 {
		t.Fatalf("GitLab invoked GitHub plugin selection %d times", selected)
	}
}

func TestGitLabNativeContextUsesOnlyCanonicalNativeDescriptions(t *testing.T) {
	base := map[string]string{"CI_PROJECT_ID": "7", "CI_PIPELINE_ID": "23", "CI_JOB_ID": "31", "CI_COMMIT_SHA": strings.Repeat("a", 40)}
	job, err := gitLabJobFromEnvironment(func(key string) string {
		if strings.HasPrefix(key, "GITHUB_") || strings.Contains(key, "TOKEN") {
			t.Fatalf("native context parser read unrelated identity or credential %s", key)
		}
		return base[key]
	})
	want := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 31, Commit: strings.Repeat("a", 40)}
	if err != nil || !reflect.DeepEqual(job, want) {
		t.Fatalf("native context = %+v, error %v", job, err)
	}
	for _, key := range []string{"CI_PROJECT_ID", "CI_PIPELINE_ID", "CI_JOB_ID", "CI_COMMIT_SHA"} {
		values := []string{"", "0", "-1", "+1", "01", " 1", "1\n", "18446744073709551616"}
		if key == "CI_COMMIT_SHA" {
			values = []string{"", strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 40) + "\n"}
		}
		for _, value := range values {
			t.Run(key+"/"+strconv.Quote(value), func(t *testing.T) {
				got, err := gitLabJobFromEnvironment(func(requested string) string {
					if requested == key {
						return value
					}
					return base[requested]
				})
				assertLifecycleCommandCode(t, err, e.CodeUsage)
				if !reflect.DeepEqual(got, factory.GitLabJobContext{}) {
					t.Fatal("refused native context leaked a partial selection")
				}
			})
		}
	}
}

func TestGitLabCommandsDoNotFallbackToGitHubContext(t *testing.T) {
	for _, key := range []string{"CI_PROJECT_ID", "CI_PIPELINE_ID", "CI_JOB_ID", "CI_COMMIT_SHA"} {
		t.Setenv(key, "")
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_RUN_ID", "23")
	t.Setenv("GITHUB_RUN_ATTEMPT", "31")
	t.Setenv("GITHUB_SHA", strings.Repeat("a", 40))
	for _, command := range [][]string{
		{"factory", "gitlab", "run-node", "--plan", "missing-plan.json", "--node", "build-example"},
		{"run", "finalize-gitlab", "--definition", "missing-definition.yaml", "--environment", "missing-environment.yaml", "--plan", "missing-plan.json"},
	} {
		t.Run(strings.Join(command[:2], "/"), func(t *testing.T) {
			_, err := invokeLifecycleCLI(t, command...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if !strings.Contains(err.Error(), "canonical CI_PROJECT_ID") {
				t.Fatalf("native identity was not admitted before Plan/input work: %v", err)
			}
		})
	}
}

func TestGitLabCatalogMaintenanceRefusesBeforeWorkflowWork(t *testing.T) {
	for _, engine := range []string{"gitlab", " GitLab "} {
		_, err := invokeLifecycleCLI(t, "catalog", "update-ci", "--engine", engine,
			"--timeout=0", "--source-checkout=not-a-credential-reference", "--app-secrets=not-an-app-reference")
		assertLifecycleCommandCode(t, err, e.CodeEngineCapability)
		// The provider-specific message proves this is the maintenance owner,
		// rather than the former global rejection of all GitLab generation.
		if !strings.Contains(err.Error(), "catalog maintenance supports only the GitHub workflow provider") {
			t.Fatalf("GitLab reached unsupported maintenance work: %v", err)
		}
	}
	for engine, code := range map[string]e.Code{"tekton": e.CodeEngineCapability, "argo": e.CodeEngineCapability, "jenkins": e.CodeUsage, "local": e.CodeUsage} {
		_, err := invokeLifecycleCLI(t, "catalog", "update-ci", "--engine", engine, "--timeout=0")
		assertLifecycleCommandCode(t, err, code)
	}
}
