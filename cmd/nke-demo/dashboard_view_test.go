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
	for _, width := range []int{80, 140, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 1, Provisioning: nkedemo.TenantConditionTrue, Provisioned: nkedemo.TenantConditionFalse, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
			_, _ = io.WriteString(d, "Build log: /tmp/private-build.log\n[start] image/build\n")
			screen := dashboardScreen(t, width)
			if width == 80 || width == 160 {
				screen.SetSize(width, 42)
			}
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, wanted := range []string{"UNQUALIFIED", "not a signed Release", "Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup", "Observed state", "Observe tenant provisioning", "Activity", "2/2 registered", "1/2 Ready", "Provisioning=True", "Provisioned=False", "State unchanged", "last observation", "20m", "remaining", "/tmp/private-build.log", "PgUp/PgDn"} {
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

func TestDashboardShortLayoutPreservesLiveStatus(t *testing.T) {
	for _, height := range []int{24, 30} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioning: nkedemo.TenantConditionTrue, Provisioned: nkedemo.TenantConditionFalse, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
			_, _ = io.WriteString(d, "latest public activity\n")
			screen := dashboardScreen(t, 80)
			screen.SetSize(80, height)
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, wanted := range []string{"Observed state", "Observe tenant provisioning", "Waiting for control plane", "worker.", "1/2 registered", "0/2 Ready", "Provisioning=True", "Provisioned=False", "State unchanged", "last observation", "20m", "Timeout remaining", "not an ETA", "latest public activity"} {
				if !strings.Contains(text, wanted) {
					t.Fatalf("missing %q in 80x%d layout:\n%s", wanted, height, text)
				}
			}
		})
	}
}

func TestDashboardQuitConfirmationFooter(t *testing.T) {
	d := newDashboard()
	d.input(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	screen := dashboardScreen(t, 80)
	screen.SetSize(80, 24)
	d.draw(screen, time.Now(), false)
	if !strings.Contains(dashboardText(screen), "Cancel this demo and clean up its resources? y/N") {
		t.Fatal("quit confirmation omitted its cancellation and cleanup meaning")
	}
	d.input(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	d.draw(screen, time.Now(), false)
	if strings.Contains(dashboardText(screen), "Cancel this demo") {
		t.Fatal("dismissed quit confirmation remains visible")
	}
}

func TestDashboardForceConfirmationFooter(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.forceAvailable = true
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 24)
			d.draw(screen, time.Now(), false)
			if strings.Contains(dashboardText(screen), "FORCE EXIT") {
				t.Fatal("force exit offered before cancellation")
			}
			d.draw(screen, time.Now(), true)
			for _, wanted := range []string{"CANCELING", "[ f ] FORCE EXIT", "confirmation required", "VMs, containers and network changes may remain."} {
				if !strings.Contains(dashboardText(screen), wanted) {
					t.Fatalf("cancellation omitted prominent action %q", wanted)
				}
			}
			d.forceInput(tcell.NewEventKey(tcell.KeyRune, 'f', tcell.ModNone))
			d.draw(screen, time.Now(), true)
			for _, wanted := range []string{"FORCE EXIT NOW?", "[y] Exit immediately", "[n / Esc] Keep waiting", "Cleanup may be incomplete; resources may remain. y/N"} {
				if !strings.Contains(dashboardText(screen), wanted) {
					t.Fatalf("force confirmation omitted %q", wanted)
				}
			}
			if d.forceInput(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) || d.snapshot().confirmForce {
				t.Fatal("Escape did not dismiss force confirmation")
			}
		})
	}
}

func TestDashboardTenantHeartbeatAndChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   nkedemo.ProgressEvent
		changed bool
		valid   bool
	}{
		{"heartbeat", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0}, false, true},
		{"ready count", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 1}, true, true},
		{"registered count", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 0}, true, true},
		{"stage", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-workload", RegisteredNodes: 1, ReadyNodes: 0}, true, true},
		{"condition", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioned: nkedemo.TenantConditionTrue}, true, true},
		{"invalid condition", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioned: "private-condition"}, false, true},
		{"unknown", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "private-condition", RegisteredNodes: 1, ReadyNodes: 0}, false, false},
		{"invalid count", nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 3, ReadyNodes: 0}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Remaining: time.Hour})
			d.tenantChangedAt = d.tenantChangedAt.Add(-2 * time.Minute)
			d.observedAt = d.observedAt.Add(-10 * time.Second)
			before := d.snapshot()
			tc.event.Elapsed, tc.event.Remaining = time.Minute, time.Hour
			d.Report(tc.event)
			after := d.snapshot()
			if after.tenantChangedAt.After(before.tenantChangedAt) != tc.changed || after.observedAt.After(before.observedAt) != tc.valid {
				t.Fatalf("stage/count change=%v latest observation=%v", after.tenantChangedAt.After(before.tenantChangedAt), after.observedAt.After(before.observedAt))
			}
		})
	}
}

func TestDashboardStaleObservationIsNotFailure(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Remaining: time.Hour})
			now := d.snapshot().observedAt.Add(5 * time.Minute)
			d.tenantChangedAt = now.Add(-10 * time.Minute)
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 42)
			d.draw(screen, now, false)
			text := dashboardText(screen)
			for _, wanted := range []string{"State unchanged: 10m0s", "last observation: 5m0s ago", "Tenant budget", "remaining", "1/2 registered", "0/2 Ready", "Activity"} {
				if !strings.Contains(text, wanted) {
					t.Fatalf("missing %q at width %d:\n%s", wanted, width, text)
				}
			}
			if strings.Contains(text, "stuck") || d.snapshot().phases[3].state != "running" {
				t.Fatal("observation age fabricated a stall or failure")
			}
		})
	}
}

func TestDashboardSiteStageObservations(t *testing.T) {
	for _, stage := range dashboardSiteStages {
		for _, name := range stage.operations {
			if name == "" {
				continue
			}
			if phase, _ := dashboardOperation(name); phase != 2 {
				t.Fatalf("site checklist operation %q has no site observation mapping", name)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		events []nkedemo.ProgressEvent
		state  string
		done   int
	}{
		{"pending", nil, "pending", 0},
		{"running", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseStarted, Name: "site-05-install-site-controller"}}, "running", 0},
		{"run does not complete check", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "site-05-install-site-controller"}}, "pending", 1},
		{"check running", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "site-05-install-site-controller"}, {Kind: nkedemo.PhaseStarted, Name: "check-05-install-site-controller"}}, "running", 1},
		{"check without run", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "check-05-install-site-controller"}}, "pending", 1},
		{"observed", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "site-05-install-site-controller"}, {Kind: nkedemo.PhaseFinished, Name: "check-05-install-site-controller"}}, "observed", 2},
		{"failed check", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "site-05-install-site-controller"}, {Kind: nkedemo.PhaseFailed, Name: "check-05-install-site-controller"}, {Kind: nkedemo.PhaseFinished, Name: "check-05-install-site-controller"}}, "failed", 1},
		{"unknown", []nkedemo.ProgressEvent{{Kind: nkedemo.PhaseFinished, Name: "private-stage-success"}}, "pending", 0},
		{"wrong family", []nkedemo.ProgressEvent{{Kind: nkedemo.DeploymentFinished, Name: "site-05-install-site-controller"}}, "pending", 0},
		{"owning demo cannot invent substeps", []nkedemo.ProgressEvent{{Kind: nkedemo.DeploymentFinished, Name: "local-demo"}}, "pending", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			_, _ = io.WriteString(d, "[done] site-05-install-site-controller\n[done] check-05-install-site-controller\n")
			for _, event := range tc.events {
				d.Report(event)
			}
			state, done, _ := d.snapshot().siteStages[9].status(2)
			if state != tc.state || done != tc.done {
				t.Fatalf("state=%q observed=%d want %q/%d", state, done, tc.state, tc.done)
			}
			if got := d.snapshot().phases[1].state; got != "pending" {
				t.Fatalf("replay fabricated artifact completion: %s", got)
			}
		})
	}
}

