// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

// The presenter owns the worker lifetime. Only its completed owning operation
// replaces provisional acquisition state with a complete command description.
func showReleaseMetadataLoad(c *cli.Context, view runPresentation, title, selection string,
	work func(context.Context, func(delivery.MetadataProgress)) (commandDisplay, error),
) error {
	defer checkedOutputSignals()()
	source := &releaseMetadataSource{selection: selection, snapshot: progress.Snapshot{Mode: "command", Phase: "inspection", Status: "loading", Title: title,
		Note: "Loading release root; metadata closure checks pending. Payloads, signatures and policy are not verified."}}
	var diagnostics rundiagnostics.Buffer
	shown := commandServices(c).show(c.Context, runpresenter.Options{Live: true, StayOpen: true, NoColor: view.noColor, Diagnostics: &diagnostics}, source, func(ctx context.Context) error {
		display, err := work(ctx, source.report)
		source.finish(display, err)
		return err
	})
	return replayRunDiagnostics(c, &diagnostics, preserveRunError(shown.WorkErr, shown.PresentationErr))
}

type releaseMetadataSource struct {
	mu        sync.Mutex
	selection string
	snapshot  progress.Snapshot
}

func (source *releaseMetadataSource) Snapshot() progress.Snapshot {
	source.mu.Lock()
	defer source.mu.Unlock()
	return progress.New(source.snapshot).Snapshot()
}

func (source *releaseMetadataSource) report(update delivery.MetadataProgress) {
	source.mu.Lock()
	defer source.mu.Unlock()
	s := &source.snapshot
	if update.Done == 0 {
		s.Items = nil
	}
	s.Note = fmt.Sprintf("Loading metadata: %d/%d objects; closure checks pending. Payloads, signatures and policy are not verified.", update.Done, update.Total)
	s.Fields = commandFields("Publication identity", update.RootDigest, "Metadata objects", fmt.Sprintf("%d/%d", update.Done, update.Total))
	if update.Name != "" && (source.selection == "" || update.Output && update.Name == source.selection) {
		prefix := "evidence/"
		if update.Output {
			prefix = "output/"
		}
		s.Items = append(s.Items, progress.Item{ID: prefix + update.Name, Name: update.Name, Kind: update.Kind, State: "loading", Primary: update.Output,
			Summary: "metadata read; closure checks pending", Fields: commandFields("Manifest digest", update.Digest)})
		sort.Slice(s.Items, func(i, j int) bool { return s.Items[i].ID < s.Items[j].ID })
	}
}

func (source *releaseMetadataSource) finish(display commandDisplay, err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	if err != nil {
		source.snapshot.Status = "failed"
		source.snapshot.FailureCode = string(runErrorCode(err))
		source.snapshot.Note = "Metadata loading failed; partial entries are incomplete. No verification result is established."
		return
	}
	source.snapshot = progress.Snapshot{Mode: "command", Phase: "result", Status: "complete", Title: display.Title, Note: display.Note, Fields: display.Fields, Items: display.Items}
	sort.Slice(source.snapshot.Items, func(i, j int) bool { return source.snapshot.Items[i].ID < source.snapshot.Items[j].ID })
}
