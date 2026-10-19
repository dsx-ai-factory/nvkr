// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// ToolConfig selects dependency preparation through the existing NVKR CLI.
// The catalog owns producers and pins; this consumer implements no downloader,
// package manager, source builder or system installation.
type ToolConfig struct {
	Repository, NVKR, NVKEClone, Go, Attempt, Out string
	Jobs                                          int
	Progress                                      io.Writer
}

// PrepareTools asks NVKR to execute, verify and extract the selected native tool
// bundle into a fresh private workspace. Failed attempts retain diagnostics.
func PrepareTools(ctx context.Context, cfg ToolConfig) (directory string, err error) {
	boundary := beginProgress(cfg.Progress, "artifacts", LaneStarted, LaneFinished, LaneFailed)
	defer func() { boundary.finish(err) }()
	path, err := prepareTools(ctx, cfg, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		var coded *nvkrerr.Error
		if errors.As(err, &coded) {
			return "", err
		}
		return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "prepare native demo dependencies through NVKR")
	}
	return path, nil
}

func nativeToolSelection(platform string) (definition, environment, bundle string, members []string, err error) {
	switch platform {
	case "darwin/arm64":
		return "inputs/definitions/demo/native-mac/definition-tools.json", "inputs/environments/local-native-mac.json", "native-tools-darwin-arm64", []string{"clusterctl", "helm", "kind", "kubectl", "nerdctl", "nico-sim", "nico-simctl", "nvke-installer-sources", "nvkectl", "oras", "vmnet-helper", "vmnet-run"}, nil
	case "linux/arm64":
		return "inputs/definitions/demo/native-linux/definition-tools.json", "inputs/environments/local-native-linux.json", "native-tools-linux-arm64", []string{"clusterctl", "helm", "kind", "kubectl", "nerdctl", "nico-sim", "nico-simctl", "nvke-installer-sources", "nvkectl", "oras"}, nil
	default:
		return "", "", "", nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "native demo tools do not support host %q", platform)
	}
}

type companionToolBundle struct {
	name, directory string
	members         []string
}

func siteStartupToolBundles(platform string) []companionToolBundle {
	if platform != "darwin/arm64" && platform != "linux/arm64" {
		return nil
	}
	return []companionToolBundle{
		{"site-startup-tools-arm64", "bin", []string{"nerdctl"}},
	}
}

