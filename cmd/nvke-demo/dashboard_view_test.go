// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: tc.name})
			s := d.snapshot()
			if s.phases[4].state != "running" || s.current != tc.label || s.tenantStages[3].state == "observed" {
				t.Fatal("readiness admission hidden or incorrectly advanced workload completion")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: tc.name})
			if d.snapshot().phases[4].state == "complete" || d.snapshot().tenantStages[3].state == "observed" {
				t.Fatal("readiness rollout became E2E workload/DNS completion")
			}
		})
	}
}

func TestDashboardPrivateRolloutNamesDoNotAdvanceReadiness(t *testing.T) {
	for _, name := range []string{"tenant-cni-calico-rollout", "tenant-dns-coredns-rollout", "tenant-dns-node-local-rollout"} {
		for _, kind := range []string{nvkedemo.PhaseStarted, nvkedemo.PhaseFinished, nvkedemo.PhaseFailed} {
			t.Run(fmt.Sprintf("%s/%s", name, kind), func(t *testing.T) {
				d := newDashboard()
				before := d.snapshot()
				d.Report(nvkedemo.ProgressEvent{Kind: kind, Name: name})
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 1, Provisioning: nvkedemo.TenantConditionTrue, Provisioned: nvkedemo.TenantConditionFalse, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
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
			if !strings.Contains(dashboardText(screen), "waiting for work owner") {
				t.Fatal("cancellation omitted actual work-owner wait")
			}
		})
	}
}

func TestDashboardShortLayoutPreservesLiveStatus(t *testing.T) {
	for _, height := range []int{24, 30} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioning: nvkedemo.TenantConditionTrue, Provisioned: nvkedemo.TenantConditionFalse, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
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
	if !strings.Contains(dashboardText(screen), "Cancel current work? Retention/cleanup follows the selected mode. y/N") {
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
		event   nvkedemo.ProgressEvent
		changed bool
		valid   bool
	}{
		{"heartbeat", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0}, false, true},
		{"ready count", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 1}, true, true},
		{"registered count", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 0}, true, true},
		{"stage", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-workload", RegisteredNodes: 1, ReadyNodes: 0}, true, true},
		{"condition", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioned: nvkedemo.TenantConditionTrue}, true, true},
		{"invalid condition", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioned: "private-condition"}, false, true},
		{"unknown", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "private-condition", RegisteredNodes: 1, ReadyNodes: 0}, false, false},
		{"invalid count", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 3, ReadyNodes: 0}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Remaining: time.Hour})
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
				event := nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: stage, RegisteredNodes: 1, Provisioning: nvkedemo.TenantConditionTrue, Elapsed: 9 * time.Minute, Remaining: time.Hour}
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Remaining: time.Hour})
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

