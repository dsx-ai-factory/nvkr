// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/inputinspect"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func cmdExplain() *cli.Command {
	return &cli.Command{Name: "explain", Usage: "explain one authored file and references within an explicit root (offline)", ArgsUsage: "FILE",
		Flags: append(commandPresentationFlags(), []cli.Flag{&cli.StringFlag{Name: "root", Usage: "root containing the complete input closure and reverse-reference scan (required)"}, &cli.BoolFlag{Name: "json", Usage: "emit a descriptive JSON report"}}...),
		Action: func(c *cli.Context) error {
			if c.NArg() != 1 {
				return e.New(e.CodeUsage, "explain requires exactly one file and --root")
			}
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			report, err := inputinspect.Explain(c.Context, c.String("root"), c.Args().First())
			if err != nil {
				return err
			}
			if view.tui {
				return showCommandResult(c, view, explanationDisplay(report), nil)
			}
			out := commandOutput(c)
			if c.Bool("json") {
				if err := json.NewEncoder(out).Encode(report); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing explanation")
				}
				return nil
			}
			text := fmt.Sprintf("%s: %q\n%s\nScanned root: %q\n%s\n", report.Kind, report.Path, report.Summary, report.Root, report.Scope)
			for _, group := range []struct {
				name string
				refs []inputinspect.Reference
			}{{"Direct references", report.References}, {"Reverse references", report.Consumers}} {
				text += group.name + ":\n"
				if len(group.refs) == 0 {
					text += "  (none within this root)\n"
				}
				for _, ref := range group.refs {
					text += fmt.Sprintf("  %q: %q", ref.Relation, ref.Path)
					if ref.Digest != "" {
						text += fmt.Sprintf(" [%q]", ref.Digest)
					}
					text += "\n"
					for _, local := range slices.Sorted(maps.Keys(ref.Mappings)) {
						text += fmt.Sprintf("    %q -> %q\n", local, ref.Mappings[local])
					}
				}
			}
			if _, err := fmt.Fprint(out, text); err != nil {
				return e.Wrap(e.CodeInternal, err, "writing explanation")
			}
			return nil
		}}
}

func explanationDisplay(report inputinspect.Report) commandDisplay {
	display := commandDisplay{Title: "Input explanation", Note: report.Scope, Fields: commandFields("Scanned root", report.Root, "Kind", report.Kind, "Path", report.Path, "Summary", report.Summary)}
	for i, ref := range append(slices.Clone(report.References), report.Consumers...) {
		fields := []progress.Field{}
		if ref.Digest != "" {
			fields = commandFields("Digest", ref.Digest)
		}
		direction := "Direct reference"
		if i >= len(report.References) {
			direction = "Reverse reference"
		}
		fields = append(fields, commandFields("Direction", direction)...)
		for _, key := range slices.Sorted(maps.Keys(ref.Mappings)) {
			fields = append(fields, commandFields(key, ref.Mappings[key])...)
		}
		display.Items = append(display.Items, progress.Item{ID: fmt.Sprintf("reference-%08d", i), Name: ref.Path, Kind: ref.Relation, State: "recorded", Fields: fields})
	}

	return display
}
