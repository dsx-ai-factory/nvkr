// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func baseEnvironment() authored.Environment {
	_, env := fixtureInputs("runtime", "worker", "")
	return env
}

// TestDeliveryRuntimeValidation covers ADR-18 §8's Environment.Runtime home for the
// runtime env: it is optional, defaults to docker, and demands digest-pinned images.
func TestDeliveryRuntimeValidation(t *testing.T) {
	if baseEnvironment().RuntimeBinary() != "docker" {
		t.Fatal("an unset runtime must default to docker")
	}
	ok := baseEnvironment()
	ok.Runtime = authored.Runtime{Runtime: "docker", RuntimeImage: "quay.io/podman/stable@sha256:" + strings.Repeat("a", 64)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid runtime profile rejected: %v", err)
	}
	if ok.RuntimeBinary() != "docker" {
		t.Fatal("configured runtime binary not honored")
	}
	unpinned := baseEnvironment()
	unpinned.Runtime = authored.Runtime{HelperImage: "ghcr.io/dsx-ai-factory/nvkr-exec:latest"}
	if err := unpinned.Validate(); err == nil {
		t.Fatal("an unpinned helperImage must be rejected")
	}
	badRuntime := baseEnvironment()
	badRuntime.Runtime = authored.Runtime{Runtime: "containerd"}
	if err := badRuntime.Validate(); err == nil {
		t.Fatal("an unsupported runtime binary must be rejected")
	}
}

// The compact helper consumes the admitted Environment's default/overridden
// runtime. The workflow contains selectors rather than inline runtime launchers.
func TestDeliveryRuntimeSourcesGeneratedRuntime(t *testing.T) {
	for _, runtimeName := range []string{"", "docker", "podman"} {
		t.Run("runtime="+runtimeName, func(t *testing.T) {
			plan, node, calls := githubRunnerRuntimeFixtureFor(t, runtimeName, "exit 0\n")
			bundle, err := Render(context.Background(), plan, EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			workflow := string(expandedGitHubWorkflow(t, bundle))
			if !strings.Contains(workflow, githubRunnerCommand(plan, "run-node", "node", node.ID)) || strings.Contains(workflow, "podman run") || strings.Contains(workflow, "docker run") {
				t.Fatal("workflow does not delegate runtime selection to helper")
			}
			if err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil || !strings.Contains(string(raw), "CALL\nrun\n") {
				t.Fatalf("selected runtime was not executed: %s %v", raw, err)
			}
			want := "docker"
			if runtimeName != "" {
				want = runtimeName
			}
			if plan.Environment.RuntimeBinary() != want {
				t.Fatalf("runtime=%s want%s", plan.Environment.RuntimeBinary(), want)
			}
		})
	}
}

func TestGitHubDockerUtilityPreservesRunnerOwnership(t *testing.T) {
	for _, runtime := range []string{"docker", "podman"} {
		t.Run(runtime, func(t *testing.T) {
			plan, node, calls := githubRunnerRuntimeFixtureFor(t, runtime, "exit 0\n")
			if err := RunGitHubNode(t.Context(), plan, node.ID, GitHubStepOptions{}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			for _, argument := range []string{"--user\n" + localContainerUser() + "\n", "-e\nHOME=/tmp\n"} {
				if strings.Contains(string(raw), argument) != (runtime == "docker") {
					t.Fatalf("ownership argument %q incorrectly scoped: %s", argument, raw)
				}
			}
		})
	}
}

func TestLocalRuntimeFollowsEnvironmentBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name, environment, override string
		conflict                    bool
	}{
		{name: "default"},
		{name: "inherit Podman", environment: "podman"},
		{name: "explicit Docker", environment: "docker", override: "docker"},
		{name: "Podman path", environment: "podman", override: "/opt/bin/podman"},
		{name: "conflicting Docker", environment: "podman", override: "docker", conflict: true},
		{name: "conflicting Podman", environment: "docker", override: "podman", conflict: true},
		{name: "unknown", override: "nerdctl", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			plan := githubTestPlan(t)
			plan.Environment.Runtime.Runtime = tc.environment
			digest, err := plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest = digest
			for _, dryRun := range []bool{true, false} {
				if !dryRun && !tc.conflict {
					continue
				}
				options := LocalRunOptions{Workspace: ".nvkr/runtime-selection", Runtime: tc.override, DryRun: dryRun}
				for _, admission := range []bool{true, false} {
					if admission {
						err = AdmitLocal(t.Context(), plan, options)
					} else {
						var result *LocalRunResult
						result, err = RunLocal(t.Context(), plan, options)
						if result != nil {
							_ = result.Close()
						}
					}
					if tc.conflict {
						var coded *e.Error
						if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
							t.Fatalf("conflict returned %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(filepath.Join(root, ".nvkr")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("admission created output: %v", err)
					}
				}
			}
		})
	}
}

func TestRuntimeSelectionIdentityBoundaries(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("runtime-identity", "worker", "ubuntu-24.04")
	implicit, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	before, err := implicit.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if implicit.Environment.RuntimeBinary() != "docker" || implicit.Environment.Runtime.Runtime != "" {
		t.Fatal("default must resolve without rewriting authored bytes")
	}
	if err := AdmitLocal(t.Context(), implicit, LocalRunOptions{Workspace: ".nvkr/runtime-identity", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	after, err := implicit.Digest()
	if err != nil || before != after {
		t.Fatalf("selection mutated Plan identity: %v", err)
	}
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			environment.Runtime.Runtime = name
			explicit, err := Compile(t.Context(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			digest, err := explicit.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if explicit.EnvironmentDigest == implicit.EnvironmentDigest || digest == before {
				t.Fatal("authored runtime must change Environment and Plan identity")
			}
			if explicit.DefinitionDigest != implicit.DefinitionDigest || len(explicit.Builds) != len(implicit.Builds) {
				t.Fatal("runtime changed Definition or Build selection")
			}
			for i, build := range explicit.Builds {
				if build.BuildDigest != implicit.Builds[i].BuildDigest {
					t.Fatal("runtime changed logical Build identity")
				}
			}
		})
	}
}