func TestDashboardFillingSiteChecklist(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			active := len(dashboardSiteStages) - 2
			for _, stage := range dashboardSiteStages[:active] {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: stage.operation})
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: dashboardSiteStages[active].operation})
			s := d.snapshot()
			now := s.siteStages[active].started.Add(75 * time.Second)
			if checklist, _ := dashboardChecklist(s, now); !strings.Contains(checklist, "1m15s") {
				t.Fatalf("active substep elapsed missing: %s", checklist)
			}
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 42)
			d.draw(screen, now, false)
			text := dashboardText(screen)
			for _, want := range []string{fmt.Sprintf("Native site stages · %d/%d checked", active, len(dashboardSiteStages)), dashboardSiteStages[active].label, "1m15s", "Tenant stages · 0/4 checked", "Tenant kubeconfig", "Tenant API query", "Nodes Ready", "Workload and DNS", "pending", "ETA unknown", "Activity"} {
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
		events []nvkedemo.ProgressEvent
		states [4]string
	}{
		{"waiting", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig"}}, [4]string{"running", "pending", "pending", "pending"}},
		{"api", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig"}, {Kind: nvkedemo.TenantWaiting, Name: "tenant-api"}}, [4]string{"observed", "running", "pending", "pending"}},
		{"ready only", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 2}}, [4]string{"pending", "pending", "observed", "pending"}},
		{"provisioned is not node readiness", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", Provisioned: nvkedemo.TenantConditionTrue}}, [4]string{"pending", "pending", "running", "pending"}},
		{"workload owner", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-workload", RegisteredNodes: 2, ReadyNodes: 2}, {Kind: nvkedemo.DeploymentFinished, Name: "tenant-provisioning"}}, [4]string{"pending", "pending", "pending", "observed"}},
		{"owner termination is not readiness failure", []nvkedemo.ProgressEvent{{Kind: nvkedemo.TenantWaiting, Name: "tenant-api"}, {Kind: nvkedemo.DeploymentFailed, Name: "tenant-provisioning"}}, [4]string{"pending", "stopped", "pending", "pending"}},
		{"missing owner observations", []nvkedemo.ProgressEvent{{Kind: nvkedemo.DeploymentFinished, Name: "tenant-provisioning"}}, [4]string{"pending", "pending", "pending", "pending"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			for _, event := range tc.events {
				d.Report(event)
			}
			for index, stage := range d.snapshot().tenantStages {
				if state := stage.status(); state != tc.states[index] {
					t.Fatalf("stage %d=%s want %s", index, state, tc.states[index])
				}
			}
		})
	}
}

func TestDashboardObservationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event nvkedemo.ProgressEvent
		phase int
		state string
	}{
		{"artifact complete", nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "artifacts"}, 1, "complete"},
		{"build command is not lane completion", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "build-all"}, 1, "observed"},
		{"installer substep is not whole preparation", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "installer-fetch"}, 0, "observed"},
		{"installer owner completed", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "installer-preparation"}, 0, "complete"},
		{"network restore is not whole cleanup", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "network-cleanup"}, 5, "observed"},
		{"site substep is not whole site completion", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "host-site-capi"}, 2, "running"},
		{"site readiness failure stays visible", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "native-site-guest"}, 2, "failed"},
		{"old Kind operation is ignored", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "create-management"}, 2, "pending"},
		{"wrong site event family", nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFinished, Name: "host-site-capi"}, 2, "pending"},
		{"old demo completion is ignored", nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFinished, Name: "local-demo"}, 2, "pending"},
		{"obsolete hold event is ignored", nvkedemo.ProgressEvent{Kind: "native-retained", Status: "private-status"}, 2, "pending"},
		{"wrong event family", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "artifacts"}, 1, "pending"},
		{"wrong conformance event family", nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "native-conformance"}, dashboardConformancePhase, "pending"},
		{"unknown event", nvkedemo.ProgressEvent{Kind: "caller-success", Name: "artifacts"}, 1, "pending"},
		{"private name", nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "private-node-name"}, 3, "pending"},
		{"ready nodes are not workload success", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 2}, 4, "pending"},
		{"invalid node count", nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 3, ReadyNodes: 2}, 3, "pending"},
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
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "network-cleanup"})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "registry-cleanup"})
	if d.snapshot().phases[5].state != "failed" {
		t.Fatal("later successful substep masked cleanup failure")
	}
}

func TestDashboardBuildLogRecognizesNeutralAndHistoricalPrefixes(t *testing.T) {
	for _, prefix := range []string{"[log +1s] ", "[build +1s] "} {
		t.Run(prefix, func(t *testing.T) {
			d := newDashboard()
			d.phases[1] = dashboardPhase{state: "running"}
			_, _ = io.WriteString(d, prefix+"  output: os [disk-image]: disk\n"+prefix+"[start] os/build-disk (build)\n")
			s := d.snapshot()
			if len(s.buildOutputs) != 1 || s.buildOutputs[0] != "os" || len(s.buildSteps) != 1 || s.buildSteps[0].id != "os/build-disk" || s.phases[1].state != "running" {
				t.Fatal("timestamp label changed descriptive graph projection or lifecycle state")
			}
		})
	}
}

