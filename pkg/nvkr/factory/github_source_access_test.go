// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestNightlyOVNFetchReceivesSelectedSourceCredential(t *testing.T) {
	for _, test := range []struct {
		profile, source string
		private         bool
	}{
		{"nvke-nightly-arm64", "https://github.com/dsx-ai-factory/ovn-kubernetes", true},
		{"nvke-nightly-upstream-ovn-arm64", "https://github.com/ovn-kubernetes/ovn-kubernetes", false},
	} {
		t.Run(test.profile, func(t *testing.T) {
			root := "../../.."
			plan, err := LoadPlanContext(t.Context(), filepath.Join(root, ".github/nvkr-run-"+test.profile, "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			plan.Environment, err = authored.LoadEnvironment(t.Context(), filepath.Join(root, plan.EnvironmentPath))
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			_, nodeJobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, node := range plan.Graph.Nodes {
				if node.Kind != NodeFetch {
					continue
				}
				selected := false
				for _, build := range plan.Builds {
					for _, source := range build.Graph.Nodes {
						selected = selected || build.Name == node.BuildTarget && source.ID == node.IRNode && source.Repo == test.source
					}
				}
				if !selected {
					continue
				}
				found++
				job := document.Jobs[nodeJobs[node.ID]]
				if job.Env[sourceTokenEnv] != "" {
					t.Fatal("source token escaped into the whole job")
				}
				tokenSteps := 0
				for _, step := range job.Steps {
					if step.Env[sourceTokenEnv] == "" {
						continue
					}
					tokenSteps++
					if step.Env[sourceTokenEnv] != "${{ secrets.NKE_SOURCE_TOKEN }}" ||
						step.Env["GIT_CONFIG_KEY_2"] != "credential."+test.source+".helper" {
						t.Fatal("OVN fetch does not receive the selected repository-scoped token")
					}
				}
				want := 0
				if test.private {
					want = 1
				}
				if tokenSteps != want {
					t.Fatalf("OVN source %s has %d credential-bearing steps, want %d", test.source, tokenSteps, want)
				}
			}
			if found == 0 {
				t.Fatal("maintained nightly Plan has no selected OVN source fetch")
			}
		})
	}
}

func authenticatedSourcePlan(t *testing.T) (*Plan, Node, string) {
	t.Helper()
	plan := githubTestPlan(t)
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeFetch {
			continue
		}
		for _, build := range plan.Builds {
			for _, source := range build.Graph.Nodes {
				if build.Name != node.BuildTarget || source.ID != node.IRNode {
					continue
				}
				plan.Environment.SourceAccess = map[string]authored.SourceAccess{
					source.Repo: {Mirror: "https://github.com/test-owner/private-source", CredentialRef: "github:NKE_SOURCE_TOKEN"},
				}
				digest, err := plan.Environment.Digest()
				if err != nil {
					t.Fatal(err)
				}
				plan.EnvironmentDigest = digest
				return plan, node, source.Repo
			}
		}
	}
	t.Fatal("fixture has no source fetch")
	return nil, Node{}, ""
}

func TestGitHubSourceCredentialIsFetchStepOnly(t *testing.T) {
	plan, fetch, _ := authenticatedSourcePlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var workflow githubWorkflowDocument
	if err := yaml.UnmarshalStrict(expandedGitHubWorkflow(t, bundle), &workflow); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, job := range workflow.Jobs {
		if job.Env[sourceTokenEnv] != "" {
			t.Fatal("credential exposed to the whole job")
		}
		for _, step := range job.Steps {
			if step.Env[sourceTokenEnv] == "" {
				continue
			}
			found++
			if job.Name != githubNodeLabel(plan, fetch) || step.ID != githubJobID(fetch.ID) || job.If != "${{ "+githubExecutionCondition+" }}" {
				t.Fatalf("credential exposed outside admitted source execution: %s / %s", job.Name, step.Name)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected one credential-bearing step, got %d", found)
	}
	_, nodeJobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobID := nodeJobs[fetch.ID]
	member, part := githubTestSharedInvocation(t, bundle, jobID, "execute", fetch.ID)
	for _, test := range []struct {
		name   string
		mutate func(*testing.T) *GeneratedBundle
	}{
		{"wrong-secret", func(t *testing.T) *GeneratedBundle {
			return mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
				call := githubTestYAMLStep(t, githubTestYAMLField(t, root, "jobs", jobID, "steps"), "uses", "./"+strings.TrimSuffix(member, "/action.yml"))
				token := githubTestYAMLField(t, call, "with", "nvkr-source-token")
				if token.Value != "${{ secrets.NKE_SOURCE_TOKEN }}" {
					t.Fatal("fixture lost its source credential binding")
				}
				token.Value = "${{ secrets.OTHER_TOKEN }}"
			})
		}},
		{"bypassed-token-check", func(t *testing.T) *GeneratedBundle {
			return mutateGitHubBundleYAML(t, bundle, member, func(root *yamlv3.Node) {
				step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "operation_execute")
				run := githubTestYAMLField(t, step, "run")
				if strings.Count(run.Value, "factory github run-node --plan") != 1 {
					t.Fatal("fixture source execution omits the admitted helper")
				}
				run.Value = strings.Replace(run.Value, "factory github run-node --plan", "factory execute-node --plan", 1)
			})
		}},
		{"wrong-mirror", func(t *testing.T) *GeneratedBundle {
			return mutateGitHubActionParameters(t, bundle, jobID, part, func(values map[string]string) {
				const key = "execute_env_GIT_CONFIG_KEY_0"
				if values[key] != "url.https://github.com/test-owner/private-source.insteadOf" {
					t.Fatal("fixture source execution omits its exact mirror")
				}
				values[key] = "url.https://github.com/other/source.insteadOf"
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := test.mutate(t)
			var coded *e.Error
			if err := ValidateGitHubBundle(context.Background(), plan, changed); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("expected credential isolation rejection, got %v", err)
			}
		})
	}
}

