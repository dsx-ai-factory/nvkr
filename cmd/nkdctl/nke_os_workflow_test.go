// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type osWorkflowStep struct {
	Name, Run, Uses, ID, If string
	Env                     map[string]string
}

type osWorkflowJob struct {
	If          string
	Permissions map[string]string
	Steps       []osWorkflowStep
}

func osValidationWorkflow(t *testing.T) map[string]osWorkflowJob {
	t.Helper()
	root := filepath.Dir(filepath.Dir(demoScript(t, "nke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "nke-os-validation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]osWorkflowJob
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

func TestOSWorkflowPreflightSelection(t *testing.T) {
	jobs := osValidationWorkflow(t)
	var selection, preflight, full string
	var requiresKVMTest bool
	for _, step := range jobs["select"].Steps {
		if step.Name == "Validate selection" {
			selection = step.Run
		}
	}
	for _, step := range jobs["validate-kvm"].Steps {
		if step.Uses != "" {
			t.Fatal("permission probe must not check out source or invoke an external action")
		}
		if step.Name == "Admit native KVM runner" {
			preflight = step.Run
		}
	}
	for _, step := range jobs["validate-full"].Steps {
		if step.Name == "Admit native KVM runner" {
			full = step.Run
		}
		if step.Name == "Check guarded Packer dispatch on the admitted KVM runner" {
			requiresKVMTest = step.Env["NKD_REQUIRE_KVM_TEST"] == "1" &&
				strings.Contains(step.Run, "^TestRunPackerContainerClassifiesBuildExit$")
		}
	}
	if selection == "" || preflight == "" || preflight != full {
		t.Fatal("probe and full build must execute the same KVM admission script")
	}
	if !requiresKVMTest {
		t.Fatal("full validation must require the guarded Packer dispatch cases to execute")
	}
	probe := jobs["validate-kvm"]
	if probe.If != "needs.select.outputs.mode == 'preflight'" || probe.Permissions == nil || len(probe.Permissions) != 0 {
		t.Fatal("permission probe must be preflight-only with no token permissions")
	}
	if jobs["fetch-nke-source"].If != "needs.select.outputs.mode == 'full'" ||
		jobs["validate-full"].If != "needs.select.outputs.mode == 'full'" {
		t.Fatal("preflight must not admit a source-credential job or image build")
	}
	// A PR-gating checkout can be shallow and have no origin/main. Model the
	// selection history locally instead of depending on the caller's Git refs.
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("config", "user.name", "Workflow Test")
	git("config", "user.email", "workflow@example.invalid")
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "main fixture")
	main := git("rev-parse", "HEAD")
	git("update-ref", "refs/remotes/origin/main", main)
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "candidate fixture")
	head := git("rev-parse", "HEAD")
	for _, test := range []struct {
		name, mode, ref, commit, event, mirror, retain string
		want                                           bool
	}{
		{name: "exact branch probe", mode: "preflight", ref: "refs/heads/probe", commit: head, want: true},
		{name: "different workflow commit", mode: "preflight", ref: "refs/heads/probe", commit: main},
		{name: "full from branch", mode: "full", ref: "refs/heads/probe", commit: head},
		{name: "toolchain from branch", mode: "toolchain", ref: "refs/heads/probe", commit: head},
		{name: "tag probe", mode: "preflight", ref: "refs/tags/probe", commit: head},
		{name: "mirror probe", mode: "preflight", ref: "refs/heads/probe", commit: head, mirror: "59"},
		{name: "image upload probe", mode: "preflight", ref: "refs/heads/probe", commit: head, retain: "true"},
		{name: "main full dispatch", mode: "full", ref: "refs/heads/main", commit: main, want: true},
		{name: "main merge push", ref: "refs/heads/main", commit: main, event: "push", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := test.event
			if event == "" {
				event = "workflow_dispatch"
			}
			retain := test.retain
			if retain == "" {
				retain = "false"
			}
			eventSHA := head
			if event == "push" {
				eventSHA = main
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-c", selection)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "EVENT_NAME="+event, "EVENT_REF="+test.ref, "EVENT_SHA="+eventSHA,
				"DISPATCH_COMMIT="+test.commit, "DISPATCH_MODE="+test.mode, "DISPATCH_ARCH=both",
				"DISPATCH_MIRROR_PR="+test.mirror, "DISPATCH_RETAIN_QCOW2="+retain,
				"GH_TOKEN=", "REPOSITORY=dsx-ai-factory/nke-recipes", "GITHUB_OUTPUT="+filepath.Join(t.TempDir(), "outputs"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.want {
				t.Fatalf("selection error=%v, want accepted=%t: %s", err, test.want, output)
			}
		})
	}
}

func TestOSWorkflowRetainsPrimaryLifecycleFailure(t *testing.T) {
	var build, record osWorkflowStep
	for _, step := range osValidationWorkflow(t)["validate-full"].Steps {
		switch step.Name {
		case "Build one QCOW2 through the NKD lifecycle":
			build = step
		case "Record exact native toolchain image ID":
			record = step
		}
	}
	if build.ID != "lifecycle" || record.If != "always() && (steps.lifecycle.outcome == 'success' || steps.lifecycle.outcome == 'failure')" {
		t.Fatal("toolchain recording must run only after an executed lifecycle step")
	}
	for _, test := range []struct {
		name, plan, outcome, image string
		wantError                  bool
	}{
		{name: "failure before Plan", outcome: "failure", image: "sha256:observed"},
		{name: "failure after Plan", outcome: "failure", image: "sha256:observed", plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`},
		{name: "invalid Plan remains an error", outcome: "failure", plan: `{}`, wantError: true},
		{name: "successful lifecycle requires Plan", outcome: "success", wantError: true},
		{name: "mismatched toolchain remains an error", outcome: "failure", image: "sha256:substituted", plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for path, body := range map[string]string{
				"scripts/nke-os-local.sh": "#!/bin/sh\necho primary-fetch-error >&2\nexit 20\n",
				"bin/docker":              "#!/bin/sh\nprintf '%s\\n' \"$OBSERVED_IMAGE\"\n",
			} {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(root, ".nkd/evidence"), 0o700); err != nil {
				t.Fatal(err)
			}
			run := strings.NewReplacer("${{ matrix.arch }}", "arm64", "${{ needs.select.outputs.commit }}", "fixture").Replace(build.Run)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", run)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 20 || !strings.Contains(string(output), "primary-fetch-error") {
				t.Fatalf("primary lifecycle error was lost: %v: %s", err, output)
			}
			if test.plan != "" {
				path := filepath.Join(root, ".nkd/runs/gha-arm64-fixture/.runner/plan.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.plan), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd = exec.CommandContext(t.Context(), "bash", "-c", record.Run)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "ARCH=arm64", "LIFECYCLE_OUTCOME="+test.outcome, "OBSERVED_IMAGE="+test.image, "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
			output, err = cmd.CombinedOutput()
			if (err != nil) != test.wantError {
				t.Fatalf("toolchain recording error=%v, want error=%t: %s", err, test.wantError, output)
			}
			if test.plan == "" && !test.wantError && !strings.Contains(string(output), "No executable Plan") {
				t.Fatalf("missing Plan was not reported: %s", output)
			}
		})
	}
}