func TestDashboardStoppedPhaseHasDistinctColor(t *testing.T) {
	for _, width := range []int{80, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			d := newDashboard()
			d.phases[2] = dashboardPhase{state: "observed"}
			d.phases[3] = dashboardPhase{state: "stopped"}
			d.tenantStages[0].state = "stopped"
			if checklist, _ := dashboardChecklist(d.snapshot(), time.Now()); !strings.Contains(checklist, "[lightsalmon]■") {
				t.Fatal("stopped checkpoint reused the observation color")
			}
			screen := dashboardScreen(t, width)
			d.draw(screen, time.Now(), false)
			screen.Show()
			cells, _, _ := screen.GetContents()
			x, y, paneWidth, paneHeight := d.phasesView.GetRect()
			color := func(label string) tcell.Color {
				letters := []rune(label)
				for start := 0; start+len(letters) <= len(cells); start++ {
					column, row := start%width, start/width
					if column < x || column+len(letters) > x+paneWidth || row < y || row >= y+paneHeight {
						continue
					}
					matches := true
					for index, letter := range letters {
						if len(cells[start+index].Runes) == 0 || cells[start+index].Runes[0] != letter {
							matches = false
							break
						}
					}
					if matches {
						foreground, _, _ := cells[start].Style.Decompose()
						return foreground
					}
				}
				t.Fatalf("phase label %q not rendered", label)
				return tcell.ColorDefault
			}
			if got := color("Site"); got != tcell.ColorLightSteelBlue {
				t.Fatalf("observed phase color=%v", got)
			}
			if got := color("Tenant"); got != tcell.ColorLightSalmon {
				t.Fatalf("stopped phase color=%v", got)
			}
		})
	}
}

func TestDashboardSanitizationAndPrivacy(t *testing.T) {
	d := newDashboard()
	_, _ = io.WriteString(d, "visible\x1b[2J [red]literal[-]\n")
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", Status: "private-secret", RegisteredNodes: 1, ReadyNodes: 0})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "private-node-condition", Status: "private-secret"})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", Provisioning: "private-condition", Provisioned: "private-secret", RegisteredNodes: 1})
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
	snapshot.siteStages[0].state = "observed"
	snapshot.tenantStages[0].state = "observed"
	if got := d.snapshot(); got.activity[0] == "mutated" || got.phases[0].state == "complete" || got.siteStages[0].state == "observed" || got.tenantStages[0].state == "observed" {
		t.Fatal("snapshot shares mutable model state")
	}
}

func TestDashboardBudgetAndNavigation(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-api", Elapsed: time.Minute, Remaining: time.Minute})
	before := d.snapshot()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", Remaining: 20 * time.Minute})
	if d.snapshot().deadline.After(before.deadline) {
		t.Fatal("remaining budget increased")
	}
	for _, test := range []struct {
		key           tcell.Key
		offset, focus int
	}{
		{tcell.KeyTab, 0, 1},
		{tcell.KeyPgUp, 10, 2},
		{tcell.KeyPgDn, 0, 2},
		{tcell.KeyPgUp, 10, 2},
		{tcell.KeyEnd, 0, 2},
	} {
		if d.input(tcell.NewEventKey(test.key, 0, tcell.ModNone)) {
			t.Fatal("navigation requested cancellation")
		}
		if got := d.snapshot(); got.offset != test.offset || got.focus != test.focus {
			t.Fatalf("key %v: offset=%d focus=%d, want %d/%d", test.key, got.offset, got.focus, test.offset, test.focus)
		}
	}
	d.input(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	d.input(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if got := d.snapshot(); got.offset != 0 || got.phases != before.phases {
		t.Fatal("navigation changed observed state")
	}
}

func TestDashboardPagingUsesCurrentScrollModel(t *testing.T) {
	for _, test := range []struct {
		name    string
		details bool
		up      int
	}{
		{"activity scrolls back from live output", false, 30},
		{"details scrolls toward the top", true, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			d.offset, d.buildDetails = 20, test.details
			before := d.snapshot()
			for _, step := range []struct {
				key    tcell.Key
				offset int
			}{{tcell.KeyPgUp, test.up}, {tcell.KeyPgDn, 20}, {tcell.KeyEnd, 0}} {
				if d.input(tcell.NewEventKey(step.key, 0, tcell.ModNone)) {
					t.Fatal("paging requested cancellation")
				}
				if got := d.snapshot(); got.offset != step.offset || got.phases != before.phases || got.confirmCancel {
					t.Fatalf("key %v changed work or wrong scroll offset: %+v", step.key, got)
				}
			}
			if d.snapshot().buildDetails {
				t.Fatal("End failed to return to live activity")
			}
		})
	}
}

func TestDashboardCancellationDoesNotShowRunningTenantBudget(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "tenant-deletion-wait"})
	screen := dashboardScreen(t, 140)
	d.draw(screen, time.Now(), true)
	text := dashboardText(screen)
	if !strings.Contains(text, "Wait for NVKE to confirm tenant deletion") || !strings.Contains(text, "Cleanup completion is not yet observed") || strings.Contains(text, "Tenant budget:") || strings.Contains(text, "Waiting for NVKE to publish tenant kubeconfig") {
		t.Fatalf("canceling view still implies active provisioning:\n%s", text)
	}
}

