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

func TestDashboardTenantReadinessIsNotWorkloadCompletion(t *testing.T) {
	for _, tc := range []struct{ name, label string }{
		{"tenant-cni-readiness", "Wait for tenant Calico node agents before DNS checks"},
		{"tenant-dns-readiness", "Wait for CoreDNS and node-local DNS rollout before the smoke test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseStarted, Name: tc.name})
			s := d.snapshot()
			if s.phases[4].state != "running" || s.current != tc.label || s.tenantStages[3].states[0] == "observed" {
				t.Fatal("readiness admission hidden or incorrectly advanced workload completion")
			}
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFinished, Name: tc.name})
			if d.snapshot().phases[4].state == "complete" || d.snapshot().tenantStages[3].states[0] == "observed" {
				t.Fatal("readiness rollout became E2E workload/DNS completion")
			}
		})
	}
}

func TestDashboardPrivateRolloutNamesDoNotAdvanceReadiness(t *testing.T) {
	for _, name := range []string{"tenant-cni-calico-rollout", "tenant-dns-coredns-rollout", "tenant-dns-node-local-rollout"} {
		for _, kind := range []string{nkedemo.PhaseStarted, nkedemo.PhaseFinished, nkedemo.PhaseFailed} {
			t.Run(fmt.Sprintf("%s/%s", name, kind), func(t *testing.T) {
				d := newDashboard()
				before := d.snapshot()
				d.Report(nkedemo.ProgressEvent{Kind: kind, Name: name})
				after := d.snapshot()
				if after.phases != before.phases || after.tenantStages != before.tenantStages || after.current != before.current || len(after.activity) != len(before.activity) {
					t.Fatal("private rollout name became a public readiness observation")
				}
			})
		}
	}
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
			for _, wanted := range []string{"UNQUALIFIED", "not a signed Release", "Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup", "Observed state", "Waiting for both tenant nodes Ready", "Activity", "2/2 registered", "1/2 Ready", "Provisioning=True", "Provisioned=False", "State unchanged", "last wait report", "20m", "remaining", "/tmp/private-build.log", "PgUp/PgDn"} {
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
			for _, wanted := range []string{"Observed state", "Waiting for both tenant nodes Ready", "Control plane", "Worker", "1/2 registered", "0/2 Ready", "Provisioning=True", "Provisioned=False", "State unchanged", "last wait report", "20m", "Timeout remaining", "not an ETA", "latest public activity", "Workload/DNS", "Cleanup"} {
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

func TestDashboardEscapeNeverCancelsWork(t *testing.T) {
	for phase, name := range []string{"prepare", "artifacts", "site", "tenant", "workload", "cleanup"} {
		for _, details := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/details=%t", name, details), func(t *testing.T) {
				d := newDashboard()
				d.phases[phase].state = "running"
				d.buildDetails = details
				before := d.snapshot()
				for range 2 {
					if d.input(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) {
						t.Fatal("Escape canceled owned work")
					}
					if got := d.snapshot(); got.buildDetails || got.confirmCancel || got.phases != before.phases {
						t.Fatal("Escape changed workflow state or left details open")
					}
				}
			})
		}
	}
}

func TestDashboardForceConfirmationFooter(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.forceAvailable = true
			d.recoveryWork = "/tmp/owned-demo"
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 24)
			d.draw(screen, time.Now(), false)
			if strings.Contains(dashboardText(screen), "FORCE EXIT") {
				t.Fatal("force exit offered before cancellation")
			}
			d.draw(screen, time.Now(), true)
			for _, wanted := range []string{"CANCELING", "[ f ] FORCE EXIT", "confirmation required", "Owned processes, VMs, containers and network changes may remain.", "Recovery workspace: /tmp/owned-demo"} {
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
			wantLines := 0
			if tc.changed {
				wantLines = 1
			}
			if got := len(after.activity) - len(before.activity); got != wantLines {
				t.Fatalf("activity added %d lines for state change=%v", got, tc.changed)
			}
		})
	}
}

