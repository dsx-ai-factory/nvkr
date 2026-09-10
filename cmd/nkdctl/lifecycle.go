package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
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
			fmt.Printf("definition: %s\ndigest: %s\nrecipes: %d\noutputs: %d\nclusters: %d\n", definition.Metadata.Name, digest, len(definition.Recipes), len(definition.Outputs), len(definition.Clusters))
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
	generationFlags := append(lifecycleInputFlags(), &cli.StringFlag{Name: "engine", Value: factory.EngineGitHub, Usage: "hosted CI adapter: github | gitlab"})
	return &cli.Command{Name: "run", Usage: "plan, generate, and execute Definition runs", Subcommands: []*cli.Command{
		{Name: "finalize", Hidden: true, Usage: "finalize one generated hosted Run from restored carriers", Flags: append(lifecycleInputFlags(),
			&cli.StringFlag{Name: "plan", Required: true}, &cli.StringFlag{Name: "attempt", Required: true},
			&cli.StringFlag{Name: "workspace", Value: ".nkd/factory"}, &cli.StringFlag{Name: "out", Value: ".nkd/run.json"}), Action: func(c *cli.Context) error {
			run, err := lifecycle.FinalizeGenerated(c.Context, c.String("definition"), c.String("environment"), c.String("plan"), factoryGeneratorVersion(), c.String("attempt"), c.String("workspace"), c.String("out"))
			if err != nil {
				return err
			}
			fmt.Printf("hosted Run %s: %s (%d observed outputs)\n", run.Attempt, run.Status, len(run.Observed))
			return nil
		}},
		{Name: "plan", Usage: "compile a Definition and Environment into an inspectable run plan", Flags: append(lifecycleInputFlags(), &cli.BoolFlag{Name: "json", Usage: "emit the generated internal plan"}), Action: func(c *cli.Context) error {
			plan, err := compileLifecycleInput(c)
			if err != nil {
				return err
			}
			if c.Bool("json") {
				raw, err := canonical.Marshal(plan)
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(append(raw, '\n'))
				return err
			}
			definition, err := lifecycle.LoadDefinition(c.Context, c.String("definition"))
			if err != nil {
				return err
			}
			digest, err := plan.Digest()
			if err != nil {
				return err
			}
			fmt.Printf("definition: %s\ndefinitionDigest: %s\nenvironmentDigest: %s\nrun plan: %s\nrecipes: %d\nexpected outputs: %d\nlane: %s\n", plan.Name, plan.DefinitionDigest, plan.EnvironmentDigest, digest, len(plan.Recipes), len(definition.Outputs), plan.Qualification)
			return nil
		}},
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
			&cli.StringFlag{Name: "attempts-root", Value: ".nkd/runs"}, &cli.StringFlag{Name: "runner-source", Value: "."},
			&cli.IntFlag{Name: "jobs", Aliases: []string{"j"}, Value: 1}, &cli.StringFlag{Name: "runtime", Value: "docker"},
			&cli.StringFlag{Name: "go", Value: "go"}, &cli.BoolFlag{Name: "publish"}, &cli.BoolFlag{Name: "dry-run"}), Action: func(c *cli.Context) error {
			prepared, err := lifecycle.PrepareFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion(), c.String("attempt"))
			if err != nil {
				return err
			}
			result, err := lifecycle.ExecuteLocal(c.Context, prepared, lifecycle.LocalOptions{AttemptsRoot: c.String("attempts-root"), RunnerSource: c.String("runner-source"), Jobs: c.Int("jobs"), Runtime: c.String("runtime"), Go: c.String("go"), Publish: c.Bool("publish"), DryRun: c.Bool("dry-run"), Stdout: os.Stdout, Stderr: os.Stderr})
			if err != nil {
				return err
			}
			workspace := filepath.Join(c.String("attempts-root"), c.String("attempt"))
			fmt.Printf("\nattempt: %s\nrun: %s\nlane: %s\nstatus: %s\nobserved outputs: %d\n", result.Attempt, result.PlanDigest, result.Lane, result.Status, len(result.Observed))
			if !c.Bool("dry-run") {
				fmt.Printf("record: %s\n", filepath.Join(workspace, "lifecycle.json"))
			}
			return nil
		}},
	}}
}

func compileLifecycleInput(c *cli.Context) (*factory.Plan, error) {
	if c.String("definition") == "" || c.String("environment") == "" {
		return nil, e.New(e.CodeUsage, "run requires --definition and --environment")
	}
	return lifecycle.CompileFiles(c.Context, c.String("definition"), c.String("environment"), factoryGeneratorVersion())
}
