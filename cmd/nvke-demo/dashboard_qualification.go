// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	qp "github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	"github.com/dsx-ai-factory/nvkr/internal/tuitheme"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// These descriptions explain the operations that emit progress. Neither a
// checklist nor a completed progress record can authorize deployment.
var qualificationStages = [...]struct {
	id, label, input, action, output, boundary, unit string
}{
	{qp.StageInputs, "Verify build outputs", "Completed build + recorded inputs + qualification policy",
		"Recheck the build inputs and every required named output.\nRead and retain the exact artifact bytes before signing evidence.",
		"Verified artifact identities and retained bytes", "Every required output is checked separately.", "outputs"},
	{qp.StageBuilder, "Sign build evidence", "Verified artifact bytes + builder signing key",
		"Sign the build record and verify the builder's signature.\nReopen the artifacts and confirm their bytes still match.",
		"Signed, verified build evidence for all required outputs", "Tool checks and qualification approval still follow.", "outputs"},
	{qp.StageSmoke, "Check delivered tools", "Delivered tools + pinned container image",
		"Run each tool's version command in an isolated container.\nKeep signing keys outside the container; sign and verify the results.",
		"Signed, verified execution results for each required tool", "Version checks confirm tools run; tenant readiness is checked during deployment.", "tools"},
	{qp.StageQualifier, "Sign qualification", "Verified build evidence + tool results + qualification policy",
		"Check the required evidence against the qualification policy.\nSign with the qualifier key, verify it and save the evidence bundle.",
		"Signed qualification decision and complete evidence bundle", "Builder, tool-check and qualifier signatures use three distinct local keys.", "checks"},
	{qp.StagePublish, "Publish artifacts", "Verified artifacts + signed evidence bundle",
		"Upload the artifacts, each disk and the signed evidence bundle.\nRecord each upload's content digest so it can be fetched exactly.",
		"Registry references pinned to exact content digests", "Next, download and verify the published content independently.", "uploads"},
	{qp.StageReadback, "Verify registry contents", "Registry references + public signing keys + consumer policy",
		"Download the artifacts and evidence using their content digests.\nRecheck the inputs, artifact bytes and all three role signatures.",
		"Independently verified registry contents", "Deployment waits for the saved result and successful resource cleanup.", "checks"},
}

type qualificationItem struct{ name, state string }
type qualificationStage struct {
	state, current    string
	completed, total  int
	started, finished time.Time
	items             []qualificationItem
}

type dashboardQualification struct {
	state             string
	started, updated  time.Time
	current, selected int
	inspecting        bool
	stages            [len(qualificationStages)]qualificationStage
}

func qualificationStageIndex(stage string) int {
	return slices.IndexFunc(qualificationStages[:], func(s struct{ id, label, input, action, output, boundary, unit string }) bool { return s.id == stage })
}

// Caller holds d.mu. Only the enclosing operation's lane event determines the
// final outcome; subprocess events and stale records cannot turn failure green.
func (d *dashboard) observeQualification(event nvkedemo.ProgressEvent) bool {
	q := &d.qualification
	if event.Name == "qualification" {
		switch event.Kind {
		case nvkedemo.LaneStarted:
			if q.state == "" {
				*q = dashboardQualification{state: "running", started: time.Now(), current: 0}
				d.current = "Qualification: waiting for progress"
				d.currentStarted = time.Time{}
			}
		case nvkedemo.LaneFinished, nvkedemo.LaneFailed:
			if q.state != "running" {
				return true
			}
			q.state = "complete"
			if event.Kind == nvkedemo.LaneFailed {
				q.state = "failed"
			}
			d.current = "Qualification " + q.state
			d.currentStarted = time.Time{}
			for i := range q.stages {
				if q.stages[i].state == qp.StateRunning {
					q.stages[i].state = "stopped"
					q.stages[i].finished = time.Now()
				}
			}
			d.appendActivity("Qualification " + q.state)
		}
		return true
	}
	if event.Kind != nvkedemo.QualificationProgress {
		return false
	}
	record := event.Qualification
	if record.Validate() != nil || q.state != "running" {
		return true
	}
	index := qualificationStageIndex(record.Stage)
	if index < 0 {
		return true
	}
	stage := &q.stages[index]
	if stage.state == qp.StateFailed || stage.state == qp.StateCompleted && record.State != qp.StateFailed {
		return true
	}
	now := time.Now()
	q.updated, q.current = now, index
	d.current = "Qualification: " + qualificationStages[index].label
	if record.Kind == qp.KindStage {
		stage.state = record.State
		if record.Total > 0 && record.Completed >= stage.completed {
			stage.completed, stage.total = record.Completed, record.Total
		}
		if record.State == qp.StateRunning && stage.started.IsZero() {
			stage.started = now
		}
		if record.State != qp.StateRunning {
			stage.finished = now
		}
		d.appendActivity("Qualification " + record.State + ": " + qualificationStages[index].label)
	} else {
		if stage.state == "" {
			// An item establishes activity even if the lossy channel omitted its
			// stage-start record. Do not invent a start time or completion.
			stage.state = qp.StateRunning
		}
		stage.current = record.Item
		if record.Total > 0 && record.Completed >= stage.completed {
			stage.completed, stage.total = record.Completed, record.Total
		}
		item := slices.IndexFunc(stage.items, func(i qualificationItem) bool { return i.name == record.Item })
		if item >= 0 {
			if stage.items[item].state == qp.StateRunning {
				stage.items[item].state = record.State
			}
		} else {
			// A small public history, not the authoritative membership set.
			if len(stage.items) == 64 {
				stage.items = stage.items[1:]
			}
			stage.items = append(stage.items, qualificationItem{record.Item, record.State})
		}
	}
	d.currentStarted = time.Time{}
	if stage.state == qp.StateRunning {
		d.currentStarted = stage.started
	}
	return true
}