func TestDashboardTenantActivityHeartbeat(t *testing.T) {
	for _, stage := range []string{"tenant-kubeconfig", "tenant-api", "tenant-nodes", "tenant-workload"} {
		for _, gap := range []time.Duration{29 * time.Second, 31 * time.Second} {
			t.Run(fmt.Sprintf("%s/%s", stage, gap), func(t *testing.T) {
				d := newDashboard()
				event := nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: stage, RegisteredNodes: 1, Provisioning: nkedemo.TenantConditionTrue, Elapsed: 9 * time.Minute, Remaining: time.Hour}
				d.Report(event)
				d.tenantChangedAt = d.tenantChangedAt.Add(-2 * time.Minute)
				d.observedAt = d.observedAt.Add(-12 * time.Second)
				d.tenantActivityAt = d.tenantActivityAt.Add(-gap)
				before, lastActivity := d.snapshot(), d.tenantActivityAt
				event.Elapsed = 9*time.Minute + 21*time.Second
				event.Status = "private-secret\x1b[2J"
				d.Report(event)
				after := d.snapshot()
				wantLines := 0
				if gap >= 30*time.Second {
					wantLines = 1
					if got := after.activity[len(after.activity)-1]; got != "Tenant wait +9m21s: latest reported conditions remain unchanged." {
						t.Fatalf("unexpected heartbeat: %q", got)
					}
				}
				if len(after.activity)-len(before.activity) != wantLines || d.tenantActivityAt.After(lastActivity) != (wantLines == 1) {
					t.Fatal("unchanged reports did not respect the activity throttle")
				}
				if !after.tenantChangedAt.Equal(before.tenantChangedAt) || !after.observedAt.After(before.observedAt) || after.phases != before.phases || after.tenantStages != before.tenantStages {
					t.Fatal("activity heartbeat changed reported state or completion")
				}
			})
		}
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
			for _, wanted := range []string{"State unchanged: 10m0s", "last wait report: 5m0s ago", "Tenant budget", "remaining", "1/2 registered", "0/2 Ready", "Activity"} {
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
		{"owner termination is not readiness failure", []nkedemo.ProgressEvent{{Kind: nkedemo.TenantWaiting, Name: "tenant-api"}, {Kind: nkedemo.DeploymentFailed, Name: "tenant-provisioning"}}, [4]string{"pending", "stopped", "pending", "pending"}},
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
			name: "observed substep is not owning site completion",
			events: []nkedemo.ProgressEvent{
				{Kind: nkedemo.PhaseFinished, Name: "create-management"},
				{Kind: nkedemo.DeploymentFailed, Name: "local-demo"},
			},
			states: [6]string{"pending", "pending", "stopped", "pending", "pending", "pending"},
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

func TestDashboardTenantTerminationStopsUnfinishedChecks(t *testing.T) {
	for _, name := range []string{"tenant-kubeconfig", "tenant-api", "tenant-nodes", "tenant-workload"} {
		t.Run(name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: name})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.DeploymentFailed, Name: "tenant-provisioning"})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.DeploymentFailed, Name: "local-demo"})
			stopped := 0
			for _, stage := range d.snapshot().tenantStages {
				state, _, _ := stage.status(1)
				if state == "failed" || state == "running" {
					t.Fatalf("owner termination falsely attributed check state %s", state)
				}
				if state == "stopped" {
					stopped++
				}
			}
			if stopped != 1 {
				t.Fatalf("stopped checks=%d want=1", stopped)
			}
		})
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
				d.Report(dashboardGuestFixture())
			}
		})
	}
	for range 10 {
		d.draw(screen, time.Now(), false)
		_ = d.snapshot()
	}
	joined.Wait()
}

func dashboardGuestFixture() nkedemo.ProgressEvent {
	return nkedemo.ProgressEvent{Kind: nkedemo.TenantGuests, Guests: [2]nkedemo.TenantGuestObservation{
		{Role: nkedemo.TenantGuestControlPlane, Available: true, Domain: nkedemo.TenantDomainRunning, ProviderReady: nkedemo.TenantConditionTrue, ConsoleKnown: true, ConsoleBytes: 12 * 1024, ConsoleChanged: true, ConsoleLastChange: 2 * time.Minute},
		{Role: nkedemo.TenantGuestWorker, Available: true, Domain: nkedemo.TenantDomainPaused, ProviderReady: nkedemo.TenantConditionFalse, ConsoleKnown: true, ConsoleBytes: 8 * 1024},
	}}
}

func TestDashboardTenantGuestsDoNotAdvanceReadiness(t *testing.T) {
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
	d.tenantChangedAt = d.tenantChangedAt.Add(-10 * time.Minute)
	before := d.snapshot()
	d.Report(dashboardGuestFixture())
	after := d.snapshot()
	if after.guestsObservedAt.IsZero() || after.guests != dashboardGuestFixture().Guests {
		t.Fatal("valid guest observations not retained")
	}
	if after.phases != before.phases || after.tenantStages != before.tenantStages || !after.observedAt.Equal(before.observedAt) || !after.tenantChangedAt.Equal(before.tenantChangedAt) || after.nodesObserved || after.registered != 0 || after.ready != 0 {
		t.Fatal("guest activity advanced tenant/node readiness or progress clocks")
	}
	copy := after
	copy.guests[0].Domain = nkedemo.TenantDomainCrashed
	if d.snapshot().guests[0].Domain != nkedemo.TenantDomainRunning {
		t.Fatal("snapshot shares guest telemetry state")
	}
}

func TestDashboardTenantGuestLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {91, 24}, {100, 24}, {111, 24}, {112, 24}, {120, 24}, {80, 42}, {140, 42}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-api", Remaining: time.Hour})
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioning: nkedemo.TenantConditionTrue, Provisioned: nkedemo.TenantConditionFalse, Remaining: time.Hour})
			d.Report(dashboardGuestFixture())
			_, _ = io.WriteString(d, "latest public activity\n")
			s := d.snapshot()
			screen := dashboardScreen(t, size[0])
			screen.SetSize(size[0], size[1])
			d.draw(screen, s.guestsObservedAt.Add(time.Minute), false)
			text := dashboardText(screen)
			for _, want := range []string{"Waiting for both tenant nodes Ready", "Checks [##--] 2/4", "Control plane", "VM running", "Infra True", "console 12 KiB; growth age 2m0s", "Worker", "VM paused", "Infra False", "console 8 KiB; baseline", "not Kubernetes readiness", "1/2 registered", "0/2 Ready", "Guest snapshot: 1m0s old", "growth ages measured at snapshot", "Timeout remaining", "not an ETA", "latest public activity", "Cleanup"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q at %v:\n%s", want, size, text)
				}
			}
			if strings.Contains(text, "guest healthy") || strings.Contains(text, "stuck") || strings.Contains(text, "2/2 Ready") {
				t.Fatal("infrastructure or observation age fabricated tenant health")
			}
		})
	}
}

func TestDashboardTenantGuestUnknownAndStale(t *testing.T) {
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
	event := nkedemo.ProgressEvent{Kind: nkedemo.TenantGuests, Guests: [2]nkedemo.TenantGuestObservation{{Role: nkedemo.TenantGuestControlPlane}, {Role: nkedemo.TenantGuestWorker}}}
	d.Report(event)
	s := d.snapshot()
	now := s.guestsObservedAt.Add(5 * time.Minute)
	screen := dashboardScreen(t, 140)
	d.draw(screen, now, false)
	text := dashboardText(screen)
	for _, want := range []string{"Waiting for tenant kubeconfig", "Checks [----] 0/4", "Control plane", "Worker", "VM unavailable", "guest observation unknown", "Ready unknown", "Guest snapshot: 5m0s old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing truthful unknown/freshness %q: %s", want, text)
		}
	}
	if strings.Contains(text, "stuck") || strings.Contains(text, "failed") {
		t.Fatal("unavailable metadata fabricated failure")
	}
}

func TestDashboardTenantGuestWorstWidthLayout(t *testing.T) {
	for _, state := range []nkedemo.TenantDomainState{nkedemo.TenantDomainUnknown, nkedemo.TenantDomainShutOff} {
		t.Run(string(state), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-nodes", Remaining: time.Hour})
			event := dashboardGuestFixture()
			for index := range event.Guests {
				event.Guests[index].Domain = state
				event.Guests[index].ProviderReady = nkedemo.TenantConditionUnknown
				event.Guests[index].ConsoleBytes = 1023 << 20
				event.Guests[index].ConsoleChanged = true
				event.Guests[index].ConsoleLastChange = 24*time.Hour - time.Second
			}
			d.Report(event)
			_, _ = io.WriteString(d, "latest public activity\n")
			s := d.snapshot()
			screen := dashboardScreen(t, 80)
			screen.SetSize(80, 24)
			d.draw(screen, s.guestsObservedAt, false)
			text := dashboardText(screen)
			for _, role := range []string{"Control plane", "Worker"} {
				row := fmt.Sprintf("%-13s VM %-8s Infra Unknown console 1023 MiB; growth age 23h59m59s", role, state)
				if !strings.Contains(text, row) {
					t.Fatalf("guest row clipped at 80x24: %q\n%s", row, text)
				}
			}
			for _, want := range []string{"○ Workload/DNS", "not Kubernetes readiness", "Guest snapshot", "growth ages measured at snapshot", "Timeout remaining", "not an ETA", "latest public activity", "Cleanup"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q at 80x24:\n%s", want, text)
				}
			}
		})
	}
}

