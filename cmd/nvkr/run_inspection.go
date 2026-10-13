// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

type inspectedRunSource struct{ value progress.Snapshot }

func (s inspectedRunSource) Snapshot() progress.Snapshot { return s.value }

func cmdRunInspect() *cli.Command {
	return &cli.Command{Name: "inspect", Usage: "browse one recorded Run without execution or payload verification", Flags: []cli.Flag{
		&cli.StringFlag{Name: "record", Required: true, Usage: "exact lifecycle.json in the existing attempt directory"},
		&cli.StringFlag{Name: "ui", Value: "plain", Usage: "plain, auto, or tui recorded Run exploration"},
		&cli.BoolFlag{Name: "json", Usage: "emit safe descriptive recorded Run metadata"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable terminal colors (also honors NO_COLOR)"},
	}, Action: func(c *cli.Context) error {
		view, err := selectCommandPresentation(c)
		if err != nil {
			return err
		}
		if c.Args().Present() {
			return e.New(e.CodeUsage, "run inspect accepts flags only")
		}
		defer checkedOutputSignals()()
		inspection, err := lifecycle.InspectRun(c.Context, c.String("record"))
		if err != nil {
			return err
		}
		if view.tui {
			shown := commandServices(c).show(c.Context, runpresenter.Options{NoColor: view.noColor}, inspectedRunSource{inspection.Snapshot()}, nil)
			return preserveRunError(shown.WorkErr, shown.PresentationErr)
		}
		if view.json {
			raw, err := canonical.Marshal(struct {
				Authority   string                  `json:"authority"`
				Observation string                  `json:"observation"`
				Record      lifecycle.RunInspection `json:"record"`
			}{"description-only", "file-recorded", inspection})
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "serializing Run inspection")
			}
			return checkedRunWrite(commandOutput(c), append(raw, '\n'))
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Recorded Run: description only\nmetadata: file-recorded\npayload verification: not performed\nattempt: %s\nrun: %s\nlane: %s\nstatus: %s\nrecord available: %t\ncleanup incomplete: %t\ninterrupted: %t\n", inspection.Attempt, inspection.PlanDigest, inspection.Lane, inspection.Status, inspection.RecordAvailable, inspection.CleanupIncomplete, inspection.Interrupted)
		if inspection.FailureCode != "" {
			fmt.Fprintf(&text, "failure code: %s\n", inspection.FailureCode)
		}
		for _, output := range inspection.Outputs {
			fmt.Fprintf(&text, "output: %q (%s): %s\n", output.Name, output.Kind, output.State)
		}
		return checkedRunWrite(commandOutput(c), []byte(text.String()))
	}}
}
