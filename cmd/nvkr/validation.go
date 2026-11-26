// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan/builtin"
	"github.com/urfave/cli/v2"
)

type validationRunner func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error)

type validationPlanner func(context.Context, validationplan.GenerateRequest, lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error)

type localValidationPlanner func(context.Context, validationplan.LocalRequest) (*validationplan.Plan, error)

func generateValidationPlan(ctx context.Context, req validationplan.GenerateRequest, options lifecycle.QualificationRuntimeOptions) (*validationplan.Plan, error) {
	return validationplan.Generate(ctx, req, builtin.Planners(), options)
}

func generateLocalValidationPlan(ctx context.Context, req validationplan.LocalRequest) (*validationplan.Plan, error) {
	return validationplan.GenerateLocal(ctx, req, builtin.Planners())
}

func cmdValidate() *cli.Command {
	return cmdValidateWithRunners(client.New().Validate, generateValidationPlan)
}
func cmdValidateWithRunner(run validationRunner) *cli.Command {
	return cmdValidateWithRunners(run, generateValidationPlan)
}

// planOnlyFlags are the flags that mean something only with --plan.
var planOnlyFlags = []string{"reference", "policy", "local-run", "planners", "planner-var", "planner-out", "plan-out"}

func cmdValidateWithRunners(run validationRunner, plan validationPlanner) *cli.Command {
	return cmdValidateWithPlanners(run, plan, generateLocalValidationPlan)
}

func cmdValidateWithPlanners(run validationRunner, plan validationPlanner, local localValidationPlanner) *cli.Command {
	return &cli.Command{Name: "validate", ArgsUsage: "[check-name]", Usage: "run configured diagnostic checks against a published source or complete local Run, or with --plan describe what to validate for a release; retain original reports without qualification signing", Flags: []cli.Flag{
		&cli.StringFlag{Name: "config", Usage: "independently selected validation profile JSON"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/validation", Usage: "private validation workspace"},
		&cli.BoolFlag{Name: "plan", Usage: "describe what to validate for a published release and run nothing. The plan is descriptive and carries no qualification authority"},
		&cli.StringFlag{Name: "reference", Usage: "with --plan, the immutable release reference, repository@sha256:digest"},
		&cli.StringFlag{Name: "policy", Usage: "with --plan, the qualification policy that authenticates the release"},
		&cli.StringFlag{Name: "local-run", Usage: "with --plan, a complete local Run directory to plan for in place of --reference and --policy. Its outputs have no registry location, so the plan is for local testing only"},
		&cli.StringFlag{Name: "planners", Usage: "with --plan, the directory holding config.yaml and each planner's files"},
		&cli.StringSliceFlag{Name: "planner-var", Usage: "with --plan, a run-scoped value for one planner as planner.key=value, repeatable"},
		&cli.StringFlag{Name: "planner-out", Usage: "with --plan, write each planner's own plan to <planner>.json in this directory, which the plan's targets name by digest"},
		&cli.StringFlag{Name: "plan-out", Usage: "with --plan, write the plan here instead of standard output"},
	}, Action: func(c *cli.Context) error {
		if c.Bool("plan") {
			return runValidationPlan(c, plan, local)
		}
		for _, name := range planOnlyFlags {
			if c.IsSet(name) {
				return e.New(e.CodeUsage, "--%s requires --plan", name)
			}
		}
		if c.NArg() > 1 || strings.TrimSpace(c.String("config")) == "" || strings.TrimSpace(c.String("workspace")) == "" || c.NArg() == 1 && strings.TrimSpace(c.Args().First()) == "" {
			return e.New(e.CodeUsage, "validate requires --config, a non-empty workspace, and at most one check name")
		}
		options := qualificationRuntimeOptions(c)
		if options.Credential.Registry == "" && options.Registry.Username == "" && options.Registry.Password == "" {
			options.Registry.Anonymous = true
		}
		outputs, runErr := run(c.Context, c.Args().First(), c.String("config"), options)
		if len(outputs) == 0 {
			return runErr
		}
		documents := make([]json.RawMessage, 0, len(outputs))
		for _, output := range outputs {
			raw, err := testresults.Encode(output)
			if err != nil {
				return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "encoding validation output description"))
			}
			documents = append(documents, raw)
		}
		raw, err := canonical.Marshal(documents)
		if err != nil {
			return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "encoding validation diagnostics"))
		}
		raw = append(raw, '\n')
		n, err := c.App.Writer.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "writing validation output descriptions"))
		}
		return runErr
	}}
}

