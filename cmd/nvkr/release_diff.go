// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func releaseDiffCmd() *cli.Command {
	return &cli.Command{
		Name: "diff", Usage: "compare the artifact inventories of two exact releases (metadata only; evidence, signatures and policy not compared)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "before", Required: true, Usage: "previous publication root repository@sha256:digest"},
			&cli.StringFlag{Name: "after", Required: true, Usage: "next publication root repository@sha256:digest"},
			&cli.StringFlag{Name: "before-layout", Usage: "inspect a local OCI layout for --before instead of pulling the registry"},
			&cli.StringFlag{Name: "after-layout", Usage: "inspect a local OCI layout for --after instead of pulling the registry"},
			&cli.StringFlag{Name: "format", Value: "text", Usage: "text or json"},
			&cli.BoolFlag{Name: "exit-code", Usage: "return differences-present status 3 when the inventories differ"},
			&cli.StringFlag{Name: "workspace", Usage: "temporary registry inspection workspace"},
			&cli.DurationFlag{Name: "timeout", Value: time.Hour, Usage: "bound both inspections together"},
			&cli.BoolFlag{Name: "no-cache", Usage: "fetch metadata without reading or writing the disk cache"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		}, commandPresentationFlags()...),
		Action: func(c *cli.Context) (returnedErr error) {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			if c.NArg() != 0 || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release diff takes flags only and requires a positive timeout")
			}
			if err := releaseview.ValidateComparisonFormat(c.String("format")); err != nil {
				return err
			}
			shown := false
			defer func() {
				if returnedErr != nil && view.tui && !shown {
					returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release comparison"), returnedErr)
				}
			}()
			comparison, err := lifecycle.CompareReleases(c.Context, lifecycle.ReleaseComparisonRequest{
				Before: c.String("before"), After: c.String("after"),
				BeforeLayout: c.String("before-layout"), AfterLayout: c.String("after-layout"),
				Inspection: lifecycle.InspectionOptions{
					WorkspaceRoot: c.String("workspace"), Timeout: c.Duration("timeout"), DisableCache: c.Bool("no-cache"),
					Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
				},
			})
			if err != nil {
				return err
			}
			var difference error
			if c.Bool("exit-code") && !comparison.Equal() {
				difference = e.New(e.CodeDiff, "release inventories differ")
			}
			if view.tui {
				shown = true
				return showCommandResult(c, view, releaseComparisonDisplay(comparison), difference)
			}
			raw, err := comparison.Render(c.String("format"))
			if err != nil {
				return err
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release comparison")
			}
			return difference
		},
	}
}

func releaseComparisonDisplay(comparison releaseview.Comparison) commandDisplay {
	display := commandDisplay{Title: "Release comparison",
		Note: "Artifact inventory metadata only. Not compared: " + strings.Join(comparison.NotCompared, ", ") +
			". The viewer shortens very long values; --format json has them in full.",
		Fields: commandFields("Before", comparison.Before.RootDigest, "After", comparison.After.RootDigest,
			"Added", fmt.Sprint(len(comparison.Added)), "Removed", fmt.Sprint(len(comparison.Removed)),
			"Changed", fmt.Sprint(len(comparison.Changed)), "Unchanged", fmt.Sprint(comparison.Unchanged))}
	for _, output := range comparison.Added {
		display.Items = append(display.Items, progress.Item{ID: "output/" + output.Name, Name: output.Name, Kind: output.Kind, State: "added", Primary: true,
			Fields: commandFields("After digest", output.Digest)})
	}
	for _, output := range comparison.Removed {
		display.Items = append(display.Items, progress.Item{ID: "output/" + output.Name, Name: output.Name, Kind: output.Kind, State: "removed", Primary: true,
			Fields: commandFields("Before digest", output.Digest)})
	}
	for _, change := range comparison.Changed {
		state := "changed"
		if change.ReferenceOnly {
			state = "reference only"
		}
		item := progress.Item{ID: "output/" + change.Name, Name: change.Name, Kind: change.After.Kind, State: state, Primary: true,
			Fields: commandFields("Before digest", change.Before.Digest, "After digest", change.After.Digest)}
		for _, pair := range change.FieldChanges() {
			// Transport references stay out of the TUI, as in release browse;
			// text and JSON carry their values.
			if pair.Field == "reference" {
				item.Fields = append(item.Fields, progress.Field{Label: "Changed field", Value: pair.Field})
				continue
			}
			// One field per side, so a long before value never hides the after value.
			item.Fields = append(item.Fields, progress.Field{Label: pair.Field + " before", Value: pair.Before},
				progress.Field{Label: pair.Field + " after", Value: pair.After})
		}
		display.Items = append(display.Items, item)
	}
	return display
}
