// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

// cmdFactory retains the generated-plan execution and admission adapters. Definition,
// Environment, Run, and provider generation are owned by the lifecycle commands.
func cmdFactory() *cli.Command {
	return &cli.Command{
		Name:  "factory",
		Usage: "execute generated internal plan nodes",
		Subcommands: []*cli.Command{{
			Name:   "execute-node",
			Hidden: true,
			Usage:  "execute one node from an exact generated plan",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "plan"},
				&cli.StringFlag{Name: "node"},
				&cli.StringFlag{Name: "plan-digest"},
				&cli.StringFlag{Name: "workspace", Value: ".nvkr/factory"},
			},
			Action: func(c *cli.Context) error {
				if c.String("plan") == "" || c.String("node") == "" {
					return e.New(e.CodeUsage, "execute-node requires --plan and --node")
				}
				if c.Args().Present() {
					return e.New(e.CodeUsage, "execute-node does not accept positional arguments")
				}
				if c.String("workspace") != ".nvkr/factory" {
					return e.New(e.CodeUsage, "generated plan nodes require workspace .nvkr/factory")
				}
				plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
				if err != nil {
					return err
				}
				if expected := c.String("plan-digest"); expected != "" {
					if err := factory.VerifyPlanDigest(plan, expected); err != nil {
						return err
					}
				}
				return factory.ExecuteNode(c.Context, plan, c.String("node"), os.Stdout, os.Stderr)
			},
		}, {
			Name:   "check-node-platform",
			Hidden: true,
			Usage:  "check the exact node's native host and container platform before launch",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "plan", Required: true},
				&cli.StringFlag{Name: "node", Required: true},
			},
			Action: func(c *cli.Context) error {
				plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
				if err != nil {
					return err
				}
				return factory.CheckNodePlatform(c.Context, plan, c.String("node"))
			},
		}, cmdFactoryGitHub()},
	}
}

func factoryGeneratorVersion() string { return version + " (" + commit + ")" }

// Provider adapters supply selectors and diagnostics only. Plan admission and all
// execution/transport behavior remain in the factory module.
func cmdFactoryGitHub() *cli.Command {
	return &cli.Command{Name: "github", Hidden: true, Usage: "execute exact generated GitHub steps", Subcommands: []*cli.Command{
		githubFactoryCommand("prepare-images", "stage", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.PrepareGitHubImages(c.Context, plan, c.String("stage"), options)
		}),
		githubFactoryCommand("run-disk", "output", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.RunGitHubDisk(c.Context, plan, c.String("output"), options)
		}),
		githubFactoryCommand("run-node", "node", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.RunGitHubNode(c.Context, plan, c.String("node"), options)
		}),
		githubFactoryCommand("restore-inputs", "stage", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.RestoreGitHubInputs(c.Context, plan, c.String("stage"), options)
		}),
		githubFactoryCommand("pack-outputs", "node", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.PackGitHubOutputs(c.Context, plan, c.String("node"), options)
		}),
		githubFactoryCommand("recover", "", func(c *cli.Context, plan *factory.Plan, options factory.GitHubStepOptions) error {
			return factory.RecoverGitHubArtifacts(c.Context, plan, os.Getenv("NVKR_JOB_RESULTS"), options)
		}),
	}}
}

func githubFactoryCommand(name, selector string, action func(*cli.Context, *factory.Plan, factory.GitHubStepOptions) error) *cli.Command {
	flags := []cli.Flag{&cli.StringFlag{Name: "plan"}}
	if selector != "" {
		flags = append(flags, &cli.StringFlag{Name: selector})
	}
	return &cli.Command{Name: name, Hidden: true, Flags: flags, Action: func(c *cli.Context) error {
		if c.String("plan") == "" || (selector != "" && c.String(selector) == "") {
			return e.New(e.CodeUsage, "%s requires --plan and its operation selector", name)
		}
		if c.Args().Present() {
			return e.New(e.CodeUsage, "%s does not accept positional arguments", name)
		}
		plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
		if err != nil {
			return err
		}
		return action(c, plan, factory.GitHubStepOptions{Stdout: os.Stdout, Stderr: os.Stderr, SummaryPath: os.Getenv("GITHUB_STEP_SUMMARY")})
	}}
}
