// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/internal/tuitheme"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (d *dashboard) observeBuildVertex(vertex planview.Vertex) {
	if !dashboardBuildID(vertex.Key) || !dashboardBuildID(vertex.Reference) || len(vertex.Label) > 512 ||
		vertex.Build != "" && !dashboardBuildID(vertex.Build) {
		d.buildTruncated = true
		return
	}
	switch vertex.Kind {
	case "operation", "artifact", "output":
	default:
		d.buildTruncated = true
		return
	}
	if !strings.HasPrefix(vertex.Key, vertex.Kind+"/") {
		d.buildTruncated = true
		return
	}
	index := slices.IndexFunc(d.buildVertices, func(v planview.Vertex) bool { return v.Key == vertex.Key })
	if index >= 0 {
		if d.buildVertices[index] != vertex {
			d.buildTruncated = true
		}
	} else if len(d.buildVertices) < 1536 {
		d.buildVertices = append(d.buildVertices, vertex)
	} else {
		d.buildTruncated = true
	}
}

func (d *dashboard) observeBuildEdge(edge planview.Edge) {
	if !dashboardBuildID(edge.From) || !dashboardBuildID(edge.To) {
		d.buildTruncated = true
		return
	}
	switch edge.Relation {
	case "consumed by", "produces", "fulfills slot":
	default:
		d.buildTruncated = true
		return
	}
	if !slices.Contains(d.buildEdges, edge) {
		if len(d.buildEdges) < 4096 {
			d.buildEdges = append(d.buildEdges, edge)
		} else {
			d.buildTruncated = true
		}
	}
}

// Each selected Run replaces the previous display, including a completed artifact
// lane followed by native tools. Neither a Plan nor a new Run clears failure.
func (d *dashboard) observeBuildPlan(event nvkedemo.ProgressEvent) {
	if (event.Name != "artifacts" && event.Name != "native-tools") || d.phases[1].state == "failed" {
		return
	}
	d.phases[1] = dashboardPhase{"running", "Selected " + event.Name + " Run"}
	d.buildRun, d.buildRequired = event.Name, 0
	d.buildSteps, d.buildOutputs, d.buildVertices, d.buildEdges, d.buildLogs = nil, nil, nil, nil, nil
	d.buildPreview, d.buildTruncated, d.buildSelected = false, false, -1
	d.buildChangedAt, d.buildOutputAt, d.buildGuestAt = time.Time{}, time.Time{}, time.Time{}
	d.buildOutputBytes, d.buildProcess, d.buildGuestStage = 0, "", ""
	d.partial, d.dropped, d.offset = nil, false, 0
	if event.BuildPlan == nil || event.BuildGraph == nil || event.BuildPlan.PlanDigest != event.BuildGraph.PlanDigest {
		return
	}
	d.buildRequired, d.buildPreview = len(event.BuildPlan.Outputs), true
	for _, operation := range event.BuildPlan.Operations {
		if len(d.buildSteps) == dashboardSteps || !dashboardBuildID(operation.ID) {
			d.buildTruncated = true
			continue
		}
		d.buildSteps = append(d.buildSteps, dashboardBuildStep{id: operation.ID, kind: operation.Kind, state: "queued", previewed: true})
	}
	keys := make(map[string]bool)
	for _, vertex := range event.BuildGraph.Vertices {
		if vertex.Kind != "route" {
			d.observeBuildVertex(vertex)
		}
	}
	for _, vertex := range d.buildVertices {
		keys[vertex.Key] = true
	}
	for _, edge := range event.BuildGraph.Edges {
		if keys[edge.From] && keys[edge.To] {
			d.observeBuildEdge(edge)
		}
	}
}

// Called for every received public build byte, including output hidden by the
// concise console filter. Counting bytes reveals no raw diagnostic contents.
func (d *dashboard) BuildOutput(size int) {
	if size <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.phases[1].state == "running" {
		d.buildOutputAt = time.Now()
		d.buildOutputBytes += uint64(size)
	}
}

func dashboardBuildHealth(s dashboardSnapshot, now time.Time) string {
	process := "Launcher process observation unavailable"
	switch s.buildProcess {
	case "started":
		process = "Launcher start observed; exit not observed"
	case "exited":
		process = "Launcher exited and joined"
	}
	age := "no output observed"
	if !s.buildOutputAt.IsZero() {
		age = fmt.Sprintf("%s ago · %d KiB received", dashboardBuildAge(now, s.buildOutputAt), s.buildOutputBytes/1024)
	}
	return process + "\nRaw build output: " + age + "\nChild health unobserved · ETA unknown"
}