func TestDashboardTenantTerminationStopsUnfinishedChecks(t *testing.T) {
	for _, name := range []string{"tenant-kubeconfig", "tenant-api", "tenant-nodes", "tenant-workload"} {
		t.Run(name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: name})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFailed, Name: "tenant-provisioning"})
			stopped := 0
			for _, stage := range d.snapshot().tenantStages {
				state := stage.status()
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: name})
			index, label := dashboardOperation(name)
			got := d.snapshot()
			if index != 5 || got.phases[5].state != "running" || got.current != label || got.currentStarted.IsZero() {
				t.Fatalf("cleanup step lost: %+v", got)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: name})
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
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0})
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

func dashboardGuestFixture() nvkedemo.ProgressEvent {
	return nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Name: "native", Guests: [3]nvkedemo.TenantGuestObservation{
		{Role: nvkedemo.TenantGuestControlPlane, Available: true, Domain: nvkedemo.TenantDomainRunning, InstanceState: nvkedemo.TenantInstanceReady, ProviderReady: nvkedemo.TenantConditionTrue, ConsoleKnown: true, ConsoleBytes: 12 * 1024, ConsoleChanged: true, ConsoleLastChange: 2 * time.Minute},
		{Role: nvkedemo.TenantGuestWorker, Available: true, Domain: nvkedemo.TenantDomainPaused, InstanceState: nvkedemo.TenantInstanceReady, ProviderReady: nvkedemo.TenantConditionFalse, ConsoleKnown: true, ConsoleBytes: 8 * 1024},
	}}
}

func TestDashboardTenantGuestsDoNotAdvanceReadiness(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
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
	copy.guests[0].Domain = nvkedemo.TenantDomainCrashed
	if d.snapshot().guests[0].Domain != nvkedemo.TenantDomainRunning {
		t.Fatal("snapshot shares guest telemetry state")
	}
}

func TestDashboardTenantGuestLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {91, 24}, {100, 24}, {111, 24}, {112, 24}, {120, 24}, {80, 42}, {140, 42}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-api", Remaining: time.Hour})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 1, ReadyNodes: 0, Provisioning: nvkedemo.TenantConditionTrue, Provisioned: nvkedemo.TenantConditionFalse, Remaining: time.Hour})
			d.Report(dashboardGuestFixture())
			_, _ = io.WriteString(d, "latest public activity\n")
			s := d.snapshot()
			screen := dashboardScreen(t, size[0])
			screen.SetSize(size[0], size[1])
			d.draw(screen, s.guestsObservedAt.Add(time.Minute), false)
			text := dashboardText(screen)
			for _, want := range []string{"Waiting for both tenant nodes Ready", "Checks [##--] 2/4", "Control plane", "NICo Ready", "VM running", "Infra Ready=True", "console 12 KiB", "Worker", "VM paused", "Infra Ready=False", "console 8 KiB", "Kubernetes node readiness is reported separately", "1/2 registered", "0/2 Ready", "Provider/VM snapshot: 1m0s old", "unknown fields remain unobserved", "Timeout remaining", "not an ETA", "latest public activity", "Cleanup"} {
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
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
	event := nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Guests: [3]nvkedemo.TenantGuestObservation{{Role: nvkedemo.TenantGuestControlPlane}, {Role: nvkedemo.TenantGuestWorker}}}
	d.Report(event)
	s := d.snapshot()
	now := s.guestsObservedAt.Add(5 * time.Minute)
	screen := dashboardScreen(t, 140)
	d.draw(screen, now, false)
	text := dashboardText(screen)
	for _, want := range []string{"Waiting for tenant kubeconfig", "Checks [----] 0/4", "Control plane", "Worker", "VM state unobserved", "provider observation unavailable", "Ready unknown", "Provider/VM snapshot: 5m0s old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing truthful unknown/freshness %q: %s", want, text)
		}
	}
	if strings.Contains(text, "stuck") || strings.Contains(text, "failed") {
		t.Fatal("unavailable metadata fabricated failure")
	}
}

