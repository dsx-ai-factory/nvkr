package main

import (
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/urfave/cli/v2"
)

// cmdFactory retains only the generated-plan execution entry point. Definition,
// Environment, Run, and provider generation are owned by the lifecycle commands.
func cmdFactory() *cli.Command {
	return &cli.Command{
		Name:  "factory",
		Usage: "execute generated internal plan nodes",
		Subcommands: []*cli.Command{{
			Name:   "execute-node",
			Hidden: true,
			Usage:  "execute one non-IR node from an exact generated plan",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "plan", Required: true},
				&cli.StringFlag{Name: "node", Required: true},
				&cli.StringFlag{Name: "workspace", Value: ".nkd/factory"},
			},
			Action: func(c *cli.Context) error {
				if c.String("workspace") != ".nkd/factory" {
					return e.New(e.CodeUsage, "generated plan nodes require workspace .nkd/factory")
				}
				plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
				if err != nil {
					return err
				}
				return factory.ExecuteNode(c.Context, plan, c.String("node"), os.Stdout, os.Stderr)
			},
		}},
	}
}

func factoryGeneratorVersion() string { return version + " (" + commit + ")" }
