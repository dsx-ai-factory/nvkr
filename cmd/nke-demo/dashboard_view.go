// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	dashboardLines = 80
	dashboardRunes = 4096
)

type dashboardPhase struct{ state, detail string }

type dashboardSnapshot struct {
	started, observedAt, deadline time.Time
	elapsed                       time.Duration
	phases                        [6]dashboardPhase
	activity                      []string
	tenant, current               string
	focus, offset                 int
	registered, ready             int
	nodesObserved                 bool
}

// Only the presentation loop touches widgets. Worker callbacks update bounded
// data under a mutex; neither callbacks nor input start work or enqueue events.
type dashboard struct {
	mu sync.Mutex
	dashboardSnapshot
	partial                                             []byte
	dropped                                             bool
	header, phasesView, stateView, activityView, footer *tview.TextView
}

func newDashboard() *dashboard {
	d := &dashboard{dashboardSnapshot: dashboardSnapshot{started: time.Now(), tenant: "Tenant readiness has not been observed."}}
	for i := range d.phases {
		d.phases[i] = dashboardPhase{"pending", "No operation observed yet"}
	}
	pane := func(title string) *tview.TextView {
		view := tview.NewTextView().SetDynamicColors(true).SetTextColor(tcell.ColorLightSteelBlue).SetWrap(true)
		view.SetBackgroundColor(tcell.ColorDefault).SetBorder(true).SetBorderColor(tcell.ColorSlateGray).SetTitle(title).SetTitleColor(tcell.ColorLightSteelBlue)
		return view
	}
	d.header, d.footer = pane(""), pane("")
	d.header.SetBorder(false)
	d.footer.SetBorder(false)
	d.phasesView, d.stateView, d.activityView = pane(" Phase map "), pane(" Observed state "), pane(" Activity · public diagnostics ")
	d.activityView.SetWrap(false)
	return d
}

func (d *dashboard) appendActivity(line string) {
	runes := []rune(terminalText(line))
	if len(runes) > dashboardRunes {
		runes = append(runes[:dashboardRunes-1], '…')
	}
	d.activity = append(d.activity, string(runes))
	if len(d.activity) > dashboardLines {
		copy(d.activity, d.activity[len(d.activity)-dashboardLines:])
		d.activity = d.activity[:dashboardLines]
	}
}

func (d *dashboard) Write(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, value := range data {
		if value == '\n' {
			line := string(d.partial)
			if d.dropped {
				line += "…"
			}
			d.appendActivity(line)
			d.partial, d.dropped = d.partial[:0], false
		} else if !d.dropped {
			if len(d.partial) == 4*dashboardRunes {
				d.dropped = true
			} else {
				d.partial = append(d.partial, value)
			}
		}
	}
	return len(data), nil
}

// Labels describe fixed source-owned operations, never command arguments,
// arbitrary Status text, node names, private responses or subprocess log claims.
func dashboardOperation(name string) (index int, label string) {
	switch name {
	case "installer-preparation":
		return 0, "Prepare and admit the pinned installer"
	case "installer-build-preflight":
		return 0, "Check build prerequisites"
	case "installer-sudo-preflight":
		return 0, "Admit unattended deployment access"
	case "installer-init", "installer-fetch", "installer-checkout", "installer-pin", "clean-installer":
		return 0, "Prepare and verify pinned installer"
	case "artifacts", "build-all":
		return 1, "Execute the combined artifact graph"
	case "network-preflight", "network-setup":
		return 2, "Prepare isolated demo networking"
	case "create-management", "management-ready":
		return 2, "Create and check Kind management cluster"
	case "start-registry", "node-registry-connectivity":
		return 2, "Admit registry access from the actual node"
	case "site-00-check-readiness", "check-00-check-readiness", "site-01-prepare-cluster", "check-01-prepare-cluster", "site-02-stage-images", "check-02-stage-images", "site-03-describe-site", "check-03-describe-site", "site-05-install-site-controller", "check-05-install-site-controller", "site-06-secure-and-register", "check-06-secure-and-register":
		return 2, "Install and check the NKE site stack"
	case "tenant-provisioning":
		return 3, "Provision QCOW2 control plane and worker"
	case "network-cleanup", "registry-cleanup", "owned-node-cleanup":
		return 5, "Restore owned demo resources"
	}
	return -1, ""
}