func TestDashboardTenantGuestWorstWidthLayout(t *testing.T) {
	for _, state := range []nvkedemo.TenantDomainState{nvkedemo.TenantDomainUnknown, nvkedemo.TenantDomainShutOff} {
		t.Run(string(state), func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", Remaining: time.Hour})
			event := dashboardGuestFixture()
			for index := range event.Guests[:2] {
				event.Guests[index].Domain = state
				event.Guests[index].ProviderReady = nvkedemo.TenantConditionUnknown
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
				vm := string(state)
				if state == nvkedemo.TenantDomainUnknown {
					vm = "unobserved"
				}
				row := fmt.Sprintf("%-13s NICo Ready Infra Ready=Unknown; VM %s; console 1023 MiB", role, vm)
				if !strings.Contains(text, row) {
					t.Fatalf("guest row clipped at 80x24: %q\n%s", row, text)
				}
			}
			for _, want := range []string{"○ Workload/DNS", "Kubernetes node readiness is reported separately", "Provider/VM snapshot", "unknown fields remain unobserved", "Timeout remaining", "not an ETA", "latest public activity", "Cleanup"} {
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
		change func(*nvkedemo.ProgressEvent)
	}{
		{"role", func(e *nvkedemo.ProgressEvent) { e.Guests[0].Role = "private-id" }},
		{"swapped roles", func(e *nvkedemo.ProgressEvent) { e.Guests[0], e.Guests[1] = e.Guests[1], e.Guests[0] }},
		{"domain", func(e *nvkedemo.ProgressEvent) { e.Guests[0].Domain = "private-status\x1b[2J" }},
		{"provider", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ProviderReady = "private-condition" }},
		{"failure", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ProvisioningFailure = "private-reason\x1b[2J" }},
		{"negative size", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleBytes = -1 }},
		{"negative age", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleLastChange = -time.Second }},
		{"unavailable has evidence", func(e *nvkedemo.ProgressEvent) { e.Guests[0].Available = false }},
		{"unknown console has evidence", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleKnown = false }},
		{"baseline has change age", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleChanged = false }},
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
	for _, state := range []nvkedemo.TenantDomainState{nvkedemo.TenantDomainUnknown, nvkedemo.TenantDomainRunning, nvkedemo.TenantDomainPaused, nvkedemo.TenantDomainShutOff, nvkedemo.TenantDomainCrashed} {
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Remaining: time.Hour})
			before := d.snapshot()
			event := nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Guests: [3]nvkedemo.TenantGuestObservation{{Role: nvkedemo.TenantGuestControlPlane, Domain: nvkedemo.TenantDomainUnknown}, {Role: nvkedemo.TenantGuestWorker, Domain: nvkedemo.TenantDomainUnknown}}}
			event.Guests[index].ProvisioningFailure = nvkedemo.TenantInstanceCreateFailed
			d.Report(event)
			screen := dashboardScreen(t, 80)
			screen.SetSize(80, 24)
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			for _, want := range []string{role + ": instance lookup/create failed (provider report)", "instance lookup/create failed; VM observation unknown", "Checks [----] 0/4", "Ready unknown", "Kubernetes node readiness is reported separately"} {
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
}