func TestDashboardTenantGuestRejectsPrivateOrInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*nkedemo.ProgressEvent)
	}{
		{"role", func(e *nkedemo.ProgressEvent) { e.Guests[0].Role = "private-id" }},
		{"swapped roles", func(e *nkedemo.ProgressEvent) { e.Guests[0], e.Guests[1] = e.Guests[1], e.Guests[0] }},
		{"domain", func(e *nkedemo.ProgressEvent) { e.Guests[0].Domain = "private-status\x1b[2J" }},
		{"provider", func(e *nkedemo.ProgressEvent) { e.Guests[0].ProviderReady = "private-condition" }},
		{"failure", func(e *nkedemo.ProgressEvent) { e.Guests[0].ProvisioningFailure = "private-reason\x1b[2J" }},
		{"negative size", func(e *nkedemo.ProgressEvent) { e.Guests[0].ConsoleBytes = -1 }},
		{"negative age", func(e *nkedemo.ProgressEvent) { e.Guests[0].ConsoleLastChange = -time.Second }},
		{"unavailable has evidence", func(e *nkedemo.ProgressEvent) { e.Guests[0].Available = false }},
		{"unknown console has evidence", func(e *nkedemo.ProgressEvent) { e.Guests[0].ConsoleKnown = false }},
		{"baseline has change age", func(e *nkedemo.ProgressEvent) { e.Guests[0].ConsoleChanged = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(dashboardGuestFixture())
			before := d.snapshot()
			event := dashboardGuestFixture()
			tc.change(&event)
			d.Report(event)
			after := d.snapshot()
			if after.guests != before.guests || !after.guestsObservedAt.Equal(before.guestsObservedAt) || after.phases != before.phases {
				t.Fatal("invalid guest description modified model")
			}
		})
	}
}

func TestDashboardTenantGuestClosedStates(t *testing.T) {
	for _, state := range []nkedemo.TenantDomainState{nkedemo.TenantDomainUnknown, nkedemo.TenantDomainRunning, nkedemo.TenantDomainPaused, nkedemo.TenantDomainShutOff, nkedemo.TenantDomainCrashed} {
		t.Run(string(state), func(t *testing.T) {
			event := dashboardGuestFixture()
			event.Guests[0].Domain = state
			d := newDashboard()
			d.Report(event)
			if got := d.snapshot().guests[0].Domain; got != state || d.snapshot().phases[3].state != "pending" {
				t.Fatal("closed infrastructure state was lost or changed readiness")
			}
		})
	}
}

func TestDashboardTenantInstanceFailureIsVisibleWithoutVM(t *testing.T) {
	for index, role := range [...]string{"Control plane", "Worker"} {
		t.Run(role, func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
			before := d.snapshot()
			event := nkedemo.ProgressEvent{Kind: nkedemo.TenantGuests, Guests: [2]nkedemo.TenantGuestObservation{{Role: nkedemo.TenantGuestControlPlane, Domain: nkedemo.TenantDomainUnknown}, {Role: nkedemo.TenantGuestWorker, Domain: nkedemo.TenantDomainUnknown}}}
			event.Guests[index].ProvisioningFailure = nkedemo.TenantInstanceCreateFailed
			d.Report(event)
			screen := dashboardScreen(t, 80)
			screen.SetSize(80, 24)
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, want := range []string{role + ": instance lookup/create failed (provider report)", "instance lookup/create failed; VM observation unknown", "Checks [----] 0/4", "Ready unknown", "not Kubernetes readiness"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing provider failure %q:\n%s", want, text)
				}
			}
			after := d.snapshot()
			if after.phases != before.phases || after.tenantStages != before.tenantStages || !after.tenantChangedAt.Equal(before.tenantChangedAt) {
				t.Fatal("descriptive provider failure changed owning lifecycle")
			}
		})
	}
}

func TestDashboardGuestActivityDistinguishesGrowthFromSnapshot(t *testing.T) {
	d := newDashboard()
	event := dashboardGuestFixture()
	d.Report(event)
	before := d.snapshot()
	event.Guests[0].ConsoleLastChange += time.Minute
	d.Report(event)
	if len(d.snapshot().activity) != len(before.activity) {
		t.Fatal("an aging snapshot was presented as guest activity")
	}
	event.Guests[0].ConsoleBytes += 1024
	event.Guests[0].ConsoleLastChange = 0
	d.Report(event)
	after := d.snapshot()
	if len(after.activity) != len(before.activity)+1 || after.activity[len(after.activity)-1] != "Control plane: console bytes grew; metadata activity is not guest health" {
		t.Fatal("positive console growth was not separately described")
	}
	if after.phases != before.phases || !after.tenantChangedAt.Equal(before.tenantChangedAt) || !after.observedAt.Equal(before.observedAt) {
		t.Fatal("console growth changed readiness or owner progress clocks")
	}
	event.Guests[0].ConsoleBytes, event.Guests[0].ConsoleChanged, event.Guests[0].ConsoleLastChange = 0, false, 0
	d.Report(event)
	if len(d.snapshot().activity) != len(after.activity) {
		t.Fatal("console reset was presented as progress")
	}
}

