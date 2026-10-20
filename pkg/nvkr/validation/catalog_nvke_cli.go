// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"os"
	"slices"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func installedAdapters() map[string]adapter {
	return map[string]adapter{"nvke-cli/v1": &nvkeCLIAdapter{}}
}

type nvkeCLIAdapter struct {
	plans map[string]*factory.CLIArtifactPlan
}

func (nvkeCLIAdapter) validate(v Validator) error {
	expectedCoverage := "artifact-contract"
	if v.Suite == "cli" {
		expectedCoverage = "mixed"
	}
	timeout, err := time.ParseDuration(v.Timeout)
	if v.Adapter != "nvke-cli/v1" || (v.Suite != "contract" && v.Suite != "cli") || len(v.Inputs) != 1 || v.Inputs["executable"] != (InputRequirement{Component: "nvkectl", Format: "executable"}) ||
		v.SuiteSource != (SuiteSource{Mode: "build-source", FromInput: "executable"}) || err != nil || timeout <= 0 || timeout > 30*time.Minute || (!slices.Contains(v.Requires, "docker") || !slices.Contains(v.Requires, "git")) || v.Coverage != expectedCoverage {
		return e.New(e.CodeEngineCapability, "entry conflicts with nvke-cli/v1 contract")
	}
	for _, p := range v.Platforms {
		if p != "linux/amd64" && p != "linux/arm64" {
			return e.New(e.CodeEngineCapability, "unsupported CLI platform %q", p)
		}
	}
	// No installed owner can admit additional execution capabilities yet.
	for _, r := range v.Requires {
		if r != "docker" && r != "git" {
			return e.New(e.CodeEngineCapability, "unsupported CLI requirement %q", r)
		}
	}
	return nil
}
func (a *nvkeCLIAdapter) prepare(ctx context.Context, v Validator, source profileSource, check profileCheck, runtime Runtime, configuration []byte) (func(context.Context) error, any, error) {
	timeout, _ := time.ParseDuration(v.Timeout)
	b := check.Inputs["executable"]
	if a.plans == nil {
		a.plans = map[string]*factory.CLIArtifactPlan{}
	}
	plan := a.plans[source.Plan]
	if plan == nil {
		var err error
		plan, err = factory.LoadCLIArtifactPlan(ctx, source.Plan)
		if err != nil {
			return nil, nil, err
		}
		a.plans[source.Plan] = plan
	}
	info, err := os.Stat(b.Artifact)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, nil, usage("artifact must be an executable regular file")
	}
	invocation, err := plan.Admit(ctx, factory.ArtifactTestOptions{Plan: source.Plan, Output: b.Output, Artifact: b.Artifact, Suite: v.Suite,
		Timeout: timeout, DependencyTimeout: 30 * time.Minute, WorkspaceRoot: runtime.WorkspaceRoot, Keep: runtime.Keep, Stdout: runtime.Stdout, Stderr: runtime.Stderr, DiagnosticConfiguration: configuration})
	if err != nil {
		return nil, nil, err
	}
	inputs := invocation.Inputs()
	if !slices.Contains(v.Platforms, inputs.Platform) {
		return nil, nil, e.New(e.CodeEngineCapability, "selected output platform is excluded by validator")
	}
	return invocation.Run, struct {
		factory.CLIArtifactTestInputs
		PlanSHA256 string `json:"planSha256"`
	}{inputs, invocation.PlanSHA256()}, nil
}

func (nvkeCLIAdapter) limitations() []string {
	return []string{
		"Exhaustive required-suite completeness is unknown; package membership is only a diagnostic guard",
		"The full CLI suite mixes source tests and supplied-executable contracts",
		"Supplied artifacts are not authenticated Run members; results grant no qualification",
		"Host Git and Docker are required; the selected toolchain must provide Go and Make; availability and upstream interface readiness remain unverified",
		"Source and dependency acquisition use existing networked owners; tests run offline in Docker with private artifact staging",
	}
}
