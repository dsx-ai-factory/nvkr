// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestOSWorkflowToolchainLockGate(t *testing.T) {
	var source osWorkflowStep
	for _, step := range osValidationWorkflow(t)["select"].Steps {
		if step.Name == "Resolve the recipe-owned NVKE source pin" {
			source = step
		}
	}
	// Execute the actual workflow expression, not a second implementation of
	// its trust policy. These delimiters identify its existing literal jq input.
	const invocation = "jq -n -e --slurpfile candidate \"${candidate_lock}\" \\\n  --slurpfile reviewed toolchains/nvkr-packer/toolchain.lock.json '\n"
	_, gateAndRemainder, invocationFound := strings.Cut(source.Run, invocation)
	if !invocationFound {
		t.Fatal("workflow lock gate must invoke jq -n -e with the actual candidate and reviewed files")
	}
	gateTail, _, gateFound := strings.Cut(gateAndRemainder, "pin=\"$(bin/nvkr catalog source-pin")
	if !gateFound {
		t.Fatal("workflow lock gate boundary is missing")
	}
	gateScript := invocation + gateTail
	_, tail, found := strings.Cut(source.Run, "--slurpfile reviewed toolchains/nvkr-packer/toolchain.lock.json '\n")
	if !found {
		t.Fatal("workflow lock gate is missing")
	}
	expression, _, found := strings.Cut(tail, "' >/dev/null || {")
	if !found {
		t.Fatal("workflow lock gate closing delimiter is missing")
	}
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	reviewed := filepath.Join(root, "toolchains/nvkr-packer/toolchain.lock.json")
	raw, err := os.ReadFile(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	validID := "sha256:" + strings.Repeat("a", 64)
	for _, row := range []struct {
		name       string
		mutate     func(map[string]any)
		duplicate  bool
		accept     bool
		diagnostic string
	}{
		{name: "checked-in Docker-only lock", accept: true},
		{name: "observed native Docker IDs", mutate: func(lock map[string]any) {
			lock["localDockerImages"] = map[string]any{"amd64": validID, "arm64": validID}
		}, accept: true},
		{name: "missing Docker IDs", mutate: func(lock map[string]any) { delete(lock, "localDockerImages") }, diagnostic: "missing localDockerImages"},
		{name: "missing native architecture", mutate: func(lock map[string]any) {
			lock["localDockerImages"] = map[string]any{"arm64": validID}
		}, diagnostic: "expected exactly amd64 and arm64"},
		{name: "additional architecture", mutate: func(lock map[string]any) {
			lock["localDockerImages"] = map[string]any{"amd64": nil, "arm64": nil, "riscv64": nil}
		}, diagnostic: "expected exactly amd64 and arm64"},
		{name: "malformed image ID", mutate: func(lock map[string]any) {
			lock["localDockerImages"] = map[string]any{"amd64": nil, "arm64": "mutable:tag"}
		}, diagnostic: "image IDs must be null or exact sha256 digests"},
		{name: "non-string image ID", mutate: func(lock map[string]any) {
			lock["localDockerImages"] = map[string]any{"amd64": nil, "arm64": 1}
		}, diagnostic: "image IDs must be null or exact sha256 digests"},
		{name: "legacy Podman IDs", mutate: func(lock map[string]any) {
			lock["localPodmanImages"] = map[string]any{"amd64": nil, "arm64": nil}
		}, diagnostic: "legacy localPodmanImages is unsupported"},
		{name: "legacy local IDs", mutate: func(lock map[string]any) {
			delete(lock, "localDockerImages")
			lock["localImages"] = map[string]any{"amd64": nil, "arm64": nil}
		}, diagnostic: "legacy localImages is unsupported"},
		{name: "mixed legacy local IDs", mutate: func(lock map[string]any) {
			lock["localImages"] = map[string]any{"amd64": nil, "arm64": nil}
		}, diagnostic: "legacy localImages is unsupported"},
		{name: "source pin substitution", mutate: func(lock map[string]any) {
			lock["validatedNVKE"].(map[string]any)["commit"] = strings.Repeat("a", 40)
		}, diagnostic: "mismatched locked fields: validatedNVKE"},
		{name: "archive pin substitution", mutate: func(lock map[string]any) {
			lock["go"].(map[string]any)["archives"].(map[string]any)["arm64"] = validID
		}, diagnostic: "mismatched locked fields: go"},
		{name: "additional trusted field", mutate: func(lock map[string]any) { lock["unknown"] = true }, diagnostic: "mismatched locked fields: unknown"},
		{name: "duplicate document", duplicate: true, diagnostic: "exactly one JSON document"},
	} {
		t.Run(row.name, func(t *testing.T) {
			var lock map[string]any
			if err := json.Unmarshal(raw, &lock); err != nil {
				t.Fatal(err)
			}
			if row.mutate != nil {
				row.mutate(lock)
			}
			candidateBytes, err := json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			if row.duplicate {
				candidateBytes = append(append(candidateBytes, '\n'), candidateBytes...)
			}
			candidate := filepath.Join(t.TempDir(), "candidate.json")
			if err := os.WriteFile(candidate, candidateBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "jq", "-n", "-e", "--slurpfile", "candidate", candidate,
				"--slurpfile", "reviewed", reviewed, expression)
			output, err := cmd.CombinedOutput()
			if (err == nil) != row.accept {
				t.Fatalf("workflow gate accepted=%t, want %t: %v: %s", err == nil, row.accept, err, output)
			}
			if !row.accept {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || strings.TrimSpace(string(output)) != "false" {
					t.Fatalf("gate did not reject by policy: %v: %s", err, output)
				}
			}
			// Execute the real shell invocation and its diagnostic handler too.
			// Policy rows alone cannot detect a missing workflow jq -e flag.
			cmd = exec.CommandContext(t.Context(), "bash", "-euc", gateScript)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "candidate_lock="+candidate)
			output, err = cmd.CombinedOutput()
			if (err == nil) != row.accept || row.diagnostic != "" && !strings.Contains(string(output), row.diagnostic) {
				t.Fatalf("workflow invocation/diagnostic accepted=%t, want %t, diagnostic=%q: %v: %s", err == nil, row.accept, row.diagnostic, err, output)
			}
			for _, line := range strings.Split(string(output), "\n") {
				if strings.TrimSpace(line) == "mismatched locked fields:" {
					t.Fatal("workflow printed an empty locked-field mismatch diagnostic")
				}
			}
		})
	}
}

