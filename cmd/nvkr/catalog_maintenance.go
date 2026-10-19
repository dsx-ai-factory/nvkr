// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalogmaintenance"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func maintenanceFlags() []cli.Flag {
	return append([]cli.Flag{
		&cli.StringFlag{Name: "config", Required: true, Usage: "reviewed catalog maintenance policy, relative to repository root"},
		&cli.DurationFlag{Name: "timeout", Value: 15 * time.Minute, Usage: "maximum resolution and generation duration"},
	}, commandPresentationFlags()...)
}

func maintenanceContext(c *cli.Context) (context.Context, context.CancelFunc, error) {
	if c.Args().Present() || c.Duration("timeout") <= 0 {
		return nil, nil, e.New(e.CodeUsage, "catalog maintenance accepts flags only and requires a positive timeout")
	}
	ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
	return ctx, cancel, nil
}

func cmdCatalogRefresh() *cli.Command {
	return &cli.Command{Name: "refresh", Usage: "bump selected pins and regenerate/check a review candidate from the repository root", Flags: append(maintenanceFlags(),
		&cli.StringFlag{Name: "source-repository", Usage: "private owner/repository the workflow transports through a local Git rewrite"},
		&cli.BoolFlag{Name: "check", Usage: "preview the complete pin transaction without writing"},
		&cli.BoolFlag{Name: "json", Usage: "emit a descriptive pin/generation result"}), Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		display := catalogCommandDisplay("Catalog refresh")
		ctx, cancel, err := maintenanceContext(c)
		if err != nil {
			return showCommandResult(c, view, display, err)
		}
		defer cancel()
		config, err := catalogmaintenance.Load(ctx, c.String("config"))
		if err != nil {
			return showCommandResult(c, view, display, err)
		}
		result, refreshErr := catalogmaintenance.Refresh(ctx, config, factoryGeneratorVersion(), c.Bool("check"), c.String("source-repository"))
		if view.tui {
			display = catalogSourceUpdateDisplay(result.Update, c.Bool("check"), refreshErr)
			display.Title = "Catalog refresh"
			display.Note = "Pin and generated-bundle maintenance have separate commits; this result grants no execution or release acceptance."
			if c.Bool("check") {
				display.Note = "Check requested; pin and generated-bundle writes were withheld. Proposed pin changes are not an executed or accepted release."
			}
			display.Fields = append(display.Fields, progress.Field{Label: "Maintenance", Value: config.Name}, progress.Field{Label: "Returned Plan digest", Value: result.PlanDigest})
			return showCommandResult(c, view, display, refreshErr)
		}
		var coded *e.Error
		if refreshErr != nil && (!errors.As(refreshErr, &coded) || coded.Code != e.CodeDiff) {
			return refreshErr
		}
		var raw []byte
		if c.Bool("json") {
			raw, err = canonical.Marshal(result)
			raw = append(raw, '\n')
		} else {
			raw = fmt.Appendf(nil, "pin changes: %d\nplan: %s\nMaintenance only; producer execution and release acceptance are separate.\n", result.Update.Changed, result.PlanDigest)
		}
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing maintenance result")
		}
		return refreshErr
	}}
}

func cmdCatalogUpdateCI() *cli.Command {
	return &cli.Command{Name: "update-ci", Usage: "render a reviewed GitHub workflow that refreshes the one inputs/policies/*-catalog-maintenance.yaml policy and opens, updates or closes its pull request, to stdout", Flags: append([]cli.Flag{
		&cli.StringFlag{Name: "engine", Value: "github", Usage: "maintenance workflow provider"},
		&cli.StringFlag{Name: "schedule", Value: catalogmaintenance.DefaultSchedule, Usage: "reviewed cron for the scheduled run; empty means manual dispatch only"},
		&cli.StringFlag{Name: "source-checkout", Usage: "private source fetched with a secret, as owner/repository:SECRET_NAME"},
		&cli.StringFlag{Name: "app-secrets", Usage: "secrets holding the GitHub App that opens the pull request, as CLIENT_ID_SECRET:PRIVATE_KEY_SECRET (default " + catalogmaintenance.DefaultAppClientSecret + ":" + catalogmaintenance.DefaultAppKeySecret + ")"},
		&cli.DurationFlag{Name: "timeout", Value: 15 * time.Minute, Usage: "maximum resolution and generation duration"},
	}, commandPresentationFlags()...), Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		display := catalogCommandDisplay("Catalog maintenance workflow")
		if err := factory.ValidateGenerationEngine(c.String("engine")); err != nil {
			return showCommandResult(c, view, display, err)
		}
		ctx, cancel, err := maintenanceContext(c)
		if err != nil {
			return showCommandResult(c, view, display, err)
		}
		defer cancel()
		options := catalogmaintenance.WorkflowOptions{Schedule: c.String("schedule")}
		if source := c.String("source-checkout"); source != "" {
			repository, secret, ok := strings.Cut(source, ":")
			if !ok {
				return showCommandResult(c, view, display, e.New(e.CodeUsage, "--source-checkout requires owner/repository:SECRET_NAME"))
			}
			options.SourceRepository, options.SourceSecret = repository, secret
		}
		if app := c.String("app-secrets"); app != "" {
			client, key, ok := strings.Cut(app, ":")
			if !ok || client == "" || key == "" {
				return showCommandResult(c, view, display, e.New(e.CodeUsage, "--app-secrets requires CLIENT_ID_SECRET:PRIVATE_KEY_SECRET"))
			}
			options.AppClientSecret, options.AppKeySecret = client, key
		}
		raw, err := catalogmaintenance.Workflow(ctx, options, factoryGeneratorVersion())
		if err != nil {
			return showCommandResult(c, view, display, err)
		}
		if view.tui {
			display.Note = "Workflow bytes were generated for review; no workflow, producer, registry publication or release verification was executed."
			display.Fields = []progress.Field{{Label: "Provider", Value: c.String("engine")}, {Label: "Schedule", Value: options.Schedule}, {Label: "Private source", Value: options.SourceRepository}, {Label: "Generated bytes", Value: strconv.Itoa(len(raw))}}
			return showCommandResult(c, view, display, nil)
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing maintenance workflow")
		}
		return nil
	}}
}
