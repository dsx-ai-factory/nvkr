// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

// PrepareInstaller prepares a downstream checkout for NVKE installation scripts.
// It is not NVKR build-source acquisition or artifact authority; Run rechecks it.
// An explicit checkout remains read-only. Without one, Git prepares the pinned
// commit in a new private attempt-local directory before the expensive build.
func PrepareInstaller(ctx context.Context, build BuildConfig, run Config) (result Config, err error) {
	return prepareInstallerOnPlatform(ctx, build, run, runtime.GOOS, runtime.GOARCH)
}

// The private platform parameters let command fixtures exercise the Linux
// boundary without misrepresenting the machine running the contract suite.
func prepareInstallerOnPlatform(ctx context.Context, build BuildConfig, run Config, system, arch string) (result Config, err error) {
	run.Network, err = normalizeNetwork(run.Network)
	if err != nil {
		return Config{}, err
	}
	if err = requireContext(ctx); err != nil {
		code := nvkrerr.CodeUsage
		if ctx != nil {
			code = nvkrerr.CodeTimeout
		}
		return Config{}, nvkrerr.Wrap(code, err, "prepare pinned NVKE installer")
	}
	if system != "linux" || arch != "arm64" {
		return Config{}, nvkrerr.New(nvkrerr.CodeEngineCapability, "installer preparation requires a native Linux ARM64 execution host")
	}
	environment, err := localBuildEnvironment()
	if err != nil {
		return Config{}, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "prepare pinned NVKE installer")
	}
	runner := Runner{Env: environment, Progress: run.Progress}
	admitHost := func() error {
		if run.Network != "kind-calico" {
			if err := admitSiteOVNKernel(); err != nil {
				return err
			}
		}
		return admitSimulatorEndpoint(ctx, runner)
	}
	boundary := beginProgress(run.Progress, "installer-preparation", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { boundary.finish(err) }()
	result, err = prepareInstaller(ctx, build, run, runner, admitHost)
	if err != nil {
		code := nvkrerr.CodePreflightFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = nvkrerr.CodeTimeout
		}
		return Config{}, nvkrerr.Wrap(code, err, "prepare pinned NVKE installer")
	}
	return result, nil
}

