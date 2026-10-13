// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
)

func TestStaticTabsAndFiltersOfferOnlySuppliedContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		states   []string
		filters  []int
		activity bool
	}{
		{"planned", []string{"planned", "planned"}, []int{0}, false},
		{"catalog changes", []string{"changed", "unchanged"}, []int{0, 1}, false},
		{"failures", []string{"failed", "unchanged"}, []int{0, 2}, true},
		{"changes and failures", []string{"added", "failed"}, []int{0, 1, 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := commandFixture()
			for i, value := range tc.states {
				state.Items[i].State = value
			}
			if tc.activity {
				state.Activity = []progress.Activity{{Sequence: 1, Text: "supplied event"}}
			}
			view := NewView(false, true)
			screen := sim(t, 80, 24)
			view.Draw(screen, state, time.Now(), false)
			if !reflect.DeepEqual(view.availableFilters(), tc.filters) {
				t.Fatal("result filters include unavailable states")
			}
			for i := range len(tc.filters) {
				view.Input(key(tcell.KeyRune, 'f'))
				if view.filter != tc.filters[(i+1)%len(tc.filters)] || len(view.visible) == 0 {
					t.Fatal("filter cycle entered an impossible empty result")
				}
			}
			view.Input(key(tcell.KeyTab, 0))
			view.Input(key(tcell.KeyTab, 0))
			expected := 0
			if tc.activity {
				expected = 2
			}
			if view.focus != expected {
				t.Fatal("tab cycle included absent Activity")
			}
			if !tc.activity {
				view.Input(key(tcell.KeyRune, 'a'))
				view.Input(key(tcell.KeyRune, '3'))
				if view.focus != 0 || strings.Contains(strings.Join(view.helpLines(), "\n"), "Activity") {
					t.Fatal("hidden tab remains reachable or advertised")
				}
			}
		})
	}
	view := NewView(true, true)
	state := fixture()
	state.Activity = nil
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	view.Input(key(tcell.KeyRune, 'a'))
	if view.focus != 2 {
		t.Fatal("live activity disappeared before its first event")
	}
}

func TestDrillPathAndDependencySectionPreserveExactBackHistory(t *testing.T) {
	state := commandFixture()
	state.Items[1].Dependencies = []string{"leaf"}
	state.Items = append(state.Items, progress.Item{ID: "leaf", Name: "leaf", Kind: "material", State: "planned"})
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.Draw(screen, state, time.Now(), false)
	view.selected = "entry-a"
	view.Input(key(tcell.KeyEnter, 0))
	view.Draw(screen, state, time.Now(), false)
	details := strings.Join(view.commandDetails(), "\n")
	if strings.Index(details, "Dependencies:") > strings.Index(details, "Commit:") || strings.Index(details, "Commit:") > strings.Index(details, "Command context") {
		t.Fatal("dependencies are buried behind metadata")
	}
	for _, id := range []string{"entry-b", "leaf"} {
		view.Input(key(tcell.KeyEnter, 0))
		view.Draw(screen, state, time.Now(), false)
		if view.selected != id || !strings.Contains(strings.Join(view.commandDetails(), "\n"), "Path:") {
			t.Fatal("drill lost identity or visible path")
		}
	}
	for _, id := range []string{"entry-b", "entry-a"} {
		view.Input(key(tcell.KeyEscape, 0))
		if view.selected != id {
			t.Fatalf("Back selected %s instead of exact identity %s", view.selected, id)
		}
	}
}