func TestGitHubSourceGitConfigurationScopesCredentialAndMirror(t *testing.T) {
	plan, fetch, canonical := authenticatedSourcePlan(t)
	environment := githubSourceEnvironment(plan, fetch)
	environment[sourceTokenEnv] = "synthetic-test-token"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(input string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = t.TempDir()
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "GIT_") && !strings.HasPrefix(value, sourceTokenEnv+"=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		for _, key := range sortedKeys(environment) {
			cmd.Env = append(cmd.Env, key+"="+environment[key])
		}
		cmd.Stdin = strings.NewReader(input)
		return cmd.CombinedOutput()
	}
	output, err := run("", "ls-remote", "--get-url", canonical)
	if err != nil || strings.TrimSpace(string(output)) != "https://github.com/test-owner/private-source" {
		t.Fatalf("canonical fetch did not select the mirror: %v / %s", err, output)
	}
	for _, test := range []struct {
		url     string
		allowed bool
	}{
		{"https://github.com/test-owner/private-source", true},
		{"https://github.com/test-owner/unrelated", false},
		{"https://example.com/test-owner/private-source", false},
	} {
		t.Run(test.url, func(t *testing.T) {
			output, err := run("url="+test.url+"\n\n", "credential", "fill")
			containsToken := bytes.Contains(output, []byte("password=synthetic-test-token"))
			if test.allowed && (err != nil || !containsToken) {
				t.Fatalf("mirror credential lookup failed: %v", err)
			}
			if !test.allowed && (err == nil || containsToken) {
				t.Fatal("credential released for a different repository or host")
			}
		})
	}
}

func TestGitHubSourceRefusesEmptyCredential(t *testing.T) {
	for _, test := range []struct {
		name, token string
		wantExit    int
	}{
		{"missing", "", 20},
		{"present", "synthetic-test-token", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", requireSourceToken)
			cmd.Env = []string{sourceTokenEnv + "=" + test.token}
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != test.wantExit {
				t.Fatalf("credential admission failed: %v", err)
			}
			if test.wantExit != 0 && !bytes.Contains(output, []byte(e.CodeFetchFailed)) {
				t.Fatal("empty credential rejection lost its stable error code")
			}
		})
	}
}

