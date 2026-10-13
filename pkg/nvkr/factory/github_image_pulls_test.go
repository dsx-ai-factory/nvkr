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
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
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
	for _, token := range []string{"", "bad\ntoken"} {
		err := pullGitHubImages(t.Context(), nil, "linux/amd64", "reader", token,
			func(context.Context, string, string, string) error {
				t.Fatal("invalid credential reached runtime")
				return nil
			})
		assertFactoryErrorCode(t, err, e.CodeFetchFailed)
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

func TestPrivateToolchainRebrandingBoundary(t *testing.T) {
	plan := arcTestPlan(t, "linux/amd64")
	for _, test := range []struct {
		name, publication, image string
		development              bool
		want                     bool
	}{
		{"current", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", false, true},
		{"published legacy subject", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, true},
		{"different publication owner", "ghcr.io/example/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", false, false},
		{"sibling repository", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nke-recipes-mirror/m2-candidate/toolchains/packer", false, false},
		{"non-toolchain subject", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/other/packer", false, false},
		{"development current toolchain", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", true, true},
		{"development legacy toolchain", "ghcr.io/dsx-ai-factory/nvkr/development", "ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", true, true},
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
}

func TestMaintainedM2PrivateImageCredentialBoundary(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	disks, fetches := 0, 0
	for _, job := range document.Jobs {
		for _, step := range job.Steps {
			if step.Env[githubImageToken] == "" {
				continue
			}
			if job.Permissions["packages"] != "read" || job.Permissions["id-token"] != "" || step.Env[sourceTokenEnv] != "" ||
				!strings.Contains(step.Run, "factory github prepare-images ") {
				t.Fatal("toolchain credential escaped its read-only preparation step")
			}
			if job.Environment == githubARCEnvironment {
				disks++
			} else {
				fetches++
			}
		}
	}
	if disks != 4 || fetches == 0 {
		t.Fatalf("missing preparation for native disks or their source vendors: %d/%d", disks, fetches)
	}
	for _, test := range []struct{ name, before, after string }{
		{"write scope", "packages: read", "packages: write"},
		{"wrong credential", "NVKR_TOOLCHAIN_READ_TOKEN: '${{ github.token }}'", "NVKR_TOOLCHAIN_READ_TOKEN: '${{ secrets.NKE_SOURCE_TOKEN }}'"},
		{"wrong helper", "factory github prepare-images --plan", "factory github run-node --plan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = make(map[string][]byte, len(bundle.Files))
			for name, value := range bundle.Files {
				changed.Files[name] = value
			}
			changed.Files[GitHubWorkflowPath] = bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte(test.before), []byte(test.after), 1)
			if bytes.Equal(changed.Files[GitHubWorkflowPath], bundle.Files[GitHubWorkflowPath]) {
				t.Fatal("mutation did not change workflow")
			}
			changed.WorkflowDigest = pipeline.FileDigest(changed.Files[GitHubWorkflowPath])
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
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