func TestSmallTerminalSuspendsInvisibleBrowsingAndRetainsStopControls(t *testing.T) {
	for _, size := range [][2]int{{39, 24}, {80, 11}, {20, 6}, {0, 0}} {
		for _, live := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/live%t", size[0], size[1], live), func(t *testing.T) {
				view := NewView(live, true)
				state := fixture()
				if !live {
					state.Mode = "recorded"
				}
				screen := sim(t, 80, 24)
				view.Draw(screen, state, time.Now(), false)
				view.selected, view.focus = "image", 1
				view.Draw(screen, state, time.Now(), false)
				screen.SetSize(size[0], size[1])
				view.Draw(screen, state, time.Now(), false)
				for _, event := range []*tcell.EventKey{key(tcell.KeyEnter, 0), key(tcell.KeyDown, 0), key(tcell.KeyTab, 0), key(tcell.KeyEscape, 0), key(tcell.KeyRune, 'f'), key(tcell.KeyRune, '/'), key(tcell.KeyRune, 'a'), key(tcell.KeyRune, '?')} {
					view.Input(event)
				}
				if view.selected != "image" || view.operation != "" || view.focus != 1 || view.filter != 0 || view.searching || view.help || view.linkIndex != 0 || len(view.back) != 0 {
					t.Fatal("invisible browsing mutated navigation")
				}
				view.Input(key(tcell.KeyRune, 'q'))
				if !view.confirmQuit || view.Input(key(tcell.KeyEnter, 0)) != None || view.confirmQuit {
					t.Fatal("small screen lost safe close confirmation")
				}
				view.Input(key(tcell.KeyRune, 'q'))
				expected := Close
				if live {
					expected = Cancel
				}
				if view.Input(key(tcell.KeyRune, 'y')) != expected || view.Input(key(tcell.KeyCtrlC, 0)) != expected {
					t.Fatal("small screen lost stop controls")
				}
				screen.SetSize(80, 24)
				view.Draw(screen, state, time.Now(), false)
				view.Input(key(tcell.KeyEnter, 0))
				if view.selected != "cli" {
					t.Fatal("visible dependency browsing did not resume")
				}
			})
		}
	}
}

func TestConstantStaticStatesAreDeferredToDetails(t *testing.T) {
	state := commandFixture()
	for i := range state.Items {
		state.Items[i].State = "planned"
	}
	view := NewView(false, true)
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	if strings.Contains(strings.Join(view.commandResults(80), "\n"), "planned") || !strings.Contains(strings.Join(view.commandDetails(), "\n"), "State: planned") {
		t.Fatal("shared state remains repetitive or was lost")
	}
}

func TestRecordedDigestIsAvailableOnlyAsRecordedMetadata(t *testing.T) {
	for _, mode := range []string{"recorded", "live"} {
		t.Run(mode, func(t *testing.T) {
			state := fixture()
			state.Mode = mode
			state.Outputs[0].RecordedDigest = "sha256:" + strings.Repeat("a", 64)
			view := NewView(mode == "live", true)
			view.Draw(sim(t, 80, 24), state, time.Now(), false)
			view.selected = "image"
			details := strings.Join(view.detailLines(time.Now()), "\n")
			if strings.Contains(details, "Recorded digest: "+state.Outputs[0].RecordedDigest) != (mode == "recorded") {
				t.Fatal("recorded identity was omitted or projected as live evidence")
			}
		})
	}
}

func TestDetailEndAndHelpOverscrollRemainImmediatelyReversible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			state := commandFixture()
			for i := range 80 {
				state.Items[0].Fields = append(state.Items[0].Fields, progress.Field{Label: fmt.Sprintf("Field-%02d", i), Value: strings.Repeat("description ", 8)})
			}
			view := NewView(false, true)
			screen := sim(t, size[0], size[1])
			view.Draw(screen, state, time.Now(), false)
			view.Input(key(tcell.KeyEnter, 0))
			view.Draw(screen, state, time.Now(), false)
			view.Input(key(tcell.KeyEnd, 0))
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			bottom, bottomOffset := text(screen), view.detailOffset
			if bottomOffset <= 0 || bottomOffset > 1000 {
				t.Fatal("End did not retain the actual bottom offset")
			}
			for _, event := range []*tcell.EventKey{key(tcell.KeyUp, 0), key(tcell.KeyPgUp, 0)} {
				view.Input(event)
				view.Draw(screen, state, time.Now(), false)
				screen.Show()
				if view.detailOffset >= bottomOffset || text(screen) == bottom {
					t.Fatal("reverse scrolling remained stuck after End")
				}
				bottom, bottomOffset = text(screen), view.detailOffset
			}
			view.Input(key(tcell.KeyRune, '?'))
			view.Draw(screen, state, time.Now(), false)
			screen.SetSize(40, 12)
			for range 100 {
				view.Input(key(tcell.KeyPgDn, 0))
				view.Draw(screen, state, time.Now(), false)
			}
			screen.Show()
			bottom, bottomOffset = text(screen), view.helpOffset
			view.Input(key(tcell.KeyUp, 0))
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			if view.helpOffset >= bottomOffset || text(screen) == bottom {
				t.Fatal("Help reverse scrolling remained stuck after repeated PgDn")
			}
		})
	}
}