func TestDashboardBuildLogSummary(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {80, 42}, {160, 42}} {
		width, height := size[0], size[1]
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
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
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFailed, Name: "artifacts"})
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
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			}
			_, _ = io.WriteString(d, tc.input)
			if len(d.snapshot().buildSteps) != 0 || d.snapshot().buildPreview {
				t.Fatal("unsupported text became a build listing")
			}
		})
	}
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	for i := range dashboardSteps + 1 {
		_, _ = fmt.Fprintf(d, "[dry-run] image/build-%d (build)\n", i)
	}
	_, _ = io.WriteString(d, "[cached] image/build-0\n[start] image/build-0 (build)\n")
	s := d.snapshot()
	if len(s.buildSteps) != dashboardSteps || s.buildSteps[0].state != "done" || !strings.Contains(dashboardBuildRibbon(s, time.Now()), "counts incomplete") {
		t.Fatal("unbounded listing, terminal log state regressed or truncation hidden")
	}
}

func TestDashboardConformanceCountsEveryRequestedNode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count       int
		conformance bool
	}{
		{"one worker", 2, false},
		{"two workers without suite", 3, false},
		{"two workers with suite", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := tc.count
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: tc.conformance, ExpectedNodes: count})
			if snapshot := d.snapshot(); snapshot.expectedNodes != count || snapshot.conformanceSelected != tc.conformance {
				t.Fatal("deployment topology changed suite selection", snapshot.expectedNodes, snapshot.conformanceSelected)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", ExpectedNodes: count, RegisteredNodes: count, ReadyNodes: count - 1, Remaining: time.Hour})
			state := d.snapshot().tenantStages[2].status()
			if state != "running" {
				t.Fatal("missing requested worker was accepted")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", ExpectedNodes: count, RegisteredNodes: count, ReadyNodes: count, Remaining: time.Hour})
			snapshot := d.snapshot()
			state = snapshot.tenantStages[2].status()
			if state != "observed" || snapshot.ready != count || snapshot.expectedNodes != count {
				t.Fatal("lost complete node observation")
			}
			screen := dashboardScreen(t, 160)
			screen.SetSize(160, 48)
			d.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			if !strings.Contains(text, fmt.Sprintf("%d/%d Ready", count, count)) {
				t.Fatalf("wrong node denominator: %s", text)
			}
			if count == 3 && (!strings.Contains(text, "Worker 1") || !strings.Contains(text, "Worker 2")) {
				t.Fatalf("worker display is incomplete: %s", text)
			}
		})
	}
}

func TestDashboardNativeChecklistUsesIndividualOwnerBoundaries(t *testing.T) {
	for selected, stage := range dashboardSiteStages {
		t.Run(stage.operation, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			// The former coarse event must not complete missing native stages.
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-05-install-site-controller"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: stage.operation})
			if state := d.snapshot().siteStages[selected].status(); state != "running" {
				t.Fatal("actual stage start missing", state)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: stage.operation})
			snapshot := d.snapshot()
			for index, row := range snapshot.siteStages {
				state := row.status()
				if (state == "observed") != (index == selected) {
					t.Fatalf("stage %d inferred another operation's result: %s", index, state)
				}
			}
			checklist, _ := dashboardChecklist(snapshot, time.Now())
			for _, forbidden := range []string{"Create Kind cluster", "Registry reachable", "Secure and register"} {
				if strings.Contains(checklist, forbidden) {
					t.Fatal("native view retained legacy stage", forbidden)
				}
			}
			if !strings.Contains(checklist, fmt.Sprintf("Native site stages · 1/%d checked", len(dashboardSiteStages))) || snapshot.phases[2].state == "complete" {
				t.Fatal("partial native stages incorrectly completed the site", checklist)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-ready"})
			if d.snapshot().phases[2].state != "complete" {
				t.Fatal("owning site's successful outcome not shown")
			}
		})
	}
}