func TestDashboardFillingSiteChecklist(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			for _, index := range []int{3, 4, 5, 6, 7, 8} {
				for _, name := range dashboardSiteStages[index].operations {
					if name != "" {
						d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: name})
					}
				}
			}
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: "site-05-install-site-controller"})
			s := d.snapshot()
			now := s.siteStages[9].started[0].Add(75 * time.Second)
			if checklist, _ := dashboardChecklist(s, now); !strings.Contains(checklist, "0/2 · 1m15s") {
				t.Fatalf("active substep elapsed missing: %s", checklist)
			}
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 42)
			d.draw(screen, now, false)
			text := dashboardText(screen)
			for _, want := range []string{"Site stages · 6/11 checked", "Install Cluster API", "Check Cluster API", "Readiness", "Deliver images", "Install NKE controller", "1m15s", "Secure and register", "Tenant stages · 0/4 checked", "Tenant kubeconfig", "Tenant API query", "Both nodes Ready", "Workload and DNS", "pending", "observed", "ETA unknown", "Activity"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %d-column checklist:\n%s", want, width, text)
				}
			}
			if d.snapshot().phases[2].state == "complete" {
				t.Fatal("successful substeps fabricated owning Site completion")
			}
		})
	}
}

func TestDashboardTenantStagesDoNotInventObservations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []nkedemo.ProgressEvent
		states [4]string
	}{
		{"waiting", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig"}}, [4]string{"running", "pending", "pending", "pending"}},
		{"api", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig"}, {Kind: nkedemo.TenantWaiting, Name: "tenant-api"}}, [4]string{"observed", "running", "pending", "pending"}},
		{"ready only", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 2}}, [4]string{"pending", "pending", "observed", "pending"}},
		{"provisioned is not node readiness", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Provisioned: nkedemo.TenantConditionTrue}}, [4]string{"pending", "pending", "running", "pending"}},
		{"workload owner", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-workload", RegisteredNodes: 2, ReadyNodes: 2}, {Kind: nkedemo.DeploymentFinished, Name: "tenant-provisioning"}}, [4]string{"pending", "pending", "pending", "observed"}},
		{"failure", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-api"}, {Kind: nkedemo.DeploymentFailed, Name: "tenant-provisioning"}}, [4]string{"pending", "failed", "pending", "pending"}},
		{"missing owner observations", []nkedemo.ProgressEvent{{Kind: nkedemo.DeploymentFinished, Name: "tenant-provisioning"}}, [4]string{"pending", "pending", "pending", "pending"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			for _, event := range tc.events {
				d.Report(event)
			}
			for index, stage := range d.snapshot().tenantStages {
				if state, _, _ := stage.status(1); state != tc.states[index] {
					t.Fatalf("stage %d=%s want %s", index, state, tc.states[index])
				}
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
		{"OVS readiness is not whole site completion", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "site-ovs-ready"}, 2, "observed"},
		{"OVN install is not whole site completion", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: "site-ovn-install"}, 2, "observed"},
		{"OVN readiness failure stays visible", nkedemo.ProgressEvent{Kind: nkedemo.PhaseFailed, Name: "site-ovn-node-ready"}, 2, "failed"},
		{"CAPI waits for site networking", nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: "install-capi"}, 2, "running"},
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

func TestDashboardFailureStopsUnfinishedPhases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []nkedemo.ProgressEvent
		states [6]string
	}{
		{
			name: "workload stops when its tenant owner fails",
			events: []nkedemo.ProgressEvent{
				{Kind: nkedemo.TenantWaiting, Name: "tenant-workload", RegisteredNodes: 2, ReadyNodes: 2},
				{Kind: nkedemo.DeploymentFailed, Name: "tenant-provisioning"},
			},
			states: [6]string{"pending", "pending", "pending", "failed", "stopped", "pending"},
		},
		{
			name: "deployment failure stops active site and cleanup",
			events: []nkedemo.ProgressEvent{
				{Kind: nkedemo.PhaseStarted, Name: "create-management"},
				{Kind: nkedemo.PhaseStarted, Name: "network-cleanup"},
				{Kind: nkedemo.DeploymentFailed, Name: "local-demo"},
			},
			states: [6]string{"pending", "pending", "stopped", "pending", "pending", "stopped"},
		},
		{
			name: "completed artifacts and failed cleanup remain explicit",
			events: []nkedemo.ProgressEvent{
				{Kind: nkedemo.LaneFinished, Name: "artifacts"},
				{Kind: nkedemo.PhaseFailed, Name: "network-cleanup"},
				{Kind: nkedemo.DeploymentFailed, Name: "local-demo"},
			},
			states: [6]string{"pending", "complete", "pending", "pending", "pending", "failed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			for _, event := range tc.events {
				d.Report(event)
			}
			for index, phase := range d.snapshot().phases {
				if phase.state != tc.states[index] {
					t.Fatalf("phase %d: state=%q want=%q", index, phase.state, tc.states[index])
				}
			}
		})
	}
}

