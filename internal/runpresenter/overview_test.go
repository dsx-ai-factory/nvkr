// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
)

func overviewFixture() progress.Snapshot {
	state := progress.Snapshot{Mode: "command", Title: "Planned dependencies", Status: "complete", Note: "Description only; no execution or verification authority.", Fields: []progress.Field{{Label: "Plan digest", Value: "sha256:context"}}, Items: []progress.Item{
		{ID: "output/product", Name: "product", Kind: "output", State: "planned", Primary: true, Dependencies: []string{"internal/000"}},
	}}
	for i := range 1000 {
		state.Items = append(state.Items, progress.Item{ID: fmt.Sprintf("internal/%03d", i), Name: fmt.Sprintf("step-%03d", i), Kind: "producer", State: "planned", Fields: []progress.Field{{Label: "Component", Value: "selected-component"}}})
	}
	return state
}

func TestOverviewKeepsCompleteGraphReachableWithoutDefaultClutter(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			state := overviewFixture()
			view := NewView(false, true)
			screen := sim(t, size[0], size[1])
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			got := text(screen)
			if len(view.visible) != 1 || view.selected != "output/product" || !strings.Contains(got, "Overview 1") || !strings.Contains(got, "1001 total") {
				t.Fatalf("graph did not start with product overview: %s", got)
			}
			for _, hidden := range []string{"step-000", "sha256:context", "Command context", "Result activity (static)"} {
				if strings.Contains(got, hidden) {
					t.Fatalf("default screen dumped %q", hidden)
				}
			}
			view.Input(key(tcell.KeyEnter, 0))
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			if !strings.Contains(text(screen), "Enter follows 1/1: [producer] step-000") {
				t.Fatal("dependency action has no visible target")
			}
			view.Input(key(tcell.KeyEnter, 0))
			view.Draw(screen, state, time.Now(), false)
			if view.selected != "internal/000" || !strings.Contains(strings.Join(view.commandDetails(), "\n"), "Outside overview") {
				t.Fatal("hidden dependency became unreachable")
			}
			details := strings.Join(view.commandDetails(), "\n")
			if strings.Index(details, "selected-component") > strings.Index(details, "Command context") {
				t.Fatal("global metadata displaced selected entry")
			}
			view.Input(key(tcell.KeyEscape, 0))
			if view.selected != "output/product" {
				t.Fatal("Back lost exact parent")
			}
			view.Input(key(tcell.KeyEscape, 0))
			view.Input(key(tcell.KeyRune, 'g'))
			if len(view.visible) != len(state.Items) {
				t.Fatal("all-entries toggle omitted nodes")
			}
			view.Input(key(tcell.KeyRune, 'g'))
			view.Input(key(tcell.KeyRune, '/'))
			for _, r := range "step-999" {
				view.Input(key(tcell.KeyRune, r))
			}
			view.Input(key(tcell.KeyEnter, 0))
			if len(view.visible) != 1 || view.selected != "internal/999" {
				t.Fatal("overview search cannot reach every supplied node")
			}
			view.Input(key(tcell.KeyRune, 'q'))
			if !view.confirmQuit {
				t.Fatal("overview bypassed quit confirmation")
			}
		})
	}
}

func TestNodeOnlyViewsNeverInventAnOutputOverview(t *testing.T) {
	state := overviewFixture()
	state.Items = state.Items[1:]
	view := NewView(false, true)
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	if view.overview() || len(view.visible) != 1000 {
		t.Fatal("producer-only graph lost supplied nodes")
	}
	view.Input(key(tcell.KeyRune, 'g'))
	if len(view.visible) != 1000 {
		t.Fatal("toggle changed the admitted view")
	}
}

func TestOverviewRowsUseOneLineAndKeepStatesBeforeLongNames(t *testing.T) {
	view := NewView(false, true)
	state := commandFixture()
	state.Items[0].Name = strings.Repeat("long-name", 20)
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	lines := view.commandResults(80)
	if len(lines) != len(state.Items) || !strings.HasPrefix(lines[0], "> changed  ") {
		t.Fatal("one-line rows lost state or expanded into extra metadata rows")
	}
}

func TestSummaryDefersExactDigestsAndDetailsKeepThem(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	state := progress.Snapshot{Mode: "command", Title: "Generation", Status: "complete", Fields: []progress.Field{{Label: "Files", Value: "current"}, {Label: "Plan digest", Value: digest}}}
	view := NewView(false, true)
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	summary := strings.Join(view.commandResults(80), "\n")
	if strings.Contains(summary, digest) || !strings.Contains(summary, "Files: current") || !strings.Contains(summary, "Enter opens Details") {
		t.Fatal("summary still dumps the full identity or loses outcome")
	}
	if !strings.Contains(strings.Join(view.commandDetails(), "\n"), digest) {
		t.Fatal("progressive disclosure lost exact identity")
	}
}

func TestPinnedDependencyKeepsKindVisibleBeforeLongName(t *testing.T) {
	state := overviewFixture()
	state.Items[1].Name = strings.Repeat("long dependency ", 30)
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.focus = 1
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	if !strings.Contains(text(screen), "Enter follows 1/1: [producer]") {
		t.Fatal("pinned hint lost target kind behind long name")
	}
}
