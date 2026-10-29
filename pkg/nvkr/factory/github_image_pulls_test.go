// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestPrivateImageCredentialLifetime(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "pull failure"}[failure], func(t *testing.T) {
			var directory string
			err := pullGitHubImages(t.Context(), []string{"ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)}, "linux/amd64", "reader", "synthetic-token",
				func(ctx context.Context, config, platform, image string) error {
					directory = config
					if _, ok := ctx.Deadline(); !ok || platform != "linux/amd64" || !strings.Contains(image, "@sha256:") {
						t.Fatal("unbounded or unpinned pull")
					}
					for _, path := range []string{config, filepath.Join(config, "config.json")} {
						info, err := os.Stat(path)
						if err != nil || info.Mode().Perm()&0o077 != 0 {
							t.Fatalf("authentication is not private: %s / %v", path, err)
						}
					}
					if failure {
						return errors.New("synthetic pull failure")
					}
					return nil
				})
			if directory == "" {
				t.Fatal("pull was not attempted")
			}
			if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
				t.Fatal("credential directory survived the preparation step")
			}
			if failure {
				assertFactoryErrorCode(t, err, e.CodeFetchFailed)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrivateImagePullCommandDoesNotInheritCredentials(t *testing.T) {
	for _, key := range []string{sourceTokenEnv, githubImageToken, "NVKR_CREDENTIAL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "DOCKER_HOST", "DOCKER_CONFIG"} {
		t.Setenv(key, "must-not-inherit")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	image := "ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)
	command := githubImagePullCommand(t.Context(), "/private-auth", "linux/arm64", image)
	if !slices.Equal(command.Args, []string{"docker", "--config", "/private-auth", "pull", "--platform", "linux/arm64", image}) ||
		!slices.Equal(command.Env, []string{"PATH=/usr/bin:/bin", "DOCKER_HOST=unix:///var/run/docker.sock"}) {
		t.Fatalf("pull escaped its fixed argument/environment boundary: %v / %v", command.Args, command.Env)
	}
}

func TestPrivateImagePullRejectsCredentialAndCancellationBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name, user, token string
		cancel            bool
		want              e.Code
	}{
		{"missing user", "", "token", false, e.CodeFetchFailed},
		{"missing token", "reader", "", false, e.CodeFetchFailed},
		{"colon user", "reader:admin", "token", false, e.CodeFetchFailed},
		{"newline user", "reader\n", "token", false, e.CodeFetchFailed},
		{"carriage return user", "reader\r", "token", false, e.CodeFetchFailed},
		{"newline token", "reader", "token\n", false, e.CodeFetchFailed},
		{"carriage return token", "reader", "token\r", false, e.CodeFetchFailed},
		{"canceled", "reader", "token", true, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancel {
				cancel()
			}
			err := pullGitHubImages(ctx, []string{"image"}, "linux/amd64", test.user, test.token,
				func(context.Context, string, string, string) error {
					t.Fatal("denied preparation reached runtime")
					return nil
				})
			assertFactoryErrorCode(t, err, test.want)
		})
	}
}