func dashboardBuildAge(now, when time.Time) time.Duration {
	return max(time.Duration(0), now.Sub(when)).Truncate(time.Second)
}

func dashboardBuildActive(s dashboardSnapshot) bool {
	return s.phases[1].state == "running" && s.phases[2].state == "pending" && s.tenantStage == ""
}

func dashboardSelectedStep(s dashboardSnapshot) int {
	if len(s.buildSteps) == 0 {
		return -1
	}
	if s.buildSelected >= 0 && s.buildSelected < len(s.buildSteps) {
		return s.buildSelected
	}
	if index := slices.IndexFunc(s.buildSteps, func(step dashboardBuildStep) bool { return step.state == "running" }); index >= 0 {
		return index
	}
	return 0
}

// Display order groups concurrent work without changing execution order or the
// selected identity. A selected step stays selected when its report changes.
func dashboardBuildOrder(s dashboardSnapshot) []int {
	order := make([]int, len(s.buildSteps))
	for index := range order {
		order[index] = index
	}
	rank := func(state string) int {
		switch state {
		case "running":
			return 0
		case "done":
			return 2
		default:
			return 1
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return rank(s.buildSteps[a].state) - rank(s.buildSteps[b].state) })
	return order
}

func dashboardBuildList(s dashboardSnapshot, rows int, now time.Time) string {
	var text strings.Builder
	selected := dashboardSelectedStep(s)
	if selected < 0 {
		return "Waiting for the planned operation listing.\nNo step state inferred from elapsed time."
	}
	order := dashboardBuildOrder(s)
	position := slices.Index(order, selected)
	fmt.Fprintf(&text, "[lightcyan::b]Step %d of %d[-::-]  running first\n", position+1, len(s.buildSteps))
	first := max(0, min(position-rows/2, len(s.buildSteps)-rows))
	for _, index := range order[first:min(first+rows, len(order))] {
		step := s.buildSteps[index]
		color, marker := "goldenrod", "○"
		switch step.state {
		case "running":
			color, marker = "lightcyan", "▶"
		case "done":
			color, marker = "lightgreen", "✓"
		}
		cursor := " "
		if index == selected {
			cursor = "›"
		}
		elapsed := ""
		if step.state == "running" && !step.started.IsZero() {
			elapsed = dashboardBuildAge(now, step.started).String()
		}
		fmt.Fprintf(&text, "%s[%s]%s %-7.7s %8s[-] %s\n", cursor, color, marker, step.kind, elapsed, tview.Escape(step.id))
	}
	if first > 0 || first+rows < len(s.buildSteps) {
		text.WriteString("[slategray]More operations above/below[-]\n")
	}
	return text.String()
}

func dashboardBuildDetail(s dashboardSnapshot, now time.Time) string {
	selected := dashboardSelectedStep(s)
	if selected < 0 {
		return "No build steps reported yet."
	}
	step := s.buildSteps[selected]
	var text strings.Builder
	color := "goldenrod"
	switch step.state {
	case "running":
		color = "lightcyan"
	case "done":
		color = "lightgreen"
	}
	fmt.Fprintf(&text, "[%s::b]%s[-::-]\nStage: %s · reported %s", color, tview.Escape("["+step.id+"]"), step.kind, step.state)
	if step.state == "running" && !step.started.IsZero() {
		fmt.Fprintf(&text, " · %s elapsed", max(time.Duration(0), now.Sub(step.started)).Truncate(time.Second))
	}
	if logs, ok := s.buildLogs[step.id]; ok && step.kind == "build" && step.state == "running" {
		text.WriteString("\n" + dashboardOSLogText(logs, now))
		text.WriteString("\nLog activity only; not guest health or readiness.")
	}
	key := "operation/" + step.id
	for _, direction := range []struct {
		incoming bool
		label    string
	}{{true, "Inputs"}, {false, "Outputs"}} {
		var refs []string
		for _, edge := range s.buildEdges {
			ref := ""
			if direction.incoming && edge.To == key {
				ref = edge.From
			} else if !direction.incoming && edge.From == key {
				ref = edge.To
			}
			if ref != "" && slices.ContainsFunc(s.buildVertices, func(v planview.Vertex) bool { return v.Key == ref }) {
				refs = append(refs, ref)
			}
		}
		slices.Sort(refs)
		fmt.Fprintf(&text, "\n[lightcyan::b]%s[-::-] · %d plan links", direction.label, len(refs))
		for _, ref := range refs[:min(2, len(refs))] {
			if direction.incoming {
				fmt.Fprintf(&text, "\n  %s ──→ %s", tview.Escape("["+ref+"]"), tview.Escape("["+step.id+"]"))
			} else {
				fmt.Fprintf(&text, "\n  %s ──→ %s", tview.Escape("["+step.id+"]"), tview.Escape("["+ref+"]"))
			}
		}
		if len(refs) > 2 {
			fmt.Fprintf(&text, "\n  +%d more planned links", len(refs)-2)
		}
	}
	if len(s.buildVertices) == 0 || s.buildTruncated {
		text.WriteString("\nPlan links unavailable/incomplete; no dependencies inferred.")
	} else {
		text.WriteString("\nPlan description only; no artifact verification implied.")
	}
	return text.String()
}

