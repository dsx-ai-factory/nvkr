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

func commandFixture() progress.Snapshot {
	return progress.Snapshot{Mode: "command", Title: "Catalog result", Status: "complete", Note: "Metadata inspection; no payload verification performed.", Fields: []progress.Field{{Label: "Catalog", Value: "example"}}, Items: []progress.Item{
		{ID: "entry-a", Name: "same name", Kind: "source", State: "changed", Fields: []progress.Field{{Label: "Commit", Value: strings.Repeat("a", 40)}}, Dependencies: []string{"entry-b"}},
		{ID: "entry-b", Name: "same name", Kind: "component", State: "unchanged", Fields: []progress.Field{{Label: "Platform", Value: "linux/arm64"}}},
	}}
}

func TestCommandViewUsesTypedResultVocabularyAcrossSizes(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {0, 0}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			view := NewView(false, true)
			screen := sim(t, size[0], size[1])
			state := commandFixture()
			view.Draw(screen, state, time.Now(), false)
			screen.Show()
			if size[0] > 0 && size[1] > 0 {
				got := text(screen)
				if !strings.Contains(got, "Entries 2") {
					t.Fatalf("command entry count hidden: %q", got)
				}
				if size[0] < 40 && !strings.Contains(got, "[1 R]") {
					t.Fatal("tiny command header displaced tab strip")
				}
				for _, bad := range []string{"Outputs observed", "Producer", "Building", "Run complete"} {
					if strings.Contains(got, bad) {
						t.Fatalf("command invented Run vocabulary %q: %s", bad, got)
					}
				}
			}
			if size[0] >= 40 && size[1] >= 12 {
				got := text(screen)
				for _, wanted := range []string{"Results", "Details", "Metadata inspection"} {
					if !strings.Contains(got, wanted) {
						t.Fatalf("command hierarchy/note missing %q: %s", wanted, got)
					}
				}
			}
			screen.SetSize(80, 24)
			view.Input(key(tcell.KeyRune, 'q'))
			view.Draw(screen, state, time.Now(), false)
			if !view.confirmQuit || view.Input(key(tcell.KeyEnter, 0)) != None {
				t.Fatal("complete command viewer skipped close confirmation")
			}
		})
	}
}

func TestCommandSelectionSearchAndDependenciesUseExactItemIdentities(t *testing.T) {
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	state := commandFixture()
	view.Draw(screen, state, time.Now(), false)
	if view.selected != "entry-a" {
		t.Fatal("initial command identity changed")
	}
	view.Input(key(tcell.KeyEnter, 0))
	view.Draw(screen, state, time.Now(), false)
	view.Input(key(tcell.KeyEnter, 0))
	if view.selected != "entry-b" {
		t.Fatal("dependency identity navigation failed for duplicate names")
	}
	view.Input(key(tcell.KeyEscape, 0))
	if view.selected != "entry-a" {
		t.Fatal("Back lost command identity")
	}
	view.Input(key(tcell.KeyEscape, 0))
	view.Input(key(tcell.KeyRune, '/'))
	for _, r := range "entry-b" {
		view.Input(key(tcell.KeyRune, r))
	}
	view.Input(key(tcell.KeyEnter, 0))
	if view.selected != "entry-b" || len(view.visible) != 1 {
		t.Fatal("ID search selected ambiguous display name")
	}
	view.Input(key(tcell.KeyEscape, 0))
	view.Input(key(tcell.KeyRune, 'f'))
	if view.selected != "entry-a" || len(view.visible) != 1 {
		t.Fatal("command Changed filter used execution states")
	}
	view.Input(key(tcell.KeyRune, '/'))
	view.Input(key(tcell.KeyRune, 'q'))
	if !view.searching || view.confirmQuit {
		t.Fatal("command query q became close")
	}
}

