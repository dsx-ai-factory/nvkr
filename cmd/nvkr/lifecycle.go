// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func lifecycleInputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "definition", Required: true, Usage: "Definition YAML"},
		&cli.StringFlag{Name: "environment", Required: true, Usage: "independently selected Environment YAML"},
	}
}

func cmdDefinition() *cli.Command {
	return &cli.Command{Name: "definition", Usage: "validate and inspect authored product definitions", Subcommands: []*cli.Command{
		{Name: "validate", Usage: "validate a Definition and print its exact identity", Flags: []cli.Flag{&cli.StringFlag{Name: "definition", Required: true}}, Action: func(c *cli.Context) error {
			definition, err := lifecycle.LoadDefinition(c.Context, c.String("definition"))
			if err != nil {
				return err
			}
			digest, err := definition.Digest()
			if err != nil {
				return err
			}
			fmt.Printf("definition: %s\ndigest: %s\noutputs: %d\n", definition.Metadata.Name, digest, len(definition.Outputs))
			return nil
		}},
		{Name: "inspect", Usage: "emit the validated Definition as canonical JSON", Flags: []cli.Flag{&cli.StringFlag{Name: "definition", Required: true}}, Action: func(c *cli.Context) error {
			definition, err := lifecycle.LoadDefinition(c.Context, c.String("definition"))
			if err != nil {
				return err
			}
			raw, err := canonical.Marshal(definition)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(append(raw, '\n'))
			return err
		}},
	}}
}

func cmdRun() *cli.Command {
	return cmdRunWithLocalExecutor(lifecycle.ExecuteLocal)
}

type localLifecycleExecutor func(context.Context, *lifecycle.PreparedRun, lifecycle.LocalOptions) (*lifecycle.Run, error)

func cmdRunWithLocalExecutor(executeLocal localLifecycleExecutor) *cli.Command {
	generationFlags := append(lifecycleInputFlags(), &cli.StringFlag{Name: "engine", Value: factory.EngineGitHub, Usage: "hosted CI adapter: github"})
	return &cli.Command{Name: "run", Usage: "plan, generate, and execute Definition runs", Subcommands: []*cli.Command{
		{Name: "finalize", Hidden: true, Usage: "finalize one generated hosted Run from restored carriers", Flags: append(lifecycleInputFlags(),
			&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "attempt", Required: true},
			&cli.StringFlag{Name: "workspace", Value: ".nvkr/factory"}, &cli.StringFlag{Name: "out", Value: ".nvkr/run.json"}), Action: func(c *cli.Context) error {
			run, err := lifecycle.FinalizeGenerated(c.Context, c.String("definition"), c.String("environment"), c.String("plan"), factoryGeneratorVersion(), c.String("attempt"), c.String("workspace"), c.String("out"))
			if err != nil {
				return err
			}
			fmt.Printf("hosted Run %s: %s (%d observed outputs)\n", run.Attempt, run.Status, len(run.Observed))
			return nil
		}},
		cmdRunPlan(),
		cmdRunGraph(),
		cmdRunDiff(),
		{Name: "generate", Usage: "generate and statically validate a hosted CI run", Flags: append(generationFlags,
			&cli.StringFlag{Name: "root", Value: ".", Usage: "repository root receiving generated files"},
			&cli.BoolFlag{Name: "check", Usage: "write nothing; fail on generated drift"}), Action: func(c *cli.Context) error {
			digest, err := lifecycle.GenerateFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("engine"), c.String("root"), c.Bool("check"))
			if err != nil {
				return err
			}
			if c.Bool("check") {
				fmt.Printf("generated run is current: %s\n", digest)
				return nil
			}
			fmt.Printf("generated %s hosted run\nplanDigest: %s\n", c.String("engine"), digest)
			return nil
		}},
		{Name: "local", Usage: "execute a local run; publication remains opt-in", Flags: append(lifecycleInputFlags(),
			&cli.StringFlag{Name: "attempt", Required: true, Usage: "new immutable attempt name"},
			&cli.StringFlag{Name: "attempts-root", Value: ".nvkr/runs"}, &cli.StringFlag{Name: "runner-source", Value: "."},
			&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 1}, &cli.StringFlag{Name: "runtime", Value: "docker"},
			&cli.StringFlag{Name: "go", Value: "go"}, &cli.BoolFlag{Name: "publish"}, &cli.BoolFlag{Name: "dry-run"},
			&cli.DurationFlag{Name: "timeout", Value: time.Hour, Usage: "bound preparation, execution and output observation"},
			&cli.IntFlag{Name: "build-activity-fd", Hidden: true, Value: -1}), Action: func(c *cli.Context) error {
			if c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "local run requires a positive timeout")
			}
			ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
			defer cancel()
			var activity *buildactivity.Sink
			if c.IsSet("build-activity-fd") {
				if c.Bool("dry-run") || c.Bool("publish") {
					return e.New(e.CodeUsage, "build activity is available only for local execution")
				}
				var err error
				activity, err = buildactivity.AdoptFD(ctx, c.Int("build-activity-fd"))
				if err != nil {
					return err
				}
				defer func() { _ = activity.Close() }()
			}
			prepared, err := lifecycle.PrepareFiles(ctx, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"))
			if err != nil {
				return err
			}
			result, err := executeLocal(ctx, prepared, lifecycle.LocalOptions{AttemptsRoot: c.String("attempts-root"), RunnerSource: c.String("runner-source"), Jobs: c.Int("jobs"), Runtime: c.String("runtime"), Go: c.String("go"), Publish: c.Bool("publish"), DryRun: c.Bool("dry-run"), Stdout: os.Stdout, Stderr: os.Stderr, Activity: activity})
			if err != nil {
				if result != nil && result.Status == lifecycle.RunComplete {
					if reportErr := printLocalRunResult(c, result); reportErr != nil {
						return e.Wrap(e.CodeImmutableViolation, errors.Join(err, reportErr),
							"completed local Run was retained, but reporting its outputs and commit cleanup both failed")
					}
					return e.Wrap(e.CodeImmutableViolation, err,
						"completed local Run and outputs were retained, but commit cleanup is incomplete")
				}
				return err
			}
			return printLocalRunResult(c, result)
		}},
	}}
}

func printLocalRunResult(c *cli.Context, result *lifecycle.Run) error {
	workspace := filepath.Join(c.String("attempts-root"), c.String("attempt"))
	fmt.Printf("\nattempt: %s\nrun: %s\nlane: %s\nstatus: %s\nobserved outputs: %d\n", result.Attempt, result.PlanDigest, result.Lane, result.Status, len(result.Observed))
	if c.Bool("dry-run") {
		return nil
	}
	fmt.Printf("record: %s\n", filepath.Join(workspace, "lifecycle.json"))
	for _, observed := range result.Observed {
		if observed.Kind != lifecycle.OutputDiskImage {
			continue
		}
		path, err := lifecycle.LocalDiskOutputPath(workspace, observed.Name)
		if err != nil {
			return err
		}
		fmt.Printf("disk: %s\n", path)
	}
	return nil
}

func compileLifecycleInput(c *cli.Context) (*factory.Plan, error) {
	if c.String("definition") == "" || c.String("environment") == "" {
		return nil, e.New(e.CodeUsage, "run requires --definition and --environment")
	}
	return lifecycle.CompileFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion())
}
