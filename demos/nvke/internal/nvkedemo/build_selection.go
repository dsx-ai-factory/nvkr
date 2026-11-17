// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// AdmitDemoBuild checks static lane and default-input support without starting
// tools, authentication, progress, or producers. Runtime owners recheck the
// selected inputs and execution capabilities at their own boundaries.
func AdmitDemoBuild(ctx context.Context, cfg BuildConfig) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "demo build admission requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "demo build admission")
	}
	if err := validateAutomaticQualification(cfg.QualifyLocal, cfg.Prepared, cfg.QualificationPolicy, cfg.Qualification); err != nil {
		return err
	}
	qualification := cfg.Prepared || cfg.QualifyLocal || cfg.QualificationPolicy != ""
	if cfg.Conformance && qualification {
		return e.New(e.CodeEngineCapability, "demo conformance selects a fresh unsigned diagnostic Run; prepared or local-signing combinations are not admitted")
	}
	if qualification {
		if err := admitQualificationHost(runtime.GOOS + "/" + runtime.GOARCH); err != nil {
			return err
		}
	}
	if err := validateSelectedBuild(cfg.Definition, cfg.Environment, cfg.NVKR, qualification); err != nil {
		return err
	}
	if qualification || cfg.Definition != "" {
		return nil
	}
	selected, environment, err := LocalArtifactInputsForPlatform(cfg.Network, cfg.Conformance, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return err
	}
	repo, err := resolveExisting(cfg.Repository)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "demo build repository")
	}
	if _, err = authored.LoadDefinition(ctx, filepath.Join(repo, selected)); err == nil {
		_, err = authored.LoadEnvironment(ctx, filepath.Join(repo, environment))
	}
	if errors.Is(err, os.ErrNotExist) {
		return e.Wrap(e.CodeEngineCapability, err, "default demo BUILD inputs are unavailable for %s", runtime.GOOS+"/"+runtime.GOARCH)
	}
	return err
}

// Explicit inputs retain their owning NVKR producers, source pins and native
// dependency bindings. The demo neither rewrites them nor runs Packer itself.
func validateSelectedBuild(definition, environment, nvkr string, qualification bool) error {
	if definition == "" && environment == "" && nvkr == "" {
		return nil
	}
	if !configString(definition) || !configString(environment) || !configString(nvkr) {
		return configUsage("explicit build inputs require definition, environment and nvkr together")
	}
	if qualification {
		return e.New(e.CodeEngineCapability, "explicit build inputs support unsigned local execution; default prepared qualification is separate")
	}
	return nil
}

func resolveSelectedBuild(ctx context.Context, repo string, cfg *BuildConfig) error {
	for _, field := range []*string{&cfg.Definition, &cfg.Environment} {
		path := *field
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo, path)
		}
		resolved, err := resolveExisting(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repo, resolved)
		if err != nil || !insidePath(resolved, repo) {
			return configUsage("selected Definition and Environment must be inside the build repository")
		}
		*field = filepath.ToSlash(relative)
	}
	tool := cfg.NVKR
	if !filepath.IsAbs(tool) {
		tool = filepath.Join(repo, tool)
	}
	var err error
	cfg.NVKR, err = resolveExisting(tool)
	if err != nil {
		return err
	}
	if _, err := authored.LoadDefinition(ctx, filepath.Join(repo, cfg.Definition)); err != nil {
		return err
	}
	environment, err := authored.LoadEnvironment(ctx, filepath.Join(repo, cfg.Environment))
	if err != nil {
		return err
	}
	if environment.Publication != nil || environment.Signing != nil {
		return e.New(e.CodeEngineCapability, "demo BUILD accepts local inputs without publication or signing")
	}
	return nil
}

func definitionIncludesNativeTools(definition authored.Definition, platform string) bool {
	_, _, host, _, err := nativeToolSelection(platform)
	if err != nil {
		return false
	}
	want := map[string]string{host: platform}
	_, arch, _ := strings.Cut(platform, "/")
	for _, companion := range siteStartupToolBundles(platform) {
		want[companion.name] = "linux/" + arch
	}
	for _, output := range definition.Outputs {
		if expected, needed := want[output.Name]; needed && output.Platform == expected && output.Format == authored.OutputExecutable {
			delete(want, output.Name)
		}
	}
	return len(want) == 0
}

func selectedBuildIncludesNativeTools(ctx context.Context, cfg BuildConfig, platform string) (bool, error) {
	definition, err := authored.LoadDefinition(ctx, filepath.Join(cfg.Repository, cfg.Definition))
	if err != nil {
		return false, err
	}
	return definitionIncludesNativeTools(definition, platform), nil
}

func selectedBuildInvocation(cfg BuildConfig) []string {
	return []string{cfg.NVKR, "run", "local", "--definition", cfg.Definition,
		"--environment", cfg.Environment, "--attempt", cfg.AttemptPrefix,
		"--attempts-root", ".nvkr/runs", "--runner-source", ".", "--jobs", strconv.Itoa(cfg.Jobs),
		"--runtime", "docker", "--capture-inputs-root=.", "--ui=plain"}
}

func prepareSelectedBuild(ctx context.Context, runner Runner, cfg BuildConfig) error {
	runner.directory = cfg.Repository
	// Planning fetches sources too. Bind the same admitted clone as execution,
	// on this runner copy only, so the final invocation adds no duplicate entries.
	env, err := localBuildSourceEnvironment(runner.Env, environmentValue(runner.Env, "NVKE_LOCAL_CLONE"))
	if err != nil {
		return err
	}
	runner.Env = env
	raw, err := runner.Capture(ctx, []string{cfg.NVKR, "run", "plan", "--definition", cfg.Definition,
		"--environment", cfg.Environment, "--json"}, "selected-build-admission", 2*time.Minute)
	if err != nil {
		return err
	}
	root := filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix)
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(root, "tmp"), 0700); err != nil {
		return err
	}
	plan := filepath.Join(root, "selected-plan.json")
	if err := os.WriteFile(plan, raw, 0600); err != nil {
		return err
	}
	// NVKR's existing Plan owner validates the returned document. This is a
	// description only; run local independently admits capabilities and bytes.
	if _, err := factory.LoadPlanContext(ctx, plan); err != nil {
		return err
	}
	invocation, err := json.Marshal(selectedBuildInvocation(cfg))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "build-invocation.json"), invocation, 0600)
}
