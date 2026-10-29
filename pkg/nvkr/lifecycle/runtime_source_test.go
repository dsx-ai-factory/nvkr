// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestRuntimeSourceGenerationReplaysFrozenSourceAfterRestore(t *testing.T) {
	definition, environment := testDefinition(t), testEnvironment()
	environment.RunsOn = "ubuntu-24.04"
	environment.Publication.RepositoryPrefix = "ghcr.io/example/runtime-source"
	environment.Publication.Anonymous = false
	environment.Publication.CredentialRef = "github:SYNTHETIC_REGISTRY_TOKEN"
	environment.GitHubRuntimePlan, environment.GitHubBundle = true, "exact-source"
	const selected = "https://github.com/kubernetes/kubernetes"
	environment.GitHubRuntimeSource = &authored.GitHubRuntimeSource{Repository: selected, Ref: "refs/heads/main"}
	repository := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--template=", "-q")
	commit := func(message string) string {
		git("-c", "user.name=Source Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", message)
		return git("rev-parse", "HEAD")
	}
	first, second := commit("first frozen source"), commit("later main source")
	git("branch", "-M", "main")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	root := t.TempDir()
	t.Chdir(root)
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
	const version = "v1.2.3 (deadbeef)"
	if _, err := GenerateFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", false); err != nil {
		t.Fatal(err)
	}
	baseline, err := CompileFiles(t.Context(), "definition.json", "environment.json", version)
	if err != nil {
		t.Fatal(err)
	}
	baselineBundle, err := factory.RenderGitHub(t.Context(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	_, err = GenerateRuntimeFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", false)
	assertCode(t, err, e.CodeUnpinnedSource)
	if _, err := GenerateRuntimeFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", true); err != nil {
		t.Fatal("static runtime contract check unnecessarily required an attempt commit", err)
	}
	if _, err := GenerateRuntimeSourceFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", first, false); err != nil {
		t.Fatal("first bootstrap could not freeze exact source", err)
	}
	frozen, err := factory.LoadPlanContext(t.Context(), baselineBundle.PlanPath)
	if err != nil || frozen.RuntimeSourceCommit != first {
		t.Fatal("generated Plan did not retain the frozen source", err)
	}
	freshBundle, err := factory.RenderGitHub(t.Context(), frozen)
	if err != nil {
		t.Fatal(err)
	}
	// Model the secondary checkout: committed bundle plus the three exact
	// same-attempt files restored by the existing transport contract.
	if err := factory.WriteGeneratedBundle(t.Context(), ".", baselineBundle); err != nil {
		t.Fatal(err)
	}
	_, manifest, err := factory.ManifestFor(freshBundle)
	if err != nil {
		t.Fatal(err)
	}
	staging := ".nvkr/runtime-inputs"
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"plan.json": freshBundle.Files[freshBundle.PlanPath], "action-data.json": freshBundle.Files[filepath.ToSlash(filepath.Join(filepath.Dir(freshBundle.PlanPath), "action-data.json"))], "manifest.json": manifest} {
		if err := os.WriteFile(filepath.Join(staging, name), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := factory.InstallGitHubRuntimeInputs(t.Context(), baseline, "."); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, selected string
		code           e.Code
	}{{"same frozen source", first, ""}, {"different dispatch source", second, e.CodePipelineDrift}} {
		t.Run(test.name, func(t *testing.T) {
			before := runtimeSourceGeneratedFiles(t, freshBundle)
			_, err := GenerateRuntimeSourceFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", test.selected, true)
			if test.code == "" {
				if err != nil {
					t.Fatal("restored bootstrap did not replay exact source", err)
				}
			} else {
				assertCode(t, err, test.code)
			}
			if !reflect.DeepEqual(before, runtimeSourceGeneratedFiles(t, freshBundle)) {
				t.Fatal("check mode changed restored runtime inputs")
			}
		})
	}
	// A missing evidence failure after a nonnil Run proves finalization has
	// authenticated/recompiled the frozen source rather than the authored tag.
	run, err := FinalizeGenerated(t.Context(), "definition.json", "environment.json", freshBundle.PlanPath, version, "source-attempt", ".nvkr/factory", ".nvkr/run.json")
	if run == nil || err == nil {
		t.Fatalf("finalizer did not reach exact frozen evidence observation: run=%v err=%v", run, err)
	}
	_, _, err = factory.CaptureCompileFiles(t.Context(), ".", "definition.json", "environment.json", version)
	assertCode(t, err, e.CodeEngineCapability)
}

func runtimeSourceGeneratedFiles(t *testing.T, bundle *factory.GeneratedBundle) map[string]string {
	t.Helper()
	files := make(map[string]string, len(bundle.Files)+1)
	for name := range bundle.Files {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(raw)
	}
	raw, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	files[bundle.ManifestPath] = string(raw)
	return files
}
