// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
	"github.com/urfave/cli/v2"
)

const planTestReference = "registry.example.test/nvkr/releases@sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111"

func planCLI(plan validationPlanner, writer io.Writer) *cli.App {
	run := func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		return nil, e.New(e.CodeInternal, "validation ran in plan mode")
	}
	command := cmdValidateWithRunners(run, plan)
	configureCommandUsage(command)
	return &cli.App{Commands: []*cli.Command{command}, Writer: writer, ErrWriter: io.Discard}
}

func planCLIWithLocal(plan validationPlanner, local localValidationPlanner, writer io.Writer) *cli.App {
	run := func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		return nil, e.New(e.CodeInternal, "validation ran in plan mode")
	}
	command := cmdValidateWithPlanners(run, plan, local)
	configureCommandUsage(command)
	return &cli.App{Commands: []*cli.Command{command}, Writer: writer, ErrWriter: io.Discard}
}

func samplePlan() *validationplan.Plan {
	return &validationplan.Plan{
		SchemaVersion: validationplan.SchemaVersion, QualificationAuthority: validationplan.AuthorityNone,
		Release: validationplan.ReleaseRef{Reference: planTestReference},
		Targets: []validationplan.Target{{ID: "e2e/a", Executor: "nvke-e2e-gotest", Source: validationplan.Source{Planner: "p"}}},
	}
}

func planArgs(extra ...string) []string {
	return append([]string{"nvkr", "validate", "--plan",
		"--reference", planTestReference, "--policy", "policy.yaml", "--planners", "planners"}, extra...)
}

func TestValidatePlanForwardsFlagsAndWritesThePlan(t *testing.T) {
	var got validationplan.GenerateRequest
	var output bytes.Buffer
	app := planCLI(func(_ context.Context, req validationplan.GenerateRequest, _ lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
		got = req
		return samplePlan(), nil
	}, &output)
	err := app.RunContext(t.Context(), planArgs("--planner-var", "p.root=./tree", "--planner-var", "p.os-image-amd64=https://x.example.test/a", "--planner-var", "q.k=v"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Reference != planTestReference || got.Policy != "policy.yaml" || got.PlannersDir != "planners" {
		t.Fatalf("request = %+v", got)
	}
	if len(got.Vars["p"]) != 2 || got.Vars["p"]["root"] != "./tree" || got.Vars["q"]["k"] != "v" {
		t.Fatalf("vars = %v", got.Vars)
	}
	if !strings.HasSuffix(output.String(), "\n") || !strings.Contains(output.String(), `"qualificationAuthority":"none"`) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestValidatePlanWritesAFileWithOut(t *testing.T) {
	out := filepath.Join(t.TempDir(), "plan.json")
	var output bytes.Buffer
	app := planCLI(func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
		return samplePlan(), nil
	}, &output)
	if err := app.RunContext(t.Context(), planArgs("--out", out)); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("standard output = %q, want nothing when --out is given", output.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(raw), `"schemaVersion":"nvkr.dev/validation-plan/v1"`) {
		t.Fatalf("file = %q, %v", raw, err)
	}
}

func TestValidatePlanAdmitsBeforeWork(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no reference", []string{"nvkr", "validate", "--plan", "--policy", "p", "--planners", "d"}},
		{"no policy", []string{"nvkr", "validate", "--plan", "--reference", planTestReference, "--planners", "d"}},
		{"no planners directory", []string{"nvkr", "validate", "--plan", "--reference", planTestReference, "--policy", "p"}},
		{"a validation profile", planArgs("--config", "profile.json")},
		{"a workspace", planArgs("--workspace", "w")},
		{"a check name", append(planArgs(), "cluster-conformance")},
		{"an empty out", planArgs("--out", " ")},
		{"a planner var with no planner", planArgs("--planner-var", "k=v")},
		{"a planner var with no value", planArgs("--planner-var", "p.k=")},
		{"a planner var with no equals", planArgs("--planner-var", "p.k")},
		{"a planner var given twice", planArgs("--planner-var", "p.k=1", "--planner-var", "p.k=2")},
		{"a plan flag without --plan", []string{"nvkr", "validate", "--config", "profile.json", "--reference", planTestReference}},
		{"--out without --plan", []string{"nvkr", "validate", "--config", "profile.json", "--out", "x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			var output bytes.Buffer
			app := planCLI(func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
				called = true
				return samplePlan(), nil
			}, &output)
			err := app.RunContext(t.Context(), tc.args)
			if cliErrorCode(err) != e.CodeUsage || called || output.Len() != 0 {
				t.Fatalf("invalid input reached work: called=%v output=%q error=%v", called, output.String(), err)
			}
		})
	}
}

