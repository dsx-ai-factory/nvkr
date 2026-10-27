// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	qp "github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	"github.com/gdamore/tcell/v2"
)

func qualificationReport(d *dashboard, kind, stage, state, item string, done, total int) {
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.QualificationProgress, Qualification: qp.Event{Kind: kind, Stage: stage, State: state, Item: item, Completed: done, Total: total}})
}

func qualificationDashboardFixture() *dashboard {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "qualification"})
	for _, stage := range []string{qp.StageInputs, qp.StageBuilder} {
		qualificationReport(d, qp.KindStage, stage, qp.StateRunning, "", 0, 0)
		qualificationReport(d, qp.KindStage, stage, qp.StateCompleted, "", 0, 0)
	}
	qualificationReport(d, qp.KindStage, qp.StageSmoke, qp.StateRunning, "", 0, 0)
	qualificationReport(d, qp.KindItem, qp.StageSmoke, qp.StateCompleted, "kubectl", 4, 10)
	qualificationReport(d, qp.KindItem, qp.StageSmoke, qp.StateRunning, "helm", 4, 10)
	return d
}

func TestQualificationDashboardLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {112, 24}, {150, 42}, {180, 48}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := qualificationDashboardFixture()
			screen := dashboardScreen(t, size[0])
			screen.SetSize(size[0], size[1])
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, want := range []string{"QUALIFICATION", "2/6 stages", "before deployment", "BUILDER", "TOOL CHECKS", "QUALIFIER", "REGISTRY", "CONSUMER", "Check delivered tools", "signing keys outside", "4/10 tools", "helm", "unqualified-local-execution", "End follow live"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q at %v:\n%s", want, size, text)
				}
			}
			if strings.Contains(text, "PASS") {
				t.Fatal("partial progress claimed successful qualification")
			}
		})
	}
}

func TestQualificationOutcomeRequiresOwner(t *testing.T) {
	for _, outcome := range []string{nvkedemo.LaneFinished, nvkedemo.LaneFailed} {
		t.Run(outcome, func(t *testing.T) {
			d := qualificationDashboardFixture()
			for _, stage := range qualificationStages[2:] {
				qualificationReport(d, qp.KindStage, stage.id, qp.StateRunning, "", 0, 0)
				qualificationReport(d, qp.KindStage, stage.id, qp.StateCompleted, "", 0, 0)
			}
			before := d.snapshot()
			if strings.Contains(qualificationSummary(before.qualification), "PASS") {
				t.Fatal("telemetry alone claimed pass")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: outcome, Name: "qualification"})
			q := d.snapshot().qualification
			if (outcome == nvkedemo.LaneFinished) != strings.Contains(qualificationSummary(q), "PASS") {
				t.Fatal("wrong owning outcome")
			}
			qualificationReport(d, qp.KindStage, qp.StageInputs, qp.StateRunning, "", 0, 0)
			if got := d.snapshot(); got.qualification.state != q.state || got.qualification.current != q.current || got.phases != before.phases {
				t.Fatal("late telemetry changed terminal outcome or build/deployment authority")
			}
		})
	}
}

func TestQualificationFailurePreservesPartialProgress(t *testing.T) {
	d := qualificationDashboardFixture()
	qualificationReport(d, qp.KindStage, qp.StageSmoke, qp.StateFailed, "", 0, 0)
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFailed, Name: "qualification"})
	screen := dashboardScreen(t, 150)
	d.draw(screen, time.Now(), false)
	text := dashboardText(screen)
	for _, want := range []string{"FAILED", "deployment blocked", "4/10", "failed", "pending"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "PASS") {
		t.Fatal("failure shown as pass")
	}
}

