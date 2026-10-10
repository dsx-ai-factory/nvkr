// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	"github.com/gdamore/tcell/v2"
)

func dashboardScreen(t *testing.T, width int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(width, 48)
	t.Cleanup(screen.Fini)
	return screen
}

func dashboardText(screen tcell.SimulationScreen) string {
	screen.Show()
	cells, width, _ := screen.GetContents()
	var text strings.Builder
	for index, cell := range cells {
		if index > 0 && index%width == 0 {
			text.WriteByte('\n')
		}
		if len(cell.Runes) == 0 {
			text.WriteByte(' ')
		} else {
			text.WriteRune(cell.Runes[0])
		}
	}
	return text.String()
}

func TestDashboardLayout(t *testing.T) {
	for _, width := range []int{80, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 1, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
			_, _ = io.WriteString(d, "Build log: /tmp/private-build.log\n[start] image/build\n")
			screen := dashboardScreen(t, width)
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, wanted := range []string{"UNQUALIFIED", "not a signed Release", "Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup", "Observed state", "Observe tenant provisioning", "Activity", "2/2 registered", "1/2 Ready", "20m", "remaining", "/tmp/private-build.log", "PgUp/PgDn"} {
				if !strings.Contains(text, wanted) {
					t.Fatalf("missing %q in %d-column layout:\n%s", wanted, width, text)
				}
			}
			d.draw(screen, time.Now(), true)
			if !strings.Contains(dashboardText(screen), "waiting for owned processes") {
				t.Fatal("cancellation omitted cleanup obligation")
			}
		})
	}
}

func TestDashboardObservationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event nkedemo.ProgressEvent
		phase int
		state string
	}{
		{"artifact complete", nkedemo.ProgressEvent{Kind: nkedemo.LaneFinished, Name: "artifacts"}, 1, "complete"},
		{"build command is not lane completion", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "build-all"}, 1, "observed"},
		{"installer substep is not whole preparation", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "installer-fetch"}, 0, "observed"},
		{"installer owner completed", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "installer-preparation"}, 0, "complete"},
		{"network restore is not whole cleanup", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "network-cleanup"}, 5, "observed"},
		{"wrong event family", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "artifacts"}, 1, "pending"},
		{"unknown event", nkedemo.ProgressEvent{Kind: "caller-success", Name: "artifacts"}, 1, "pending"},
		{"private name", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFailed, Name: "private-node-name"}, 3, "pending"},
		{"ready nodes are not workload success", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 2}, 4, "pending"},
		{"invalid node count", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 3, ReadyNodes: 2}, 3, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			_, _ = io.WriteString(d, "[done] artifacts — all Ready\n")
			d.Report(tc.event)
			if got := d.snapshot().phases[tc.phase].state; got != tc.state {
				t.Fatalf("state=%q want=%q", got, tc.state)
			}
		})
	}
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFailed, Name: "network-cleanup"})
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "registry-cleanup"})
	if d.snapshot().phases[5].state != "failed" {
		t.Fatal("later successful substep masked cleanup failure")
	}
}

func TestDashboardSanitizationAndPrivacy(t *testing.T) {
	d := newDashboard()
	_, _ = io.WriteString(d, "visible\x1b[2J [red]literal[-]\n")
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Status: "private-secret", RegisteredNodes: 1, ReadyNodes: 0})
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "private-node-condition", Status: "private-secret"})
	screen := dashboardScreen(t, 140)
	d.draw(screen, time.Now(), false)
	text := dashboardText(screen)
	if strings.ContainsRune(text, '\x1b') || strings.Contains(text, "private-secret") || strings.Contains(text, "private-node-condition") || !strings.Contains(text, `visible\x1b[2J [red]literal[-]`) {
		t.Fatalf("unsafe or altered display:\n%s", text)
	}
}

func TestDashboardBoundsAndImmutableSnapshot(t *testing.T) {
	d := newDashboard()
	for i := range 100 {
		_, _ = fmt.Fprintf(d, "activity-%03d\n", i)
	}
	_, _ = io.WriteString(d, strings.Repeat("界", 6000))
	snapshot := d.snapshot()
	if len(snapshot.activity) != dashboardLines || len(d.partial) > 4*dashboardRunes || utf8.RuneCountInString(snapshot.activity[len(snapshot.activity)-1]) > dashboardRunes {
		t.Fatal("activity history or partial line exceeded bounds")
	}
	snapshot.activity[0] = "mutated"
	snapshot.phases[0].state = "complete"
	if got := d.snapshot(); got.activity[0] == "mutated" || got.phases[0].state == "complete" {
		t.Fatal("snapshot shares mutable model state")
	}
}

func TestDashboardBudgetAndNavigation(t *testing.T) {
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-api", Elapsed: time.Minute, Remaining: time.Minute})
	before := d.snapshot()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Remaining: 20 * time.Minute})
	if d.snapshot().deadline.After(before.deadline) {
		t.Fatal("remaining budget increased")
	}
	for _, key := range []tcell.Key{tcell.KeyTab, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyEnd, tcell.KeyCtrlC} {
		d.input(tcell.NewEventKey(key, 0, tcell.ModNone))
	}
	d.input(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if got := d.snapshot(); got.offset != 0 || got.phases != before.phases {
		t.Fatal("navigation changed observed state")
	}
}

func TestDashboardConcurrentReportsAndDraw(t *testing.T) {
	d := newDashboard()
	screen := dashboardScreen(t, 140)
	var joined sync.WaitGroup
	for range 4 {
		joined.Go(func() {
			for range 100 {
				_, _ = io.WriteString(d, "public activity\n")
				d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0})
			}
		})
	}
	for range 10 {
		d.draw(screen, time.Now(), false)
		_ = d.snapshot()
	}
	joined.Wait()
}
