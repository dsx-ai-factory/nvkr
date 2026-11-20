// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestGenerateFilesUsesLifecycleInputsForSupportedHostedEngines(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition := "docs/examples/nvke-module-tags/definition.yaml"
	environments := map[string]string{
		factory.EngineGitHub: "docs/examples/nvke-module-tags/environment-github.yaml",
	}
	for engine, environment := range environments {
		t.Run(engine, func(t *testing.T) {
			root := t.TempDir()
			digest, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, false)
			if err != nil {
				t.Fatal(err)
			}
			if !canonical.IsDigest(digest) {
				t.Fatalf("plan digest = %q", digest)
			}
			checked, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", engine, root, true)
			if err != nil {
				t.Fatal(err)
			}
			if checked != digest {
				t.Fatalf("checked digest %s != generated digest %s", checked, digest)
			}
		})
	}
}

// An unsupported projection selection proves which paths consult
// the selection: top-level check and write render through it and write
// nothing on refusal, and runtime refresh refuses it before reading inputs.
func TestGenerateFilesHonorsProjectionSelectionOnlyAtTopLevel(t *testing.T) {
	selected := factory.WithProjectionAdapter(context.Background(),
		factory.ProjectionAdapter{Transport: "unsupported-transport", Release: "test"})
	untouched := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := GenerateRuntimeFiles(selected, "missing-definition.yaml", "missing-environment.yaml", "test", factory.EngineGitHub, untouched, true)
	assertCode(t, err, nvkrerr.CodeEngineCapability)

	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definition, environment := "docs/examples/nvke-module-tags/definition.yaml", "docs/examples/nvke-module-tags/environment-github.yaml"
	root := t.TempDir()
	if _, err := GenerateFiles(context.Background(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, false); err != nil {
		t.Fatal(err)
	}
	_, err = GenerateFiles(selected, definition, environment, "test (deadbeef)", factory.EngineGitHub, root, true)
	assertCode(t, err, nvkrerr.CodeEngineCapability)
	_, err = GenerateFiles(selected, definition, environment, "test (deadbeef)", factory.EngineGitHub, untouched, false)
	assertCode(t, err, nvkrerr.CodeEngineCapability)
	if _, statErr := os.Stat(untouched); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused projection touched output root: %v", statErr)
	}
}

func TestGenerateFilesRejectsUnsupportedEngineBeforeReadingInputsOrOutput(t *testing.T) {
	for _, engine := range []string{factory.EngineArgo} {
		t.Run(engine, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			_, err := GenerateFiles(context.Background(), "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, false)
			assertCode(t, err, nvkrerr.CodeEngineCapability)
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsupported generation touched output root: %v", statErr)
			}
		})
	}
}

func TestCompileRuntimeGenerationFreezesSourcesWithoutPublishing(t *testing.T) {
	definition, environment := testDefinition(t), testEnvironment()
	root := t.TempDir()
	for _, name := range []string{catalog.RegistryFile, catalog.LockFile} {
		raw, err := os.ReadFile(filepath.Join(definition.Catalog.Base, name))
		if err != nil {
			t.Fatal(err)
		}
		writeQualificationHandoffFile(t, filepath.Join(root, "catalog", name), raw)
	}
	definition.Catalog.Base = "catalog"
	environment.Publication = nil
	environment.RunsOn = "ubuntu-24.04"
	environment.GitHubRuntimePlan, environment.GitHubBundle = true, "frozen"
	const repository = "https://github.com/kubernetes/kubernetes"
	environment.GitHubRuntimeSources = []authored.GitHubRuntimeSource{{Repository: repository, Ref: "refs/heads/main"}}
	local := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+local+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", repository)
	git := func(args ...string) string {
		t.Helper()
		raw, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", local}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--template=", "-q", "-b", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "frozen source")
	commit := git("rev-parse", "HEAD")
	t.Chdir(root)
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
	const version = "dev (unknown)"
	if _, err := GenerateFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, root, false); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, ".github/nvkr-run-frozen/manifest.json")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := CompileRuntimeGeneration(t.Context(), "definition.json", "environment.json", version, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RuntimeInputs == nil || len(plan.RuntimeInputs.Sources) != 1 || plan.RuntimeInputs.Sources[0].Commit != commit {
		t.Fatal("coordinated compilation did not freeze the declared source")
	}
	after, err := os.ReadFile(manifest)
	if err != nil || string(after) != string(before) {
		t.Fatal("runtime compilation published a bundle", err)
	}
}

func TestGitLabGenerationRejectsUnsupportedRefreshAndProjectionBeforeIO(t *testing.T) {
	selected := factory.WithProjectionAdapter(t.Context(), factory.ProjectionAdapter{
		Transport: factory.ProjectionInProcess, Release: "test",
	})
	for _, engine := range []string{factory.EngineGitLab, " GitLab "} {
		for _, check := range []bool{false, true} {
			for _, operation := range []string{"runtime", "source", "projection"} {
				t.Run(engine+"/"+operation+"/check="+fmt.Sprint(check), func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "must-not-exist")
					var digest string
					var err error
					switch operation {
					case "runtime":
						digest, err = GenerateRuntimeFiles(t.Context(), "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, check)
					case "source":
						digest, err = GenerateRuntimeSourceFiles(t.Context(), "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, strings.Repeat("a", 40), check)
					case "projection":
						digest, err = GenerateFiles(selected, "missing-definition.yaml", "missing-environment.yaml", "test", engine, root, check)
					}
					assertCode(t, err, nvkrerr.CodeEngineCapability)
					if digest != "" || !strings.Contains(err.Error(), "GitLab generation does not support") {
						t.Fatalf("unsupported GitLab operation returned digest %q and error %v", digest, err)
					}
					if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("unsupported GitLab operation touched output: %v", statErr)
					}
				})
			}
		}
	}
}

func TestGitLabGenerateFilesRoundTripAuthoredSelections(t *testing.T) {
	for _, outputs := range []int{1, 2} {
		t.Run(fmt.Sprint(outputs), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			definition, environment := testDefinition(t), testEnvironment()
			if outputs == 2 {
				second := definition.Outputs[0]
				second.Name = "second-cli"
				definition.Outputs = append(definition.Outputs, second)
			}
			environment.Publication = nil
			environment.GitLab = &authored.GitLab{APIURL: "https://gitlab.example.test/api/v4", ProjectID: 7,
				APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token",
				Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"synthetic-native-runner"},
					BootstrapImage: "registry.example.test/go@sha256:" + strings.Repeat("a", 64)}}}
			writeCanonicalTestFile(t, "definition.json", definition)
			writeCanonicalTestFile(t, "environment.json", environment)
			generated, err := GenerateFiles(t.Context(), "definition.json", "environment.json", "test (deadbeef)", " GitLab ", root, false)
			if err != nil || !canonical.IsDigest(generated) {
				t.Fatalf("authored GitLab generation: %s, %v", generated, err)
			}
			checked, err := GenerateFiles(t.Context(), "definition.json", "environment.json", "test (deadbeef)", factory.EngineGitLab, root, true)
			if err != nil || checked != generated {
				t.Fatalf("exact GitLab check: %s, %v", checked, err)
			}
			plan, err := factory.LoadPlanContext(t.Context(), factory.GitLabPlanPath)
			if err != nil || len(plan.LifecycleOutputs) != outputs {
				t.Fatalf("authored selection was not preserved: %v", err)
			}
			if _, err := os.Stat(".gitlab-ci.yml"); !os.IsNotExist(err) {
				t.Fatal("generation took ownership of the operator root")
			}
		})
	}
}