func prepareTools(ctx context.Context, cfg ToolConfig, platform string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	definition, environment, bundle, _, err := nativeToolSelection(platform)
	if err != nil {
		return "", err
	}
	if !safePhase(cfg.Attempt) || cfg.Jobs < 1 {
		return "", nvkrerr.New(nvkrerr.CodeUsage, "tools require a canonical fresh attempt and positive jobs")
	}
	repo, err := resolveExisting(cfg.Repository)
	if err != nil {
		return "", err
	}
	for _, path := range []string{definition, environment} {
		if _, err := boundedRegularRead(filepath.Join(repo, path), 1<<20); err != nil {
			return "", err
		}
	}
	tool, err := resolveExisting(cfg.NVKR)
	if err != nil {
		return "", err
	}
	env, err := localBuildEnvironment()
	if err != nil {
		return "", err
	}
	if cfg.NVKEClone != "" {
		clone, err := resolveExisting(cfg.NVKEClone)
		if err != nil {
			return "", err
		}
		env, err = localBuildSourceEnvironment(env, clone)
		if err != nil {
			return "", err
		}
	}
	if cfg.Go == "" {
		cfg.Go = "go"
	}
	goTool, err := commandPath(cfg.Go, env)
	if err != nil {
		return "", err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return "", err
	}
	runsRelative := filepath.Join(".nvkr", "native-tools", "runs")
	runs := filepath.Join(repo, runsRelative)
	if insidePath(out, filepath.Join(runs, cfg.Attempt)) || insidePath(filepath.Join(runs, cfg.Attempt), out) {
		return "", nvkrerr.New(nvkrerr.CodeUsage, "tool extraction must not overlap its immutable Run")
	}
	runner := Runner{Env: env, Progress: cfg.Progress, directory: repo}
	if platform == "darwin/arm64" {
		root, err := runner.Capture(ctx, []string{goTool, "env", "GOROOT"}, "native-go-sdk-location", time.Minute)
		if err != nil {
			return "", err
		}
		compiler, err := commandPath("clang", env)
		if err != nil {
			return "", err
		}
		compiler, err = filepath.EvalSymlinks(compiler)
		if err != nil {
			return "", err
		}
		input, err := runner.Capture(ctx, []string{tool, "factory", "prepare-native-go", "--definition", definition, "--environment", environment, "--go-root", strings.TrimSpace(string(root)), "--cc", compiler, "--out", filepath.Join(".nvkr", "native-tools", "prepared", cfg.Attempt)}, "native-go-input-preparation", 3*time.Minute)
		if err != nil {
			return "", err
		}
		var prepared factory.NativeBuildPreparation
		if err := json.Unmarshal(input, &prepared); err != nil {
			return "", err
		}
		definition, environment = prepared.Definition, prepared.Environment
	}
	// NVKR's full graph validation precedes the consumer workspace. RunLocal
	// independently admits runtime capabilities before reserving its attempt.
	args := []string{tool, "run", "local", "--definition", definition, "--environment", environment, "--attempt", cfg.Attempt, "--attempts-root", runsRelative, "--runner-source", repo, "--go", goTool, "--runtime", "docker", "--jobs", fmt.Sprint(cfg.Jobs), "--ui", "plain"}
	if err := runner.Run(ctx, []string{tool, "run", "plan", "--definition", definition, "--environment", environment}, "native-tools-admission", 2*time.Minute); err != nil {
		return "", err
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return "", err
	}
	runner.Work, runner.output = out, cfg.Progress
	if err := runner.Run(ctx, args, "native-tools-run", time.Hour); err != nil {
		return "", err
	}
	root := filepath.Join(runs, cfg.Attempt)
	observed, err := CompleteOutput(ctx, root, bundle, "executable")
	if err != nil {
		return "", err
	}
	layout := filepath.Join(root, "packages", bundle)
	if err := diagnosticIndexMatches(layout, observed.Digest); err != nil {
		return "", err
	}
	publication := filepath.Join(out, "publication")
	publicationArgs := []string{tool, "publish", "root", "--bundle", bundle + "=" + layout}
	companions := siteStartupToolBundles(platform)
	for _, companion := range companions {
		observed, err := CompleteOutput(ctx, root, companion.name, "executable")
		if err != nil {
			return "", err
		}
		layout := filepath.Join(root, "packages", companion.name)
		if err := diagnosticIndexMatches(layout, observed.Digest); err != nil {
			return "", err
		}
		publicationArgs = append(publicationArgs, "--bundle", companion.name+"="+layout)
	}
	publicationArgs = append(publicationArgs, "--out", publication)
	if err := runner.Run(ctx, publicationArgs, "native-tools-verify", 2*time.Minute); err != nil {
		return "", err
	}
	directory := filepath.Join(out, "bin")
	if err := extractNativeHostTools(ctx, layout, publication, directory, platform); err != nil {
		return "", err
	}
	if len(companions) > 0 {
		if err := os.Mkdir(filepath.Join(out, "site"), 0o700); err != nil {
			return "", err
		}
	}
	for _, companion := range companions {
		directory := filepath.Join(out, "site", companion.directory)
		if err := runner.Run(ctx, []string{tool, "publish", "extract", "--layout", publication, "--bundle", companion.name, "--out", directory}, "native-site-tools-extract-"+companion.name, 2*time.Minute); err != nil {
			return "", err
		}
		if err := verifyToolMembers(directory, companion.members); err != nil {
			return "", err
		}
	}
	if platform == "darwin/arm64" || platform == "linux/arm64" {
		installer := filepath.Join(out, "installer")
		if err := os.Mkdir(installer, 0o700); err != nil {
			return "", err
		}
		// NVKR has already verified the archive's exact bundle member. Reuse
		// the existing bounded standard-library decoder and held writes; these
		// are source-owned files, not a second deployment implementation.
		if err := extractNativeInstaller(ctx, filepath.Join(directory, "nvke-installer-sources"), installer); err != nil {
			return "", err
		}
	}
	return directory, nil
}
