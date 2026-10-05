package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func sourceFetchTestEnvironment(plan *Plan, node Node) []string {
	var environment []string
	for key, value := range githubSourceEnvironment(plan, node) {
		if key == sourceTokenEnv {
			value = "test-only-synthetic-token"
		}
		environment = append(environment, key+"="+value)
	}
	return environment
}

func TestSourceFetchRejectsSymlinkDestinationBeforeWork(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	outside := t.TempDir()
	if err := os.Symlink(outside, ".nkd"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
	if called || factoryErrorCode(err) != e.CodeFetchFailed {
		t.Fatalf("unsafe destination delegated=%t error=%v", called, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsafe destination wrote outside source root: %v / %v", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "factory")); !os.IsNotExist(err) {
		t.Fatalf("created destination before admission: %v", err)
	}
}

func TestSourceFetchDelegatesOnlyExactSelectedInstruction(t *testing.T) {
	plan, node, source := authenticatedSourcePlan(t)
	called := 0
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called++
			if instruction.Kind != ir.KindFetch || instruction.Repo != source || instruction.ID != node.IRNode || options.SourceRoot == "" || !options.Launch {
				t.Fatalf("unexpected source delegation: %#v / %#v", instruction, options)
			}
			return nil, nil
		})
	if err != nil || called != 1 {
		t.Fatalf("calls=%d error=%v", called, err)
	}
}

func TestSourceFetchDoesNotOpenRequirementBearingGitHubPlan(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	bindFirstTestPlanExecution(t, plan, testExecutionRequirements())
	t.Chdir(t.TempDir())
	called := false
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(instruction ir.Node, _ pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			if instruction.Kind != ir.KindFetch || instruction.ID != node.IRNode {
				t.Fatalf("source-only route delegated another operation: %+v", instruction)
			}
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("selected source-only delegation called=%t error=%v", called, err)
	}
	var logs bytes.Buffer
	err = RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{Stdout: &logs, Stderr: &logs})
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if logs.Len() != 0 {
		t.Fatal("closed requirement-bearing hosted route wrote progress output")
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if bundle != nil {
		t.Fatal("source-only route authorized a requirement-bearing generated bundle")
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("source-only contract test created output: %v / %v", entries, err)
	}
}

func TestSourceFetchRejectsOutsideScopeBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Plan, *string, *[]string)
		code   e.Code
	}{
		{"build", func(plan *Plan, id *string, _ *[]string) {
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodeBuild {
					*id = node.ID
					return
				}
			}
		}, e.CodeEngineCapability},
		{"missing", func(_ *Plan, id *string, _ *[]string) { *id = "missing" }, e.CodeEngineCapability},
		{"altered pinned commit", func(plan *Plan, id *string, _ *[]string) {
			for buildIndex := range plan.Builds {
				for nodeIndex := range plan.Builds[buildIndex].Graph.Nodes {
					node := &plan.Builds[buildIndex].Graph.Nodes[nodeIndex]
					if node.Kind == ir.KindFetch {
						node.Commit = strings.Repeat("f", 40)
						return
					}
				}
			}
		}, e.CodeInvalidCatalog},
		{"publication credential", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "NKD_CREDENTIAL=synthetic") }, e.CodeEngineCapability},
		{"ambient GitHub token", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GITHUB_TOKEN=synthetic") }, e.CodeEngineCapability},
		{"ambient CLI token", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GH_TOKEN=synthetic") }, e.CodeEngineCapability},
		{"sibling source credential", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "NKD_PIN_SOURCE_TOKEN_1=synthetic") }, e.CodeEngineCapability},
		{"OIDC credential", func(_ *Plan, _ *string, env *[]string) {
			*env = append(*env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN=synthetic")
		}, e.CodeEngineCapability},
		{"Git destination override", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GIT_WORK_TREE=/wrong") }, e.CodeEngineCapability},
		{"wrong mirror", func(_ *Plan, _ *string, env *[]string) {
			*env = append(*env, "GIT_CONFIG_KEY_0=url.https://github.com/wrong/repo.insteadOf")
		}, e.CodeEngineCapability},
		{"missing credential", func(_ *Plan, _ *string, env *[]string) { *env = nil }, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, node, _ := authenticatedSourcePlan(t)
			id := node.ID
			env := sourceFetchTestEnvironment(plan, node)
			tc.change(plan, &id, &env)
			called := false
			err := executeSourceFetch(t.Context(), plan, id, env, nil, nil, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
			if called || factoryErrorCode(err) != tc.code {
				t.Fatalf("delegated=%t error=%v", called, err)
			}
		})
	}
}

func TestSourceFetchCancellationAndWrongResultFailClosed(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	env := sourceFetchTestEnvironment(plan, node)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := executeSourceFetch(ctx, plan, node.ID, env, nil, nil, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
	if called || factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled delegated=%t error=%v", called, err)
	}
	err = executeSourceFetch(t.Context(), plan, node.ID, env, nil, nil, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
		return &pipeline.ArtifactResult{Component: "wrong"}, nil
	})
	if factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("unexpected fetch artifact result: %v", err)
	}
}
