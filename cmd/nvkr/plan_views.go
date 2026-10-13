// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/urfave/cli/v2"
)

func planViewInputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "plan", Usage: "existing canonical generated Plan (instead of authored inputs)"},
		&cli.StringFlag{Name: "definition", Usage: "Definition YAML to compile"},
		&cli.StringFlag{Name: "environment", Usage: "Environment YAML to compile"},
	}
}

func cmdRunPlan() *cli.Command {
	return &cli.Command{Name: "plan", Usage: "compile or inspect a complete Plan; views grant no execution authority", Flags: append(planViewInputFlags(),
		&cli.BoolFlag{Name: "json", Usage: "emit exact canonical generated Plan bytes"},
		&cli.BoolFlag{Name: "pretty", Usage: "emit a display-only indented JSON view"},
		&cli.StringFlag{Name: "ui", Value: "plain", Usage: "plain, auto, or tui Plan exploration"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable terminal colors (also honors NO_COLOR)"}), Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		if view.tui || view.json || c.Bool("pretty") {
			defer checkedOutputSignals()()
		}
		if view.tui {
			plan, err := loadPlanInspection(c)
			if err != nil {
				return err
			}
			monitor, err := factory.NewProgress(plan, "")
			if err != nil {
				return err
			}
			var diagnostics rundiagnostics.Buffer
			shown := commandServices(c).show(c.Context, runpresenter.Options{NoColor: view.noColor, Diagnostics: &diagnostics}, monitor, nil)
			return replayRunDiagnostics(c, &diagnostics, preserveRunError(shown.WorkErr, shown.PresentationErr))
		}
		format := "text"
		if c.Bool("json") || c.Bool("pretty") {
			format = "json"
		}
		options := planview.PlanOptions{Format: format, Pretty: c.Bool("pretty")}
		document, err := loadPlanView(c)
		if err != nil {
			return err
		}
		raw, err := document.Render(options)
		if err != nil {
			return err
		}
		return writePlanView(c.Context, raw)
	}}
}

func cmdRunGraph() *cli.Command {
	return &cli.Command{Name: "graph", Usage: "visualize complete planned producer, artifact, evidence and publication dependencies", Flags: append(append(planViewInputFlags(), commandPresentationFlags()...),
		&cli.StringFlag{Name: "view", Value: "complete", Usage: "complete, producers, or artifacts"},
		&cli.StringFlag{Name: "format", Value: "mermaid", Usage: "mermaid, dot, or json"}), Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		options := planview.GraphOptions{View: c.String("view"), Format: c.String("format")}
		if err := options.Validate(); err != nil {
			return err
		}
		document, err := loadPlanView(c)
		if err != nil {
			return showCommandResult(c, view, commandDisplay{Title: "Planned dependencies", Note: "Plan admission failed."}, err)
		}
		if view.tui {
			graph, err := document.GraphSnapshot(options.View)
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Planned dependencies", Note: "Graph projection failed."}, err)
			}
			return showCommandResult(c, view, graphDisplay(graph), nil)
		}
		raw, err := document.Graph(options)
		if err != nil {
			return err
		}
		return writePlanView(c.Context, raw)
	}}
}

func cmdRunDiff() *cli.Command {
	return &cli.Command{Name: "diff", Usage: "compare validated generated Plans by named outputs and fields", Flags: append(commandPresentationFlags(),
		&cli.StringFlag{Name: "before", Required: true, Usage: "previous canonical Plan"},
		&cli.StringFlag{Name: "after", Required: true, Usage: "next canonical Plan"},
		&cli.StringFlag{Name: "format", Value: "text", Usage: "text or json"},
		&cli.BoolFlag{Name: "details", Usage: "include every changed path in text output"},
		&cli.BoolFlag{Name: "exit-code", Usage: "return differences-present status 3 when plans differ"},
	), Action: func(c *cli.Context) error {
		if c.Args().Present() {
			return e.New(e.CodeUsage, "run diff accepts flags only")
		}
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		options := planview.ComparisonOptions{Format: c.String("format"), Details: c.Bool("details")}
		if err := options.Validate(); err != nil {
			return err
		}
		before, err := planview.Load(c.Context, c.String("before"))
		if err != nil {
			return showCommandResult(c, view, commandDisplay{Title: "Plan comparison", Note: "Plan admission failed."}, err)
		}
		after, err := planview.Load(c.Context, c.String("after"))
		if err != nil {
			return showCommandResult(c, view, commandDisplay{Title: "Plan comparison", Note: "Plan admission failed."}, err)
		}
		comparison, err := planview.Compare(before, after)
		if err != nil {
			return showCommandResult(c, view, commandDisplay{Title: "Plan comparison", Note: "Comparison failed."}, err)
		}
		if view.tui {
			var difference error
			if c.Bool("exit-code") && !comparison.Equal() {
				difference = e.New(e.CodeDiff, "generated Plans differ")
			}
			return showCommandResult(c, view, comparisonDisplay(comparison), difference)
		}
		raw, err := comparison.RenderView(options)
		if err != nil {
			return err
		}
		if err := writePlanView(c.Context, raw); err != nil {
			return err
		}
		if c.Bool("exit-code") && !comparison.Equal() {
			return e.New(e.CodeDiff, "generated Plans differ")
		}
		return nil
	}}
}

func loadPlanView(c *cli.Context) (*planview.Document, error) {
	plan, err := loadPlanInspection(c)
	if err != nil {
		return nil, err
	}
	return planview.Inspect(plan)
}

func loadPlanInspection(c *cli.Context) (*factory.Plan, error) {
	if c.Args().Present() {
		return nil, e.New(e.CodeUsage, "run %s accepts flags only", c.Command.Name)
	}
	if c.String("plan") != "" {
		if c.IsSet("definition") || c.IsSet("environment") {
			return nil, e.New(e.CodeUsage, "select --plan or --definition and --environment")
		}
		return factory.LoadPlanContext(c.Context, c.String("plan"))
	}
	plan, err := compileLifecycleInput(c)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func writePlanView(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "plan view canceled")
	}
	if _, err := os.Stdout.Write(raw); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing plan view")
	}
	return nil
}