func TestCommandFieldsAndDependenciesAreCompleteAndNotJSON(t *testing.T) {
	state := commandFixture()
	state.Items[0].Dependencies = nil
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("dependency-%02d", i)
		state.Items[0].Dependencies = append(state.Items[0].Dependencies, id)
		state.Items = append(state.Items, progress.Item{ID: id, Name: id, Kind: "component"})
	}
	state.Items[0].Fields = append(state.Items[0].Fields, progress.Field{Label: "Exact digest", Value: "sha256:" + strings.Repeat("b", 64)})
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.Draw(screen, state, time.Now(), false)
	view.selected = "entry-a"
	view.focus = 1
	view.Draw(screen, state, time.Now(), false)
	details := strings.Join(view.commandDetails(), "\n")
	for _, wanted := range []string{"Catalog: example", "Exact digest: sha256:" + strings.Repeat("b", 64), "Dependencies: 24", "dependency-23"} {
		if !strings.Contains(details, wanted) {
			t.Fatalf("typed detail omitted %q: %s", wanted, details)
		}
	}
	if strings.Contains(details, `"Fields":`) || strings.Contains(details, `"Items":`) {
		t.Fatal("command dumped serialized result into pane")
	}
	view.linkIndex = 23
	view.Input(key(tcell.KeyEnter, 0))
	if view.selected != "dependency-23" {
		t.Fatal("complete dependency set not reachable")
	}
	state.Items = nil
	view.Draw(screen, state, time.Now(), false)
	if !strings.Contains(strings.Join(view.commandResults(80), "\n"), "Catalog: example") {
		t.Fatal("field-only command result disappeared")
	}
}

func TestCommandSnapshotsAndHostileTextRemainDetachedAndSafe(t *testing.T) {
	initial := commandFixture()
	source := progress.New(initial)
	initial.Fields[0].Value = "mutated caller"
	initial.Items[0].Fields[0].Value = "mutated child"
	initial.Items[0].Dependencies[0] = "mutated dependency"
	state := source.Snapshot()
	state.Title = "Result [red]literal[-]\x1b]52;hidden\a"
	state.Note = "Closed note\u202e"
	state.Items[0].Fields = append(state.Items[0].Fields, progress.Field{Label: "Literal", Value: "[red]text[-]\x1b[2J\xff"})
	view := NewView(false, true)
	screen := sim(t, 120, 40)
	view.Draw(screen, state, time.Now(), false)
	view.selected = "entry-a"
	view.focus = 1
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	if strings.Contains(got, "mutated") || strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') || !strings.Contains(got, "[red]literal[-]") || !strings.Contains(got, `\x1b`) {
		t.Fatalf("unsafe/shared generic presentation: %s", got)
	}
	state.Items[0].Fields[0].Value = "mutated snapshot"
	if source.Snapshot().Items[0].Fields[0].Value == "mutated snapshot" {
		t.Fatal("snapshot restored caller mutability")
	}
	cells, _, _ := screen.GetContents()
	for _, cell := range cells {
		fg, bg, _ := cell.Style.Decompose()
		if fg != tcell.ColorDefault || bg != tcell.ColorDefault {
			t.Fatal("command mode forced terminal colors")
		}
	}
}

func TestCommandFailureCodeAndOwningNoteRemainInHeader(t *testing.T) {
	state := commandFixture()
	state.Status = "failed"
	state.FailureCode = "NVKR-E-FETCH-FAILED"
	state.Note = "Source operation failed; projected metadata only."
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.focus = 2
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	for _, wanted := range []string{state.FailureCode, "Entries 2", state.Note} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("persistent command qualifier hidden %q: %s", wanted, got)
		}
	}
	view.Input(key(tcell.KeyRune, 'q'))
	view.Draw(screen, state, time.Now(), false)
	if !view.confirmQuit {
		t.Fatal("failed command blocked read-only close confirmation")
	}
}

func TestStaticCommandActivityDoesNotImplyLiveObservation(t *testing.T) {
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	state := commandFixture()
	view.focus = 2
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	if got := text(screen); strings.Contains(got, "Activity") || view.focus != 0 {
		t.Fatalf("empty static activity tab retained: %s", got)
	}
	state.Activity = []progress.Activity{{Sequence: 1, Text: "Result supplied"}}
	view.Draw(screen, state, time.Now(), false)
	view.Input(key(tcell.KeyRune, '3'))
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	if got := text(screen); !strings.Contains(got, "Result activity (static)") || strings.Contains(got, "Following newest") {
		t.Fatalf("static command looked like a live activity feed: %s", got)
	}
}

func TestTinyCommandFailureKeepsNoteCountCodeAndTabs(t *testing.T) {
	state := commandFixture()
	state.Status = "failed"
	state.FailureCode = "NVKR-E-FETCH-FAILED"
	state.Note = "Metadata only"
	view := NewView(false, true)
	screen := sim(t, 20, 6)
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	var unpadded strings.Builder
	for _, line := range strings.Split(got, "\n") {
		unpadded.WriteString(strings.TrimRight(line, " "))
	}
	for _, wanted := range []string{"Entries 2", "NVKR-E-FETCH-FAILED", state.Note, "[1 R]", "q confirm close"} {
		if !strings.Contains(unpadded.String(), wanted) {
			t.Fatalf("tiny command failure lost %q: %s", wanted, got)
		}
	}
}