func TestDashboardGuestPresentationBounds(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		bytes      int64
	}{
		{"empty", "0 KiB", 0},
		{"kilobytes", "12 KiB", 12 * 1024},
		{"megabytes", "1 MiB", 1 << 20},
		{"gigabytes", "1 GiB", 1 << 30},
		{"maximum", "8589934591 GiB", 1<<63 - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashboardConsoleSize(tc.bytes); got != tc.want {
				t.Fatalf("console size=%q want %q", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, want string
		age        time.Duration
	}{
		{"now", "0s", 0},
		{"seconds", "2m1s", 121 * time.Second},
		{"long", "over 24h", 24 * time.Hour},
		{"maximum age", "over 24h", 1<<63 - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashboardGuestAge(tc.age); got != tc.want {
				t.Fatalf("console age=%q want %q", got, tc.want)
			}
		})
	}
}

func TestDashboardBuildLogSummary(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {80, 42}, {160, 42}} {
		width, height := size[0], size[1]
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := newDashboard()
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
			input := "[build +1s]   output: os [disk-image]: disk\n[build +1s]   output: chart [helm-oci]: chart\n[build +1s]   output: chart [helm-oci]: chart\n[build +1s] [dry-run] os/build-disk (build)\n[build +1s] [dry-run] chart/build (build)\n[build +1s] [dry-run] package/chart (package)\n[start] os/build-disk (build)\n[done] chart/build\n[cached] chart/build\n[start] other/build (acquire)\n[start] third/build (package)\n[start] fourth/build (build)\n"
			for _, b := range []byte(input) {
				_, _ = d.Write([]byte{b})
			}
			before := d.snapshot()
			_, _ = io.WriteString(d, "[start] os/build-disk (build)\n")
			if !d.snapshot().buildSteps[0].started.Equal(before.buildSteps[0].started) {
				t.Fatal("duplicate start reset the elapsed clock")
			}
			screen := dashboardScreen(t, width)
			screen.SetSize(width, height)
			d.draw(screen, before.buildSteps[0].started.Add(75*time.Second), false)
			text := dashboardText(screen)
			for _, want := range []string{"Required outputs 2", "1 done", "4 running", "1 queued", "Preview [######--------------]", "1/3 reported done/cached", "os/build-disk", "1m15s", "Last step change", "artifact verification still required", "ETA unknown"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q at width %d:\n%s", want, width, text)
				}
			}
			before.buildSteps[0].state = "done"
			if d.snapshot().buildSteps[0].state != "running" || d.snapshot().phases[1].state != "running" {
				t.Fatal("mutable snapshot or logs changed artifact authority")
			}
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneFailed, Name: "artifacts"})
			d.draw(screen, time.Now(), false)
			if strings.Contains(dashboardText(screen), "Artifact pipeline") {
				t.Fatal("failed artifact owner still shows running-step clocks")
			}
		})
	}
}

func TestDashboardBuildLogBounds(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"outside lane", "[dry-run] image/build (build)\n"},
		{"missing kind", "[dry-run] image/build\n"},
		{"unknown kind", "[dry-run] image/build (private-secret)\n"},
		{"terminal controls", "[start] image/\x1b[2J (build)\n"},
		{"markup", "[start] [red]image (build)\n"},
		{"embedded marker", "compiler says [start] image/build (build)\n"},
		{"overlong", "[start] " + strings.Repeat("a", 257) + " (build)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			if tc.name != "outside lane" {
				d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
			}
			_, _ = io.WriteString(d, tc.input)
			if len(d.snapshot().buildSteps) != 0 || d.snapshot().buildPreview {
				t.Fatal("unsupported text became a build listing")
			}
		})
	}
	d := newDashboard()
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
	for i := range dashboardSteps + 1 {
		_, _ = fmt.Fprintf(d, "[dry-run] image/build-%d (build)\n", i)
	}
	_, _ = io.WriteString(d, "[cached] image/build-0\n[start] image/build-0 (build)\n")
	s := d.snapshot()
	if len(s.buildSteps) != dashboardSteps || s.buildSteps[0].state != "done" || !strings.Contains(dashboardBuildRibbon(s, time.Now()), "counts incomplete") {
		t.Fatal("unbounded listing, terminal log state regressed or truncation hidden")
	}
}
