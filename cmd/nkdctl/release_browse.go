package main

import (
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/releaseview"
	"github.com/urfave/cli/v2"
)

func releaseBrowseCmd() *cli.Command {
	return &cli.Command{
		Name: "browse", Usage: "inspect exact OCI release metadata and evidence (payloads and signatures not verified)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "reference", Required: true, Usage: "publication root repository@sha256:digest"},
			&cli.StringFlag{Name: "layout", Usage: "inspect a local OCI layout instead of pulling the registry"},
			&cli.StringFlag{Name: "format", Value: "text", Usage: "text, json or mermaid"},
			&cli.StringFlag{Name: "output", Usage: "focus on one exact named output and its evidence"},
			&cli.StringFlag{Name: "workspace", Usage: "temporary registry inspection workspace"},
			&cli.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "bound the complete inspection"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release browse takes flags only and requires a positive timeout")
			}
			presentation := releaseview.Options{Format: c.String("format"), Output: c.String("output")}
			if err := presentation.Validate(); err != nil {
				return err
			}
			description, err := lifecycle.InspectRelease(c.Context, c.String("reference"), lifecycle.InspectionOptions{
				LayoutPath: c.String("layout"), WorkspaceRoot: c.String("workspace"), Timeout: c.Duration("timeout"),
				Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
			})
			if err != nil {
				return err
			}
			raw, err := releaseview.Render(description, presentation)
			if err != nil {
				return err
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release view")
			}
			return nil
		},
	}
}