type osWorkflowStep struct {
	Name, Run, Uses, ID, If string
	Env                     map[string]string
	With                    map[string]string
}

type osWorkflowJob struct {
	If          string
	Permissions map[string]string
	Outputs     map[string]string
	Steps       []osWorkflowStep
}

func osValidationWorkflow(t *testing.T) map[string]osWorkflowJob {
	t.Helper()
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "nvke-os-validation.yaml"))
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
			requiresKVMTest = step.Env["NVKR_REQUIRE_KVM_TEST"] == "1" &&
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
	if jobs["fetch-nvke-source"].If != "needs.select.outputs.mode == 'full'" ||
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
		{name: "candidate private retention forbidden", mode: "full", ref: "refs/heads/main", commit: main, retain: "true"},
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
				"GH_TOKEN=", "REPOSITORY=dsx-ai-factory/nvkr", "GITHUB_OUTPUT="+filepath.Join(t.TempDir(), "outputs"))
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
		case "Build one QCOW2 through the NVKR lifecycle":
			build = step
		case "Record exact native toolchain image ID":
			record = step
		}
	}
	if build.ID != "lifecycle" || record.If != "always() && (steps.lifecycle.outcome == 'success' || steps.lifecycle.outcome == 'failure')" {
		t.Fatal("toolchain recording must run only after an executed lifecycle step")
	}
	var toolchainID, recordCondition string
	for _, step := range osValidationWorkflow(t)["validate-toolchain"].Steps {
		switch step.Name {
		case "Build and validate pinned native toolchain":
			toolchainID = step.ID
		case "Record exact native toolchain image ID":
			recordCondition = step.If
		}
	}
	if toolchainID != "toolchain" || recordCondition != "always() && steps.toolchain.outcome == 'success'" {
		t.Fatal("toolchain-only recording must not mask a failed or skipped toolchain build")
	}
	for _, test := range []struct {
		name, plan, outcome, image string
		wantError                  bool
		missingImage               bool
		wantOutput                 string
	}{
		{name: "failure before Plan", outcome: "failure", image: "sha256:observed", wantOutput: "No executable Plan"},
		{name: "failure after Plan", outcome: "failure", image: "sha256:observed", plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`},
		{name: "successful lifecycle retains image ID", outcome: "success", image: "sha256:observed", plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`},
		{name: "invalid Plan remains an error", outcome: "failure", plan: `{}`, wantError: true, wantOutput: "Executable Plan must identify exactly one vm-image toolchain"},
		{name: "Plan without disk toolchain remains an error", outcome: "failure", plan: `{"builds":[{"executionGraph":{"nodes":[]}}]}`, wantError: true, wantOutput: "Executable Plan must identify exactly one vm-image toolchain"},
		{name: "successful lifecycle requires Plan", outcome: "success", wantError: true, wantOutput: "Successful lifecycle did not retain its executable Plan"},
		{name: "mismatched toolchain remains an error", outcome: "failure", image: "sha256:substituted", plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`, wantError: true, wantOutput: "Observed toolchain image does not match"},
		{name: "missing toolchain remains an error", outcome: "failure", missingImage: true, plan: `{"builds":[{"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","toolchain":"sha256:observed"}]}}]}`, wantError: true, wantOutput: "Executable Plan toolchain image is not available locally"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for path, body := range map[string]string{
				"scripts/nvke-os-local.sh": "#!/bin/sh\necho primary-fetch-error >&2\nexit 20\n",
				"bin/docker":               "#!/bin/sh\nif test \"$MISSING_IMAGE\" = true; then echo 'No such image' >&2; exit 1; fi\nprintf '%s\\n' \"$OBSERVED_IMAGE\"\n",
			} {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(root, ".nvkr/evidence"), 0o700); err != nil {
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
				path := filepath.Join(root, ".nvkr/runs/gha-arm64-fixture/.runner/plan.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(test.plan), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd = exec.CommandContext(t.Context(), "bash", "-c", record.Run)
			cmd.Dir = root
			missingImage := "false"
			if test.missingImage {
				missingImage = "true"
			}
			cmd.Env = append(os.Environ(), "ARCH=arm64", "LIFECYCLE_OUTCOME="+test.outcome, "OBSERVED_IMAGE="+test.image,
				"MISSING_IMAGE="+missingImage, "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
			output, err = cmd.CombinedOutput()
			if (err != nil) != test.wantError {
				t.Fatalf("toolchain recording error=%v, want error=%t: %s", err, test.wantError, output)
			}
			if test.wantOutput != "" && !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("missing expected diagnostic %q: %s", test.wantOutput, output)
			}
			if test.plan != "" && !test.wantError {
				imageID, err := os.ReadFile(filepath.Join(root, ".nvkr/evidence/toolchain-arm64.image-id"))
				if err != nil || string(imageID) != test.image+"\n" {
					t.Fatalf("retained image ID=%q, error=%v, want %q", imageID, err, test.image)
				}
			}
		})
	}
}

func TestOSWorkflowPrivateTransferCredentialBoundary(t *testing.T) {
	jobs := osValidationWorkflow(t)
	const transfer = "./.github/nvkr-run/actions/private-transfer"
	for _, test := range []struct {
		job, mode, token string
	}{
		{"fetch-nvke-source", "upload", "${{ secrets.NVKR_TRANSFER_WRITE }}"},
		{"validate-full", "download", "${{ secrets.NVKR_TRANSFER_READ }}"},
	} {
		t.Run(test.job, func(t *testing.T) {
			trusted, transferred, candidate := -1, -1, -1
			for index, step := range jobs[test.job].Steps {
				if step.Name == "Check out trusted transport implementation" {
					if !strings.HasPrefix(step.Uses, "actions/checkout@") || step.With["ref"] != "${{ github.sha }}" || step.With["persist-credentials"] != "false" {
						t.Fatal("private source transport must execute the trusted workflow checkout")
					}
					trusted = index
				}
				if step.Name == "Check out the selected NVKR candidate" {
					candidate = index
				}
				if step.Uses == transfer && step.With["mode"] == test.mode {
					if step.With["token"] != test.token || step.With["registry"] != "${{ vars.NVKR_TRANSFER_PACKAGE }}" {
						t.Fatal("private transfer lost exact registry and phase credential binding")
					}
					if test.mode == "download" && step.With["expected-index"] != "${{ toJSON(needs) }}" {
						t.Fatal("source transfer does not bind the successful producing job's immutable digest")
					}
					transferred = index
				}
				if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && (strings.Contains(step.With["path"], ".qcow2") || strings.Contains(step.With["path"], "source.tar")) {
					t.Fatal("payload bytes returned to public workflow artifact storage")
				}
			}
			if trusted < 0 || transferred <= trusted || candidate >= 0 && transferred >= candidate {
				t.Fatal("source credential transfer must finish before candidate checkout and execution")
			}
		})
	}
	if jobs["fetch-nvke-source"].Outputs["nvkr-transfer-index"] != "${{ steps.nvkr-transfer-index.outputs.index }}" {
		t.Fatal("source fetch does not expose its immutable transfer index")
	}
	var retained, diagnostics bool
	for _, step := range jobs["validate-full"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
			(strings.Contains(step.With["path"], "diagnostics/packer") || strings.Contains(step.With["path"], ".nvkr/evidence/**")) {
			t.Fatal("raw producer directories must not reach public workflow artifacts")
		}
		if step.With["token"] != "${{ secrets.NVKR_TRANSFER_WRITE }}" {
			continue
		}
		if step.Name == "Upload raw Packer diagnostics privately" {
			diagnostics = true
			if step.Uses != transfer || step.With["mode"] != "upload" ||
				step.If != "always() && needs.select.outputs.commit == github.sha && steps.private-diagnostics.outputs.present == 'true'" ||
				!strings.HasSuffix(step.With["path"], "/diagnostics/packer") {
				t.Fatal("raw diagnostic write credentials can reach a selected candidate's runner")
			}
			continue
		}
		retained = true
		if step.Uses != transfer || step.With["mode"] != "upload" ||
			step.If != "success() && needs.select.outputs.retain_qcow2 == 'true' && needs.select.outputs.commit == github.sha" {
			t.Fatal("QCOW2 write credential can reach a selected candidate's runner")
		}
	}
	if !retained {
		t.Fatal("trusted QCOW2 retention has no private transfer")
	}
	if !diagnostics {
		t.Fatal("trusted raw Packer diagnostics have no private retention")
	}
}

func TestWorkflowsDoNotRetainPublicCompiledCaches(t *testing.T) {
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	for _, filename := range []string{"merge-gate.yaml", "vulnerability-scan.yaml"} {
		t.Run(filename, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", filename))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct{ Jobs map[string]osWorkflowJob }
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			for jobID, job := range workflow.Jobs {
				codeQLAdmitted := false
				for _, step := range job.Steps {
					if step.Name == "Admit CodeQL without database artifact uploads" {
						if step.If != "" || !strings.Contains(step.Run, `"${RUNNER_DEBUG:-0}" = '1'`) ||
							!strings.Contains(step.Run, "exit 1") {
							t.Fatalf("%s does not reject automatic CodeQL debug database uploads", jobID)
						}
						for _, debug := range []string{"0", "1"} {
							command := exec.CommandContext(t.Context(), "bash", "-eu", "-c", step.Run)
							command.Env = append(os.Environ(), "RUNNER_DEBUG="+debug)
							output, err := command.CombinedOutput()
							if (err != nil) != (debug == "1") || debug == "1" && !strings.Contains(string(output), "NVKR-E-ENGINE-CAPABILITY") {
								t.Fatalf("%s debug admission (%s): err=%v output=%s", jobID, debug, err, output)
							}
						}
						codeQLAdmitted = true
					}
					switch {
					case strings.HasPrefix(step.Uses, "actions/cache"):
						t.Fatalf("%s retains an Actions cache that can contain compiled Go objects", jobID)
					case strings.HasPrefix(step.Uses, "actions/setup-go@"):
						if step.With["cache"] != "false" {
							t.Fatalf("%s leaves setup-go compiled caches enabled", jobID)
						}
					case strings.HasPrefix(step.Uses, "golangci/golangci-lint-action@"):
						if step.With["skip-cache"] != "true" {
							t.Fatalf("%s leaves golangci compiled analysis caches enabled", jobID)
						}
					case strings.HasPrefix(step.Uses, "github/codeql-action/analyze@"):
						if step.With["upload-database"] != "false" {
							t.Fatalf("%s retains its compiled CodeQL database", jobID)
						}
					case strings.HasPrefix(step.Uses, "github/codeql-action/init@"):
						if !codeQLAdmitted || step.With["debug"] != "false" || step.With["trap-caching"] != "false" ||
							step.With["dependency-caching"] != "false" || step.Env["CODEQL_OVERLAY_DATABASE_MODE"] != "none" {
							t.Fatalf("%s permits CodeQL database artifacts or compiled caches", jobID)
						}
					}
				}
			}
		})
	}
}
