// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func baseEnvironment() authored.Environment {
	_, env := fixtureInputs("runtime", "worker", "")
	return env
}

// TestDeliveryRuntimeValidation covers ADR-18 §8's Environment.Runtime home for the
// runtime env: it is optional, defaults to podman, and demands digest-pinned images.
func TestDeliveryRuntimeValidation(t *testing.T) {
	if baseEnvironment().RuntimeBinary() != "podman" {
		t.Fatal("an unset runtime must default to rootless podman")
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
	for _, runtimeName := range []string{"", "docker"} {
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
			want := "podman"
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
