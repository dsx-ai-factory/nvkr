package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubOperationLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		node Node
		want string
	}{
		{"image", Node{ID: "package/worker", Kind: NodePackage, PackageKind: PackageImage}, "Package container image: worker"},
		{"helm binding", Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, HelmImage: testHelmBinding("package/worker", "controller")}, "Bind image digest and package Helm: charts"},
		{"helm alone", Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm}, "Package Helm bundle: charts"},
		{"binary", Node{ID: "package/cli", Kind: NodePackage, PackageKind: PackageBinaryBundle}, "Package binaries: cli"},
		{"apt", Node{ID: "package/packages", Kind: NodePackage, PackageKind: PackageAPT}, "Package APT repository: packages"},
		{"publication bundle", Node{ID: "package/worker-release", Kind: NodePackage, PackageKind: PackageOCI}, "Package publication bundle: worker-release"},
		{"observation", Node{Kind: NodeVerify, BuildTarget: "worker"}, "Verify built artifacts: worker"},
		{"sbom", Node{Kind: NodeSBOM, BuildTarget: "worker"}, "Generate SBOMs: worker"},
		{"publish", Node{ID: "publish/worker", Kind: NodePublish}, "Publish worker"},
		{"sign", Node{ID: "sign/worker", Kind: NodeSign}, "Sign and attest release and deliverables: worker"},
		{"verify publication", Node{ID: "verify-publication/worker", Kind: NodeVerifyPublication}, "Verify release and deliverable signatures: worker"},
		{"tool", Node{ID: "tool/cosign", Kind: NodeAcquire}, "Acquire pinned cosign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubNodeLabel(&Plan{}, tc.node); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitHubOperationLogsPreserveExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		code   int
	}{
		{"success", "0", 0},
		{"failure", "23", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Exercise the emitted shell against an executable protocol fixture. This
			// checks logging/exit behavior, not a build, registry or signing service.
			if err := os.WriteFile(filepath.Join(dir, "nkd-exec"), []byte("#!/bin/sh\nprintf 'child output\\n'\nexit \"$TEST_STATUS\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			node := Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, Outputs: []string{"package/charts"}, HelmImage: testHelmBinding("package/image", "controller")}
			commands := githubNodeCommands(node, &Plan{})
			cmd := exec.CommandContext(t.Context(), "bash", "-c", strings.Join(commands, "\n"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TEST_STATUS="+tc.status, "NKD_CREDENTIAL=do-not-log-this")
			raw, err := cmd.CombinedOutput()
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if cmd.ProcessState.ExitCode() != tc.code {
				t.Fatalf("exit=%d, want %d: %s", cmd.ProcessState.ExitCode(), tc.code, raw)
			}
			log := string(raw)
			for _, want := range []string{"Starting: Bind image digest and package Helm: charts", "Node: package/charts", "child output"} {
				if !strings.Contains(log, want) {
					t.Errorf("missing %q in %s", want, log)
				}
			}
			if tc.code == 0 {
				if !strings.Contains(log, "Completed:") || !strings.Contains(log, "Produced: package/charts") || strings.Contains(log, "Failed:") {
					t.Fatalf("incorrect success log: %s", log)
				}
			} else if !strings.Contains(log, "Failed:") || !strings.Contains(log, "exit 23") || strings.Contains(log, "Completed:") || strings.Contains(log, "Produced:") {
				t.Fatalf("incorrect failure log: %s", log)
			}
			if strings.Contains(log, "do-not-log-this") {
				t.Fatal("credential leaked into operation log")
			}
		})
	}
}

func TestMaintainedWorkflowMakesImageBindingVisible(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	found := false
	for _, job := range document.Jobs {
		if strings.Contains(job.Name, "terminal-observe") || strings.Contains(job.Name, "fetch-source-") {
			t.Errorf("internal identity used as job label: %s", job.Name)
		}
		for _, step := range job.Steps {
			if step.Name == "" {
				t.Errorf("unnamed step in %s", job.Name)
			}
			if step.ID == githubJobID("package/controller-chart") {
				found = true
				if !strings.Contains(job.Name, "Helm image binding") || step.Name != "Bind image digest and package Helm: controller-chart" {
					t.Fatalf("binding hidden: %s / %s", job.Name, step.Name)
				}
			}
		}
	}
	if !found {
		t.Fatal("maintained workflow omitted Helm binding")
	}
}
