// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func releasePullCmd() *cli.Command {
	return releasePullCmdWithOperation(lifecycle.PullReleaseOutput)
}

type releasePullOperation func(context.Context, string, string, string, lifecycle.PullOptions) (delivery.PulledOutput, error)

func releasePullCmdWithOperation(pull releasePullOperation) *cli.Command {
	return &cli.Command{
		Name: "pull", Usage: "download one named output (signatures and policy not verified)",
		OnUsageError: codedUsageError,
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "reference", Usage: "publication root repository@sha256:digest (required)"},
			&cli.StringFlag{Name: "output", Usage: "exact output name from release browse (required)"},
			&cli.StringFlag{Name: "out", Usage: "new destination directory for the selected output (required)"},
			&cli.StringFlag{Name: "workspace", Usage: "parent for temporary registry download storage"},
			&cli.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "bound the complete download and extraction"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		}, commandPresentationFlags()...),
		Action: func(c *cli.Context) (returnedErr error) {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			shown := false
			defer func() {
				if returnedErr != nil && view.tui && !shown {
					returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release output pull"), returnedErr)
				}
			}()
			if c.NArg() != 0 || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release pull takes flags only and requires a positive timeout")
			}
			for _, flag := range []string{"reference", "output", "out"} {
				if !c.IsSet(flag) {
					return e.New(e.CodeUsage, "release pull requires --%s", flag)
				}
			}
			result, err := pull(c.Context, c.String("reference"), c.String("output"), c.String("out"), lifecycle.PullOptions{
				WorkspaceRoot: c.String("workspace"), Timeout: c.Duration("timeout"),
				Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
			})
			if err != nil {
				return err
			}
			if view.tui {
				shown = true
				return showCommandResult(c, view, releasePullDisplay(c.String("output"), result), nil)
			}
			if _, err := c.App.Writer.Write([]byte(releasePullMessage(c.String("output"), c.String("out"), result))); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release pull result")
			}
			return nil
		},
	}
}

func releasePullMessage(output, out string, result delivery.PulledOutput) string {
	var message strings.Builder
	fmt.Fprintf(&message, "Pulled %q (%q) into %q; payload integrity checked, signatures and policy not verified.\n",
		output, result.Output.Kind, out)
	if result.Output.OS != "" {
		fmt.Fprintf(&message, "  Platform: %q\n", result.Output.OS+"/"+result.Output.Arch)
	}
	if result.Output.ImageIndex != nil {
		fmt.Fprintf(&message, "  Platforms: %q\n", strings.Join(result.Output.ImageIndex.RequiredPlatforms, ", "))
	}
	for _, file := range result.Files {
		fmt.Fprintf(&message, "  %q (%s)\n", filepath.Join(out, file.Path), file.SHA256)
	}
	return message.String()
}

func releasePullDisplay(output string, result delivery.PulledOutput) commandDisplay {
	display := commandDisplay{Title: "Release output pull",
		Note:   "The selected output was written. Payload integrity checked; signatures and policy were not verified.",
		Fields: commandFields("Output", output, "Format", result.Output.Kind)}
	if result.Output.OS != "" {
		display.Fields = append(display.Fields, commandFields("Platform", result.Output.OS+"/"+result.Output.Arch)...)
	}
	if result.Output.ImageIndex != nil {
		display.Fields = append(display.Fields, commandFields("Platforms", strings.Join(result.Output.ImageIndex.RequiredPlatforms, ", "))...)
	}
	for _, file := range result.Files {
		display.Items = append(display.Items, progress.Item{ID: "file/" + file.Path, Name: file.Path, Kind: result.Output.Kind, State: "written", Primary: true,
			Fields: commandFields("SHA256", file.SHA256, "Bytes", fmt.Sprint(file.Size))})
	}
	return display
}