func TestQualificationNavigationDoesNotChangeWork(t *testing.T) {
	d := qualificationDashboardFixture()
	before := d.snapshot()
	d.input(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if q := d.snapshot().qualification; !q.inspecting || q.selected != 3 {
		t.Fatal("cannot inspect next gate")
	}
	qualificationReport(d, qp.KindStage, qp.StagePublish, qp.StateRunning, "", 0, 0)
	if d.snapshot().qualification.selected != 3 {
		t.Fatal("live update stole selection")
	}
	d.input(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if q := d.snapshot().qualification; q.inspecting || qualificationSelection(q) != 4 {
		t.Fatal("End did not follow live stage")
	}
	if d.snapshot().phases != before.phases {
		t.Fatal("navigation changed work")
	}
	d.input(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if !d.snapshot().confirmCancel {
		t.Fatal("qualification swallowed cancellation confirmation")
	}
}

func TestQualificationRejectsUnboundedAndPrivateDescriptions(t *testing.T) {
	for _, change := range []func(*qp.Event){
		func(e *qp.Event) { e.Stage = "private-stage" }, func(e *qp.Event) { e.State = "qualified" },
		func(e *qp.Event) { e.Item = "\x1b[2J" }, func(e *qp.Event) { e.Item = "/private/key" },
		func(e *qp.Event) { e.Item = strings.Repeat("a", 1025) }, func(e *qp.Event) { e.Completed = 11 },
	} {
		d := qualificationDashboardFixture()
		before := d.snapshot()
		e := qp.Event{Kind: qp.KindItem, Stage: qp.StageSmoke, State: qp.StateCompleted, Item: "helm", Completed: 5, Total: 10}
		change(&e)
		d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.QualificationProgress, Qualification: e})
		after := d.snapshot()
		if after.qualification.updated != before.qualification.updated {
			t.Fatalf("invalid description accepted: %+v", e)
		}
	}
	d := newDashboard()
	qualificationReport(d, qp.KindStage, qp.StageInputs, qp.StateRunning, "", 0, 0)
	if d.snapshot().qualification.state != "" {
		t.Fatal("unsolicited event started qualification")
	}
}

func TestQualificationSnapshotOwnsMemberHistory(t *testing.T) {
	d := qualificationDashboardFixture()
	copy := d.snapshot()
	copy.qualification.stages[2].items[0].name = "mutated"
	if d.snapshot().qualification.stages[2].items[0].name == "mutated" {
		t.Fatal("snapshot aliases member history")
	}
	for i := 0; i < 100; i++ {
		qualificationReport(d, qp.KindItem, qp.StageSmoke, qp.StateCompleted, fmt.Sprintf("tool-%d", i), i+1, 100)
	}
	if len(d.snapshot().qualification.stages[2].items) != 64 {
		t.Fatal("member history is not bounded")
	}
}

func TestQualificationPlainProgressAndRetainedOutcome(t *testing.T) {
	var out bytes.Buffer
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &checkedWriter{out: &out, cancel: cancel, started: time.Now(), phases: map[string]time.Time{}}
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.QualificationProgress, Qualification: qp.Event{Kind: qp.KindStage, Stage: qp.StageSmoke, State: qp.StateRunning}})
	if !strings.Contains(out.String(), "[Qualification running] Check delivered tools") {
		t.Fatal(out.String())
	}
	out.Reset()
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "qualification"})
	out.Reset()
	w.replayResults(true)
	if !strings.Contains(out.String(), "Qualification passed") || !strings.Contains(out.String(), "unqualified-local-execution") {
		t.Fatal(out.String())
	}
}

func TestQualificationMissingProgressNeverInventsCompletion(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "qualification"})
	qualificationReport(d, qp.KindItem, qp.StageSmoke, qp.StateCompleted, "helm", 10, 10)
	q := d.snapshot().qualification
	if q.stages[2].state != qp.StateRunning || !q.stages[2].started.IsZero() || q.stages[1].state != "" {
		t.Fatal("a member observation invented a start time or predecessor completion")
	}
	if strings.Contains(qualificationSummary(q), "PASS") || strings.Contains(qualificationSummary(q), "1/6") {
		t.Fatal("member total completed an unreported stage")
	}
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFailed, Name: "qualification"})
	if d.snapshot().qualification.stages[2].state != "stopped" {
		t.Fatal("command failure left an unfinished stage running")
	}
}

func TestQualificationCancellationNamesCurrentWork(t *testing.T) {
	d := qualificationDashboardFixture()
	screen := dashboardScreen(t, 150)
	d.draw(screen, time.Now(), true)
	text := dashboardText(screen)
	for _, want := range []string{"CANCELING", "Qualification: Check delivered tools", "Cleanup completion is not yet observed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "PASS") {
		t.Fatal("cancellation claimed success")
	}
}

func TestQualificationYieldsToSubsequentNativeToolBuild(t *testing.T) {
	for _, outcome := range []string{nvkedemo.LaneFinished, nvkedemo.LaneFailed} {
		t.Run(outcome, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "artifacts"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "qualification"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "qualification"})
			if !dashboardQualificationActive(d.snapshot()) {
				t.Fatal("qualification result is not visible before subsequent work")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			if dashboardQualificationActive(d.snapshot()) {
				t.Fatal("qualification result hides the native tool build")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: outcome, Name: "artifacts"})
			snapshot := d.snapshot()
			if dashboardQualificationActive(snapshot) || snapshot.qualification.state != "complete" {
				t.Fatal("native tool outcome revived the qualification page or changed its historical result")
			}
			screen := dashboardScreen(t, 150)
			d.draw(screen, time.Now(), false)
			if strings.Contains(dashboardText(screen), "End follow live") {
				t.Fatal("render still shows the qualification page after native tools")
			}
		})
	}
}
