package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"sigs.k8s.io/yaml"
)

func authenticatedSourcePlan(t *testing.T) (*Plan, Node, string) {
	t.Helper()
	plan := githubTestPlan(t)
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeFetch {
			continue
		}
		for _, recipe := range plan.Recipes {
			for _, source := range recipe.Graph.Nodes {
				if recipe.Name != node.RecipeTarget || source.ID != node.IRNode {
					continue
				}
				plan.ExecutionSettings.SourceAccess = map[string]SourceAccess{
					source.Repo: {Mirror: "https://github.com/test-owner/private-source", CredentialRef: "github:NKE_SOURCE_TOKEN"},
				}
				digest, err := plan.ExecutionSettings.Digest()
				if err != nil {
					t.Fatal(err)
				}
				plan.ExecutionSettingsDigest = digest
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
	if err := yaml.UnmarshalStrict(bundle.Files[GitHubWorkflowPath], &workflow); err != nil {
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
	for _, test := range []struct{ name, old, replacement string }{
		{"wrong-secret", "secrets.NKE_SOURCE_TOKEN", "secrets.OTHER_TOKEN"},
		{"missing-token-check", requireSourceToken, "true"},
		{"wrong-mirror", "url.https://github.com/test-owner/private-source.insteadOf", "url.https://github.com/other/source.insteadOf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				changed.Files[name] = raw
			}
			changed.Files[GitHubWorkflowPath] = bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte(test.old), []byte(test.replacement), 1)
			changed.WorkflowDigest = pipeline.FileDigest(changed.Files[GitHubWorkflowPath])
			var coded *e.Error
			if err := ValidateGitHubBundle(context.Background(), plan, &changed); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
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
