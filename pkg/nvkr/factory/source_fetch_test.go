// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func sourceFetchTestEnvironment(plan *Plan, node Node) []string {
	var paths []string
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(path) {
			paths = append(paths, path)
		}
	}
	environment := []string{"PATH=" + strings.Join(paths, string(os.PathListSeparator))}
	for key, value := range githubSourceEnvironment(plan, node) {
		if key == sourceTokenEnv {
			value = "test-only-synthetic-token"
		}
		environment = append(environment, key+"="+value)
	}
	return environment
}

func TestSourceFetchDelegatesDependencyVendoringToHeldSourceOwner(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued held source workspace requires Linux")
	}
	original := githubTestPlan(t)
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, component := range base.Components {
		if component.FromSource {
			component.VendorCommand = []string{"go", "mod", "vendor"}
			base.Components[name] = component
		}
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), original.Definition, original.Environment, stack, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	var node Node
	for _, candidate := range plan.Graph.Nodes {
		if candidate.Kind == NodeFetch {
			node = candidate
			break
		}
	}
	if node.ID == "" {
		t.Fatal("vendoring fixture has no source fetch")
	}
	t.Chdir(t.TempDir())
	called := false
	err = executeSourceFetch(t.Context(), plan, node.ID, nil, nil, nil,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			if options.SourceWorkspace == nil || !options.Launch || !slices.Equal(instruction.VendorCmd, []string{"go", "mod", "vendor"}) {
				t.Fatalf("vendor declaration lost held source-owner routing: %+v / %+v", instruction, options)
			}
			return nil, nil
		})
	if !called || err != nil {
		t.Fatalf("held vendor route delegated=%t error=%v", called, err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("description-only delegation wrote output: %v / %v", entries, err)
	}
}