func TestOverviewActionReturnsFromSearchAndFilterToTheOverview(t *testing.T) {
	for _, kind := range []string{"search", "filter"} {
		t.Run(kind, func(t *testing.T) {
			state := overviewFixture()
			state.Items[1].State = "changed"
			view := NewView(false, true)
			screen := sim(t, 80, 24)
			view.Draw(screen, state, time.Now(), false)
			if kind == "search" {
				view.Input(key(tcell.KeyRune, '/'))
				for _, r := range "step-000" {
					view.Input(key(tcell.KeyRune, r))
				}
				view.Input(key(tcell.KeyEnter, 0))
			} else {
				view.Input(key(tcell.KeyRune, 'f'))
			}
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			if !strings.Contains(text(screen), "g overview") || view.overview() {
				t.Fatal("All-entry tab and overview action disagree")
			}
			view.Input(key(tcell.KeyRune, 'g'))
			view.Draw(screen, state, time.Now(), false)
			if !view.overview() || view.query != "" || view.filter != 0 || view.selected != "output/product" {
				t.Fatal("overview action had no visible effect")
			}
		})
	}
}

func TestCommandListsSortDisplayedNamesWithExactIdentityTieBreak(t *testing.T) {
	state := commandFixture()
	state.Items = []progress.Item{{ID: "a-id", Name: "Zulu"}, {ID: "z-id", Name: "Alpha"}, {ID: "b-id", Name: "Alpha"}, {ID: "Delta"}}
	view := NewView(false, true)
	view.Draw(sim(t, 80, 24), state, time.Now(), false)
	var got []string
	for _, row := range view.visible {
		got = append(got, row.Name)
	}
	if !reflect.DeepEqual(got, []string{"b-id", "z-id", "Delta", "a-id"}) {
		t.Fatalf("displayed label ordering lost exact identity tie break: %v", got)
	}
}

func TestShrinkingDependencySetKeepsTheHintAndEnterTargetAligned(t *testing.T) {
	state := fixture()
	view := NewView(true, true)
	screen := sim(t, 80, 24)
	view.Draw(screen, state, time.Now(), false)
	view.selected, view.focus, view.linkIndex = "image", 1, 1
	view.Draw(screen, state, time.Now(), false)
	state.Outputs[0].Operations = nil
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	if view.linkIndex != 0 || !strings.Contains(text(screen), "Enter follows 1/1: cli") {
		t.Fatal("dependency shrink retained an impossible target index")
	}
	view.Input(key(tcell.KeyEnter, 0))
	if view.selected != "cli" {
		t.Fatal("Enter did not follow the displayed remaining dependency")
	}
}

func TestEmptyOutputAndTinyFallbackLabelsAreTruthful(t *testing.T) {
	for _, live := range []bool{false, true} {
		view := NewView(live, true)
		state := progress.Snapshot{}
		screen := sim(t, 30, 10)
		view.Draw(screen, state, time.Now(), false)
		screen.Show()
		label := "NVKR inspection"
		if live {
			label = "NVKR live"
		}
		if !strings.Contains(text(screen), label) || !strings.Contains(text(screen), "40x12") || !strings.Contains(text(screen), "30x10") {
			t.Fatal("tiny view lost mode fallback or explicit resize threshold")
		}
		screen.SetSize(80, 24)
		view.Draw(screen, state, time.Now(), false)
		if lines := strings.Join(view.outputLines(80), "\n"); !strings.Contains(lines, "No outputs supplied") || strings.Contains(lines, "clears filter") {
			t.Fatal("empty unfiltered view claimed an active filter")
		}
	}
	view := NewView(false, true)
	screen := sim(t, 30, 10)
	view.Draw(screen, progress.Snapshot{Mode: "command"}, time.Now(), false)
	screen.Show()
	if !strings.Contains(text(screen), "NVKR Command results") {
		t.Fatal("tiny command lost the title fallback")
	}
}
