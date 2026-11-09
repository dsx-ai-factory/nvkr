// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func cmdRunList() *cli.Command {
	return &cli.Command{Name: "list", Usage: "list recorded local Runs in one attempts root without execution or payload verification", Flags: []cli.Flag{
		&cli.StringFlag{Name: "attempts-root", Value: ".nvkr/runs", Usage: "directory whose attempt directories are listed, one level deep"},
		&cli.StringFlag{Name: "ui", Value: "plain", Usage: "plain, auto, or tui result presentation"},
		&cli.BoolFlag{Name: "json", Usage: "emit safe descriptive recorded Run listing"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable terminal colors (also honors NO_COLOR)"},
	}, Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		if c.Args().Present() {
			return e.New(e.CodeUsage, "run list accepts flags only")
		}
		if c.String("attempts-root") == "" {
			return e.New(e.CodeUsage, "--attempts-root must not be empty")
		}
		if view.tui || view.json {
			defer checkedOutputSignals()()
		}
		list, ownerErr := lifecycle.ListRuns(c.Context, c.String("attempts-root"))
		if ownerErr != nil && list.SchemaVersion == "" {
			return showCommandResult(c, view, commandDisplay{Title: "Recorded Runs", Note: "Listing failed; nothing was executed or verified."}, ownerErr)
		}
		if view.tui {
			return showCommandResult(c, view, runListDisplay(list), ownerErr)
		}
		if view.json {
			raw, err := canonical.Marshal(list)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "serializing Run listing")
			}
			if err := checkedRunWrite(commandOutput(c), append(raw, '\n')); err != nil {
				return err
			}
			return ownerErr
		}
		if err := checkedRunWrite(commandOutput(c), []byte(runListText(list))); err != nil {
			return err
		}
		return ownerErr
	}}
}

// runListText is the plain listing. Attempt and entry names come from the
// filesystem, so they are quoted.
func runListText(list lifecycle.RunList) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Recorded Runs: description only\norder: attempt name (no chronology)\npayload verification: not performed\nruns: %d\n", len(list.Runs))
	for _, run := range list.Runs {
		fmt.Fprintf(&text, "%q: %s, lane %s, outputs %d/%d observed", run.Attempt, run.Status, run.Lane, run.ObservedOutputs, run.RequiredOutputs)
		if run.FailureCode != "" {
			fmt.Fprintf(&text, ", failure %s", run.FailureCode)
		}
		if run.Cleanup != "" {
			fmt.Fprintf(&text, ", %s", run.Cleanup)
		}
		fmt.Fprintf(&text, "\n  record: %q\n", run.Record)
	}
	if len(list.Diagnostics) > 0 {
		fmt.Fprintf(&text, "unavailable: %d\n", len(list.Diagnostics))
		for _, diagnostic := range list.Diagnostics {
			fmt.Fprintf(&text, "%q: %s (%s): %s\n", diagnostic.Entry, diagnostic.Problem, diagnostic.Code, diagnostic.Description)
		}
	}
	return text.String()
}

func runListDisplay(list lifecycle.RunList) commandDisplay {
	display := commandDisplay{Title: "Recorded Runs", Note: "Stored metadata only, ordered by attempt name; no chronology, liveness or payload verification.",
		Fields: commandFields("Runs", strconv.Itoa(len(list.Runs)), "Unavailable entries", strconv.Itoa(len(list.Diagnostics)))}
	for _, run := range list.Runs {
		fields := commandFields("Lane", run.Lane, "Outputs observed", fmt.Sprintf("%d of %d", run.ObservedOutputs, run.RequiredOutputs), "Record", run.Record)
		if run.FailureCode != "" {
			fields = append(fields, progress.Field{Label: "Failure code", Value: run.FailureCode})
		}
		if run.Cleanup != "" {
			fields = append(fields, progress.Field{Label: "Cleanup", Value: run.Cleanup})
		}
		display.Items = append(display.Items, progress.Item{ID: "run/" + run.Attempt, Name: run.Attempt, Kind: "recorded Run", State: run.Status, Fields: fields})
	}
	for _, diagnostic := range list.Diagnostics {
		display.Items = append(display.Items, progress.Item{ID: "entry/" + diagnostic.Entry, Name: diagnostic.Entry, Kind: "attempts-root entry", State: "unavailable",
			Fields: commandFields("Problem", diagnostic.Problem, "Code", diagnostic.Code, "Description", diagnostic.Description)})
	}
	return display
}
