// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"sort"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

const commandPresentationServicesKey = "nvkr.command-presentation-services"

// commandDisplay contains only explicitly projected public fields from an
// owning result. Raw console bytes, transport locators and errors are excluded.
type commandDisplay struct {
	Title, Note string
	Fields      []progress.Field
	Items       []progress.Item
}

func commandPresentationFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "ui", Value: "plain", Usage: "plain, auto, or tui result presentation"},
		&cli.BoolFlag{Name: "no-color", Usage: "disable terminal colors (also honors NO_COLOR)"},
	}
}

func commandServices(c *cli.Context) runPresentationServices {
	if services, ok := c.App.Metadata[commandPresentationServicesKey].(runPresentationServices); ok && services.eligible != nil && services.show != nil {
		return services
	}
	return defaultRunPresentationServices()
}

// Selection precedes loaders, resolution, transactions, and verification. An
// explicitly requested serialized format keeps its stdout contract and conflicts with
// explicit TUI; implicit default machine formats may be viewed with --ui=tui.
func selectCommandPresentation(c *cli.Context) (runPresentation, error) {
	view, err := selectRunPresentationWithEligibility(c, true, commandServices(c).eligible)
	if err != nil {
		return view, err
	}
	explicitFormat := c.IsSet("format") && (c.String("format") == "json" || c.String("format") == "mermaid")
	if c.Command.Name == "resolve" && c.IsSet("output") && (c.String("output") == "canonical" || c.String("output") == "nodes") {
		explicitFormat = true
	}
	if explicitFormat {
		if c.String("ui") == "tui" {
			return runPresentation{}, e.New(e.CodeUsage, "--ui=tui cannot be combined with explicit JSON or Mermaid output")
		}
		view.tui = false
	}
	return view, nil
}

// The operation has already returned and released its receipts before this
// read-only view opens. Closing the viewer neither reruns nor rolls back it.
func showCommandResult(c *cli.Context, view runPresentation, display commandDisplay, ownerErr error) error {
	if !view.tui {
		return ownerErr
	}
	defer checkedOutputSignals()()
	status := "complete"
	if ownerErr != nil {
		status = "failed"
		if runErrorCode(ownerErr) == e.CodeDiff {
			status = "different"
		}
	}
	snapshot := progress.Snapshot{Mode: "command", Phase: "result", Status: status,
		Title: display.Title, Note: display.Note, Fields: display.Fields, Items: slices.Clone(display.Items),
		FailureCode: string(runErrorCode(ownerErr))}
	// Sorting changes only the detached item order. New takes the defensive
	// nested copy once, after navigation validation, before exposing the source.
	sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].ID < snapshot.Items[j].ID })
	ids := make(map[string]struct{}, len(snapshot.Items))
	for _, item := range snapshot.Items {
		if item.ID == "" {
			return preserveRunError(ownerErr, e.New(e.CodeInternal, "command result has no navigation key"))
		}
		if _, exists := ids[item.ID]; exists {
			return preserveRunError(ownerErr, e.New(e.CodeInternal, "command result repeats a navigation key"))
		}
		ids[item.ID] = struct{}{}
	}
	source := progress.New(snapshot)
	var diagnostics rundiagnostics.Buffer
	shown := commandServices(c).show(c.Context, runpresenter.Options{NoColor: view.noColor, Diagnostics: &diagnostics}, source, nil)
	return replayRunDiagnostics(c, &diagnostics, preserveRunError(ownerErr, preserveRunError(shown.WorkErr, shown.PresentationErr)))
}