func TestSourceFetchRejectsSymlinkDestinationBeforeWork(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	outside := t.TempDir()
	if err := os.Symlink(outside, ".nvkr"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
	want := e.CodeFetchFailed
	if runtime.GOOS != "linux" {
		want = e.CodeEngineCapability
	}
	if called || factoryErrorCode(err) != want {
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

func TestSourceFetchAdmitsEveryDestinationComponentWithoutFollowingLinks(t *testing.T) {
	for _, test := range []struct {
		name, member string
		symlink      bool
	}{
		{"first component", ".nvkr", true},
		{"nested parent", ".nvkr/factory", true},
		{"checkout leaf", ".nvkr/factory/src/source", true},
		{"regular parent", ".nvkr/factory", false},
		{"regular checkout", ".nvkr/factory/src/source", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(test.member), 0o755); err != nil {
				t.Fatal(err)
			}
			var err error
			if test.symlink {
				err = os.Symlink(outside, test.member)
			} else {
				err = os.WriteFile(test.member, []byte("not a directory"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, admitSourceFetchPath(t.Context(), ".nvkr/factory/src/source", true), e.CodeFetchFailed)
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("admission traversed symlink: %v / %v", entries, err)
			}
		})
	}
	t.Run("receipt leaf symlink", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.MkdirAll(".nvkr/factory/receipts", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), ".nvkr/factory/receipts/source.json"); err != nil {
			t.Fatal(err)
		}
		assertFactoryErrorCode(t, admitSourceFetchPath(t.Context(), ".nvkr/factory/receipts/source.json", false), e.CodeFetchFailed)
	})
	t.Run("all components present", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.MkdirAll(".nvkr/factory/src/source", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := admitSourceFetchPath(t.Context(), ".nvkr/factory/src/source", true); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSourceFetchRejectsPathSubstitutionAtRealFetchOwner(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	outside := t.TempDir()
	called := false
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			if err := os.Symlink(outside, ".nvkr"); err != nil {
				t.Fatal(err)
			}
			return (pipeline.Local{}).ExecNode(instruction, options)
		})
	want := e.CodeFetchFailed
	if runtime.GOOS != "linux" {
		want = e.CodeEngineCapability
	}
	if called != (runtime.GOOS == "linux") || factoryErrorCode(err) != want {
		t.Fatalf("real fetch boundary called=%t error=%v", called, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("real fetch wrote outside held workspace: %v / %v", entries, err)
	}
}

func TestSourceFetchGitHubDispatchUsesScopedFetchOwner(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	for _, pair := range os.Environ() {
		key, value, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") || strings.HasPrefix(key, "ACTIONS_ID_TOKEN_") {
			t.Setenv(key, value)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, key := range []string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER", "GITHUB_TOKEN", "GH_TOKEN", "DOCKER_CONFIG", "REGISTRY_AUTH_FILE", "ACTIONS_RUNTIME_TOKEN"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(key, "")
	}
	for _, pair := range sourceFetchTestEnvironment(plan, node) {
		key, value, _ := strings.Cut(pair, "=")
		t.Setenv(key, value)
	}
	commands := t.TempDir()
	marker := filepath.Join(commands, "held-git")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
test "$1" = -C
test "$2" = /proc/self/fd/3/.
test -d "$2"
test "$3" = --git-dir=/proc/self/fd/4/.
test -d /proc/self/fd/4/.
test "$4" = --work-tree=/proc/self/fd/3/.
test "$5" = -c
test "$6" = core.hooksPath=/dev/null
test "$7" = -c
test "$8" = core.fsmonitor=false
test "$9" = -c
test "${10}" = diff.ignoreSubmodules=all
test "${11}" = -c
test "${12}" = submodule.recurse=false
test "${13}" = init
test "${GIT_CONFIG_GLOBAL}" = /dev/null
test "${GIT_CONFIG_NOSYSTEM}" = 1
test "${NVKR_SOURCE_TOKEN+x}" != x
test "${HOME}" = /proc/self/fd/5/.
test "${XDG_CONFIG_HOME}" = "$HOME"
test -d "$HOME"
test "${SSH_AUTH_SOCK+x}" != x
test "${HTTPS_PROXY+x}" != x
printf held > %q
exit 1
`, marker)
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{commands}
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(path) {
			paths = append(paths, path)
		}
	}
	t.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
	err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{})
	if runtime.GOOS == "linux" {
		if factoryErrorCode(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), "git init -q in") {
			t.Fatalf("actual hosted dispatch did not reach controlled Git: %v", err)
		}
		if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "held" {
			t.Fatalf("actual hosted dispatch did not use the held directory and selected child environment: %q, %v", bytes, err)
		}
	} else if factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "inherited directory") {
		t.Fatalf("actual hosted dispatch bypassed held-source platform admission: %v", err)
	}
}

func TestSourceFetchDelegatesOnlyExactSelectedInstruction(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issued held source workspace requires Linux")
	}
	plan, node, source := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	_, artifacts := planIndex(plan)
	var checkout string
	for _, output := range node.Outputs {
		if artifacts[output].Kind == "source-closure" {
			checkout = artifacts[output].Path
		}
	}
	if checkout == "" {
		t.Fatal("selected fetch has no source closure")
	}
	called := 0
	err := executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called++
			if instruction.Kind != ir.KindFetch || instruction.Repo != source || instruction.ID != node.IRNode || filepath.Join(options.SourceRoot, instruction.Dest) != checkout || !options.Launch {
				t.Fatalf("unexpected source delegation: %#v / %#v", instruction, options)
			}
			return nil, nil
		})
	if err != nil || called != 1 {
		t.Fatalf("calls=%d error=%v", called, err)
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("contract callback wrote output: %v / %v", entries, err)
	}
}

func TestSourceFetchChildEnvironmentContainsOnlySelectedSourceConfiguration(t *testing.T) {
	t.Setenv("PATH", "node_modules/.bin"+string(os.PathListSeparator)+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "private"}[private], func(t *testing.T) {
			plan, node, _ := authenticatedSourcePlan(t)
			if !private {
				plan.Environment.SourceAccess = nil
				var err error
				plan.EnvironmentDigest, err = plan.Environment.Digest()
				if err != nil {
					t.Fatal(err)
				}
			}
			environment := append(sourceFetchTestEnvironment(plan, node), "HOME=/ambient-secret-home", "XDG_CONFIG_HOME=/ambient-config", "SSH_AUTH_SOCK=/ambient-agent", "UNSELECTED_SECRET=synthetic-secret", "GITHUB_RUN_ID=123")
			t.Chdir(t.TempDir())
			got, err := admitSourceFetchEnvironment(plan, node, environment)
			if err != nil {
				t.Fatal(err)
			}
			allowed := githubSourceEnvironment(plan, node)
			if !private {
				allowed = map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": ""}
			}
			selectedPath, err := sourcegit.SearchPath(environment)
			if err != nil {
				t.Fatal(err)
			}
			allowed["PATH"], allowed["LC_ALL"] = selectedPath, "C"
			if private {
				allowed[sourceTokenEnv] = "test-only-synthetic-token"
			}
			var want []string
			for _, key := range sortedKeys(allowed) {
				want = append(want, key+"="+allowed[key])
			}
			if !slices.Equal(got, want) {
				t.Fatal("source child environment differs from exact selected allowlist")
			}
		})
	}
}

func TestSourceFetchRequiresEverySelectedGitConfigurationField(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	for _, key := range sortedKeys(githubSourceEnvironment(plan, node)) {
		if key == sourceTokenEnv {
			continue
		}
		t.Run(key, func(t *testing.T) {
			environment := sourceFetchTestEnvironment(plan, node)
			environment = slices.DeleteFunc(environment, func(pair string) bool { return strings.HasPrefix(pair, key+"=") })
			t.Chdir(t.TempDir())
			called := false
			err := executeSourceFetch(t.Context(), plan, node.ID, environment, nil, nil,
				func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
			if called || factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing selected field %s delegated=%t error=%v", key, called, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("missing selected field wrote output: %v / %v", entries, err)
			}
		})
	}
}

func TestSourceFetchDelegationCarriesSelectedDeadline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual source delegation requires an issued Linux workspace")
	}
	plan, node, _ := authenticatedSourcePlan(t)
	t.Chdir(t.TempDir())
	expected, cancel, err := githubUtilityContext(t.Context(), plan, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := expected.Deadline()
	if !ok {
		t.Fatal("selected utility stage has no deadline")
	}
	called := false
	err = executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
		func(_ ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			observed, ok := options.Ctx.Deadline()
			if !ok || observed.Before(deadline) || observed.Sub(deadline) > time.Second {
				t.Fatalf("source owner lost selected stage deadline: want %v got %v / %t", deadline, observed, ok)
			}
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("deadline callback not admitted: called=%t error=%v", called, err)
	}
}

func TestSourceFetchRejectsInvalidPathAndProxyBeforeDelegation(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	for _, test := range []struct{ name, key, value string }{
		{"relative PATH", "PATH", ".:/usr/bin"},
		{"empty PATH component", "PATH", "/usr/bin:"},
		{"missing Git", "PATH", t.TempDir()},
		{"HTTP proxy", "HTTP_PROXY", "http://proxy.invalid"},
		{"HTTPS proxy", "HTTPS_PROXY", "http://proxy.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			environment := sourceFetchTestEnvironment(plan, node)
			environment = slices.DeleteFunc(environment, func(pair string) bool { return strings.HasPrefix(pair, test.key+"=") })
			environment = append(environment, test.key+"="+test.value)
			called := false
			err := executeSourceFetch(t.Context(), plan, node.ID, environment, nil, nil,
				func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
			if called || factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("unsupported environment reached work: called=%t error=%v", called, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("environment rejection wrote output: %v / %v", entries, err)
			}
		})
	}
}

func TestSourceFetchRejectsUnrelatedCredentialsAndUnsupportedProxies(t *testing.T) {
	for _, key := range []string{"NVKR_REGISTRY_USER", "NVKR_CREDENTIAL", "ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_RUNTIME_TOKEN", "DOCKER_CONFIG", "REGISTRY_AUTH_FILE", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "NO_PROXY"} {
		for _, value := range []string{"", "synthetic-secret"} {
			if value == "" && strings.Contains(strings.ToUpper(key), "PROXY") {
				continue
			}
			t.Run(key+map[bool]string{false: "/empty", true: "/nonempty"}[value != ""], func(t *testing.T) {
				plan, node, _ := authenticatedSourcePlan(t)
				environment := append(sourceFetchTestEnvironment(plan, node), key+"="+value)
				t.Chdir(t.TempDir())
				called := false
				err := executeSourceFetch(t.Context(), plan, node.ID, environment, nil, nil,
					func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
				if called || factoryErrorCode(err) != e.CodeEngineCapability || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatalf("unsupported source scope delegated=%t error=%v", called, err)
				}
			})
		}
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
	if runtime.GOOS == "linux" && (err != nil || !called) {
		t.Fatalf("selected source-only delegation called=%t error=%v", called, err)
	}
	if runtime.GOOS != "linux" && (called || factoryErrorCode(err) != e.CodeEngineCapability) {
		t.Fatalf("unsupported hosted workspace delegated=%t error=%v", called, err)
	}
	var logs bytes.Buffer
	err = executeGitHubSourceFetch(t.Context(), plan, node.ID, &logs, &logs)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
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
		{"missing", func(_ *Plan, id *string, _ *[]string) { *id = "missing" }, e.CodeInvalidCatalog},
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
		{"publication credential", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "NVKR_CREDENTIAL=synthetic") }, e.CodeEngineCapability},
		{"ambient GitHub token", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GITHUB_TOKEN=synthetic") }, e.CodeEngineCapability},
		{"ambient CLI token", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GH_TOKEN=synthetic") }, e.CodeEngineCapability},
		{"sibling source credential", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "NVKR_PIN_SOURCE_TOKEN_1=synthetic") }, e.CodeEngineCapability},
		{"OIDC credential", func(_ *Plan, _ *string, env *[]string) {
			*env = append(*env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN=synthetic")
		}, e.CodeEngineCapability},
		{"Git destination override", func(_ *Plan, _ *string, env *[]string) { *env = append(*env, "GIT_WORK_TREE=/wrong") }, e.CodeEngineCapability},
		{"wrong mirror", func(_ *Plan, _ *string, env *[]string) {
			for index, pair := range *env {
				if strings.HasPrefix(pair, "GIT_CONFIG_KEY_0=") {
					(*env)[index] = "GIT_CONFIG_KEY_0=url.https://github.com/wrong/repo.insteadOf"
				}
			}
		}, e.CodeEngineCapability},
		{"duplicate Git field", func(_ *Plan, _ *string, env *[]string) {
			for _, pair := range *env {
				if strings.HasPrefix(pair, "GIT_CONFIG_KEY_2=") {
					*env = append(*env, pair)
					return
				}
			}
		}, e.CodeEngineCapability},
		{"missing Git helper", func(_ *Plan, _ *string, env *[]string) {
			for index, pair := range *env {
				if strings.HasPrefix(pair, "GIT_CONFIG_KEY_2=") {
					*env = append((*env)[:index], (*env)[index+1:]...)
					return
				}
			}
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

func TestSourceFetchRejectsRuntimeStorageBeforeWork(t *testing.T) {
	for _, driver := range []definition.PodmanStorageDriver{definition.PodmanStorageVFS, definition.PodmanStorageOverlay} {
		t.Run(string(driver), func(t *testing.T) {
			plan, node, _ := authenticatedSourcePlan(t)
			plan.Environment.Runtime.Runtime = "podman"
			plan.Environment.Runtime.PodmanStorageDriver = driver
			var err error
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			called := false
			err = executeSourceFetch(t.Context(), plan, node.ID, sourceFetchTestEnvironment(plan, node), nil, nil,
				func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
			if called || factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("storage override delegated=%t error=%v", called, err)
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("storage rejection wrote output: %v / %v", entries, err)
			}
		})
	}
}

func TestSourceFetchPreCanceledContextAndArtifactResultFailClosed(t *testing.T) {
	plan, node, _ := authenticatedSourcePlan(t)
	env := sourceFetchTestEnvironment(plan, node)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := executeSourceFetch(ctx, plan, node.ID, env, nil, nil, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
	if called || factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled delegated=%t error=%v", called, err)
	}
	if runtime.GOOS != "linux" {
		return // Positive executor delegation requires an issued Linux workspace.
	}
	err = executeSourceFetch(t.Context(), plan, node.ID, env, nil, nil, func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
		return &pipeline.ArtifactResult{}, nil
	})
	if factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("unexpected fetch artifact result: %v", err)
	}
}

func TestSourceFetchRejectsDriftedNodeShapesAtCompletePlanBoundary(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Node)
		code e.Code
	}{
		{"copied fetch requirements", func(node *Node) { node.Requirements = testExecutionRequirements() }, e.CodePipelineDrift},
		{"missing producer reference", func(node *Node) { node.IRNode = "" }, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, selected, _ := authenticatedSourcePlan(t)
			for index := range plan.Graph.Nodes {
				if plan.Graph.Nodes[index].ID == selected.ID {
					test.edit(&plan.Graph.Nodes[index])
				}
			}
			// These shapes cannot pass the complete Plan boundary. Do not forge a
			// "valid" Plan or bypass its owner solely to reach a defensive guard.
			assertFactoryErrorCode(t, plan.Validate(), test.code)
			t.Chdir(t.TempDir())
			called := false
			err := executeSourceFetch(t.Context(), plan, selected.ID, sourceFetchTestEnvironment(plan, selected), nil, nil,
				func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { called = true; return nil, nil })
			assertFactoryErrorCode(t, err, test.code)
			if called {
				t.Fatal("drifted source shape reached the executor")
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("drifted source shape wrote output: %v / %v", entries, err)
			}
		})
	}
}