func TestValidatePlanWritesNothingWhenPlanningFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "plan.json")
	var output bytes.Buffer
	app := planCLI(func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
		return nil, e.New(e.CodeVerifyFailed, "release did not authenticate")
	}, &output)
	err := app.RunContext(t.Context(), planArgs("--out", out))
	if cliErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("error = %v, want the planner's code", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q", output.String())
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("a failed plan left a file: %v", statErr)
	}
}

func TestValidatePlanRefusesAPlanThatClaimsAuthority(t *testing.T) {
	var output bytes.Buffer
	app := planCLI(func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
		claimed := samplePlan()
		claimed.QualificationAuthority = "qualified"
		return claimed, nil
	}, &output)
	err := app.RunContext(t.Context(), planArgs())
	if err == nil || output.Len() != 0 {
		t.Fatalf("a plan claiming authority was printed: %q, %v", output.String(), err)
	}
}

func TestValidatePlanLocalRunForwardsAndNeedsNoPolicyOrReference(t *testing.T) {
	var got validationplan.LocalRequest
	hostedCalled := false
	var output bytes.Buffer
	app := planCLIWithLocal(
		func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
			hostedCalled = true
			return samplePlan(), nil
		},
		func(_ context.Context, req validationplan.LocalRequest) (*validationplan.Plan, error) {
			got = req
			return samplePlan(), nil
		}, &output)
	err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--plan", "--local-run", ".nvkr/runs/a", "--planners", "planners", "--planner-var", "p.k=v"})
	if err != nil {
		t.Fatal(err)
	}
	if hostedCalled {
		t.Fatal("the published-release path ran for a local Run")
	}
	if got.AttemptDir != ".nvkr/runs/a" || got.PlannersDir != "planners" || got.Vars["p"]["k"] != "v" {
		t.Fatalf("request = %+v", got)
	}
	if !strings.Contains(output.String(), `"qualificationAuthority":"none"`) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestValidatePlanLocalRunAdmitsBeforeWork(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"a local Run with a reference", []string{"nvkr", "validate", "--plan", "--local-run", "r", "--reference", planTestReference, "--planners", "d"}},
		{"a local Run with a policy", []string{"nvkr", "validate", "--plan", "--local-run", "r", "--policy", "p", "--planners", "d"}},
		{"an empty local Run path", []string{"nvkr", "validate", "--plan", "--local-run", " ", "--planners", "d"}},
		{"a local Run without planners", []string{"nvkr", "validate", "--plan", "--local-run", "r"}},
		{"no source at all", []string{"nvkr", "validate", "--plan", "--planners", "d"}},
		{"a reference without a policy", []string{"nvkr", "validate", "--plan", "--reference", planTestReference, "--planners", "d"}},
		{"--local-run without --plan", []string{"nvkr", "validate", "--config", "profile.json", "--local-run", "r"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			var output bytes.Buffer
			app := planCLIWithLocal(
				func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
					called = true
					return samplePlan(), nil
				},
				func(context.Context, validationplan.LocalRequest) (*validationplan.Plan, error) {
					called = true
					return samplePlan(), nil
				}, &output)
			err := app.RunContext(t.Context(), tc.args)
			if cliErrorCode(err) != e.CodeUsage || called || output.Len() != 0 {
				t.Fatalf("invalid input reached work: called=%v output=%q error=%v", called, output.String(), err)
			}
		})
	}
}