func dashboardQualificationActive(s dashboardSnapshot) bool {
	return s.qualification.state != "" && s.phases[2].state == "pending" && s.tenantStage == ""
}

func (d *dashboard) qualificationInput(event *tcell.EventKey) bool {
	if !dashboardQualificationActive(d.dashboardSnapshot) {
		return false
	}
	q := &d.qualification
	switch event.Key() {
	case tcell.KeyUp, tcell.KeyDown:
		if !q.inspecting {
			q.selected = q.current
		}
		q.inspecting = true
		if event.Key() == tcell.KeyUp {
			q.selected = max(0, q.selected-1)
		} else {
			q.selected = min(len(q.stages)-1, q.selected+1)
		}
	case tcell.KeyEnd, tcell.KeyEscape:
		q.inspecting = false
	default:
		if event.Key() != tcell.KeyRune || event.Rune() != 'a' {
			return false
		}
		q.inspecting = false
	}
	return true
}

func qualificationStyle(state string) (color, marker, label string) {
	switch state {
	case qp.StateRunning:
		return "lightcyan", "▶", "running"
	case qp.StateCompleted:
		return "lightgreen", "✓", "checked"
	case qp.StateFailed:
		return "lightcoral", "✗", "failed"
	case "stopped":
		return "lightsalmon", "■", "stopped"
	default:
		return "slategray", "○", "pending"
	}
}

func qualificationSelection(q dashboardQualification) int {
	if q.inspecting {
		return q.selected
	}
	return q.current
}

func qualificationChecklist(q dashboardQualification, now time.Time, compact bool) string {
	var out strings.Builder
	for i, spec := range qualificationStages {
		stage := q.stages[i]
		color, marker, state := qualificationStyle(stage.state)
		cursor := " "
		if qualificationSelection(q) == i {
			cursor = "›"
		}
		fmt.Fprintf(&out, "%s [%s]%s %d  %s[-]\n", cursor, color, marker, i+1, spec.label)
		if !compact {
			elapsed := ""
			if !stage.started.IsZero() {
				end := stage.finished
				if end.IsZero() {
					end = now
				}
				elapsed = " · " + max(time.Duration(0), end.Sub(stage.started)).Truncate(time.Second).String()
			}
			fmt.Fprintf(&out, "       %s%s\n", state, elapsed)
			if stage.total > 0 {
				fmt.Fprintf(&out, "       %d/%d %s reported\n", stage.completed, stage.total, spec.unit)
			}
			if i < len(q.stages)-1 {
				out.WriteString("       │\n")
			}
		}
	}
	return out.String()
}

func qualificationSummary(q dashboardQualification) string {
	completed := 0
	for _, stage := range q.stages {
		if stage.state == qp.StateCompleted {
			completed++
		}
	}
	summary := fmt.Sprintf("[lightcyan::b]QUALIFICATION[-::-]    %d/6 stages reported complete\n", completed)
	switch q.state {
	case "complete":
		summary += "[lightgreen]PASS · artifacts and evidence verified; result saved[-]"
	case "failed":
		summary += "[lightcoral]FAILED · deployment blocked; inspect saved diagnostics[-]"
	default:
		summary += "[goldenrod]Verifying artifacts and evidence before deployment[-]"
	}
	return summary
}

func qualificationRoleChain(q dashboardQualification) string {
	var out strings.Builder
	for n, role := range []struct {
		label string
		stage int
	}{{"BUILDER", 1}, {"TOOL CHECKS", 2}, {"QUALIFIER", 3}, {"REGISTRY", 4}, {"CONSUMER", 5}} {
		if n > 0 {
			out.WriteString("  →  ")
		}
		color, marker, _ := qualificationStyle(q.stages[role.stage].state)
		fmt.Fprintf(&out, "[%s]%s %s[-]", color, marker, role.label)
	}
	return out.String()
}