func TestDashboardSiteFailureCannotBeOverwritten(t *testing.T) {
	for index, stage := range dashboardSiteStages {
		t.Run(stage.operation, func(t *testing.T) {
			d := newDashboard()
			_, _ = io.WriteString(d, "[done] "+stage.operation+"\n")
			if d.snapshot().siteStages[index].status() != "pending" {
				t.Fatal("log text invented an owned observation")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: stage.operation})
			started := d.snapshot().siteStages[index].started
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: stage.operation})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: stage.operation})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: stage.operation})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-ready"})
			snapshot := d.snapshot()
			if snapshot.siteStages[index].status() != "failed" || !snapshot.siteStages[index].started.Equal(started) || snapshot.phases[2].state != "failed" {
				t.Fatal("a later event hid failure or changed its original start")
			}
		})
	}
}

func TestDashboardConformanceOutcomesAndReset(t *testing.T) {
	for _, scenario := range []struct {
		previous, kind, state string
	}{
		{"", nvkedemo.PhaseStarted, "running"},
		{"", nvkedemo.PhaseFinished, "complete"},
		{"", nvkedemo.PhaseFailed, "failed"},
		{nvkedemo.PhaseFailed, nvkedemo.PhaseStarted, "running"},
		{nvkedemo.PhaseFinished, nvkedemo.PhaseStarted, "running"},
	} {
		t.Run(scenario.state+"/after-"+scenario.previous, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			if d.snapshot().phases[dashboardConformancePhase].state != "pending" {
				t.Fatal("requested conformance omitted before execution")
			}
			if scenario.previous != "" {
				d.Report(nvkedemo.ProgressEvent{Kind: scenario.previous, Name: "native-conformance"})
			}
			d.Report(nvkedemo.ProgressEvent{Kind: scenario.kind, Name: "native-conformance"})
			snapshot := d.snapshot()
			phases := dashboardNamedPhases(snapshot)
			if len(phases) != 7 || phases[5].name != "Conformance" || phases[5].phase.state != scenario.state || phases[6].name != "Cleanup" {
				t.Fatal("conformance/lifetime phase order or outcome lost", phases)
			}
			if snapshot.phases[5].state != "pending" {
				t.Fatal("suite outcome claimed cleanup", snapshot.phases[5])
			}
			if scenario.state == "running" && snapshot.currentStarted.IsZero() {
				t.Fatal("explicit suite start lost its observation clock")
			}
		})
	}
}

func TestDashboardNativeProviderStatusDoesNotClaimQEMULiveness(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", ExpectedNodes: 3, Remaining: time.Hour})
	guests := [3]nvkedemo.TenantGuestObservation{}
	for index := range guests {
		role := nvkedemo.TenantGuestWorker
		if index == 0 {
			role = nvkedemo.TenantGuestControlPlane
		}
		guests[index] = nvkedemo.TenantGuestObservation{Role: role, Available: true, Domain: nvkedemo.TenantDomainUnknown, ProviderReady: nvkedemo.TenantConditionTrue, InstanceState: nvkedemo.TenantInstanceReady}
	}
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Name: "native", ExpectedNodes: 3, Guests: guests})
	text := dashboardTenantSummary(d.snapshot(), time.Now())
	for _, want := range []string{"Worker 1", "Worker 2", "NICo Ready", "Infra Ready=True", "VM unobserved", "Kubernetes: registered unknown", "Provider/VM snapshot:"} {
		if !strings.Contains(text, want) {
			t.Fatal("native observation missing", want, text)
		}
	}
	if strings.Contains(text, "VM running") || strings.Contains(text, "VM unavailable") || d.snapshot().tenantStages[0].state != "running" {
		t.Fatal("provider observation claimed VM or tenant readiness", text)
	}
	bad := guests
	bad[0].InstanceState = "private-arbitrary-status"
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Guests: bad})
	if d.snapshot().guests != guests {
		t.Fatal("unmapped private provider status entered public model")
	}
}

func TestCheckedWriterForwardsDeploymentConfiguration(t *testing.T) {
	d := newDashboard()
	writer := &checkedWriter{out: d, observer: d}
	nativePresentation(writer, true)
	if !d.snapshot().conformanceSelected {
		t.Fatal("typed deployment configuration was dropped before dashboard")
	}
	var plain bytes.Buffer
	writer = &checkedWriter{out: &plain}
	writer.Report(nvkedemo.ProgressEvent{Kind: "native-retained", Status: "private-status"})
	if plain.Len() != 0 {
		t.Fatal("obsolete private lifetime status was exposed")
	}
}