func TestPrivateToolchainPublicationBoundary(t *testing.T) {
	plan := arcTestPlan(t, "linux/amd64")
	for _, test := range []struct {
		name, publication, image string
		development              bool
		want                     bool
	}{
		{"current", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, true},
		{"disk family current subject", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, true},
		{"disk family prefix lookalike", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks-mirror", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"disk family child prefix", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks/other", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"core family prefix", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/core", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"services family prefix", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/services", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"telemetry family prefix", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/telemetry", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"foreign disk family owner", "ghcr.io/other/nvkr/m2-candidate/disks", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"disk family foreign toolchain owner", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks", "ghcr.io/other/nvkr/m2-candidate/toolchains/packer", false, false},
		{"disk family unsupported repository", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, false},
		{"disk family non-toolchain subject", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/other/packer", false, false},
		{"unsupported repository", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, false},
		{"nightly current subject", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, true},
		{"nightly unsupported repository", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, false},
		{"upstream nightly current subject", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-upstream-ovn-arm64", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, true},
		{"upstream nightly unsupported repository", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-upstream-ovn-arm64", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, false},
		{"nightly prefix lookalike", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64-mirror", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"nightly child prefix", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64/other", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"foreign nightly owner", "ghcr.io/other/nvkr/nightly/nvke-nightly-arm64", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"foreign toolchain owner", "ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64", "ghcr.io/other/nvkr/m2-candidate/toolchains/packer", false, false},
		{"different publication owner", "ghcr.io/example/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
		{"sibling repository", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nvkr-mirror/m2-candidate/toolchains/packer", false, false},
		{"non-toolchain subject", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/other/packer", false, false},
		{"development current toolchain", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", true, true},
		{"development unsupported repository", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", true, false},
		{"development different owner", "ghcr.io/example/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", true, false},
		{"development sibling repository", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr-mirror/m2-candidate/toolchains/packer", true, false},
		{"development non-toolchain subject", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/other/packer", true, false},
		{"development prefix without policy", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan.Environment.Publication.RepositoryPrefix = test.publication
			plan.Environment.GitHubDevelopment = test.development
			image := test.image + "@sha256:" + strings.Repeat("a", 64)
			if got := githubPrivateToolchain(plan, image); got != test.want {
				t.Fatalf("private preparation = %t, want %t", got, test.want)
			}
			if githubPrivateToolchain(plan, test.image+":latest") {
				t.Fatal("unpinned image admitted")
			}
		})
	}
	plan.Environment.GitHubARC = "unsupported"
	if githubPrivateToolchain(plan, "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer@sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("unsupported ARC policy received private image preparation")
	}
}

// The extended split matrix calls this on each actual non-disk family bundle,
// avoiding another full render matrix in the default suite. Publication readers
// remain legitimate; this assertion concerns only private toolchain preparation.
func assertNoPrivateToolchainPreparation(t *testing.T, bundle *GeneratedBundle) {
	t.Helper()
	workflow := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
	for id, job := range workflow.Jobs {
		for _, step := range job.Steps {
			for _, input := range []string{"nvkr-toolchain-read-token", "nvkr-toolchain-read-user"} {
				if _, exists := step.With[input]; exists {
					t.Fatalf("non-disk family passes toolchain reader input in %s/%s", id, step.ID)
				}
			}
		}
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	assertNoReader := func(scope string, environment map[string]string) {
		for _, key := range []string{githubImageToken, githubImageUser} {
			if _, exists := environment[key]; exists {
				t.Fatalf("non-disk family exposes toolchain reader in %s", scope)
			}
		}
	}
	assertNoReader("workflow", document.Env)
	for id, job := range document.Jobs {
		assertNoReader(id, job.Env)
		for _, step := range job.Steps {
			assertNoReader(id+"/"+step.ID, step.Env)
			if strings.Contains(step.Run, "factory github prepare-images ") ||
				strings.Contains(step.Run, githubImageToken) || strings.Contains(step.Run, githubImageUser) {
				t.Fatalf("non-disk family retained private toolchain preparation in %s/%s", id, step.ID)
			}
		}
	}
}

func TestMaintainedNamedPrivateImageCredentialBoundary(t *testing.T) {
	// The combined and nightly bundles share their assertions with
	// TestMaintainedGitHubBundles. Keep the disks-family namespace covered too.
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run-nvke-disks/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubPrivateImageCredentialBoundary(t, plan, bundle, 4)
}

// Maintained product checks share their already-rendered bundle with the entry
// workflow contract. Negative mutations use the focused fixture below.
func assertGitHubPrivateImageCredentialBoundary(t *testing.T, plan *Plan, bundle *GeneratedBundle, wantDisks int) {
	t.Helper()
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	disks, fetches := 0, 0
	for _, job := range document.Jobs {
		prepared := false
		prepareIndex, executionIndex := -1, -1
		for index, step := range job.Steps {
			if step.Env[githubImageToken] != "" {
				if prepared || job.Permissions["packages"] != "" || job.Permissions["id-token"] != "" || job.Env[githubImageToken] != "" || step.Env[sourceTokenEnv] != "" ||
					step.Env[githubImageToken] != "${{ secrets.NVKR_REGISTRY_READ }}" || !strings.Contains(step.Run, "factory github prepare-images ") {
					t.Fatal("toolchain credential escaped its separate read-only preparation step")
				}
				prepared, prepareIndex = true, index
				if job.Environment == githubARCEnvironment {
					disks++
				} else {
					fetches++
				}
				continue
			}
			if executionIndex < 0 && (strings.Contains(step.Run, "factory github run-node ") || strings.Contains(step.Run, "factory github run-disk ")) {
				executionIndex = index
			}
		}
		if prepared && executionIndex <= prepareIndex {
			t.Fatal("toolchain preparation did not precede source or producer execution")
		}
	}
	if disks != wantDisks || fetches == 0 {
		t.Fatalf("missing private preparation for native disks or their source vendors: %d/%d, want %d disks", disks, fetches, wantDisks)
	}
}

func TestPrivateImageCredentialBoundary(t *testing.T) {
	plan := privateImageTestPlan(t, ProductionRegistryWriteCredential)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubPrivateImageCredentialBoundary(t, plan, bundle, 1)
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobID := ""
	for _, stage := range stages {
		if stage.DiskOutput != "" && len(githubStagePrivateImages(plan, stage)) > 0 {
			jobID = stage.ID
			break
		}
	}
	if jobID == "" {
		t.Fatal("fixture lacks a native private-image preparation job")
	}
	member, part := githubTestSharedInvocation(t, bundle, jobID, "images", jobID)
	// Missing, empty and substituted caller inputs are exhaustively covered at
	// githubProjectInterfaceRole by TestPrivateImageProjectionRejectsCallerCredentialSubstitution.
	for _, test := range []struct {
		name, member string
		mutate       func(*testing.T, *yamlv3.Node)
	}{
		{"caller credential", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			for _, step := range githubTestYAMLField(t, root, "jobs", jobID, "steps").Content {
				with := githubYAMLField(step, "with")
				if githubYAMLValue(with, "parameters") == githubParametersExpression(part) {
					githubYAMLSet(with, "nvkr-toolchain-read-token", githubYAMLString("${{ secrets.OTHER_TOKEN }}"))
					return
				}
			}
			t.Fatal("private image caller missing")
		}},
		{"source write scope", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, root, "jobs", jobID, "permissions", "contents").Value = "write"
		}},
		{"wrong credential", member, func(t *testing.T, root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "operation_images")
			token := githubTestYAMLField(t, step, "env", githubImageToken)
			if token.Value != "${{ inputs.nvkr-toolchain-read-token }}" {
				t.Fatalf("fixture image token differs: %q", token.Value)
			}
			token.Value = "${{ secrets.NKE_SOURCE_TOKEN }}"
		}},
		{"wrong helper", member, func(t *testing.T, root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "operation_images")
			run := githubTestYAMLField(t, step, "run")
			if strings.Count(run.Value, "factory github prepare-images --plan") != 1 {
				t.Fatal("fixture image preparation omits its exact helper")
			}
			run.Value = strings.Replace(run.Value, "factory github prepare-images --plan", "factory github run-node --plan", 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, test.member, func(root *yamlv3.Node) { test.mutate(t, root) })
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestPrivateImageActiveInterruption(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancellation", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			var directory string
			err := pullGitHubImages(ctx, []string{"ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)}, "linux/amd64", "reader", "synthetic-token",
				func(active context.Context, config, _, _ string) error {
					directory = config
					if !deadline {
						cancel()
					}
					<-active.Done()
					return active.Err()
				})
			assertFactoryErrorCode(t, err, e.CodeTimeout)
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("lost context cause: %v", err)
			}
			if directory == "" {
				t.Fatal("pull did not start")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("credential directory survived interruption")
			}
		})
	}
}

func TestGitHubStageExecutionRejectsPrivateImageOrdering(t *testing.T) {
	plan := arcTestPlan(t, "linux/amd64")
	plan.Environment.Publication.RepositoryPrefix = "ghcr.io/example/nvkr"
	plan.Environment.DiskInspectionProfiles = nil
	plan.Environment.DiskEvidence = "build-provenance-v1"
	stage := githubStage{ID: "disk-stage", DiskOutput: "image", Nodes: []Node{{Container: plan.Environment.Publication.RepositoryPrefix + "/toolchains/packer@sha256:" + strings.Repeat("a", 64)}}}
	stage.diskExecutionBudget = time.Minute
	prepare := githubStep{Run: githubRunnerCommand(plan, "prepare-images", "stage", stage.ID), Env: githubImageEnvironment()}
	run := githubStep{ID: "run-disk", Run: githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput), TimeoutMinutes: 1 + githubDiskCleanupHeadroomMinutes}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	var fragment bytes.Buffer
	writeGitHubPackerDiagnosticStep(&fragment, stage, actions)
	var diagnostic struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict([]byte("steps:\n"+fragment.String()), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubStageExecution(plan, stage, []githubStep{prepare, run, diagnostic.Steps[0]}); err != nil {
		t.Fatalf("valid private preparation/executor/diagnostic fixture rejected: %v", err)
	}
	conditional, ignored, credential := run, run, run
	conditional.If = "false"
	ignored.ContinueOnError = true
	credential.Env = map[string]string{sourceTokenEnv: "token"}
	for _, test := range []struct {
		name   string
		steps  []githubStep
		reason string
	}{
		{"before prepare", []githubStep{run, prepare}, "must precede"},
		{"duplicate prepare", []githubStep{prepare, prepare, run}, "exactly once"},
		{"missing prepare", nil, "preparation is missing"},
		{"missing disk executor", []githubStep{prepare, diagnostic.Steps[0]}, "sealed route transport"},
		{"conditional disk executor", []githubStep{prepare, conditional, diagnostic.Steps[0]}, "bounded unconditional live executor"},
		{"ignored disk failure", []githubStep{prepare, ignored, diagnostic.Steps[0]}, "bounded unconditional live executor"},
		{"disk executor credential", []githubStep{prepare, credential, diagnostic.Steps[0]}, "bounded unconditional live executor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateGitHubStageExecution(plan, stage, test.steps)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("wrong boundary rejected: %v", err)
			}
		})
	}
}
