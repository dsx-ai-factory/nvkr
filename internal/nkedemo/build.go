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
	env := localBuildEnvironment()
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
	for _, root := range []string{"runs", "laptop-bootstrap"} {
		owned := filepath.Join(repo, ".nkd", root, cfg.AttemptPrefix)
		if _, readErr := os.Lstat(owned); readErr == nil {
			return BuiltInputs{}, fmt.Errorf("attempt destination %q already exists; choose a fresh --attempt", owned)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return BuiltInputs{}, fmt.Errorf("cannot inspect attempt destination %q: %w", owned, readErr)
		}
	}
	if err = runner.Run(ctx, []string{"bash", script, "--check", "all"}, "preflight-all", 120*time.Second); err != nil {
		return BuiltInputs{}, err
	}
	if err = buildLocalArtifacts(ctx, runner, script, cfg); err != nil {
		return BuiltInputs{}, err
	}
	return builtInputs(repo, cfg.AttemptPrefix), nil
}

func builtInputs(repo, attempt string) BuiltInputs {
	path := filepath.Join(repo, ".nkd/runs", attempt)
	return BuiltInputs{RunInputs{OS: path, Components: path, Providers: path, Site: path, Tools: path, Profiles: path, Simulator: path}, filepath.Join(repo, ".nkd/laptop-bootstrap", attempt, "nkdctl")}
}

// HOME selects the developer's source credentials. The sourcegit owner projects
// only credential configuration into network fetches; other Git operations use
// its closed baseline. Deployment excludes ambient source credentials.
func localBuildEnvironment() []string {
	env := SetEnv(NativeEnvironment(), map[string]string{"CONTAINER_RUNTIME": "docker"})
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK",
		"https_proxy", "http_proxy", "no_proxy", "ALL_PROXY", "all_proxy",
		"GIT_SSL_CAINFO", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = SetEnv(env, map[string]string{key: value})
		}
	}
	return env
}

func buildLocalArtifacts(ctx context.Context, runner Runner, script string, cfg BuildConfig) (result error) {
	boundary := beginProgress(runner.Progress, "artifacts", LaneStarted, LaneFinished, LaneFailed)
	defer func() { boundary.finish(result) }()
	if runner.Progress != nil {
		fmt.Fprintln(runner.Progress, "Build all artifacts in one Definition/Run (existing launcher → nkdctl run local)")
	}
	return runner.Run(ctx, []string{"bash", script, cfg.AttemptPrefix, strconv.Itoa(cfg.Jobs), "all"}, "build-all", 6*time.Hour)
}