func qualificationDetail(q dashboardQualification, now time.Time, compact bool) string {
	index := qualificationSelection(q)
	spec, stage := qualificationStages[index], q.stages[index]
	color, marker, state := qualificationStyle(stage.state)
	mode := "following live work"
	if q.inspecting {
		mode = "inspecting stage · End returns live"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[%s::b]%s %s[-::-] · %s\n", color, marker, spec.label, mode)
	fmt.Fprintf(&out, "[lightcyan]IN[-]   %s\n", spec.input)
	out.WriteString(spec.action + "\n")
	fmt.Fprintf(&out, "[lightcyan]OUT[-]  %s\n", spec.output)
	if !compact {
		fmt.Fprintf(&out, "\n[goldenrod]%s[-]\n", spec.boundary)
	}
	if stage.total > 0 {
		bars := min(20, stage.completed*20/stage.total)
		fmt.Fprintf(&out, "\n%s %d/%d %s reported complete\n", tview.Escape("["+strings.Repeat("#", bars)+strings.Repeat("·", 20-bars)+"]"), stage.completed, stage.total, spec.unit)
	} else {
		fmt.Fprintf(&out, "\nStage: %s\n", state)
	}
	if stage.current != "" {
		fmt.Fprintf(&out, "Latest item: %s\n", tview.Escape(stage.current))
	}
	if !compact && !q.updated.IsZero() {
		fmt.Fprintf(&out, "Last update %s ago\n", max(time.Duration(0), now.Sub(q.updated)).Truncate(time.Second))
	}
	return out.String()
}

func qualificationMembers(q dashboardQualification, rows int) string {
	stage := q.stages[qualificationSelection(q)]
	if len(stage.items) == 0 {
		return "Stage progress is shown above.\nIndividual items appear here when reported."
	}
	var out strings.Builder
	start := max(0, len(stage.items)-max(1, rows))
	for _, item := range stage.items[start:] {
		color, marker, state := qualificationStyle(item.state)
		fmt.Fprintf(&out, " [%s]%s %-7s[-] %s\n", color, marker, state, tview.Escape(item.name))
	}
	return out.String()
}

func (d *dashboard) drawQualification(screen tcell.Screen, s dashboardSnapshot, now time.Time, width, height int) {
	q := s.qualification
	palette := tuitheme.Colors(false)
	d.header.SetText(qualificationSummary(q) + "\n[slategray]Artifacts → Qualification → Site → Tenant → Workload/DNS → Cleanup[-]")
	d.header.SetRect(1, 0, max(0, width-2), min(3, height))
	d.phasesView.SetTitle(" Qualification stages ")
	d.phasesView.SetWrap(false)
	d.stateView.SetTitle(" How this stage works ")
	d.stateView.SetWrap(true)
	d.activityView.SetTitle(" Current work ")
	d.activityView.SetWrap(false)
	content := max(0, height-6)
	wide := width >= 112
	if wide {
		left := 34
		d.phasesView.SetText(qualificationChecklist(q, now, height < 32))
		d.phasesView.SetRect(0, 3, left, content)
		detailHeight := min(17, max(0, content-5))
		d.stateView.SetText(qualificationRoleChain(q) + "\n\n" + qualificationDetail(q, now, detailHeight < 17))
		d.stateView.SetRect(left, 3, max(0, width-left), detailHeight)
		d.activityView.SetRect(left, 3+detailHeight, max(0, width-left), max(0, content-detailHeight))
	} else {
		// Keep the live explanation, count and result visible even at 80x24.
		d.phasesView.SetText(qualificationRoleChain(q))
		d.phasesView.SetRect(0, 3, width, min(3, content))
		detailHeight := min(12, max(0, content-6))
		d.stateView.SetText(qualificationDetail(q, now, true))
		d.stateView.SetRect(0, 6, width, detailHeight)
		d.activityView.SetRect(0, 6+detailHeight, width, max(0, content-3-detailHeight))
	}
	_, _, _, rows := d.activityView.GetInnerRect()
	d.activityView.SetText(qualificationMembers(q, rows))
	for _, pane := range []*tview.TextView{d.phasesView, d.stateView, d.activityView} {
		pane.SetBorderColor(palette.Border)
		pane.ScrollToBeginning()
		pane.Draw(screen)
	}
	help := " ↑/↓ explore stages · End follow live · q / Ctrl-C confirm cancel"
	if s.confirmCancel {
		help = " Cancel current work? Retention/cleanup follows the selected mode. y/N"
	}
	d.footer.SetText("[goldenrod] Local signing keys · execution lane: unqualified-local-execution[-]\n" + help + "\n Deployment waits for verified results and resource cleanup.")
	d.footer.SetRect(0, max(0, height-3), width, min(3, height))
	d.header.Draw(screen)
	d.footer.Draw(screen)
}

func (w *checkedWriter) qualificationProgress(event nvkedemo.ProgressEvent) {
	record := event.Qualification
	if record.Validate() != nil {
		return
	}
	if w.observer != nil {
		w.observer.Report(event)
		return
	}
	index := qualificationStageIndex(record.Stage)
	if index < 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if record.Kind == qp.KindStage {
		w.emit(fmt.Sprintf("[Qualification %s] %s\n", record.State, qualificationStages[index].label))
	} else {
		w.emit(fmt.Sprintf("[Qualification %s] %s · %s · %d/%d %s\n", record.State, qualificationStages[index].label, record.Item, record.Completed, record.Total, qualificationStages[index].unit))
	}
}