// Artifact mode dedicates the whole workspace to the current pipeline. Future
// deployment checklists do not displace active work, and selection changes only
// the descriptive inspection pane, never the execution or its authority.
func (d *dashboard) drawBuild(screen tcell.Screen, s dashboardSnapshot, now time.Time, width, height int) {
	content := max(0, height-5)
	ribbon := dashboardBuildRibbon(s, now)
	statusHeight := min(len(tview.WordWrap(ribbon, max(1, width-2)))+2, max(0, content-6))
	workspace := content - statusHeight
	boardWidth := width * 4 / 10
	if width <= 90 {
		boardWidth = width / 2
	}
	var phases strings.Builder
	for index, named := range dashboardNamedPhases(s) {
		if index != 0 {
			phases.WriteString(" › ")
		}
		color := "slategray"
		switch named.phase.state {
		case "complete", "passed":
			color = "lightgreen"
		case "running":
			color = "lightcyan"
		}
		fmt.Fprintf(&phases, "[%s]%s[-]", color, named.name)
	}
	d.header.SetText(fmt.Sprintf("[lightcyan::b] NVKE local build & demo[-::-]    %s elapsed\n [goldenrod]UNQUALIFIED local diagnostic · not a signed Release[-]\n %s", dashboardBuildAge(now, s.started), phases.String()))
	d.stateView.SetTitle(" Artifact pipeline · reported execution ")
	d.stateView.SetText(ribbon)
	d.stateView.SetRect(0, 3, width, statusHeight)
	d.phasesView.SetTitle(" Operations · ↑/↓ select ")
	d.phasesView.SetWrap(false).SetText(dashboardBuildList(s, max(1, workspace-4), now))
	d.phasesView.SetRect(0, 3+statusHeight, boardWidth, workspace)
	activity := s.activity
	if s.buildDetails {
		activity = dashboardSelectedEvents(s)
		d.activityView.SetTitle(" Selected · plan links & events ")
		d.activityView.SetWrap(true)
		d.activityView.SetText(dashboardBuildDetail(s, now) + "\n\n[lightcyan::b]Selected public events[-::-]\n" + tview.Escape(strings.Join(activity, "\n")))
	} else {
		d.activityView.SetTitle(" Shared activity · Enter inspects selected operation ")
		d.activityView.SetWrap(false)
		d.activityView.SetText(tview.Escape(strings.Join(activity, "\n")))
	}
	d.activityView.SetRect(boardWidth, 3+statusHeight, width-boardWidth, workspace)
	help := " ↑/↓ select · Enter inspect · a activity · q / Ctrl-C confirm cancel"
	if s.buildDetails {
		help = " ↑/↓ select · Enter/Esc activity · PgUp/PgDn scroll · q / Ctrl-C confirm cancel"
	}
	if s.confirmCancel {
		help = " Cancel current work? Retention/cleanup follows the selected mode. y/N"
	}
	d.footer.SetText("[lightcyan]" + help + "[-]\n " + dashboardLogLocator(s))
	d.header.SetRect(0, 0, width, min(3, height))
	d.footer.SetRect(0, max(0, height-2), width, min(2, height))
	_, _, _, activityHeight := d.activityView.GetInnerRect()
	if s.buildDetails {
		d.activityView.ScrollTo(s.offset, 0)
	} else {
		d.activityView.ScrollTo(max(0, len(activity)-activityHeight-s.offset), 0)
	}
	palette := tuitheme.Colors(false)
	for index, view := range []*tview.TextView{d.phasesView, d.stateView, d.activityView} {
		color := palette.Border
		if index == s.focus || index == 0 && s.buildSelected >= 0 {
			color = palette.Accent
		}
		view.SetBorderColor(color)
		view.Draw(screen)
	}
	d.header.Draw(screen)
	d.footer.Draw(screen)
}

