// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
)

func releaseSearchFixture() progress.Snapshot {
	return progress.Snapshot{Mode: "command", Title: "Release metadata", Fields: []progress.Field{{Label: "Context", Value: "global-context-only"}}, Items: []progress.Item{
		{ID: "output/tools", Name: "tools 工具", Kind: "executable", State: "metadata", Primary: true, Summary: "portable tools",
			Fields: []progress.Field{{Label: "Recorded platform", Value: "linux/arm64"}, {Label: "Digest", Value: "sha256:" + strings.Repeat("a", 64)}}, Dependencies: []string{"evidence/tools-sbom"}},
		{ID: "evidence/tools-sbom", Name: "tools-sbom", Kind: "spdx", State: "metadata", Dependencies: []string{"output/tools"}},
		{ID: "output/chart", Name: "chart", Kind: "helm-oci", State: "metadata", Primary: true},
	}}
}

func enterSearch(view *View, query string) {
	view.Input(key(tcell.KeyRune, '/'))
	for _, r := range query {
		view.Input(key(tcell.KeyRune, r))
	}
	view.Input(key(tcell.KeyEnter, 0))
}

func TestCommandSearchFindsSafeMetadataAcrossTheCompleteMembership(t *testing.T) {
	for _, test := range []struct {
		name, query string
		want        []string
	}{
		{name: "display name", query: "tools", want: []string{"output/tools", "evidence/tools-sbom"}},
		{name: "navigation identity", query: "output/tools", want: []string{"output/tools"}},
		{name: "existing combined name and identity", query: "tools 工具 output/tools", want: []string{"output/tools"}},
		{name: "kind ignores case", query: "EXECUTABLE", want: []string{"output/tools"}},
		{name: "summary", query: "portable", want: []string{"output/tools"}},
		{name: "field label", query: "recorded platform", want: []string{"output/tools"}},
		{name: "platform", query: "linux/arm64", want: []string{"output/tools"}},
		{name: "full digest", query: "sha256:" + strings.Repeat("a", 64), want: []string{"output/tools"}},
		{name: "Unicode name", query: "工具", want: []string{"output/tools"}},
		{name: "evidence outside overview", query: "spdx", want: []string{"evidence/tools-sbom"}},
		{name: "no match", query: "missing"},
		{name: "global fields are not item metadata", query: "global-context-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := NewView(false, true)
			view.Draw(sim(t, 80, 24), releaseSearchFixture(), time.Now(), false)
			enterSearch(view, test.query)
			var got []string
			for _, row := range view.visible {
				got = append(got, row.Name)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("search %q membership = %v, want %v", test.query, got, test.want)
			}
			if len(test.want) == 0 {
				if view.selected != "" || !strings.Contains(strings.Join(view.commandResults(78), "\n"), "No entries match") {
					t.Fatal("empty search retained an invisible selection or lost its explanation")
				}
			} else if view.selected != test.want[0] {
				t.Fatalf("search selected %q, want %q", view.selected, test.want[0])
			}
		})
	}
}

func TestCommandMetadataSearchRetainsExactDependencyAndBackNavigation(t *testing.T) {
	state := releaseSearchFixture()
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.Draw(screen, state, time.Now(), false)
	enterSearch(view, "spdx")
	view.Input(key(tcell.KeyEnter, 0))
	view.Draw(screen, state, time.Now(), false)
	view.Input(key(tcell.KeyEnter, 0))
	view.Draw(screen, state, time.Now(), false)
	if view.selected != "output/tools" || !strings.Contains(strings.Join(view.commandDetails(), "\n"), "Outside current filter") {
		t.Fatal("metadata search prevented following an exact binding outside its results")
	}
	view.Input(key(tcell.KeyEscape, 0))
	if view.selected != "evidence/tools-sbom" || view.query != "spdx" {
		t.Fatal("Back lost the exact evidence selection or metadata query")
	}
}

func TestLiveSearchRetainsNameOnlyBehavior(t *testing.T) {
	for _, query := range []string{"image", "linux/arm64", "sha256:recorded", "oci"} {
		t.Run(query, func(t *testing.T) {
			view := NewView(true, true)
			state := progress.Snapshot{Outputs: []progress.Output{{Name: "image", Platform: "linux/arm64", Kind: "oci", RecordedDigest: "sha256:recorded"}}}
			view.Draw(sim(t, 80, 24), state, time.Now(), false)
			enterSearch(view, query)
			want := 0
			if query == "image" {
				want = 1
			}
			if got := len(view.visible); got != want {
				t.Fatalf("live search %q selected %d rows, want %d", query, got, want)
			}
		})
	}
}

func TestCommandSummaryFitsWithoutDisplacingNames(t *testing.T) {
	for _, test := range []struct {
		name, label, summary string
		width                int
		want                 string
	}{
		{name: "empty summary", label: "tool", width: 80, want: "> tool"},
		{name: "exact fit", label: "tool", summary: "linux", width: 15, want: "> tool  · linux"},
		{name: "narrow row", label: "tool", summary: "linux", width: 14, want: "> tool"},
		{name: "wide Unicode fits", label: "工具", summary: "linux", width: 15, want: "> 工具  · linux"},
		{name: "wide Unicode hides summary", label: "工具", summary: "linux", width: 14, want: "> 工具"},
		{name: "literal markup fits", label: "[red]x[-]", summary: "linux", width: 20, want: "> [red]x[-]  · linux"},
		{name: "literal markup takes visible space", label: "[red]x[-]", summary: "linux", width: 19, want: "> [red]x[-]"},
		{name: "controls escaped", label: "tool", summary: "linux\x1b[2J\u202e", width: 80, want: `> tool  · linux\x1b[2J\u202e`},
		{name: "long name retained", label: "complete-long-name", summary: "linux", width: 8, want: "> complete-long-name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := NewView(false, true)
			view.update(progress.Snapshot{Mode: "command", Items: []progress.Item{{ID: "item", Name: test.label, Summary: test.summary}}})
			if got := view.commandResults(test.width); len(got) != 1 || got[0] != test.want {
				t.Fatalf("summary rows = %q, want one row %q", got, test.want)
			}
		})
	}
}

func TestCommandSummaryRemainsLiteralOnScreen(t *testing.T) {
	state := progress.Snapshot{Mode: "command", Items: []progress.Item{{ID: "item", Name: "tools", Summary: "[red]linux[-]\x1b[2J\u202e"}}}
	view := NewView(false, true)
	screen := sim(t, 80, 24)
	view.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') || !strings.Contains(got, `[red]linux[-]\x1b[2J\u202e`) {
		t.Fatalf("unsafe or interpreted summary: %s", got)
	}
}