func (d *dashboard) Report(event nkedemo.ProgressEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if event.Kind == nkedemo.TenantWaiting {
		d.tenantObservation(event)
		return
	}
	state := ""
	switch event.Kind {
	case nkedemo.LaneStarted, nkedemo.PhaseStarted, nkedemo.DeploymentStarted:
		state = "running"
	case nkedemo.LaneFinished, nkedemo.PhaseFinished, nkedemo.DeploymentFinished:
		state = "observed"
	case nkedemo.LaneFailed, nkedemo.PhaseFailed, nkedemo.DeploymentFailed:
		state = "failed"
	default:
		return
	}
	if event.Name == "local-demo" {
		switch event.Kind {
		case nkedemo.DeploymentStarted:
			d.current = "Prepare management cluster and local delivery"
		case nkedemo.DeploymentFinished:
			d.current = "Deployment, tenant checks and owned cleanup completed"
			for _, index := range []int{2, 3, 4, 5} {
				d.phases[index] = dashboardPhase{"complete", "Owning demo operation completed"}
			}
		case nkedemo.DeploymentFailed:
			d.current = "Demo stopped; inspect retained private diagnostics"
			for index := range d.phases {
				if d.phases[index].state == "running" {
					d.phases[index] = dashboardPhase{"stopped", "Stopped without owning phase completion"}
				}
			}
		}
		return
	}
	index, label := dashboardOperation(event.Name)
	if index < 0 {
		return
	}
	family := "phase-"
	switch event.Name {
	case "artifacts":
		family = "lane-"
	case "tenant-provisioning":
		family = "deployment-"
	}
	if !strings.HasPrefix(event.Kind, family) {
		return
	}
	if event.Name == "artifacts" && event.Kind == nkedemo.LaneFinished || event.Name == "installer-preparation" && event.Kind == nkedemo.PhaseFinished {
		state = "complete"
	}
	if d.phases[index].state != "failed" && (d.phases[index].state != "complete" || state == "failed") {
		d.phases[index] = dashboardPhase{state, label}
	}
	d.current = label
	d.appendActivity(state + ": " + label)
	if event.Name == "tenant-provisioning" && event.Kind == nkedemo.DeploymentFinished {
		d.phases[3] = dashboardPhase{"complete", "Owning tenant operation completed"}
		d.phases[4] = dashboardPhase{"complete", "Workload and DNS checks completed"}
	}
	if event.Name == "tenant-provisioning" && event.Kind == nkedemo.DeploymentFailed && d.phases[4].state == "running" {
		d.phases[4] = dashboardPhase{"stopped", "Tenant operation ended; workload/DNS completion unconfirmed"}
	}
}

func (d *dashboard) tenantObservation(event nkedemo.ProgressEvent) {
	if event.RegisteredNodes < 0 || event.RegisteredNodes > 2 || event.ReadyNodes < 0 || event.ReadyNodes > event.RegisteredNodes || event.Elapsed < 0 || event.Remaining < 0 {
		return
	}
	message := ""
	switch event.Name {
	case "tenant-kubeconfig":
		message = "Waiting for NKE to publish tenant kubeconfig. Guest boot and node readiness are not yet observed."
	case "tenant-api":
		message = "Kubeconfig available. Waiting for a successful tenant API node query; readiness is not yet observed."
	case "tenant-nodes":
		d.registered, d.ready, d.nodesObserved = event.RegisteredNodes, event.ReadyNodes, true
		message = fmt.Sprintf("QCOW2 tenant: %d/2 registered · %d/2 Ready. Waiting for control plane and worker.", event.RegisteredNodes, event.ReadyNodes)
		if event.RegisteredNodes == 2 && event.ReadyNodes == 2 {
			message = "QCOW2 tenant: 2/2 registered · 2/2 Ready. Workload and DNS checks still required."
		}
	case "tenant-workload":
		message = "Running workload and cluster DNS checks. Successful completion is still required."
		d.phases[4] = dashboardPhase{"running", "Check workload execution and cluster DNS"}
	default:
		return
	}
	now := time.Now()
	if deadline := now.Add(event.Remaining); d.deadline.IsZero() || deadline.Before(d.deadline) {
		d.deadline = deadline
	}
	if d.observedAt.IsZero() {
		d.elapsed = event.Elapsed
	} else {
		d.elapsed = max(event.Elapsed, d.elapsed+max(time.Duration(0), now.Sub(d.observedAt)))
	}
	d.observedAt, d.tenant = now, message
	d.current = "Observe tenant provisioning and readiness"
	d.phases[3] = dashboardPhase{"running", "Observe the real tenant API and nodes"}
}

