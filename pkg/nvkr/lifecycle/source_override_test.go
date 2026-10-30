// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestSourceOverrideGenerationReplaysFrozenSelectionAfterRestore(t *testing.T) {
	definition, environment := testDefinition(t), testEnvironment()
	environment.RunsOn = "ubuntu-24.04"
	environment.Publication = nil
	const selected = "https://github.com/kubernetes/kubernetes"
	environment.GitHubDevelopment = true
	environment.Overridable = &Overridable{Sources: map[string]OverridableSource{"kubernetes": {Repository: selected}}}
	repository := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository, "-c", "user.name=Source Fixture",
			"-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--template=", "-q", "-b", "main")
	release := func(tag string) string {
		git("commit", "--allow-empty", "-qm", tag)
		git("tag", tag)
		return git("rev-parse", "HEAD")
	}
	first := release("v1.1.0")
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
	const dispatch = `{"kubernetes":"1.x"}`
	if _, err := GenerateSourceOverrideFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", `{"kubernetes":""}`, true); err != nil {
		t.Fatal("empty overrides did not admit the committed bundle", err)
	}
	if _, err := GenerateSourceOverrideFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", dispatch, false); err != nil {
		t.Fatal("first bootstrap could not freeze the override", err)
	}
	frozen, err := factory.LoadPlanContext(t.Context(), baselineBundle.PlanPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]factory.SourceOverride{selected: {Request: "1.x", Tag: "v1.1.0", Commit: first}}
	if !reflect.DeepEqual(frozen.SourceOverrides, want) {
		t.Fatalf("generated Plan froze %+v, want %+v", frozen.SourceOverrides, want)
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
	// A newer matching release between bootstraps must not change the replay.
	release("v1.2.0")
	for _, test := range []struct {
		name, dispatch string
		code           e.Code
	}{
		{"same dispatch", dispatch, ""},
		{"different dispatch", `{"kubernetes":"1.2.x"}`, e.CodePipelineDrift},
		{"cleared dispatch", `{"kubernetes":""}`, e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := runtimeSourceGeneratedFiles(t, freshBundle)
			_, err := GenerateSourceOverrideFiles(t.Context(), "definition.json", "environment.json", version, factory.EngineGitHub, ".", test.dispatch, true)
			if test.code == "" {
				if err != nil {
					t.Fatal("restored bootstrap did not replay the frozen override", err)
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
	// replayed the frozen override rather than the reviewed pin.
	run, err := FinalizeGenerated(t.Context(), "definition.json", "environment.json", freshBundle.PlanPath, version, "override-attempt", ".nvkr/factory", ".nvkr/run.json")
	if run == nil || err == nil {
		t.Fatalf("finalizer did not reach frozen evidence observation: run=%v err=%v", run, err)
	}
	// Declaring overridable sources does not block input capture; only a Run
	// that used an override is excluded from qualification. This fixture's
	// absolute catalog path is rejected later, as not repository-relative.
	_, _, err = factory.CaptureCompileFiles(t.Context(), ".", "definition.json", "environment.json", version)
	assertCode(t, err, e.CodeInvalidCatalog)
	_, err = FinalizeGeneratedWithHandoff(t.Context(), "definition.json", "environment.json", freshBundle.PlanPath, version, "override-attempt", ".nvkr/factory", ".nvkr/run.json", ".nvkr/handoff.json")
	assertCode(t, err, e.CodeEngineCapability)
	err = factory.AdmitDevelopmentQualification(frozen)
	assertCode(t, err, e.CodeEngineCapability)
	if !strings.Contains(err.Error(), "source overrides") {
		t.Fatalf("development qualification rejected the overridden Plan for another reason: %v", err)
	}
}

func TestLocalSourceOverridePreparationFreezesSelectionForExecution(t *testing.T) {
	const selected = "https://github.com/kubernetes/kubernetes"
	repository := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository, "-c", "user.name=Source Fixture",
			"-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--template=", "-q", "-b", "main")
	git("commit", "--allow-empty", "-qm", "v1.1.0")
	git("tag", "v1.1.0")
	commit := git("rev-parse", "HEAD")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	environment := testEnvironment()
	environment.Publication = nil
	environment.Overridable = &Overridable{Sources: map[string]OverridableSource{"kubernetes": {Repository: selected}}}
	t.Chdir(t.TempDir())
	writeCanonicalTestFile(t, "definition.json", testDefinition(t))
	writeCanonicalTestFile(t, "environment.json", environment)
	baseline, err := PrepareFiles(t.Context(), "definition.json", "environment.json", "test", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareSourceOverrideFiles(t.Context(), "definition.json", "environment.json", "test", "attempt", []SourceOverrideRequest{{Name: "kubernetes", Request: "1.x"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]factory.SourceOverride{selected: {Request: "1.x", Tag: "v1.1.0", Commit: commit}}
	if !reflect.DeepEqual(prepared.plan.SourceOverrides, want) {
		t.Fatalf("prepared Plan froze %+v, want %+v", prepared.plan.SourceOverrides, want)
	}
	before := prepared.Description()
	if before.PlanDigest == "" || before.PlanDigest == baseline.Description().PlanDigest || prepared.inputs != "" {
		t.Fatal("prepared Run does not identify the overridden Plan or retained captured inputs")
	}
	got, err := ExecuteLocal(t.Context(), prepared, LocalOptions{AttemptsRoot: filepath.Join(".nvkr", "runs"), DryRun: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal("local execution did not admit the overridden Plan", err)
	}
	if !reflect.DeepEqual(*got, before) {
		t.Fatalf("dry run changed frozen state: %+v", got)
	}
	for _, test := range []struct {
		name     string
		requests []SourceOverrideRequest
		code     e.Code
	}{
		{"unmatched constraint", []SourceOverrideRequest{{Name: "kubernetes", Request: "2.x"}}, e.CodeNoMatch},
		{"undeclared name", []SourceOverrideRequest{{Name: "containerd", Request: "1.x"}}, e.CodeUsage},
		{"repeated name", []SourceOverrideRequest{{Name: "kubernetes", Request: "1.x"}, {Name: "kubernetes", Request: commit}}, e.CodeUsage},
		{"malformed request", []SourceOverrideRequest{{Name: "kubernetes", Request: "main"}}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareSourceOverrideFiles(t.Context(), "definition.json", "environment.json", "test", "attempt", test.requests)
			assertCode(t, err, test.code)
		})
	}
}
