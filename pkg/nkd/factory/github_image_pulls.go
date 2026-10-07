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

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const githubImageToken = "NKD_TOOLCHAIN_READ_TOKEN"

func githubPrivateToolchain(plan *Plan, image string) bool {
	return plan.Environment.GitHubARC == "native-kvm-v1" &&
		plan.Environment.Publication != nil &&
		strings.HasPrefix(plan.Environment.Publication.RepositoryPrefix, "ghcr.io/") &&
		strings.HasPrefix(image, plan.Environment.Publication.RepositoryPrefix+"/toolchains/") && strings.Contains(image, "@sha256:")
}

func githubStagePrivateImages(plan *Plan, stage githubStage) []string {
	var images []string
	for _, node := range stage.Nodes {
		if githubPrivateToolchain(plan, node.Container) {
			images = append(images, node.Container)
		}
	}
	if stage.DiskOutput != "" && !diskBuildProvenance(plan) {
		image := plan.Environment.DiskInspectionProfiles[stage.Nodes[0].Platform].Image
		if githubPrivateToolchain(plan, image) {
			images = append(images, image)
		}
	}
	return uniqueSorted(images)
}

func githubImageEnvironment() map[string]string {
	return map[string]string{githubImageToken: "${{ github.token }}", "NKD_TOOLCHAIN_READ_USER": "${{ github.actor }}"}
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
		os.Getenv("NKD_TOOLCHAIN_READ_USER"), os.Getenv(githubImageToken), func(ctx context.Context, config, platform, image string) error {
			command := exec.CommandContext(ctx, "docker", "--config", config, "pull", "--platform", platform, image)
			// The registry token exists only in the private config, never in a
			// child environment, command argument, producer mount or log.
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=unix:///var/run/docker.sock"}
			command.Stdout, command.Stderr = options.Stdout, options.Stderr
			return ownedcmd.Run(ctx, command)
		})
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
	directory, err := os.MkdirTemp("", "nkd-toolchain-auth-")
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