func dashboardBuildRibbon(s dashboardSnapshot, now time.Time) string {
	done, running, queued, previewed, previewDone := 0, 0, 0, 0, 0
	for _, step := range s.buildSteps {
		if step.previewed {
			previewed++
			if step.state == "done" {
				previewDone++
			}
		}
		switch step.state {
		case "done":
			done++
		case "running":
			running++
		case "queued":
			queued++
		}
	}
	var text strings.Builder
	if s.buildGuestStage != "" {
		fmt.Fprintf(&text, "[lightcyan::b]Last OS guest report: %s[-::-] (%s ago)\n", tview.Escape(s.buildGuestStage), dashboardBuildAge(now, s.buildGuestAt))
	}
	// Only show telemetry beside an exact reported running build operation.
	// An out-of-order or unknown diagnostic cannot invent a running operation.
	for _, step := range s.buildSteps {
		if step.kind == "build" && step.state == "running" {
			if logs, ok := s.buildLogs[step.id]; ok {
				text.WriteString(dashboardOSLogText(logs, now) + "\n")
				break
			}
		}
	}
	if s.buildRun != "" {
		fmt.Fprintf(&text, "[lightcyan::b]Selected %s Run[-::-]\n", s.buildRun)
	}
	required := s.buildRequired
	if required == 0 && !s.buildTruncated {
		required = len(s.buildOutputs)
	}
	if required > 0 {
		fmt.Fprintf(&text, "[lightcyan::b]Required outputs[-::-] %d   [lightcyan::b]Steps[-::-] ", required)
	} else {
		text.WriteString("Required output total unknown   ")
	}
	fmt.Fprintf(&text, "[lightgreen]%d done[-]  [lightcyan]%d running[-]  [goldenrod]%d queued[-]\n", done, running, queued)
	if s.buildPreview && !s.buildTruncated && previewed > 0 {
		filled := 20 * previewDone / previewed
		fmt.Fprintf(&text, "Preview [lightgreen][%s[-][slategray]%s][-] %d/%d reported done/cached\n", strings.Repeat("#", filled), strings.Repeat("-", 20-filled), previewDone, previewed)
	} else {
		text.WriteString("Preview total unavailable/incomplete; no percentage inferred.\n")
		if s.buildTruncated {
			text.WriteString("Step counts incomplete; projection unavailable, rejected or capped.\n")
		}
	}
	text.WriteString(dashboardBuildHealth(s, now))
	if !s.buildChangedAt.IsZero() {
		fmt.Fprintf(&text, "\nLast step change: %s ago (not a stall detector)", dashboardBuildAge(now, s.buildChangedAt))
	}
	text.WriteString("\nLog reports only; artifact verification still required.")
	return text.String()
}

func dashboardOSLogText(logs dashboardOSLogs, now time.Time) string {
	age := dashboardBuildAge(now, logs.received) + time.Duration(logs.record.SampleAgeMillis)*time.Millisecond
	if !logs.record.Available {
		return fmt.Sprintf("OS build logs: metadata unavailable; report received %s ago", age.Truncate(time.Second))
	}
	growth := "growth not yet observed"
	if logs.record.GrowthObserved {
		growthAge := age + time.Duration(logs.record.GrowthAgeMillis)*time.Millisecond
		growth = "observed growth " + growthAge.Truncate(time.Second).String() + " ago"
	}
	return fmt.Sprintf("OS build logs: %d files, %d KiB; %s\nMetadata report received %s ago (not a health check)",
		logs.record.Files, logs.record.Bytes/1024, growth, age.Truncate(time.Second))
}

func dashboardSelectedEvents(s dashboardSnapshot) []string {
	selected := dashboardSelectedStep(s)
	if selected < 0 {
		return []string{"No selected step events yet."}
	}
	id := s.buildSteps[selected].id
	var lines []string
	for _, line := range s.activity {
		_, log, found := strings.Cut(line, "] ")
		if !found || !strings.HasPrefix(line, "[build +") {
			log = line
		}
		for _, marker := range []string{"[dry-run] ", "[start] ", "[done] ", "[cached] "} {
			if rest, ok := strings.CutPrefix(log, marker); ok && (rest == id || strings.HasPrefix(rest, id+" (")) {
				lines = append(lines, line)
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No selected marker in retained activity window (80 lines).")
	}
	return append(lines, "Raw parallel output is shared: use the retained private build log.")
}
