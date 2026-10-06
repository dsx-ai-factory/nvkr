package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalogmaintenance"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/urfave/cli/v2"
)

func maintenanceFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Required: true, Usage: "reviewed catalog maintenance policy, relative to repository root"},
		&cli.DurationFlag{Name: "timeout", Value: 15 * time.Minute, Usage: "maximum resolution and generation duration"},
	}
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
		&cli.BoolFlag{Name: "check", Usage: "preview the complete pin transaction without writing"},
		&cli.BoolFlag{Name: "json", Usage: "emit a descriptive pin/generation result"}), Action: func(c *cli.Context) error {
		ctx, cancel, err := maintenanceContext(c)
		if err != nil {
			return err
		}
		defer cancel()
		config, err := catalogmaintenance.Load(ctx, c.String("config"))
		if err != nil {
			return err
		}
		result, refreshErr := catalogmaintenance.Refresh(ctx, config, factoryGeneratorVersion(), c.Bool("check"))
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
	return &cli.Command{Name: "update-ci", Usage: "render a reviewed GitHub catalog-maintenance workflow to stdout", Flags: append(maintenanceFlags(),
		&cli.StringFlag{Name: "engine", Value: "github", Usage: "maintenance workflow provider"}), Action: func(c *cli.Context) error {
		if err := factory.ValidateGenerationEngine(c.String("engine")); err != nil {
			return err
		}
		ctx, cancel, err := maintenanceContext(c)
		if err != nil {
			return err
		}
		defer cancel()
		config, err := catalogmaintenance.Load(ctx, c.String("config"))
		if err != nil {
			return err
		}
		raw, err := catalogmaintenance.Workflow(ctx, config, c.String("config"), factoryGeneratorVersion())
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing maintenance workflow")
		}
		return nil
	}}
}
