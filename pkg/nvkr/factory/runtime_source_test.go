// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func runtimeSourceFixture(t *testing.T) (*Plan, *Plan, string, string) {
	t.Helper()
	repository := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	run("init", "--template=", "-q")
	run("-c", "user.name=Source Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "exact source")
	commit := run("rev-parse", "HEAD")
	run("branch", "-M", "main")
	run("checkout", "-qb", "unmerged-sibling")
	run("-c", "user.name=Source Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "unmerged sibling")
	sibling := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	const selected = "https://github.com/containerd/containerd"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	definition, environment := fixtureInputs("exact-source", "cli-amd64", "ubuntu-24.04")
	var err error
	definition.Catalog.Base, err = filepath.Abs("../../../testdata/catalogs/kubernetes-layered")
	if err != nil {
		t.Fatal(err)
	}
	second := definition.Outputs[0]
	second.Name, second.Platform = "cli-arm64", "linux/arm64"
	secondCatalog := definition.Catalog
	second.Catalog = &secondCatalog
	definition.Outputs = append(definition.Outputs, second)
	environment.RunsOn = ""
	environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"}
	environment.GitHubBundle, environment.GitHubRuntimePlan = "exact-source", true
	environment.GitHubRuntimeSource = &authored.GitHubRuntimeSource{Repository: selected, Ref: "refs/heads/main"}
	environment.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nvkr-tools", CredentialRef: "github:NVKR_REGISTRY_AUTH"}
	environment.SourceAccess = map[string]authored.SourceAccess{selected: {Mirror: selected, CredentialRef: "github:SYNTHETIC_SOURCE_TOKEN"}}
	before, err := CompileDocuments(t.Context(), "definition.json", definition, "environment.json", environment, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	after, err := CompileRuntimeSourceDocuments(t.Context(), "definition.json", definition, "environment.json", environment, "v1.2.3 (deadbeef)", commit)
	if err != nil {
		t.Fatal(err)
	}
	return before, after, commit, sibling
}

func TestRuntimeSourceCompilationFreezesEverySelectedProducer(t *testing.T) {
	before, after, commit, sibling := runtimeSourceFixture(t)
	_, err := CompileRuntimeSourceDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, before.Environment, before.GeneratorVersion, sibling)
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	if after.RuntimeSourceCommit != commit || after.DefinitionDigest != before.DefinitionDigest || after.EnvironmentDigest != before.EnvironmentDigest {
		t.Fatal("exact source changed authored identity or omitted its frozen commit")
	}
	for index, build := range after.Builds {
		for name, got := range build.ResolvedComponents {
			original := before.Builds[index].ResolvedComponents[name]
			if got.Repo == after.Environment.GitHubRuntimeSource.Repository {
				if got.Commit != commit || got.Tag != commit || !reflect.DeepEqual(got.BuildCommand, original.BuildCommand) || got.ToolchainImage != original.ToolchainImage {
					t.Fatal("source was not frozen without changing its producer contract")
				}
			} else if !reflect.DeepEqual(got, original) {
				t.Fatal("independent source or acquired dependency changed")
			}
		}
	}
	assertFactoryErrorCode(t, AdmitQualificationHandoff(before), e.CodeEngineCapability)
	assertFactoryErrorCode(t, AdmitQualificationHandoff(after), e.CodeEngineCapability)
	for _, test := range []struct {
		name string
		edit func(*Plan)
		code e.Code
	}{
		{"missing policy", func(p *Plan) { p.Environment.GitHubRuntimeSource = nil }, e.CodeEngineCapability},
		{"mutable commit", func(p *Plan) { p.RuntimeSourceCommit = "main" }, e.CodeEngineCapability},
		{"mixed producer", func(p *Plan) { p.RuntimeSourceCommit = strings.Repeat("a", 40) }, e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *after
			copy.Environment = after.Environment.Clone()
			test.edit(&copy)
			assertFactoryErrorCode(t, copy.Validate(), test.code)
		})
	}
}

func TestRuntimeSourceGeneratedBootstrapsRecompileExactRestoredInputs(t *testing.T) {
	before, after, _, _ := runtimeSourceFixture(t)
	root := t.TempDir()
	writeRuntimeTrustedInput(t, root, before.DefinitionPath, before.Definition)
	writeRuntimeTrustedInput(t, root, before.EnvironmentPath, before.Environment)
	baseline := writeGitHubRuntimeBaseline(t, root, before)
	fresh := stageGitHubRuntimeInputs(t, root, after)
	if !bytes.Equal(baseline.Files[baseline.WorkflowPath], fresh.Files[fresh.WorkflowPath]) {
		t.Fatal("freezing the source changed executable workflow bytes")
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, fresh))
	bootstraps := runtimeTransportBootstraps(t, after)
	first := bootstraps[sortedKeys(bootstraps)[0]]
	for _, id := range bootstraps {
		found := false
		for _, step := range document.Jobs[id].Steps {
			if step.ID != "validate-catalog" {
				continue
			}
			found = true
			command := githubRuntimeGenerationCommand(after)
			if id != first {
				command += " --check"
			}
			if !strings.Contains(step.Run, command) || step.Env["NVKR_RUNTIME_SOURCE_COMMIT"] != "${{ github.event.inputs.source_commit }}" || step.Env["NVKR_PIN_SOURCE_TOKEN_0"] != "${{ secrets.SYNTHETIC_SOURCE_TOKEN }}" {
				t.Fatalf("bootstrap %s lost its exact-source owner or scoped credentials: %+v", id, step)
			}
		}
		if !found {
			t.Fatal("bootstrap lost catalog validation")
		}
	}
	if strings.Contains(joinRuns(document.Jobs["nvkr-lifecycle-run"].Steps), " --handoff ") {
		t.Fatal("unsupported exact-source qualification handoff was emitted")
	}
	if err := InstallGitHubRuntimeInputs(t.Context(), before, root); err != nil {
		t.Fatal("restored exact-source inputs were rejected", err)
	}
	if err := CheckGitHubRuntimeInputs(t.Context(), after, root); err != nil {
		t.Fatal(err)
	}
	if err := PrepareGitHubRuntimeBundle(t.Context(), before, root, true); err != nil {
		t.Fatal("baseline static admission rejected a restored frozen bundle", err)
	}
	if err := PrepareGitHubRuntimeBundle(t.Context(), after, root, true); err != nil {
		t.Fatal(err)
	}
}
