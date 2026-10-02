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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"sigs.k8s.io/yaml"
)

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

func TestMaintainedPinSetCatalogCredentials(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	found := map[string]int{}
	for _, job := range document.Jobs {
		for key := range job.Env {
			if strings.HasPrefix(key, "NKD_PIN_SOURCE_TOKEN_") {
				t.Fatal("job-scoped pin credential")
			}
		}
		for _, step := range job.Steps {
			if step.Env["NKD_PIN_SOURCE_TOKEN_0"] == "" {
				continue
			}
			found[step.ID]++
			if step.ID != "validate-catalog" && step.ID != "finalize-catalog" {
				t.Fatalf("credential leaked to %s", step.Name)
			}
			if !strings.Contains(job.If, githubExecutionCondition) {
				t.Fatal("catalog credential exposed outside default-branch dispatch")
			}
			if strings.Contains(step.Run, "secrets.") {
				t.Fatal("credential embedded in command")
			}
		}
	}
	if found["validate-catalog"] != 1 || found["finalize-catalog"] != 1 {
		t.Fatalf("catalog revalidation credentials missing: %v", found)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"wrong secret", "secrets.NKE_SOURCE_TOKEN", "secrets.OTHER"},
		{"wrong mirror", "credential.https://github.com/dsx-ai-factory/nke.helper", "credential.https://github.com/other/repo.helper"},
		{"missing presence check", strings.ReplaceAll(requireSourceToken, sourceTokenEnv, "NKD_PIN_SOURCE_TOKEN_0"), "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				changed.Files[name] = raw
			}
			changed.Files[GitHubWorkflowPath] = bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte(tc.old), []byte(tc.replacement), 1)
			if bytes.Equal(changed.Files[GitHubWorkflowPath], bundle.Files[GitHubWorkflowPath]) {
				t.Fatal("mutation did not apply")
			}
			changed.WorkflowDigest = pipeline.FileDigest(changed.Files[GitHubWorkflowPath])
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
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
	if env["GIT_CONFIG_COUNT"] != "6" || env["NKD_PIN_SOURCE_TOKEN_2"] != "" {
		t.Fatalf("repositories not deduplicated/scoped: %v", env)
	}
	env["NKD_PIN_SOURCE_TOKEN_0"] = "synthetic-a"
	env["NKD_PIN_SOURCE_TOKEN_1"] = "synthetic-b"
	for _, tc := range []struct{ url, token string }{{first, "synthetic-a"}, {second, "synthetic-b"}, {"https://github.com/test/unrelated", ""}} {
		t.Run(tc.url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "git", "credential", "fill")
			cmd.Dir = t.TempDir()
			for _, item := range os.Environ() {
				if !strings.HasPrefix(item, "GIT_") && !strings.HasPrefix(item, "NKD_PIN_SOURCE_TOKEN_") {
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
