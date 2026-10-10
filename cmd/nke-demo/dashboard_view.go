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

type dashboardStage struct {
	states  [2]string
	started [2]time.Time
}

// This bounded checklist names source-owned operations, not inferred deployment
// milestones. Paired NKE stages need both the run and check observations.
var dashboardSiteStages = [...]struct {
	label      string
	operations [2]string
}{
	{"Create Kind cluster", [2]string{"create-management"}},
	{"Registry reachable", [2]string{"start-registry", "node-registry-connectivity"}},
	{"Site nodes Ready", [2]string{"management-ready"}},
	{"Install Cluster API", [2]string{"install-capi"}},
	{"Check Cluster API", [2]string{"check-capi"}},
	{"Readiness", [2]string{"site-00-check-readiness", "check-00-check-readiness"}},
	{"Prepare cluster", [2]string{"site-01-prepare-cluster", "check-01-prepare-cluster"}},
	{"Deliver images", [2]string{"site-02-stage-images", "check-02-stage-images"}},
	{"Describe site", [2]string{"site-03-describe-site", "check-03-describe-site"}},
	{"Install NKE controller", [2]string{"site-05-install-site-controller", "check-05-install-site-controller"}},
	{"Secure and register", [2]string{"site-06-secure-and-register", "check-06-secure-and-register"}},
}

var dashboardTenantStages = [...]string{"Tenant kubeconfig", "Tenant API query", "Both nodes Ready", "Workload and DNS"}

type dashboardSnapshot struct {
	started, observedAt, deadline time.Time
	elapsed                       time.Duration
	phases                        [6]dashboardPhase
	activity                      []string
	tenant, current               string
	focus, offset                 int
	registered, ready             int
	nodesObserved                 bool
	confirmCancel                 bool
	confirmForce, forceAvailable  bool
	currentStarted                time.Time
	tenantChangedAt               time.Time
	tenantStage                   string
	provisioning, provisioned     nkedemo.TenantConditionStatus
	siteStages                    [len(dashboardSiteStages)]dashboardStage
	tenantStages                  [len(dashboardTenantStages)]dashboardStage
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
	case "site-ovn-chart", "site-ovn-defaults":
		return 2, "Verify the built NVIDIA OVN chart and image bindings"
	case "site-ovs-fresh", "site-ovs-create", "site-ovs-ready":
		return 2, "Start and check Open vSwitch for the owned Kind node"
	case "site-ovn-install", "site-ovn-control-plane-ready", "site-ovn-node-ready":
		return 2, "Install NVIDIA OVN and wait for its site controllers"
	case "site-ovn-observation":
		return 2, "Check live site OVN images against the selected output"
	case "install-capi", "check-capi":
		return 2, "Install and check Cluster API after site networking"
	case "site-00-check-readiness", "check-00-check-readiness", "site-01-prepare-cluster", "check-01-prepare-cluster", "site-02-stage-images", "check-02-stage-images", "site-03-describe-site", "check-03-describe-site", "site-05-install-site-controller", "check-05-install-site-controller", "site-06-secure-and-register", "check-06-secure-and-register":
		return 2, "Install and check the NKE site stack"
	case "tenant-provisioning":
		return 3, "Provision QCOW2 control plane and worker"
	case "tenant-cleanup":
		return 5, "Clean up the owned tenant"
	case "tenant-diagnostics":
		return 5, "Collect bounded private guest diagnostics"
	case "tenant-delete":
		return 5, "Ask NKE to delete the owned tenant"
	case "tenant-deletion-wait":
		return 5, "Wait for NKE to confirm tenant deletion"
	case "network-cleanup", "registry-cleanup", "owned-node-cleanup":
		return 5, "Restore owned demo resources"
	}
	return -1, ""
}

func (stage *dashboardStage) observe(part int, state string, now time.Time) {
	if stage.states[part] == "failed" || stage.states[part] == "observed" && state != "failed" {
		return
	}
	if state == "running" && stage.started[part].IsZero() {
		stage.started[part] = now
	}
	stage.states[part] = state
}

func (stage dashboardStage) status(required int) (state string, observed int, started time.Time) {
	state = "pending"
	for part := range required {
		switch stage.states[part] {
		case "observed":
			observed++
		case "running":
			if state == "pending" {
				state = "running"
			}
			if started.IsZero() || stage.started[part].Before(started) {
				started = stage.started[part]
			}
		case "failed":
			state = "failed"
		case "stopped":
			if state != "failed" {
				state = "stopped"
			}
		}
	}
	if observed == required {
		state = "observed"
	}
	return state, observed, started
}

