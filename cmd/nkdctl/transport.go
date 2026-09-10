package main

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/urfave/cli/v2"
)

// cmdTransportPack and cmdTransportUnpack are runner-internal carrier adapters.
// They deliberately do not authenticate release content: the source, result, OCI,
// and runner receipt boundaries do that after/before transport. Their authority is
// limited to an exact path allow-list and safe, atomic archive handling.
func cmdTransportPack() *cli.Command {
	return &cli.Command{
		Name:   "transport-pack",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "archive", Required: true},
			&cli.StringFlag{Name: "root", Value: "."},
			&cli.StringSliceFlag{Name: "path", Required: true},
		},
		Action: func(c *cli.Context) error {
			return pipeline.PackTransport(c.Context, c.String("archive"), c.String("root"), c.StringSlice("path"))
		},
	}
}

func cmdTransportUnpack() *cli.Command {
	return &cli.Command{
		Name:   "transport-unpack",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "archive", Required: true},
			&cli.StringFlag{Name: "root", Value: "."},
			&cli.StringSliceFlag{Name: "path", Required: true},
		},
		Action: func(c *cli.Context) error {
			return pipeline.UnpackTransport(c.Context, c.String("archive"), c.String("root"), c.StringSlice("path"))
		},
	}
}

// cmdSandboxExec is the runner-internal boundary that applies the measured
// execution policy immediately before replacing itself with a build command.
// Keeping it in the runner means every CI adapter gets the same enforcement;
// the surrounding engine is still responsible for supplying the pinned image.
func cmdSandboxExec() *cli.Command {
	return &cli.Command{
		Name:      "sandbox-exec",
		Hidden:    true,
		ArgsUsage: "-- command [argument...]",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "write-root", Required: true},
		},
		Action: func(c *cli.Context) error {
			return pipeline.SandboxExec(c.StringSlice("write-root"), c.Args().Slice())
		},
	}
}