func (d *dashboard) snapshot() dashboardSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	copy := d.dashboardSnapshot
	copy.activity = append([]string(nil), d.activity...)
	if len(d.partial) != 0 {
		line := []rune(terminalText(string(d.partial)))
		copy.activity = append(copy.activity, string(line[:min(len(line), dashboardRunes)]))
	}
	if len(copy.activity) > dashboardLines {
		copy.activity = copy.activity[len(copy.activity)-dashboardLines:]
	}
	return copy
}

func (d *dashboard) input(event *tcell.EventKey) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch event.Key() {
	case tcell.KeyTab:
		d.focus = (d.focus + 1) % 3
	case tcell.KeyPgUp:
		d.focus, d.offset = 2, min(dashboardLines, d.offset+10)
	case tcell.KeyPgDn:
		d.focus, d.offset = 2, max(0, d.offset-10)
	case tcell.KeyEnd:
		d.offset = 0
	}
}

func (d *dashboard) draw(screen tcell.Screen, now time.Time, canceling bool) {
	s := d.snapshot()
	width, height := screen.Size()
	content := max(0, height-5)
	screen.Clear()
	d.header.SetText(fmt.Sprintf("[lightcyan::b] NKE · local ARM64 build & demo[-::-]    %s elapsed\n [goldenrod]UNQUALIFIED local diagnostic · not a signed Release[-]", max(time.Duration(0), now.Sub(s.started)).Truncate(time.Second)))
	var phases strings.Builder
	names := [...]string{"Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup"}
	colors := map[string]string{"pending": "goldenrod", "running": "lightcyan", "observed": "lightsteelblue", "complete": "lightgreen", "failed": "lightcoral", "stopped": "lightsteelblue"}
	for index, phase := range s.phases {
		if width <= 90 || content < 28 {
			fmt.Fprintf(&phases, "[%s::b]%-14s[-::-] %s\n", colors[phase.state], names[index], phase.state)
		} else {
			fmt.Fprintf(&phases, "[%s::b]%s[-::-]\n  %s · %s\n\n", colors[phase.state], names[index], phase.state, tview.Escape(phase.detail))
		}
	}
	d.phasesView.SetText(phases.String())
	state := tview.Escape(s.current) + "\n\n" + tview.Escape(s.tenant)
	if s.nodesObserved {
		state += fmt.Sprintf("\nLast node query: %d/2 registered · %d/2 Ready", s.registered, s.ready)
	}
	if !s.observedAt.IsZero() {
		state += fmt.Sprintf("\n\nTenant budget: %s elapsed · %s remaining", (s.elapsed + max(time.Duration(0), now.Sub(s.observedAt))).Truncate(time.Second), max(time.Duration(0), s.deadline.Sub(now)).Truncate(time.Second))
	}
	d.stateView.SetText(state)
	d.activityView.SetText(tview.Escape(strings.Join(s.activity, "\n")))
	help := " Tab focus · PgUp/PgDn activity · End live · q/Ctrl-C cancel safely"
	if canceling {
		help = " Canceling · waiting for owned processes and resource cleanup"
	}
	d.footer.SetText("[lightcyan]" + help + "[-]\n Details: retained private build and deployment logs")
	if width > 90 {
		left, upper := width*3/10, min(12, content)
		d.phasesView.SetRect(0, 3, left, content)
		d.stateView.SetRect(left, 3, width-left, upper)
		d.activityView.SetRect(left, 3+upper, width-left, content-upper)
	} else {
		phaseHeight := min(8, content)
		stateHeight := min(10, (content-phaseHeight)/2)
		d.phasesView.SetRect(0, 3, width, phaseHeight)
		d.stateView.SetRect(0, 3+phaseHeight, width, stateHeight)
		d.activityView.SetRect(0, 3+phaseHeight+stateHeight, width, content-phaseHeight-stateHeight)
	}
	d.header.SetRect(0, 0, width, min(3, height))
	d.footer.SetRect(0, max(0, height-2), width, min(2, height))
	_, _, _, activityHeight := d.activityView.GetInnerRect()
	d.activityView.ScrollTo(max(0, len(s.activity)-activityHeight-s.offset), 0)
	for index, view := range []*tview.TextView{d.phasesView, d.stateView, d.activityView} {
		color := tcell.ColorSlateGray
		if index == s.focus {
			color = tcell.ColorLightCyan
		}
		view.SetBorderColor(color)
		view.Draw(screen)
	}
	d.header.Draw(screen)
	d.footer.Draw(screen)
	// The event-loop owner calls Show and handles terminal failures.
}
