// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
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
		{Name: "validate", Usage: "validate a Definition and print its exact identity", Flags: append(commandPresentationFlags(), &cli.StringFlag{Name: "definition", Required: true}), Action: func(c *cli.Context) error {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			definition, err := lifecycle.LoadDefinition(c.Context, c.String("definition"))
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Definition", Note: "Definition admission failed."}, err)
			}
			digest, err := definition.Digest()
			if err != nil {
				return err
			}
			if view.tui {
				return showCommandResult(c, view, definitionDisplay(definition, digest), nil)
			}
			fmt.Printf("definition: %s\ndigest: %s\noutputs: %d\n", definition.Metadata.Name, digest, len(definition.Outputs))
			return nil
		}},
		{Name: "inspect", Usage: "emit the validated Definition as canonical JSON", Flags: append(commandPresentationFlags(), &cli.StringFlag{Name: "definition", Required: true}), Action: func(c *cli.Context) error {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			definition, err := lifecycle.LoadDefinition(c.Context, c.String("definition"))
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Definition", Note: "Definition admission failed."}, err)
			}
			if view.tui {
				digest, err := definition.Digest()
				if err != nil {
					return err
				}
				return showCommandResult(c, view, definitionDisplay(definition, digest), nil)
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
	generationFlags := append(append(lifecycleInputFlags(), commandPresentationFlags()...), &cli.StringFlag{Name: "engine", Value: factory.EngineGitHub, Usage: "hosted CI adapter: github"})
	return &cli.Command{Name: "run", Usage: "plan, generate, and execute Definition runs", Subcommands: []*cli.Command{
		{Name: "finalize", Hidden: true, Usage: "finalize one generated hosted Run from restored carriers", Flags: append(lifecycleInputFlags(),
			&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "attempt", Required: true},
			&cli.StringFlag{Name: "workspace", Value: ".nvkr/factory"}, &cli.StringFlag{Name: "out", Value: ".nvkr/run.json"},
			&cli.StringFlag{Name: "handoff", Hidden: true, Usage: "generated qualification handoff output"}), Action: func(c *cli.Context) error {
			var run *lifecycle.Run
			var err error
			if c.IsSet("handoff") {
				run, err = lifecycle.FinalizeGeneratedWithHandoff(c.Context, c.String("definition"), c.String("environment"), c.String("plan"), factoryGeneratorVersion(), c.String("attempt"), c.String("workspace"), c.String("out"), c.String("handoff"))
			} else {
				run, err = lifecycle.FinalizeGenerated(c.Context, c.String("definition"), c.String("environment"), c.String("plan"), factoryGeneratorVersion(), c.String("attempt"), c.String("workspace"), c.String("out"))
			}
			if err != nil {
				return err
			}
			fmt.Printf("hosted Run %s: %s (%d observed outputs)\n", run.Attempt, run.Status, len(run.Observed))
			return nil
		}},
		cmdRunPlan(),
		cmdArtifactTestInputs(),
		cmdArtifactTest(),
		cmdRunGraph(),
		cmdRunDiff(),
		cmdRunInspect(),
		cmdRunCheckArtifacts(),
		cmdRunQualify(),
		cmdQualificationSource(),
		cmdRunQualifyLocal(),
		cmdRunSmokeWorker(),
		{Name: "generate", Usage: "generate and statically validate a hosted CI run", Flags: append(generationFlags,
			&cli.StringFlag{Name: "root", Value: ".", Usage: "repository root receiving generated files"},
			&cli.StringFlag{Name: "qualification-policy", Usage: "also generate hosted qualification from this independent policy"},
			&cli.BoolFlag{Name: "runtime", Usage: "refresh named production Plan data within its reviewed workflow contract"},
			&cli.StringFlag{Name: "source-commit", Usage: "freeze one exact source commit under the authored GitHub runtime source policy (requires --runtime)"},
			&cli.BoolFlag{Name: "check", Usage: "write nothing; fail on generated drift"}), Action: func(c *cli.Context) error {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			var digest string
			if c.IsSet("source-commit") && !c.Bool("runtime") {
				return e.New(e.CodeUsage, "--source-commit requires --runtime")
			}
			if c.Bool("runtime") && c.IsSet("qualification-policy") {
				return e.New(e.CodeUsage, "runtime Plan preparation does not generate qualification workflows")
			}
			if c.Bool("runtime") && c.IsSet("source-commit") {
				digest, err = lifecycle.GenerateRuntimeSourceFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("engine"), c.String("root"), c.String("source-commit"), c.Bool("check"))
			} else if c.Bool("runtime") {
				digest, err = lifecycle.GenerateRuntimeFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("engine"), c.String("root"), c.Bool("check"))
			} else if c.IsSet("qualification-policy") {
				digest, err = qualification.GenerateFiles(c.Context, c.String("definition"), c.String("environment"), c.String("qualification-policy"), factoryGeneratorVersion(), c.String("engine"), c.String("root"), c.Bool("check"))
			} else {
				digest, err = lifecycle.GenerateFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("engine"), c.String("root"), c.Bool("check"))
			}
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Run generation", Note: "Generation failed; no execution or release acceptance."}, err)
			}
			if view.tui {
				state := "written"
				if c.Bool("check") {
					state = "current"
				}
				return showCommandResult(c, view, commandDisplay{Title: "Run generation", Note: "Static generation; no execution or release acceptance.", Fields: commandFields("Engine", c.String("engine"), "Plan digest", digest, "Files", state)}, nil)
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
			&cli.StringFlag{Name: "capture-inputs-root", Usage: "retain compiler inputs beneath this root; definition and environment paths must be repository-relative"},
			&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 1}, &cli.StringFlag{Name: "runtime", Value: "docker"},
			&cli.StringFlag{Name: "go", Value: "go"}, &cli.BoolFlag{Name: "publish"}, &cli.BoolFlag{Name: "dry-run"},
			&cli.IntFlag{Name: "build-activity-fd", Hidden: true, Value: -1},
			&cli.StringFlag{Name: "ui", Value: "auto", Usage: "auto, plain, or tui presentation"},
			&cli.BoolFlag{Name: "json", Usage: "emit one safe descriptive local Run result"},
			&cli.BoolFlag{Name: "no-color", Usage: "disable terminal colors (also honors NO_COLOR)"}), Action: func(c *cli.Context) error {
			return runLocalCommand(c, executeLocal)
		}},
	}}
}

func compileLifecycleInput(c *cli.Context) (*factory.Plan, error) {
	if c.String("definition") == "" || c.String("environment") == "" {
		return nil, e.New(e.CodeUsage, "run requires --definition and --environment")
	}
	return lifecycle.CompileFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion())
}
