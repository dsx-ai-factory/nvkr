// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// BuiltInputs describes output paths of the existing launcher; it confers no
// authority. Run independently admits observations and calls nkdctl to verify.
type BuiltInputs struct {
	Runs   RunInputs
	NKDCTL string
}

// BuildArtifacts invokes the repository's existing bootstrap and nkdctl local
// build flow. It does not import execution from a Go SDK, implement a builder,
// modify NKE sources or fold deployment into an NKD Definition.
func BuildArtifacts(ctx context.Context, cfg BuildConfig) (BuiltInputs, error) {
	result, err := buildArtifacts(ctx, cfg)
	if err != nil {
		return BuiltInputs{}, nkderr.Wrap(nkderr.CodeBuildFailed, err, "local NKE artifact build")
	}
	return result, nil
}
func buildArtifacts(ctx context.Context, cfg BuildConfig) (BuiltInputs, error) {
	if err := requireContext(ctx); err != nil {
		return BuiltInputs{}, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return BuiltInputs{}, nkderr.New(nkderr.CodeEngineCapability, "build inside native Ubuntu ARM64 execution host")
	}
	repo, err := resolveExisting(cfg.Repository)
	if err != nil {
		return BuiltInputs{}, err
	}
	if cfg.AttemptPrefix == "" || strings.ContainsAny(cfg.AttemptPrefix, "/\\\x00\r\n\t ") || strings.HasPrefix(cfg.AttemptPrefix, "-") {
		return BuiltInputs{}, errors.New("require explicit fresh local attempt prefix")
	}
	if cfg.Jobs < 1 {
		return BuiltInputs{}, errors.New("jobs must be positive")
	}
	script := filepath.Join(repo, "scripts/nke-full-arm64-local.sh")
	if _, err = boundedRegularRead(script, 1<<20); err != nil {
		return BuiltInputs{}, err
	}
	env := SetEnv(NativeEnvironment(), map[string]string{"CONTAINER_RUNTIME": "docker"})
	if cfg.NKEClone != "" {
		clone, readErr := resolveExisting(cfg.NKEClone)
		if readErr != nil {
			return BuiltInputs{}, readErr
		}
		env = SetEnv(env, map[string]string{"NKE_LOCAL_CLONE": clone})
	}
	runner := Runner{Env: env, Progress: cfg.Progress, output: cfg.Progress}
	// All host checks and fresh-path checks precede the first bootstrap. The
	// script remains authoritative for exact runtime/toolchain admission.
	lanes := []string{"os", "components", "site", "tools", "profiles", "simulator"}
	for _, lane := range lanes {
		attempt := cfg.AttemptPrefix + "-" + lane
		for _, root := range []string{"runs", "laptop-bootstrap"} {
			if _, readErr := os.Lstat(filepath.Join(repo, ".nkd", root, attempt)); !errors.Is(readErr, os.ErrNotExist) {
				return BuiltInputs{}, errors.New("attempt destination exists or cannot be inspected; choose a fresh prefix")
			}
		}
		if err = runner.Run(ctx, []string{"bash", script, "--check", lane}, "preflight-"+lane, 120*time.Second); err != nil {
			return BuiltInputs{}, err
		}
	}
	for _, lane := range lanes {
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "Build lane: %s (existing launcher → nkdctl run local)\n", lane)
		}
		if err = runner.Run(ctx, []string{"bash", script, cfg.AttemptPrefix + "-" + lane, strconv.Itoa(cfg.Jobs), lane}, "build-"+lane, 6*time.Hour); err != nil {
			return BuiltInputs{}, err
		}
	}
	path := func(lane string) string { return filepath.Join(repo, ".nkd/runs", cfg.AttemptPrefix+"-"+lane) }
	return BuiltInputs{RunInputs{OS: path("os"), Components: path("components"), Providers: path("components"), Site: path("site"), Tools: path("tools"), Profiles: path("profiles"), Simulator: path("simulator")}, filepath.Join(repo, ".nkd/laptop-bootstrap", cfg.AttemptPrefix+"-tools", "nkdctl")}, nil
}
