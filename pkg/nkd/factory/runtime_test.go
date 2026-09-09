package factory

import (
	"context"
	"strings"
	"testing"
)

func baseExecutionSettings() ExecutionSettings {
	return ExecutionSettings{
		APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "env"},
		Targets:  map[string]Target{"reg": {Kind: TargetOCI, Ref: "r.example/x", Anonymous: true}},
		Routes:   []Route{{Name: "r", Input: "target/component", Target: "reg"}},
	}
}

// TestDeliveryRuntimeValidation covers ADR-18 §8 §1's ExecutionSettings.Runtime home for the
// runtime env: it is optional, defaults to podman, and demands digest-pinned images.
func TestDeliveryRuntimeValidation(t *testing.T) {
	if baseExecutionSettings().RuntimeBinary() != "podman" {
		t.Fatal("an unset runtime must default to rootless podman")
	}
	ok := baseExecutionSettings()
	ok.Runtime = RuntimeProfile{Runtime: "docker", RuntimeImage: "quay.io/podman/stable@sha256:" + strings.Repeat("a", 64)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid runtime profile rejected: %v", err)
	}
	if ok.RuntimeBinary() != "docker" {
		t.Fatal("configured runtime binary not honored")
	}
	unpinned := baseExecutionSettings()
	unpinned.Runtime = RuntimeProfile{HelperImage: "ghcr.io/nvidia/nkd-exec:latest"}
	if err := unpinned.Validate(); err == nil {
		t.Fatal("an unpinned helperImage must be rejected")
	}
	badRuntime := baseExecutionSettings()
	badRuntime.Runtime = RuntimeProfile{Runtime: "containerd"}
	if err := badRuntime.Validate(); err == nil {
		t.Fatal("an unsupported runtime binary must be rejected")
	}
}

// TestDeliveryRuntimeSourcesGeneratedRuntime proves `run generate` sources the
// container runtime from internal Environment execution settings (default podman; overridable to
// docker) instead of a hardcoded value.
func TestDeliveryRuntimeSourcesGeneratedRuntime(t *testing.T) {
	plan := gitlabTestPlan(t)
	bundle, err := Render(context.Background(), plan, EngineGitLab)
	if err != nil {
		t.Fatal(err)
	}
	wf := string(bundle.Files[GitLabWorkflowPath])
	// The runtime flows to BOTH the Model-B launch line (IR build/fetch nodes orchestrate
	// their pinned container via `exec-node --launch --runtime <rt>`) and the glue wrapper
	// (`<rt> run`). Default is rootless podman; docker must appear in neither place.
	if !strings.Contains(wf, "exec-node --launch --runtime podman") || !strings.Contains(wf, "podman run") {
		t.Fatalf("default ExecutionSettings runtime must generate podman:\n%s", wf)
	}
	if strings.Contains(wf, "--runtime docker") || strings.Contains(wf, "docker run") {
		t.Fatalf("default ExecutionSettings runtime must not emit docker:\n%s", wf)
	}

	override := gitlabTestPlan(t)
	override.ExecutionSettings.Runtime = RuntimeProfile{Runtime: "docker"}
	digest, err := override.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	override.ExecutionSettingsDigest = digest
	bundle2, err := Render(context.Background(), override, EngineGitLab)
	if err != nil {
		t.Fatal(err)
	}
	wf2 := string(bundle2.Files[GitLabWorkflowPath])
	if !strings.Contains(wf2, "exec-node --launch --runtime docker") || !strings.Contains(wf2, "docker run") {
		t.Fatalf("ExecutionSettings runtime override to docker was not honored:\n%s", wf2)
	}
}
