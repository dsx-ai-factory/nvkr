package factory

import (
	"context"
	"strings"
	"testing"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
)

func baseEnvironment() authored.Environment {
	_, env := fixtureInputs("runtime", "worker", "")
	return env
}

// TestDeliveryRuntimeValidation covers ADR-18 §8 §1's Environment.Runtime home for the
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
	unpinned.Runtime = authored.Runtime{HelperImage: "ghcr.io/nvidia/nkd-exec:latest"}
	if err := unpinned.Validate(); err == nil {
		t.Fatal("an unpinned helperImage must be rejected")
	}
	badRuntime := baseEnvironment()
	badRuntime.Runtime = authored.Runtime{Runtime: "containerd"}
	if err := badRuntime.Validate(); err == nil {
		t.Fatal("an unsupported runtime binary must be rejected")
	}
}

// TestDeliveryRuntimeSourcesGeneratedRuntime proves `run generate` sources the
// container runtime from internal Environment execution settings (default podman; overridable to
// docker) instead of a hardcoded value.
func TestDeliveryRuntimeSourcesGeneratedRuntime(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := Render(context.Background(), plan, EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(bundle.Files[GitHubWorkflowPath])
	// Utility containers render the authored runtime. Producer runtime selection
	// is tested through the shared execution operation.
	if !strings.Contains(wf, "podman run") {
		t.Fatalf("default Environment runtime must generate podman:\n%s", wf)
	}
	if strings.Contains(wf, "--runtime docker") || strings.Contains(wf, "docker run") {
		t.Fatalf("default Environment runtime must not emit docker:\n%s", wf)
	}

	override := githubTestPlan(t)
	override.Environment.Runtime = authored.Runtime{Runtime: "docker"}
	digest, err := override.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	override.EnvironmentDigest = digest
	bundle2, err := Render(context.Background(), override, EngineGitHub)
	if err != nil {
		t.Fatal(err)
	}
	wf2 := string(bundle2.Files[GitHubWorkflowPath])
	if !strings.Contains(wf2, "docker run") {
		t.Fatalf("Environment runtime override to docker was not honored:\n%s", wf2)
	}
}

func TestGitHubDockerUtilityPreservesRunnerOwnership(t *testing.T) {
	for _, runtime := range []string{"docker", "podman"} {
		t.Run(runtime, func(t *testing.T) {
			plan := githubTestPlan(t)
			plan.Environment.Runtime.Runtime = runtime
			digest, err := plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest = digest
			bundle, err := RenderGitHub(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workflow := string(bundle.Files[GitHubWorkflowPath])
			owner := `--user "$(id -u):$(id -g)" -e HOME=/tmp`
			if strings.Contains(workflow, owner) != (runtime == "docker") {
				t.Fatalf("utility ownership missing or applied to the wrong runtime: %s", runtime)
			}
		})
	}
}
