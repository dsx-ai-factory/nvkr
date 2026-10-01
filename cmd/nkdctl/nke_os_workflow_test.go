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

func TestOSWorkflowPreflightSelection(t *testing.T) {
	root := filepath.Dir(filepath.Dir(demoScript(t, "nke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "nke-os-validation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If          string
			Permissions map[string]string
			Steps       []struct {
				Name, Run, Uses string
				Env             map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	var selection, preflight, full string
	var requiresKVMTest bool
	for _, step := range workflow.Jobs["select"].Steps {
		if step.Name == "Validate selection" {
			selection = step.Run
		}
	}
	for _, step := range workflow.Jobs["validate-kvm"].Steps {
		if step.Uses != "" {
			t.Fatal("permission probe must not check out source or invoke an external action")
		}
		if step.Name == "Admit native KVM runner" {
			preflight = step.Run
		}
	}
	for _, step := range workflow.Jobs["validate-full"].Steps {
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
	probe := workflow.Jobs["validate-kvm"]
	if probe.If != "needs.select.outputs.mode == 'preflight'" || probe.Permissions == nil || len(probe.Permissions) != 0 {
		t.Fatal("permission probe must be preflight-only with no token permissions")
	}
	if workflow.Jobs["fetch-nke-source"].If != "needs.select.outputs.mode == 'full'" ||
		workflow.Jobs["validate-full"].If != "needs.select.outputs.mode == 'full'" {
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