// runValidationPlan maps the plan flags and writes the plan. The planning
// itself lives in validationplan, and nothing here decides what a plan holds.
func runValidationPlan(c *cli.Context, plan validationPlanner, local localValidationPlanner) error {
	if c.NArg() != 0 || c.IsSet("config") || c.IsSet("workspace") {
		return e.New(e.CodeUsage, "validate --plan takes no check name, --config or --workspace")
	}
	if strings.TrimSpace(c.String("planners")) == "" {
		return e.New(e.CodeUsage, "validate --plan requires --planners")
	}
	// One source: a published release, which needs both its reference and the
	// policy that authenticates it, or a local Run, which needs neither.
	localRun := strings.TrimSpace(c.String("local-run"))
	if c.IsSet("local-run") && localRun == "" {
		return e.New(e.CodeUsage, "validate --plan --local-run requires a path")
	}
	if localRun != "" {
		if c.IsSet("reference") || c.IsSet("policy") {
			return e.New(e.CodeUsage, "validate --plan takes --local-run or --reference with --policy, not both")
		}
	} else {
		for _, name := range []string{"reference", "policy"} {
			if strings.TrimSpace(c.String(name)) == "" {
				return e.New(e.CodeUsage, "validate --plan requires --%s, or --local-run", name)
			}
		}
	}
	if c.IsSet("plan-out") && strings.TrimSpace(c.String("plan-out")) == "" {
		return e.New(e.CodeUsage, "validate --plan --plan-out requires a path")
	}
	if c.IsSet("planner-out") && strings.TrimSpace(c.String("planner-out")) == "" {
		return e.New(e.CodeUsage, "validate --plan --planner-out requires a path")
	}
	vars, err := parsePlannerVars(c.StringSlice("planner-var"))
	if err != nil {
		return err
	}
	var result *validationplan.Plan
	if localRun != "" {
		result, err = local(c.Context, validationplan.LocalRequest{AttemptDir: localRun, PlannersDir: c.String("planners"), Vars: vars})
	} else {
		options := qualificationRuntimeOptions(c)
		if options.Credential.Registry == "" && options.Registry.Username == "" && options.Registry.Password == "" {
			options.Registry.Anonymous = true
		}
		result, err = plan(c.Context, validationplan.GenerateRequest{
			Reference:   c.String("reference"),
			Policy:      c.String("policy"),
			PlannersDir: c.String("planners"),
			Vars:        vars,
		}, options)
	}
	if err != nil {
		return err
	}
	raw, err := result.Encode()
	if err != nil {
		return err
	}
	// The planner plans go first, so a written plan never names a file that is
	// missing.
	if dir := c.String("planner-out"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return e.Wrap(e.CodeInternal, err, "creating %s", dir)
		}
		for _, own := range result.PlannerPlans {
			if err := writeAtomic(c.Context, filepath.Join(dir, own.FileName()), own.Encode(), 0o644); err != nil {
				return err
			}
		}
	}
	if out := c.String("plan-out"); out != "" {
		return writeAtomic(c.Context, out, raw, 0o644)
	}
	n, err := c.App.Writer.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "writing the validation plan")
	}
	return nil
}

// parsePlannerVars reads planner.key=value pairs into one map for each planner. A
// repeated key is an error, so a value is never silently replaced.
func parsePlannerVars(values []string) (map[string]map[string]string, error) {
	vars := map[string]map[string]string{}
	for _, value := range values {
		pair, text, ok := strings.Cut(value, "=")
		planner, key, dotted := strings.Cut(pair, ".")
		if !ok || !dotted || planner == "" || key == "" || text == "" {
			return nil, e.New(e.CodeUsage, "--planner-var %q must be planner.key=value", value)
		}
		if vars[planner] == nil {
			vars[planner] = map[string]string{}
		}
		if _, repeated := vars[planner][key]; repeated {
			return nil, e.New(e.CodeUsage, "--planner-var %s.%s is given more than once", planner, key)
		}
		vars[planner][key] = text
	}
	return vars, nil
}
