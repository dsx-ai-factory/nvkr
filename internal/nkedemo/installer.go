// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

// PrepareInstaller prepares a downstream checkout for NKE installation scripts.
// It is not NKD build-source acquisition or artifact authority; Run rechecks it.
// An explicit checkout remains read-only. Without one, Git prepares the pinned
// commit in a new private attempt-local directory before the expensive build.
func PrepareInstaller(ctx context.Context, build BuildConfig, run Config) (Config, error) {
	result, err := prepareInstaller(ctx, build, run, Runner{Env: localBuildEnvironment(), Progress: run.Progress})
	if err != nil {
		code := nkderr.CodePreflightFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = nkderr.CodeTimeout
		}
		return Config{}, nkderr.Wrap(code, err, "prepare pinned NKE installer")
	}
	return result, nil
}

func prepareInstaller(ctx context.Context, build BuildConfig, run Config, runner Runner) (result Config, returnErr error) {
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	if !safePhase(build.AttemptPrefix) || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", rune(build.AttemptPrefix[0])) || build.Jobs < 1 || run.Timeout < 300*time.Second || run.Timeout > time.Hour {
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
	work := filepath.Join(repo, ".nkd", "installers", build.AttemptPrefix)
	for _, name := range []string{"git", "bash", "python3"} {
		if _, err = commandPath(name, runner.Env); err != nil {
			return result, err
		}
	}
	for _, root := range []string{"runs", "laptop-bootstrap", "installers"} {
		owned := filepath.Join(repo, ".nkd", root, build.AttemptPrefix)
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
	source := "https://github.com/dsx-ai-factory/nke.git"
	if build.NKEClone != "" {
		source, err = resolveExisting(build.NKEClone)
		if err != nil {
			return result, err
		}
		runner.Env = SetEnv(runner.Env, map[string]string{"NKE_LOCAL_CLONE": source})
	}
	if run.NKECheckout != "" {
		run.NKECheckout, err = resolveExisting(run.NKECheckout)
		if err != nil {
			return result, err
		}
		if insidePath(out, run.NKECheckout) {
			return result, errors.New("diagnostic output must be outside installer")
		}
		if err = verifyInstaller(ctx, runner, run.NKECheckout); err != nil {
			return result, err
		}
	}
	script := filepath.Join(repo, "scripts/nke-full-arm64-local.sh")
	if _, err = boundedRegularRead(script, 1<<20); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err = runner.Run(ctx, []string{"bash", script, "--check", "all"}, "installer-build-preflight", 120*time.Second); err != nil {
		return result, err
	}
	if _, err = runner.Capture(ctx, []string{"sudo", "-n", "true"}, "installer-sudo-preflight", 30*time.Second); err != nil {
		return result, fmt.Errorf("unattended deployment requires approved non-interactive sudo: `sudo -n true` failed; ask the host administrator to arrange access before starting E2E; a temporary `sudo -v` cache can expire during the build; see docs/local-nke-demo.md: %w", err)
	}
	if run.NKECheckout != "" {
		return run, nil
	}
	// The existing descriptor-relative directory owner rejects substituted
	// symlink parents; mkdir is exclusive and never adopts a retained attempt.
	parent, name, err := securepath.CreateParentBeneath(repo, filepath.Join(".nkd", "installers", build.AttemptPrefix))
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
	run.NKECheckout = checkout
	return run, nil
}

// E2E checks deployment-only image prerequisites before preparing sources or
// building artifacts. BuildArtifacts deliberately does not require these images.
func admitInstallerImages(ctx context.Context, runner Runner) error {
	guidance := func(image string, cause error) error {
		return fmt.Errorf("deployment requires a cached ARM64 image before building; run `docker pull %s` against the same native Linux Docker daemon (DOCKER_HOST=%q, DOCKER_CONTEXT=%q); see docs/local-nke-demo.md: %w", image, environmentValue(runner.Env, "DOCKER_HOST"), environmentValue(runner.Env, "DOCKER_CONTEXT"), cause)
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