func prepareInstaller(ctx context.Context, build BuildConfig, run Config, runner Runner, admitHost func() error) (result Config, returnErr error) {
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	if !safePhase(build.AttemptPrefix) || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", rune(build.AttemptPrefix[0])) || build.Jobs < 1 || run.Timeout < MinTenantTimeoutSeconds*time.Second || run.Timeout > MaxTenantTimeoutSeconds*time.Second {
		return result, errors.New("require a fresh attempt, positive jobs and valid tenant budget")
	}
	repo, err := resolveExisting(build.Repository)
	if err != nil {
		return result, err
	}
	out, err := freshDestination(run.Out)
	if err != nil {
		return result, err
	}
	work := filepath.Join(repo, ".nvkr", "installers", build.AttemptPrefix)
	for _, name := range []string{"git", "bash", "python3"} {
		if _, err = commandPath(name, runner.Env); err != nil {
			return result, err
		}
	}
	for _, root := range []string{"runs", "laptop-bootstrap", "installers"} {
		owned := filepath.Join(repo, ".nvkr", root, build.AttemptPrefix)
		if insidePath(out, owned) || insidePath(owned, out) {
			return result, errors.New("diagnostic output must not overlap installer, Run or bootstrap attempt")
		}
		if _, err = os.Lstat(owned); err == nil {
			return result, fmt.Errorf("attempt destination %q already exists; choose a fresh --attempt", owned)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("cannot inspect attempt destination %q: %w", owned, err)
		}
	}
	if err = admitInstallerImages(ctx, runner); err != nil {
		return result, err
	}
	if admitHost == nil {
		return result, errors.New("host admission required before installer preparation")
	}
	if err = admitHost(); err != nil {
		return result, err
	}
	source := "https://github.com/dsx-ai-factory/nvke.git"
	if build.NVKEClone != "" {
		source, err = resolveExisting(build.NVKEClone)
		if err != nil {
			return result, err
		}
		runner.Env = SetEnv(runner.Env, map[string]string{"NVKE_LOCAL_CLONE": source})
	}
	if run.NVKECheckout != "" {
		run.NVKECheckout, err = resolveExisting(run.NVKECheckout)
		if err != nil {
			return result, err
		}
		if insidePath(out, run.NVKECheckout) {
			return result, errors.New("diagnostic output must be outside installer")
		}
		if err = verifyInstaller(ctx, runner, run.NVKECheckout); err != nil {
			return result, err
		}
	}
	script := filepath.Join(repo, "scripts/nvke-full-arm64-local.sh")
	if _, err = boundedRegularRead(script, 1<<20); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err = runner.Run(ctx, []string{"bash", script, "--check", "all"}, "installer-build-preflight", 120*time.Second); err != nil {
		return result, err
	}
	interactive, err := runner.authenticateSudo(ctx)
	if err == nil {
		_, err = runner.Capture(ctx, networkSudoCommand([]string{"true"}, interactive), "installer-sudo-preflight", 40*time.Second)
	}
	if err != nil {
		if interactive {
			return result, fmt.Errorf("interactive deployment networking authorization failed; authenticate in the original terminal before starting E2E; see docs/local-nvke-demo.md: %w", err)
		}
		return result, fmt.Errorf("unattended deployment requires approved non-interactive networking authorization: `%s` failed; ask the host administrator to arrange access before starting E2E; temporary `sudo -v` credentials are deliberately ignored, not cleared; see docs/local-nvke-demo.md: %w", networkSudoProbe, err)
	}
	if run.NVKECheckout != "" {
		return run, nil
	}
	// The existing descriptor-relative directory owner rejects substituted
	// symlink parents; mkdir is exclusive and never adopts a retained attempt.
	parent, name, err := securepath.CreateParentBeneath(repo, filepath.Join(".nvkr", "installers", build.AttemptPrefix))
	if err != nil {
		return result, err
	}
	defer parent.Close()
	if err = unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return result, err
	}
	defer func() {
		if returnErr != nil && !errors.Is(returnErr, ErrUnjoined) {
			returnErr = errors.Join(returnErr, os.RemoveAll(work))
		}
	}()
	runner.Work = work
	runner.Env = SetEnv(runner.Env, map[string]string{"GIT_TERMINAL_PROMPT": "0"})
	checkout := filepath.Join(work, "source")
	for _, step := range []struct {
		args   []string
		phase  string
		budget time.Duration
	}{
		{[]string{"git", "init", "--quiet", checkout}, "installer-init", 30 * time.Second},
		{[]string{"git", "-C", checkout, "fetch", "--no-tags", "--depth=1", source, InstallerCommit}, "installer-fetch", 240 * time.Second},
		{[]string{"git", "-C", checkout, "checkout", "--detach", InstallerCommit}, "installer-checkout", 60 * time.Second},
	} {
		if err = runner.Run(ctx, step.args, step.phase, step.budget); err != nil {
			return result, err
		}
	}
	if err = verifyInstaller(ctx, runner, checkout); err != nil {
		return result, err
	}
	run.NVKECheckout = checkout
	return run, nil
}

// E2E checks deployment-only image prerequisites before preparing sources or
// building artifacts. BuildArtifacts deliberately does not require these images.
func admitInstallerImages(ctx context.Context, runner Runner) error {
	if _, err := commandPath("docker", runner.Env); err != nil {
		return fmt.Errorf("deployment requires the Docker CLI before image admission; install Docker on the native Linux execution host; see docs/local-nvke-demo.md: %w", err)
	}
	if _, err := runner.Capture(ctx, []string{"docker", "version", "--format", "{{.Server.Version}}"}, "installer-docker-preflight", 30*time.Second); err != nil {
		return fmt.Errorf("deployment Docker daemon is unavailable; start it or restore this user's access before image admission; see docs/local-nvke-demo.md: %w", err)
	}
	guidance := func(image string, cause error) error {
		return fmt.Errorf("deployment requires a cached ARM64 image before building; run `docker pull %s` against the same native Linux Docker daemon (DOCKER_HOST=%q, DOCKER_CONTEXT=%q); see docs/local-nvke-demo.md: %w", image, environmentValue(runner.Env, "DOCKER_HOST"), environmentValue(runner.Env, "DOCKER_CONTEXT"), cause)
	}
	if err := admitKindImage(ctx, runner); err != nil {
		return guidance(kindNodeImage, err)
	}
	images, err := captureJSON[[]dockerImage](ctx, runner, []string{"docker", "image", "inspect", registryImage}, "installer-registry-image-preflight")
	if err != nil {
		return guidance(registryImage, err)
	}
	if len(images) != 1 || images[0].Architecture != "arm64" || !canonical.IsDigest(images[0].ID) {
		return guidance(registryImage, errors.New("docker inspection did not select one ARM64 registry image with a valid image ID"))
	}
	return nil
}