func TestMaintainedPinSetCatalogCredentials(t *testing.T) {
	testutil.RequireExtended(t)
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	found := map[string]int{}
	bootstraps := 0
	for id, job := range document.Jobs {
		if strings.HasPrefix(id, "build-runner-") {
			bootstraps++
		}
		for key := range job.Env {
			if strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") {
				t.Fatal("job-scoped pin credential")
			}
		}
		for _, step := range job.Steps {
			if step.Env["NVKR_PIN_SOURCE_TOKEN_0"] == "" {
				continue
			}
			found[step.ID]++
			if step.ID != "validate-catalog" && step.ID != "finalize-catalog" {
				t.Fatalf("credential leaked to %s", step.Name)
			}
			if !strings.Contains(job.If, githubDispatchCondition(plan)) {
				t.Fatal("catalog credential exposed outside admitted branch dispatch")
			}
			if strings.Contains(step.Run, "secrets.") {
				t.Fatal("credential embedded in command")
			}
		}
	}
	if bootstraps == 0 || found["validate-catalog"] != bootstraps || found["finalize-catalog"] != 1 {
		t.Fatalf("catalog revalidation credentials missing: %v", found)
	}
	for _, tc := range []struct {
		name, old, replacement string
		allOccurrences         bool
	}{
		{"wrong secret", "secrets.NKE_SOURCE_TOKEN", "secrets.OTHER", false},
		{"wrong mirror", "credential.https://github.com/dsx-ai-factory/nvke.helper", "credential.https://github.com/other/repo.helper", true},
		{"missing presence check", strings.ReplaceAll(requireSourceToken, sourceTokenEnv, "NVKR_PIN_SOURCE_TOKEN_0"), "true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tree yamlv3.Node
			if err := yamlv3.Unmarshal(expandedGitHubWorkflow(t, bundle), &tree); err != nil {
				t.Fatal(err)
			}
			mutated := false
			var mutate func(*yamlv3.Node)
			mutate = func(node *yamlv3.Node) {
				if (!mutated || tc.allOccurrences) && node.Kind == yamlv3.ScalarNode && strings.Contains(node.Value, tc.old) {
					// Keep shared bootstrap programs equal so validation rejects
					// the credential scope or guard, not template divergence.
					node.Value = strings.ReplaceAll(node.Value, tc.old, tc.replacement)
					mutated = true
				}
				for _, child := range node.Content {
					mutate(child)
				}
			}
			mutate(&tree)
			if !mutated {
				t.Fatal("mutation did not apply")
			}
			raw, err := yamlv3.Marshal(&tree)
			if err != nil {
				t.Fatal(err)
			}
			changed := relowerGitHubTestBundle(t, plan, bundle, raw)
			if err := validateGitHubWorkflowSize(changed.Files[GitHubWorkflowPath]); err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestCatalogSourceCredentialsAreRepositoryScoped(t *testing.T) {
	const first = "https://github.com/test/a"
	const second = "https://github.com/test/b"
	plan := &Plan{
		CatalogSnapshot: &catalog.Catalog{Sources: map[string]catalog.Source{"a": {Repo: first, PinSet: &catalog.PinSet{}}, "unrelated": {Repo: "https://github.com/test/unrelated"}}},
		Builds:          []BuildPlan{{CatalogSnapshot: &catalog.Catalog{Sources: map[string]catalog.Source{"a-again": {Repo: first, PinSet: &catalog.PinSet{}}, "b": {Repo: second, PinSet: &catalog.PinSet{}}}}}},
		Environment: authored.Environment{SourceAccess: map[string]authored.SourceAccess{
			first: {Mirror: first, CredentialRef: "github:A"}, second: {Mirror: second, CredentialRef: "github:B"},
			"https://github.com/test/unrelated": {Mirror: "https://github.com/test/unrelated", CredentialRef: "github:UNRELATED"},
		}},
	}
	env := githubCatalogSourceEnvironment(plan)
	if env["GIT_CONFIG_COUNT"] != "6" || env["NVKR_PIN_SOURCE_TOKEN_2"] != "" {
		t.Fatalf("repositories not deduplicated/scoped: %v", env)
	}
	env["NVKR_PIN_SOURCE_TOKEN_0"] = "synthetic-a"
	env["NVKR_PIN_SOURCE_TOKEN_1"] = "synthetic-b"
	for _, tc := range []struct{ url, token string }{{first, "synthetic-a"}, {second, "synthetic-b"}, {"https://github.com/test/unrelated", ""}} {
		t.Run(tc.url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "git", "credential", "fill")
			cmd.Dir = t.TempDir()
			for _, item := range os.Environ() {
				if !strings.HasPrefix(item, "GIT_") && !strings.HasPrefix(item, "NVKR_PIN_SOURCE_TOKEN_") {
					cmd.Env = append(cmd.Env, item)
				}
			}
			for _, key := range sortedKeys(env) {
				cmd.Env = append(cmd.Env, key+"="+env[key])
			}
			cmd.Stdin = strings.NewReader("url=" + tc.url + "\n\n")
			raw, err := cmd.CombinedOutput()
			if tc.token == "" {
				if err == nil || bytes.Contains(raw, []byte("synthetic-")) {
					t.Fatal("credential escaped its repository")
				}
			} else if err != nil || !bytes.Contains(raw, []byte("password="+tc.token)) {
				t.Fatalf("repository credential lookup failed: %v", err)
			}
		})
	}
}