func (d *dashboard) observeSiteStage(event nkedemo.ProgressEvent, state string) string {
	for index, stage := range dashboardSiteStages {
		for part, operation := range stage.operations {
			if operation != "" && event.Name == operation {
				d.siteStages[index].observe(part, state, time.Now())
				label := stage.label
				if strings.HasPrefix(operation, "site-") {
					label += " · run"
				} else if strings.HasPrefix(operation, "check-") && stage.operations[1] != "" {
					label += " · check"
				}
				return label
			}
		}
	}
	return ""
}

func stopDashboardStages(stages []dashboardStage, state string) {
	for index := range stages {
		for part, previous := range stages[index].states {
			if previous == "running" {
				stages[index].states[part] = state
			}
		}
	}
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
			d.currentStarted = time.Now()
		case nkedemo.DeploymentFinished:
			d.current = "Deployment, tenant checks and owned cleanup completed"
			d.currentStarted = time.Time{}
			for _, index := range []int{2, 3, 4, 5} {
				d.phases[index] = dashboardPhase{"complete", "Owning demo operation completed"}
			}
		case nkedemo.DeploymentFailed:
			d.current = "Demo stopped; inspect retained private diagnostics"
			d.currentStarted = time.Time{}
			stopDashboardStages(d.siteStages[:], "stopped")
			stopDashboardStages(d.tenantStages[:], "stopped")
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
	if event.Kind == nkedemo.PhaseStarted || event.Kind == nkedemo.PhaseFinished || event.Kind == nkedemo.PhaseFailed {
		if stageLabel := d.observeSiteStage(event, state); stageLabel != "" {
			label = stageLabel
		}
	}
	if event.Name == "artifacts" && event.Kind == nkedemo.LaneFinished || event.Name == "installer-preparation" && event.Kind == nkedemo.PhaseFinished {
		state = "complete"
	}
	if d.phases[index].state != "failed" && (d.phases[index].state != "complete" || state == "failed") {
		d.phases[index] = dashboardPhase{state, label}
	}
	d.current = label
	if state == "running" {
		d.currentStarted = time.Now()
	} else {
		d.currentStarted = time.Time{}
	}
	d.appendActivity(state + ": " + label)
	if event.Name == "tenant-provisioning" && event.Kind == nkedemo.DeploymentFinished {
		if d.tenantStages[3].states[0] == "running" {
			d.tenantStages[3].observe(0, "observed", time.Now())
		}
		d.phases[3] = dashboardPhase{"complete", "Owning tenant operation completed"}
		d.phases[4] = dashboardPhase{"complete", "Workload and DNS checks completed"}
	}
	if event.Name == "tenant-provisioning" && event.Kind == nkedemo.DeploymentFailed {
		stopDashboardStages(d.tenantStages[:], "failed")
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
	now := time.Now()
	previousRegistered, previousReady := d.registered, d.ready
	switch event.Name {
	case "tenant-kubeconfig":
		d.tenantStages[0].observe(0, "running", now)
		message = "Waiting for NKE to publish tenant kubeconfig. Guest boot and node readiness are not yet observed."
	case "tenant-api":
		if d.tenantStages[0].states[0] == "running" {
			d.tenantStages[0].observe(0, "observed", now)
		}
		d.tenantStages[1].observe(0, "running", now)
		message = "Kubeconfig available. Waiting for a successful tenant API node query; readiness is not yet observed."
	case "tenant-nodes":
		if d.tenantStages[1].states[0] == "running" {
			d.tenantStages[1].observe(0, "observed", now)
		}
		d.tenantStages[2].observe(0, "running", now)
		d.registered, d.ready, d.nodesObserved = event.RegisteredNodes, event.ReadyNodes, true
		message = fmt.Sprintf("QCOW2 tenant: %d/2 registered · %d/2 Ready. Waiting for control plane and worker.", event.RegisteredNodes, event.ReadyNodes)
		if event.RegisteredNodes == 2 && event.ReadyNodes == 2 {
			d.tenantStages[2].observe(0, "observed", now)
			message = "QCOW2 tenant: 2/2 registered · 2/2 Ready. Workload and DNS checks still required."
		}
	case "tenant-workload":
		d.tenantStages[3].observe(0, "running", now)
		message = "Running workload and cluster DNS checks. Successful completion is still required."
		d.phases[4] = dashboardPhase{"running", "Check workload execution and cluster DNS"}
	default:
		return
	}
	conditionChanged := false
	for _, condition := range []struct {
		observed nkedemo.TenantConditionStatus
		stored   *nkedemo.TenantConditionStatus
	}{{event.Provisioning, &d.provisioning}, {event.Provisioned, &d.provisioned}} {
		switch condition.observed {
		case nkedemo.TenantConditionTrue, nkedemo.TenantConditionFalse, nkedemo.TenantConditionUnknown:
			conditionChanged = conditionChanged || condition.observed != *condition.stored
			*condition.stored = condition.observed
		}
	}
	// A repeated wait is an observation heartbeat, not evidence of forward
	// progress. Only a fixed stage, count or whitelisted condition change resets this.
	if d.tenantStage != event.Name || conditionChanged || event.Name == "tenant-nodes" && (previousRegistered != event.RegisteredNodes || previousReady != event.ReadyNodes) {
		d.tenantChangedAt = now
	}
	d.tenantStage = event.Name
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
	d.currentStarted = time.Time{}
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

func (d *dashboard) input(event *tcell.EventKey) (cancel bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if event.Key() == tcell.KeyCtrlC {
		d.confirmCancel = false
		return true
	}
	if d.confirmCancel {
		switch {
		case event.Key() == tcell.KeyRune && (event.Rune() == 'y' || event.Rune() == 'Y'):
			d.confirmCancel = false
			return true
		case event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyRune && (event.Rune() == 'n' || event.Rune() == 'N'):
			d.confirmCancel = false
		}
		return false
	}
	if event.Key() == tcell.KeyRune && (event.Rune() == 'q' || event.Rune() == 'Q') {
		d.confirmCancel = true
		return false
	}
	switch event.Key() {
	case tcell.KeyEscape:
		return true
	case tcell.KeyTab:
		d.focus = (d.focus + 1) % 3
	case tcell.KeyPgUp:
		d.focus, d.offset = 2, min(dashboardLines, d.offset+10)
	case tcell.KeyPgDn:
		d.focus, d.offset = 2, max(0, d.offset-10)
	case tcell.KeyEnd:
		d.offset = 0
	}
	return false
}

// Only the event-loop owner calls this while graceful cancellation is already
// in progress. It cannot start cancellation or manipulate execution resources.
func (d *dashboard) forceInput(event *tcell.EventKey) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.forceAvailable {
		return false
	}
	if d.confirmForce {
		switch {
		case event.Key() == tcell.KeyRune && (event.Rune() == 'y' || event.Rune() == 'Y'):
			d.confirmForce = false
			return true
		case event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyRune && (event.Rune() == 'n' || event.Rune() == 'N'):
			d.confirmForce = false
		}
		return false
	}
	if event.Key() == tcell.KeyRune && (event.Rune() == 'f' || event.Rune() == 'F') {
		d.confirmForce = true
	}
	return false
}

func dashboardChecklist(s dashboardSnapshot, now time.Time) (string, int) {
	var rows strings.Builder
	checked := 0
	write := func(label string, stage dashboardStage, required int) {
		state, observed, started := stage.status(required)
		color, marker := "goldenrod", "○"
		detail := state
		switch state {
		case "observed":
			checked++
			color, marker = "lightgreen", "✓"
		case "running":
			color, marker = "lightcyan", "▶"
			if !started.IsZero() {
				detail = max(time.Duration(0), now.Sub(started)).Truncate(time.Second).String()
			}
		case "failed":
			color, marker = "lightcoral", "✗"
		case "stopped":
			color, marker = "lightsteelblue", "■"
		}
		if required == 2 && observed == 1 && state == "pending" {
			detail = "1/2 pending"
		} else if required == 2 && state == "running" {
			detail = fmt.Sprintf("%d/2 · %s", observed, detail)
		}
		fmt.Fprintf(&rows, "[%s]%s[-] %-23s %s\n", color, marker, label, detail)
	}
	for index, stage := range dashboardSiteStages {
		required := 1
		if stage.operations[1] != "" {
			required = 2
		}
		write(stage.label, s.siteStages[index], required)
	}
	site := fmt.Sprintf("[lightcyan::b]Site stages · %d/%d checked[-::-]\n", checked, len(dashboardSiteStages)) + rows.String()
	rows.Reset()
	checked = 0
	for index, label := range dashboardTenantStages {
		write(label, s.tenantStages[index], 1)
	}
	tenant := fmt.Sprintf("[lightcyan::b]Tenant stages · %d/%d checked[-::-]\n", checked, len(dashboardTenantStages)) + rows.String()
	return site + "\n" + tenant, len(dashboardSiteStages) + len(dashboardTenantStages) + 3
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
		if width <= 90 {
			fmt.Fprintf(&phases, "[%s::b]%s[-::-] %s", colors[phase.state], names[index], phase.state)
			if index%3 == 2 {
				phases.WriteByte('\n')
			} else {
				phases.WriteString(" · ")
			}
		} else {
			fmt.Fprintf(&phases, "[%s::b]%-14s[-::-] %s\n", colors[phase.state], names[index], phase.state)
		}
	}
	checklist, stageRows := dashboardChecklist(s, now)
	phases.WriteByte('\n')
	phases.WriteString(checklist)
	d.phasesView.SetText(phases.String())
	current := tview.Escape(s.current)
	if !s.currentStarted.IsZero() {
		current += " · " + max(time.Duration(0), now.Sub(s.currentStarted)).Truncate(time.Second).String()
	}
	state := current
	if canceling {
		state = "[lightcoral::b]CANCELING · cleanup still running[-::-]\n" + current
		if s.forceAvailable {
			if s.confirmForce {
				state += "\n\n[lightcoral::b]FORCE EXIT NOW?[-::-]\n" + tview.Escape("[y] Exit immediately   [n / Esc] Keep waiting")
			} else {
				state += "\n\n[lightcoral::b][ f ] FORCE EXIT[-::-] · confirmation required"
			}
			state += "\nVMs, containers and network changes may remain."
		} else {
			state += "\nCancellation requested; waiting for owned cleanup to return."
		}
	} else {
		state += "\n" + tview.Escape(s.tenant)
	}
	if !canceling && s.nodesObserved {
		state += fmt.Sprintf("\nLast node query: %d/2 registered · %d/2 Ready", s.registered, s.ready)
	}
	if !canceling && (s.provisioning != "" || s.provisioned != "") {
		conditions := []string{}
		if s.provisioning != "" {
			conditions = append(conditions, "Provisioning="+string(s.provisioning))
		}
		if s.provisioned != "" {
			conditions = append(conditions, "Provisioned="+string(s.provisioned))
		}
		state += "\nLast NKE conditions: " + strings.Join(conditions, " · ")
	}
	if canceling {
		state += "\nCleanup completion is not yet observed; ETA unknown."
	} else if !s.observedAt.IsZero() {
		state += fmt.Sprintf("\nState unchanged: %s · last observation: %s ago", max(time.Duration(0), now.Sub(s.tenantChangedAt)).Truncate(time.Second), max(time.Duration(0), now.Sub(s.observedAt)).Truncate(time.Second))
		state += fmt.Sprintf("\nTenant budget: %s elapsed · ETA unknown\nTimeout remaining: %s (not an ETA)", (s.elapsed + max(time.Duration(0), now.Sub(s.observedAt))).Truncate(time.Second), max(time.Duration(0), s.deadline.Sub(now)).Truncate(time.Second))
	} else {
		state += "\nETA unknown; no completion estimate reported"
	}
	d.stateView.SetText(state)
	d.activityView.SetText(tview.Escape(strings.Join(s.activity, "\n")))
	help := " Tab focus · PgUp/PgDn activity · End live · q confirm quit · Ctrl-C cancel"
	if s.confirmCancel {
		help = " Cancel this demo and clean up its resources? y/N"
	}
	if canceling {
		help = " Canceling · waiting for owned processes and resource cleanup"
		if s.forceAvailable {
			help += " · f force exit"
		}
		if s.confirmForce {
			help = " FORCE EXIT? Cleanup may be incomplete; resources may remain. y/N"
		}
	}
	d.footer.SetText("[lightcyan]" + help + "[-]\n Details: retained private build and deployment logs")
	if width > 90 {
		left, upper := width*3/10, min(12, content)
		d.phasesView.SetRect(0, 3, left, content)
		d.stateView.SetRect(left, 3, width-left, upper)
		d.activityView.SetRect(left, 3+upper, width-left, content-upper)
	} else {
		// Reserve live status and a small activity window before allocating the
		// checklist. On short terminals, pending rows may clip; the current wait,
		// observation clocks and timeout must remain visible.
		stateHeight := min(10, max(0, content-4))
		phaseHeight := min(stageRows+5, max(0, content-stateHeight-4))
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
