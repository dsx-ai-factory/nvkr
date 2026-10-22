// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const githubImageToken = "NVKR_TOOLCHAIN_READ_TOKEN"
const githubImageUser = "NVKR_TOOLCHAIN_READ_USER"

func githubPrivateToolchain(plan *Plan, image string) bool {
	if plan.Environment.GitHubARC != "native-kvm-v1" || !strings.Contains(image, "@sha256:") {
		return false
	}
	if GitHubDiskBuildOnly(plan) {
		// Build-only iteration consumes the same published native tooling, with
		// read access independent of any output publication configuration.
		return strings.HasPrefix(image, "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/")
	}
	if plan.Environment.Publication == nil || !strings.HasPrefix(plan.Environment.Publication.RepositoryPrefix, "ghcr.io/") {
		return false
	}
	prefix := plan.Environment.Publication.RepositoryPrefix
	if strings.HasPrefix(image, prefix+"/toolchains/") {
		return true
	}
	// Development publication does not move already-pinned native toolchains.
	// Only the maintained NVKR development namespace may prepare the
	// toolchain roots, using the separate read-only registry credential.
	if plan.Environment.GitHubDevelopment && prefix == "ghcr.io/dsx-ai-factory/nvkr/development" {
		return strings.HasPrefix(image, "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/")
	}
	// Maintained disk-family and nightly outputs reuse the published M2 tooling.
	// Output publication does not relocate those subjects or authorize other owners.
	switch prefix {
	case "ghcr.io/dsx-ai-factory/nvkr/m2-candidate",
		"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/disks",
		"ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-arm64",
		"ghcr.io/dsx-ai-factory/nvkr/nightly/nvke-nightly-upstream-ovn-arm64":
		return strings.HasPrefix(image, "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/")
	default:
		return false
	}
}

func githubStagePrivateImages(plan *Plan, stage githubStage) []string {
	var images []string
	for _, node := range stage.Nodes {
		if githubPrivateToolchain(plan, node.Container) {
			images = append(images, node.Container)
		}
	}
	if stage.DiskOutput != "" && githubDiskInspectionRequired(plan) {
		image := plan.Environment.DiskInspectionProfiles[stage.Nodes[0].Platform].Image
		if githubPrivateToolchain(plan, image) {
			images = append(images, image)
		}
	}
	return uniqueSorted(images)
}

func githubImageEnvironment() map[string]string {
	return map[string]string{githubImageToken: "${{ secrets.NVKR_REGISTRY_READ }}", githubImageUser: "${{ vars.NVKR_REGISTRY_READ_USER }}"}
}

// PrepareGitHubImages is a separate credential-bearing step. Its temporary Docker
// config is destroyed before any source fetch or producer helper can start.
func PrepareGitHubImages(ctx context.Context, plan *Plan, stageID string, options GitHubStepOptions) (returnErr error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return err
	}
	stage, ok := byNodeForJob(stages, stageID)
	if !ok || len(githubStagePrivateImages(plan, stage)) == 0 || runtime.GOOS != "linux" || stage.Nodes[0].Platform != "linux/"+runtime.GOARCH {
		return e.New(e.CodeEngineCapability, "private toolchain preparation requires an admitted native stage")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if stage.DiskOutput != "" {
		workspace, _, err := securepath.OpenDirectory(".")
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "hold image preparation workspace")
		}
		defer func() {
			returnErr = joinFactoryCleanupError(returnErr, workspace.Close(), "close image preparation workspace")
		}()
		for _, node := range stage.Nodes {
			if node.Kind != NodeBuild {
				continue
			}
			_, instruction, err := plan.producerInstruction(node)
			if err != nil {
				return err
			}
			if instruction.BuilderType == ir.BuilderVMImage {
				admission, err := AdmitLocalVMBuild(ctx, plan, node.ID, workspace)
				if err != nil {
					return err
				}
				defer func() {
					returnErr = joinFactoryCleanupError(returnErr, admission.Close(), "close image preparation admission")
				}()
			}
		}
	}
	return pullGitHubImages(ctx, githubStagePrivateImages(plan, stage), stage.Nodes[0].Platform,
		os.Getenv("NVKR_TOOLCHAIN_READ_USER"), os.Getenv(githubImageToken), func(ctx context.Context, config, platform, image string) error {
			command := githubImagePullCommand(ctx, config, platform, image)
			command.Stdout, command.Stderr = options.Stdout, options.Stderr
			return ownedcmd.Run(ctx, command)
		})
}

func githubImagePullCommand(ctx context.Context, config, platform, image string) *exec.Cmd {
	command := exec.CommandContext(ctx, "docker", "--config", config, "pull", "--platform", platform, image)
	// Only the separate private configuration holds the registry credential.
	// Do not inherit source tokens, OIDC tokens or a caller-selected Docker host.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=unix:///var/run/docker.sock"}
	return command
}

func pullGitHubImages(ctx context.Context, images []string, platform, user, token string,
	run func(context.Context, string, string, string) error) (returnErr error) {
	if user == "" || token == "" || strings.ContainsAny(user, ":\r\n") || strings.ContainsAny(token, "\r\n") {
		return e.New(e.CodeFetchFailed, "private toolchain pull credential is absent or malformed")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "private toolchain preparation canceled")
	}
	directory, err := os.MkdirTemp("", "nvkr-toolchain-auth-")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "create private toolchain authentication directory")
	}
	defer func() {
		returnErr = joinFactoryCleanupError(returnErr, os.RemoveAll(directory), "remove private toolchain authentication")
	}()
	config := struct {
		Auths map[string]map[string]string `json:"auths"`
	}{Auths: map[string]map[string]string{"ghcr.io": {"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + token))}}}
	raw, err := json.Marshal(config)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encode private toolchain authentication")
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), raw, 0o600); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "write private toolchain authentication")
	}
	for _, image := range images {
		if err := run(ctx, directory, platform, image); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "pull exact private toolchain"))
		}
	}
	return nil
}
