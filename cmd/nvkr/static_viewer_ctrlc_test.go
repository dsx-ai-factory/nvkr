// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/gdamore/tcell/v2"
)

type committedViewerScreen struct {
	tcell.SimulationScreen
	once sync.Once
}

func (s *committedViewerScreen) Show() {
	s.SimulationScreen.Show()
	s.once.Do(func() { _ = s.PostEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)) })
}

func TestGenerationCommitSurvivesReadOnlyViewerCtrlC(t *testing.T) {
	lifecycleCommandRepo(t)
	root := t.TempDir()
	workflow := filepath.Join(root, ".github", "workflows", "nvkr-run.yml")
	var committed []byte
	shown := false
	app := newApp()
	app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{
		eligible: func(io.Writer) bool { return true },
		show: func(ctx context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
			shown = true
			var err error
			committed, err = os.ReadFile(workflow)
			if err != nil || len(committed) == 0 {
				t.Fatal("result viewer opened before generation committed", err)
			}
			if source.Snapshot().Status != "complete" || options.Live || work != nil {
				t.Fatal("committed generation is not a read-only successful result")
			}
			options.Screen = &committedViewerScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			return runpresenter.Run(ctx, options, source, nil)
		},
	}}
	args := []string{"nvkr", "run", "generate", "--definition", "definitions/nvke-v1/definition.yaml", "--environment", "definitions/nvke-v1/environment-github.yaml", "--root", root, "--ui=tui"}
	if err := app.RunContext(t.Context(), args); err != nil || !shown {
		t.Fatal("Ctrl-C changed the committed command outcome", err)
	}
	after, err := os.ReadFile(workflow)
	if err != nil || !bytes.Equal(committed, after) {
		t.Fatal("closing result viewer changed committed generation", err)
	}
}
