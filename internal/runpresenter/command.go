// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/rivo/tview"
)

func (v *View) commandMode() bool { return v.snapshot.Mode == "command" }

func (v *View) overview() bool {
	return v.hasOverview() && !v.showAll && v.query == "" && v.filter == 0
}

func (v *View) hasOverview() bool {
	return v.commandMode() && v.primaryCount > 0 && v.primaryCount < len(v.snapshot.Items)
}

func (v *View) titleLabel() string {
	if v.snapshot.Title == "" {
		return "Command results"
	}
	return v.snapshot.Title
}

func (v *View) commandHeader() []string {
	title := v.titleLabel()
	count := fmt.Sprintf("Entries %d", len(v.snapshot.Items))
	if v.overview() {
		count = fmt.Sprintf("Overview %d | %d total entries", v.primaryCount, len(v.snapshot.Items))
	}
	lines := []string{"NVKR " + safeText(title) + " | " + count}
	if v.snapshot.Status != "" && v.snapshot.Status != "complete" {
		lines[0] += " | " + safeText(v.stateLabel())
	}
	if v.snapshot.FailureCode != "" {
		lines = append(lines, "Code: "+safeText(v.snapshot.FailureCode))
	}
	if v.snapshot.Note != "" {
		lines = append(lines, safeText(v.snapshot.Note))
	}
	return lines
}

func (v *View) commandTinyHeader(width int) []string {
	lines := []string{fmt.Sprintf("Entries %d | %s", len(v.snapshot.Items), safeText(v.stateLabel()))}
	if v.snapshot.FailureCode != "" {
		lines = append(lines, tview.WordWrap("Code: "+safeText(v.snapshot.FailureCode), width)...)
	} else {
		lines = append(lines, "NVKR "+safeText(v.titleLabel()))
	}
	if v.snapshot.Note != "" {
		lines = append(lines, safeText(v.snapshot.Note))
	}
	return lines
}

func fieldLines(fields []progress.Field) []string {
	lines := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.Value == "" {
			continue
		}
		lines = append(lines, safeText(field.Label)+": "+safeText(field.Value))
	}
	return lines
}

// commandMatches searches only the detached public projection, never transport
// locations or private diagnostic data held by the owning operation.
func commandMatches(item progress.Item, query string) bool {
	if query == "" {
		return true
	}
	for _, value := range []string{item.Name + " " + item.ID, item.Kind, item.Summary} {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	for _, field := range item.Fields {
		if strings.Contains(strings.ToLower(field.Label), query) || strings.Contains(strings.ToLower(field.Value), query) {
			return true
		}
	}
	return false
}

func (v *View) commandResults(width int) []string {
	var lines []string
	if v.filter != 0 {
		lines = append(lines, "Filter: "+v.filterName())
	}
	if v.query != "" {
		lines = append(lines, "Search: "+safeText(v.query))
	}
	if len(v.snapshot.Items) == 0 {
		var summary []progress.Field
		identities := 0
		for _, field := range v.snapshot.Fields {
			if canonical.IsDigest(field.Value) {
				identities++
			} else {
				summary = append(summary, field)
			}
		}
		lines = append(lines, fieldLines(summary)...)
		if identities > 0 {
			lines = append(lines, "Identities available: Enter opens Details")
		}
		if len(v.snapshot.Fields) == 0 {
			lines = append(lines, "No result entries or fields returned")
		}
		return lines
	}
	if len(v.visible) == 0 {
		if v.query != "" || v.filter != 0 {
			return append(lines, "No entries match; Esc clears filter/search")
		}
		return append(lines, "No entries supplied")
	}
	index := slices.IndexFunc(v.visible, func(row progress.Output) bool { return row.Name == v.selected })
	start := max(0, min(max(0, index-v.rows/2), len(v.visible)-v.rows))
	// A shared static state adds no distinction between entries. The owning
	// state remains in Details; mixed states stay visible beside each name.
	state := v.visible[0].State
	mixedStates := slices.ContainsFunc(v.visible, func(row progress.Output) bool { return row.State != state })
	for _, row := range v.visible[start:min(len(v.visible), start+v.rows)] {
		item := v.commandItems[row.Name]
		cursor := " "
		if item.ID == v.selected {
			cursor = ">"
		}
		label := item.Name
		if label == "" {
			label = item.ID
		}
		line := cursor + " "
		if mixedStates && item.State != "" {
			line += safeText(item.State) + "  "
		}
		line += safeText(label)
		if item.Summary != "" {
			withSummary := line + "  · " + safeText(item.Summary)
			// Measure the escaped display string in terminal cells. A summary
			// never displaces or truncates the full name, including wide Unicode.
			if tview.TaggedStringWidth(tview.Escape(withSummary)) <= width {
				line = withSummary
			}
		}
		lines = append(lines, line)
	}
	if start > 0 || start+v.rows < len(v.visible) {
		lines = append(lines, fmt.Sprintf("Showing %d-%d of %d; PgUp/PgDn", start+1, min(len(v.visible), start+v.rows), len(v.visible)))
	}
	return lines
}

func (v *View) commandDetails() []string {
	var lines []string
	v.links = nil
	if item, ok := v.commandItems[v.selected]; ok {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		lines = append(lines, "Entry: "+safeText(name))
		if len(v.back) > 0 {
			parent := v.back[len(v.back)-1]
			lines = append(lines, "Path: "+safeText(v.linkName(parent.output))+" > "+safeText(v.linkName(item.ID)), fmt.Sprintf("Drill depth: %d; Esc back", len(v.back)))
		}
		v.links = append(v.links, item.Dependencies...)
		v.clampLinkIndex()
		lines = append(lines, "", fmt.Sprintf("Dependencies: %d; Enter follows, Esc back", len(v.links)), strings.Repeat("-", 24))
		for i, ref := range v.links {
			cursor := " "
			if i == v.linkIndex {
				cursor = ">"
			}
			lines = append(lines, cursor+" "+safeText(v.linkName(ref)))
		}
		if len(v.links) == 0 {
			lines = append(lines, "No dependencies supplied")
		}
		lines = append(lines, "", "Details", strings.Repeat("-", 24), "Kind: "+safeText(item.Kind))
		if item.State != "" {
			lines = append(lines, "State: "+safeText(item.State))
		}
		lines = append(lines, fieldLines(item.Fields)...)
		if !slices.ContainsFunc(v.visible, func(row progress.Output) bool { return row.Name == item.ID }) {
			if v.overview() {
				lines = append(lines, "Outside overview; followed dependency")
			} else {
				lines = append(lines, "Outside current filter; selection remains pinned")
			}
		}
		lines = append(lines, "Identity: "+safeText(item.ID))
	} else if len(v.snapshot.Fields) == 0 {
		lines = append(lines, "No selected entry")
	}
	lines = append(lines, "", "Command context", strings.Repeat("-", 24))
	lines = append(lines, fieldLines(v.snapshot.Fields)...)
	if v.snapshot.Note != "" {
		lines = append(lines, "Scope: "+safeText(v.snapshot.Note))
	}
	return lines
}

func (v *View) linkName(ref string) string {
	if item, ok := v.commandItems[ref]; v.commandMode() && ok && item.Name != "" {
		if item.Kind != "" {
			return "[" + item.Kind + "] " + item.Name
		}
		return item.Name
	}
	return ref
}
