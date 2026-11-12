// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRuntimeSourcesFreezeCompositeAndReplayWithoutMain(t *testing.T) {
	root, _ := compositeFactoryFixture(t, nil)
	_, environment := fixtureInputs("snapshot-fixture", "tools", "ubuntu-24.04")
	environment.GitHubBundle, environment.GitHubRuntimePlan = "floating-product", true
	environment.GitHubRuntimeSources = []authored.GitHubRuntimeSource{
		{Repository: "https://github.com/containerd/containerd", Ref: "refs/heads/main"},
		{Repository: "https://github.com/kubernetes/kubernetes", Ref: "refs/heads/main"},
	}
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	before, _, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	baseline := writeGitHubRuntimeBaseline(t, root, before)
	_, err = admitExecutionNode(t.Context(), before, before.Graph.Nodes[0].ID)
	assertFactoryErrorCode(t, err, e.CodeUnpinnedSource)
	assertFactoryErrorCode(t, admitGitHubPlan(t.Context(), before), e.CodeUnpinnedSource)
	assertFactoryErrorCode(t, admitLocalPlanBase(t.Context(), before), e.CodeEngineCapability)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	var repositories []string
	git := func(repo string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for i, source := range environment.GitHubRuntimeSources {
		repo := t.TempDir()
		repositories = append(repositories, repo)
		git(repo, "init", "--template=", "-q", "-b", "main")
		git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "frozen source "+source.Repository)
		t.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(i), "url."+repo+".insteadOf")
		t.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(i), source.Repository)
	}
	t.Setenv("GIT_CONFIG_COUNT", "2")
	after, err := FreezeRuntimeSources(t.Context(), root, compositeFixturePath, snapshotEnvironment, before.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if after.RuntimeInputs == nil || len(after.RuntimeInputs.Sources) != 2 || !after.Composition.Equal(before.Composition) {
		t.Fatal("freeze lost exact sources or complete composition")
	}
	for i, build := range after.Builds {
		for name, component := range build.ResolvedComponents {
			old := before.Builds[i].ResolvedComponents[name]
			if component.FromSource {
				if component.Commit == old.Commit || component.Tag != component.Commit || component.ToolchainImage != old.ToolchainImage || !reflect.DeepEqual(component.BuildCommand, old.BuildCommand) {
					t.Fatal("freeze changed the producer contract or failed to change its source")
				}
			} else if !reflect.DeepEqual(component, old) {
				t.Fatal("freeze changed independent acquisition")
			}
		}
	}
	fresh := stageGitHubRuntimeInputs(t, root, after)
	for name, raw := range baseline.Files {
		if name == baseline.PlanPath || name == githubActionDataPathFor(before) {
			continue
		}
		if !bytes.Equal(raw, fresh.Files[name]) {
			t.Fatalf("freeze changed executable member %s", name)
		}
	}
	if err := InstallGitHubRuntimeInputs(t.Context(), before, root); err != nil {
		t.Fatal(err)
	}
	if err := CheckGitHubRuntimeInputs(t.Context(), after, root); err != nil {
		t.Fatal(err)
	}
	for _, repo := range repositories {
		git(repo, "branch", "-m", "retained-frozen-commit")
	}
	want, _ := after.Digest()
	retried, err := FreezeRuntimeSources(t.Context(), root, compositeFixturePath, snapshotEnvironment, before.GeneratorVersion)
	if err != nil {
		t.Fatal("retry consulted absent main", err)
	}
	if got, _ := retried.Digest(); got != want {
		t.Fatal("retry changed frozen identity")
	}
	replayed, err := CompileSnapshot(t.Context(), *after.RuntimeInputs, after.GeneratorVersion)
	if err != nil {
		t.Fatal("replay consulted absent main", err)
	}
	if got, err := replayed.Digest(); err != nil || got != want {
		t.Fatalf("replay digest=%s want=%s: %v", got, want, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*InputSnapshot)
		code e.Code
	}{
		{"missing source", func(s *InputSnapshot) { s.Sources = s.Sources[:1] }, e.CodeUnpinnedSource},
		{"foreign source", func(s *InputSnapshot) { s.Sources[0].Repository = "https://github.com/example/foreign" }, e.CodeUnpinnedSource},
		{"mutable commit", func(s *InputSnapshot) { s.Sources[0].Commit = "main" }, e.CodeUnpinnedSource},
		{"changed tree", func(s *InputSnapshot) { s.Sources[0].TreeHash = strings.Repeat("a", 40) }, e.CodeUnverifiedRef},
		{"changed timestamp", func(s *InputSnapshot) { s.Sources[0].SourceDateEpoch++ }, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := cloneSnapshot(t, *after.RuntimeInputs)
			tc.edit(&input)
			got, err := CompileSnapshot(t.Context(), input, after.GeneratorVersion)
			assertFactoryErrorCode(t, err, tc.code)
			if got != nil {
				t.Fatal("failed replay returned partial Plan")
			}
		})
	}
	writeSnapshotFixtureFile(t, root, "config/alpha.json", []byte("substituted member"))
	assertFactoryErrorCode(t, CheckFrozenAuthoredInputs(t.Context(), after, root), e.CodePipelineDrift)
}