func TestDashboardSanitizationAndPrivacy(t *testing.T) {
	d := newDashboard()
	_, _ = io.WriteString(d, "visible\x1b[2J [red]literal[-]\n")
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Status: "private-secret", RegisteredNodes: 1, ReadyNodes: 0})
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "private-node-condition", Status: "private-secret"})
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Provisioning: "private-condition", Provisioned: "private-secret", RegisteredNodes: 1})
	screen := dashboardScreen(t, 140)
	d.draw(screen, time.Now(), false)
	text := dashboardText(screen)
	if strings.ContainsRune(text, '\x1b') || strings.Contains(text, "private-secret") || strings.Contains(text, "private-condition") || strings.Contains(text, "private-node-condition") || !strings.Contains(text, `visible\x1b[2J [red]literal[-]`) {
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
	snapshot.siteStages[0].states[0] = "observed"
	snapshot.tenantStages[0].states[0] = "observed"
	if got := d.snapshot(); got.activity[0] == "mutated" || got.phases[0].state == "complete" || got.siteStages[0].states[0] == "observed" || got.tenantStages[0].states[0] == "observed" {
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

func TestDashboardCancellationDoesNotShowRunningTenantBudget(t *testing.T) {
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: "tenant-deletion-wait"})
	screen := dashboardScreen(t, 140)
	d.draw(screen, time.Now(), true)
	text := dashboardText(screen)
	if !strings.Contains(text, "Wait for NKE to confirm tenant deletion") || !strings.Contains(text, "Cleanup completion is not yet observed") || strings.Contains(text, "Tenant budget:") || strings.Contains(text, "Waiting for NKE to publish tenant kubeconfig") {
		t.Fatalf("canceling view still implies active provisioning:\n%s", text)
	}
}

func TestDashboardCleanupShowsOwningStep(t *testing.T) {
	for _, name := range []string{"tenant-cleanup", "tenant-diagnostics", "tenant-delete", "tenant-deletion-wait"} {
		t.Run(name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: name})
			index, label := dashboardOperation(name)
			got := d.snapshot()
			if index != 5 || got.phases[5].state != "running" || got.current != label || got.currentStarted.IsZero() {
				t.Fatalf("cleanup step lost: %+v", got)
			}
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFailed, Name: name})
			if d.snapshot().phases[5].state != "failed" {
				t.Fatal("cleanup failure was hidden")
			}
		})
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
