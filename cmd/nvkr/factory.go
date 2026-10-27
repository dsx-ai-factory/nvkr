// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"strings"

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
		}, {
			Name:   "inspect-source",
			Hidden: true,
			Usage:  "verify one restored pinned source without fetching or building",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "plan"},
				&cli.StringFlag{Name: "plan-digest"},
				&cli.StringFlag{Name: "node"},
			},
			Action: func(c *cli.Context) error {
				if c.Args().Present() || c.String("plan") == "" || c.String("plan-digest") == "" || c.String("node") == "" {
					return e.New(e.CodeUsage, "inspect-source requires --plan, --plan-digest and --node, with no positional arguments")
				}
				plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
				if err != nil {
					return err
				}
				if err := factory.VerifyPlanDigest(plan, c.String("plan-digest")); err != nil {
					return err
				}
				description, err := factory.InspectRestoredSource(c.Context, plan, c.String("node"))
				if err != nil {
					return err
				}
				if err := json.NewEncoder(c.App.Writer).Encode(description); err != nil {
					return e.Wrap(e.CodeVerifyFailed, err, "writing source inspection")
				}
				return nil
			},
		}, {
			Name:   "prepare-native-go",
			Hidden: true,
			Usage:  "bind local Mac CGO producers to an existing installed SDK",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "definition", Required: true},
				&cli.StringFlag{Name: "environment", Required: true},
				&cli.StringFlag{Name: "go-root", Required: true},
				&cli.StringFlag{Name: "cc", Required: true},
				&cli.StringFlag{Name: "out", Required: true},
			},
			Action: func(c *cli.Context) error {
				if c.Args().Present() {
					return e.New(e.CodeUsage, "prepare-native-go accepts no positional arguments")
				}
				prepared, err := factory.PrepareNativeGo(c.Context, c.String("definition"), c.String("environment"), c.String("go-root"), c.String("cc"), c.String("out"))
				if err != nil {
					return err
				}
				if err := json.NewEncoder(c.App.Writer).Encode(prepared); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing native Go input locations")
				}
				return nil
			},
		}, {
			Name: "prepare-native-packer", Hidden: true,
			Usage: "capture and validate a local native Packer dependency bundle",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "manifest", Required: true},
				&cli.StringFlag{Name: "out", Required: true},
			},
			Action: func(c *cli.Context) error {
				if c.Args().Present() {
					return e.New(e.CodeUsage, "prepare-native-packer accepts no positional arguments")
				}
				prepared, err := factory.PrepareNativePacker(c.Context, c.String("manifest"), c.String("out"))
				if err != nil {
					return err
				}
				if err := json.NewEncoder(c.App.Writer).Encode(prepared); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing native Packer dependency locations")
				}
				return nil
			},
		}, {
			Name: "prepare-native-packer-inputs", Hidden: true,
			Usage: "bind selected disk producers to a verified native Packer bundle",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "definition", Required: true},
				&cli.StringFlag{Name: "environment", Required: true},
				&cli.StringFlag{Name: "layout", Required: true},
				&cli.StringFlag{Name: "bundle", Required: true},
				&cli.StringFlag{Name: "toolchain", Required: true},
				&cli.StringFlag{Name: "out", Required: true},
				&cli.StringFlag{Name: "accelerator", Value: "hvf", Usage: "explicit native Mac accelerator: hvf or tcg"},
				&cli.StringSliceFlag{Name: "variable"},
			},
			Action: func(c *cli.Context) error {
				if c.Args().Present() {
					return e.New(e.CodeUsage, "prepare-native-packer-inputs accepts no positional arguments")
				}
				variables := make(map[string]string)
				for _, override := range c.StringSlice("variable") {
					key, value, ok := strings.Cut(override, "=")
					if _, repeated := variables[key]; !ok || key == "" || value == "" || repeated {
						return e.New(e.CodeUsage, "require unique nonempty Packer variable assignments")
					}
					variables[key] = value
				}
				prepared, err := factory.PrepareNativePackerBuildWithAccelerator(c.Context, c.String("definition"), c.String("environment"), c.String("layout"), c.String("bundle"), c.String("toolchain"), c.String("out"), c.String("accelerator"), variables)
				if err != nil {
					return err
				}
				if err := json.NewEncoder(c.App.Writer).Encode(prepared); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing native Packer build input locations")
				}
				return nil
			},
		}, cmdFactoryGitHub()},
	}
}

func factoryGeneratorVersion() string { return version + " (" + commit + ")" }

// Provider adapters supply selectors and diagnostics only. Plan admission and all
// execution/transport behavior remain in the factory module.
func cmdFactoryGitHub() *cli.Command {
	return &cli.Command{Name: "github", Hidden: true, Usage: "execute exact generated GitHub steps", Subcommands: []*cli.Command{
		cmdFactoryCIUpload(),
		cmdFactoryCIFetchLocators(),
		cmdFactoryCIDownload(),
		githubFactoryCommand("check-runtime-inputs", "", func(c *cli.Context, plan *factory.Plan, _ factory.GitHubStepOptions) error {
			return factory.CheckGitHubRuntimeInputs(c.Context, plan, ".")
		}),
		githubFactoryCommand("install-runtime-inputs", "", func(c *cli.Context, plan *factory.Plan, _ factory.GitHubStepOptions) error {
			return factory.InstallGitHubRuntimeInputs(c.Context, plan, ".")
		}),
		githubFactoryCommand("describe-action", "action", func(c *cli.Context, plan *factory.Plan, _ factory.GitHubStepOptions) error {
			return factory.WriteGitHubActionData(c.Context, plan, c.String("action"), os.Getenv("GITHUB_OUTPUT"))
		}),
		cmdFactoryCleanupDevelopment(),
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
		// The generated workflow owns opening and final Run summaries. Individual
		// operations retain their full diagnostics in job logs.
		return action(c, plan, factory.GitHubStepOptions{Stdout: os.Stdout, Stderr: os.Stderr})
	}}
}