func TestDashboardNativeSequentialTenantStartsWithFreshObservations(t *testing.T) {
	for _, outcome := range []string{nvkedemo.PhaseFinished, nvkedemo.PhaseFailed} {
		t.Run(outcome, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-ready"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentStarted, Name: "tenant-provisioning"})
			for _, stage := range []string{"tenant-kubeconfig", "tenant-api", "tenant-nodes", "tenant-workload"} {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: stage, ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3, Elapsed: time.Minute, Remaining: time.Hour})
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFinished, Name: "tenant-provisioning"})
			d.Report(nvkedemo.ProgressEvent{Kind: outcome, Name: "native-conformance"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "tenant-cleanup"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentStarted, Name: "tenant-provisioning"})
			snapshot := d.snapshot()
			if snapshot.phases[2].state != "complete" || snapshot.phases[3].state != "running" || snapshot.phases[4].state != "pending" || snapshot.phases[5].state != "pending" || snapshot.phases[dashboardConformancePhase].state != "pending" {
				t.Fatal("new tenant reused outcomes or reset the site", snapshot.phases)
			}
			if snapshot.nodesObserved || snapshot.registered != 0 || snapshot.ready != 0 || !snapshot.observedAt.IsZero() || !snapshot.deadline.IsZero() || !snapshot.guestsObservedAt.IsZero() {
				t.Fatal("new tenant reused old observations")
			}
			for _, stage := range snapshot.tenantStages {
				if state := stage.status(); state != "pending" {
					t.Fatal("previous tenant checkmark survived", state)
				}
			}
		})
	}
}

func TestTenantCloudInitProgressIsVisibleInBothPresenters(t *testing.T) {
	for _, mode := range []string{"plain", "dashboard"} {
		t.Run(mode, func(t *testing.T) {
			event := nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-cloud-init", ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3, Remaining: time.Hour}
			var text string
			if mode == "plain" {
				var output bytes.Buffer
				writer := &checkedWriter{out: &output}
				writer.Report(event)
				text = output.String()
			} else {
				d := newDashboard()
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3})
				d.Report(event)
				text = dashboardTenantSummary(d.snapshot(), time.Now())
				if d.snapshot().phases[4].state != "pending" || d.snapshot().phases[dashboardConformancePhase].state != "pending" {
					t.Fatal("host cloud-init observation claimed workload or conformance completion")
				}
			}
			if !strings.Contains(text, "cloud-init") {
				t.Fatal("host cloud-init gate missing from presentation", text)
			}
		})
	}
}

func TestIdentityFailureDoesNotCompleteTenant(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "native-site-identity-handoff"})
	if s := d.snapshot(); s.phases[2].state != "running" || s.current != "Site host identity handoff" {
		t.Fatal("identity start is hidden", s.phases[2], s.current)
	}
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "native-site-identity-handoff"})
	s := d.snapshot()
	if s.phases[2].state != "failed" || s.current != "Site host identity handoff" {
		t.Fatal("site failure was hidden", s.current, s.phases[2])
	}
	for _, i := range []int{3, 4, 5} {
		if s.phases[i].state != "pending" {
			t.Fatalf("unstarted phase %d claimed %s", i, s.phases[i].state)
		}
	}
	if s.phases[dashboardConformancePhase].state != "pending" || strings.Contains(s.tenant, "completed") {
		t.Fatal("site retention claimed tenant or suite completion", s.tenant, s.phases[dashboardConformancePhase])
	}
	for _, stage := range s.tenantStages {
		if state := stage.status(); state != "pending" {
			t.Fatal("tenant checks were inferred", state)
		}
	}
	var plain bytes.Buffer
	writer := &checkedWriter{out: &plain, started: time.Now(), phases: make(map[string]time.Time)}
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "native-site-identity-handoff"})
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "native-site-identity-handoff"})
	for _, want := range []string{"[START ", "[FAIL ", "native-site-identity-handoff"} {
		if !strings.Contains(plain.String(), want) {
			t.Fatal("plain output omitted identity state", want, plain.String())
		}
	}
}
