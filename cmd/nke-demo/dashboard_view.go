// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/buildactivity"
	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/planview"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	dashboardLines = 80
	dashboardRunes = 4096
	dashboardSteps = 512
)

type dashboardPhase struct{ state, detail string }

type dashboardStage struct {
	states  [2]string
	started [2]time.Time
}

type dashboardBuildStep struct {
	id, state, kind string
	started         time.Time
	previewed       bool
}

type dashboardOSLogs struct {
	record   buildactivity.Record
	received time.Time
}

// This bounded checklist names source-owned operations, not inferred deployment
// milestones. Paired NKE stages need both the run and check observations.
var dashboardSiteStages = [...]struct {
	label      string
	operations [2]string
}{
	{"Create Kind cluster", [2]string{"create-management"}},
	{"Registry reachable", [2]string{"start-registry", "registry-admission"}},
	{"Site networking (OVN)", [2]string{"site-ovs-ready", "site-ovn-admission"}},
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
	guests                        [2]nkedemo.TenantGuestObservation
	guestsObservedAt              time.Time
	siteStages                    [len(dashboardSiteStages)]dashboardStage
	siteOVN                       bool
	tenantStages                  [len(dashboardTenantStages)]dashboardStage
	buildSteps                    []dashboardBuildStep
	buildPreview, buildTruncated  bool
	buildChangedAt                time.Time
	buildOutputs                  []string
	buildOutputAt                 time.Time
	buildOutputBytes              uint64
	buildProcess                  string
	buildGuestStage               string
	buildGuestAt                  time.Time
	buildLogs                     map[string]dashboardOSLogs
	buildVertices                 []planview.Vertex
	buildEdges                    []planview.Edge
	buildSelected                 int
	buildDetails                  bool
	buildLogPath                  string
	recoveryWork                  string
}

// Only the presentation loop touches widgets. Worker callbacks update bounded
// data under a mutex; neither callbacks nor input start work or enqueue events.
type dashboard struct {
	mu sync.Mutex
	dashboardSnapshot
	tenantActivityAt                                    time.Time
	partial                                             []byte
	dropped                                             bool
	header, phasesView, stateView, activityView, footer *tview.TextView
}

func newDashboard() *dashboard {
	d := &dashboard{dashboardSnapshot: dashboardSnapshot{started: time.Now(), tenant: "Tenant readiness has not been observed.", buildSelected: -1, buildDetails: true}}
	for i := range d.phases {
		d.phases[i] = dashboardPhase{"pending", "No operation observed yet"}
	}
	pane := func(title string) *tview.TextView {
		view := tview.NewTextView().SetDynamicColors(true).SetTextColor(tcell.ColorLightSteelBlue).SetWrap(true)
		view.SetBackgroundColor(tcell.NewHexColor(0x1c2633)).SetBorder(true).SetBorderColor(tcell.NewHexColor(0x52657a)).SetTitle(title).SetTitleColor(tcell.ColorLightCyan)
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
			if !d.dropped && d.observeBuildGraph(line) {
				d.partial = d.partial[:0]
				continue
			}
			if !d.dropped {
				d.observeBuildLog(line)
			}
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

// Summarize the existing human factory listing, not a Run record or receipt.
// Only the owning artifact lane can complete artifacts; these bounded log
// counters never change lifecycle state or execution decisions.
func (d *dashboard) observeBuildLog(line string) {
	if d.phases[1].state != "running" {
		return
	}
	if strings.HasPrefix(line, "[build +") {
		_, line, _ = strings.Cut(line, "] ")
	}
	// These fixed public Packer messages are last reports, not a guest-health
	// probe. Do not attribute arbitrary parallel output to an operation.
	if stage := dashboardGuestReport(line); stage != "" {
		d.buildGuestStage, d.buildGuestAt = stage, time.Now()
		return
	}
	if output, ok := strings.CutPrefix(line, "  output: "); ok {
		name, _, found := strings.Cut(output, " [")
		if found && dashboardBuildID(name) && !slices.Contains(d.buildOutputs, name) {
			if len(d.buildOutputs) < dashboardSteps {
				d.buildOutputs = append(d.buildOutputs, name)
			} else {
				d.buildTruncated = true
			}
		}
		return
	}
	marker, id, ok := strings.Cut(line, "] ")
	if !ok {
		return
	}
	state := ""
	switch marker {
	case "[dry-run":
		state = "queued"
	case "[start":
		state = "running"
	case "[done", "[cached":
		state = "done"
	default:
		return
	}
	kind := ""
	if state != "done" {
		id, kind, ok = strings.Cut(id, " (")
		if !ok || !strings.HasSuffix(kind, ")") {
			return
		}
		kind = strings.TrimSuffix(kind, ")")
		switch kind {
		case "fetch", "build", "acquire", "terminal-observe", "sbom", "package", "inventory":
		default:
			return
		}
	}
	if !dashboardBuildID(id) {
		return
	}
	index := slices.IndexFunc(d.buildSteps, func(step dashboardBuildStep) bool { return step.id == id })
	if index < 0 {
		if len(d.buildSteps) == dashboardSteps {
			d.buildTruncated = true
			return
		}
		index = len(d.buildSteps)
		d.buildSteps = append(d.buildSteps, dashboardBuildStep{id: id})
	}
	step := &d.buildSteps[index]
	if kind != "" {
		step.kind = kind
	}
	if state == "queued" {
		step.previewed, d.buildPreview = true, true
	}
	if step.state == "done" || state == "queued" && step.state != "" {
		return
	}
	if step.state != state {
		d.buildChangedAt = time.Now()
	}
	if state == "running" && step.started.IsZero() {
		step.started = time.Now()
	}
	step.state = state
}

const dashboardGuestReportPrefix = "OS image report: "

// These are descriptive reports from the selected Packer build, not guest
// health, installer telemetry or artifact completion. Variable suffixes can
// contain private paths and addresses, so only fixed summaries leave the log.
var dashboardPackerReports = [...]struct {
	message, summary string
	prefix           bool
}{
	{"Starting VM, booting from CD-ROM", "Launching OS-image VM from installer ISO", false},
	{"Waiting 30s for boot...", "Waiting for the OS-image boot console", false},
	{"Connecting to VM via VNC (", "Connecting to the OS-image boot console", true},
	{"Typing the boot commands over VNC...", "Sending installer boot commands", false},
	{"Waiting for SSH to become available...", "Waiting for SSH from the OS-image guest", false},
	{"Connected to SSH!", "SSH connected to the OS-image guest", false},
	{"Running local shell script: ", "Running a source-owned local shell step", true},
	{"Uploading ", "Transferring files to the OS-image guest", true},
	{"Provisioning with shell script: ", "Running guest shell provisioning", true},
	{"Gracefully halting virtual machine...", "Shutting down the OS-image guest", false},
	{"Waiting for shutdown...", "Waiting for OS-image guest shutdown", false},
	{"Running post-processor: shell-local", "Starting final QCOW2 post-processing", false},
}

func dashboardGuestReport(line string) string {
	if len(line) > dashboardRunes {
		return ""
	}
	for _, value := range line {
		if value < ' ' || value > '~' {
			return ""
		}
	}
	if summary, ok := strings.CutPrefix(line, dashboardGuestReportPrefix); ok {
		for _, report := range dashboardPackerReports {
			if summary == report.summary {
				return summary
			}
		}
		return ""
	}
	message, ok := strings.CutPrefix(line, "==> nke-node-non-fde.qemu.nke-non-fde: ")
	if !ok {
		return ""
	}
	for _, report := range dashboardPackerReports {
		if message == report.message && !report.prefix || report.prefix && len(message) > len(report.message) && strings.HasPrefix(message, report.message) {
			return report.summary
		}
	}
	return ""
}

// Restrict descriptive labels to bounded printable IDs, not terminal markup.
func dashboardBuildID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, value := range id {
		if !(value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-_/.:", value)) {
			return false
		}
	}
	return true
}

// Labels describe fixed source-owned operations, never command arguments,
// arbitrary Status text, node names, private responses or subprocess log claims.
func dashboardOperation(name string) (index int, label string) {
	switch name {
	case "installer-preparation":
		return 0, "Prepare and admit the pinned installer"
	case "installer-build-preflight":
		return 0, "Check build prerequisites"
	case "installer-docker-preflight":
		return 0, "Check deployment Docker daemon before image admission"
	case "installer-sudo-preflight":
		return 0, "Admit unattended deployment access"
	case "installer-init", "installer-fetch", "installer-checkout", "installer-pin", "clean-installer":
		return 0, "Prepare and verify pinned installer"
	case "artifacts", "build-all":
		return 1, "Execute the combined artifact graph"
	case "network-preflight", "network-setup":
		return 2, "Prepare isolated demo networking"
	case "create-management":
		return 2, "Create and check Kind management cluster"
	case "management-ready":
		return 2, dashboardSiteStages[2].label
	case "start-registry", "node-registry-connectivity", "registry-admission":
		return 2, "Admit registry access from the actual node"
	case "node-simulator-connectivity":
		return 2, "Check Kind-to-simulator connectivity before site installation"
	case "site-ovn-chart", "site-ovn-defaults":
		return 2, "Verify the selected OVN chart and image bindings"
	case "site-ovs-fresh", "site-ovs-create", "site-ovs-ready":
		return 2, "Start and check Open vSwitch for the owned Kind node"
	case "site-ovn-install", "site-ovn-control-plane-ready", "site-ovn-node-ready":
		return 2, "Install selected OVN and wait for its site controllers"
	case "site-ovn-observation", "site-ovn-admission":
		return 2, "Check live site OVN images against the selected output"
	case "install-capi", "check-capi":
		return 2, "Install and check Cluster API after site networking"
	case "site-00-check-readiness", "check-00-check-readiness", "site-01-prepare-cluster", "check-01-prepare-cluster", "site-02-stage-images", "check-02-stage-images", "site-03-describe-site", "check-03-describe-site", "site-05-install-site-controller", "check-05-install-site-controller", "site-06-secure-and-register", "check-06-secure-and-register":
		return 2, "Install and check the NKE site stack"
	case "tenant-provisioning":
		return 3, "Provision QCOW2 control plane and worker"
	case "tenant-cni-readiness":
		return 4, "Wait for tenant Calico node agents before DNS checks"
	case "tenant-dns-readiness":
		return 4, "Wait for CoreDNS and node-local DNS rollout before the smoke test"
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
	if strings.HasPrefix(event.Name, "site-ovs-") || strings.HasPrefix(event.Name, "site-ovn-") {
		d.siteOVN = true
		part := 1
		if strings.HasPrefix(event.Name, "site-ovs-") || event.Name == "site-ovn-chart" || event.Name == "site-ovn-defaults" {
			part = 0
		}
		if state != "observed" || event.Name == dashboardSiteStages[2].operations[part] {
			d.siteStages[2].observe(part, state, time.Now())
		}
		return dashboardSiteStages[2].label
	}
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
	if event.Kind == nkedemo.OSBuildLogs {
		if d.phases[1].state != "running" || event.BuildLogs.Validate() != nil {
			return
		}
		if !slices.ContainsFunc(d.buildSteps, func(step dashboardBuildStep) bool {
			return step.id == event.BuildLogs.Operation && step.kind == "build" && step.state == "running"
		}) {
			return
		}
		if d.buildLogs == nil {
			d.buildLogs = make(map[string]dashboardOSLogs)
		}
		if _, exists := d.buildLogs[event.BuildLogs.Operation]; !exists && len(d.buildLogs) >= 64 {
			return
		}
		d.buildLogs[event.BuildLogs.Operation] = dashboardOSLogs{record: event.BuildLogs, received: time.Now()}
		// Metadata refresh is not step progress or an authority observation.
		return
	}
	if event.Kind == nkedemo.ProcessStarted || event.Kind == nkedemo.ProcessExited {
		if event.Name == "build-all" {
			if event.Kind == nkedemo.ProcessExited {
				d.buildProcess = "exited"
			} else if d.buildProcess != "exited" {
				d.buildProcess = "started"
			}
		}
		return
	}
	if event.Kind == nkedemo.TenantWaiting {
		d.tenantObservation(event)
		return
	}
	if event.Kind == nkedemo.TenantGuests {
		d.tenantGuestObservation(event)
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
				if d.phases[index].state == "running" || d.phases[index].state == "observed" {
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
		// The owner reports every termination here, including cancellation.
		// It does not establish that an unfinished readiness check failed.
		stopDashboardStages(d.tenantStages[:], "stopped")
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
	stage := 0
	now := time.Now()
	previousRegistered, previousReady := d.registered, d.ready
	switch event.Name {
	case "tenant-kubeconfig":
		d.tenantStages[0].observe(0, "running", now)
		message = "Waiting for NKE to publish tenant kubeconfig. Guest boot and node readiness are not yet observed."
	case "tenant-api":
		stage = 1
		if d.tenantStages[0].states[0] == "running" {
			d.tenantStages[0].observe(0, "observed", now)
		}
		d.tenantStages[1].observe(0, "running", now)
		message = "Kubeconfig available. Waiting for a successful tenant API node query; readiness is not yet observed."
	case "tenant-nodes":
		stage = 2
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
		stage = 3
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
	changed := d.tenantStage != event.Name || conditionChanged || event.Name == "tenant-nodes" && (previousRegistered != event.RegisteredNodes || previousReady != event.ReadyNodes)
	if changed {
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
	if changed || d.tenantActivityAt.IsZero() || now.Sub(d.tenantActivityAt) >= 30*time.Second {
		// Waiting reports also occur after a pause or an unsuccessful tenant API
		// query. They describe reported state, not a freshly successful API poll.
		activity := "latest reported conditions remain unchanged."
		if changed {
			activity = "reported state changed · " + dashboardTenantStages[stage]
			if event.Name == "tenant-nodes" {
				activity += fmt.Sprintf(" · %d/2 registered · %d/2 Ready", d.registered, d.ready)
			}
			if d.provisioning != "" {
				activity += " · Provisioning=" + string(d.provisioning)
			}
			if d.provisioned != "" {
				activity += " · Provisioned=" + string(d.provisioned)
			}
		}
		d.appendActivity(fmt.Sprintf("Tenant wait +%s: %s", d.elapsed.Truncate(time.Second), activity))
		d.tenantActivityAt = now
	}
	d.current = "Observe tenant provisioning and readiness"
	d.currentStarted = time.Time{}
	d.phases[3] = dashboardPhase{"running", "Observe the real tenant API and nodes"}
}

// Guest telemetry is descriptive infrastructure activity, independently timed.
// It cannot advance a tenant checkpoint, refresh a node query or complete work.
func (d *dashboard) tenantGuestObservation(event nkedemo.ProgressEvent) {
	if !dashboardValidGuests(event.Guests) {
		return
	}
	for index, role := range [...]string{"Control plane", "Worker"} {
		guest, previous := event.Guests[index], d.guests[index]
		if guest.Available != previous.Available || guest.Domain != previous.Domain || guest.ProviderReady != previous.ProviderReady || guest.ProvisioningFailure != previous.ProvisioningFailure {
			message := role + ": guest observation unavailable"
			if guest.Available {
				message = fmt.Sprintf("%s: VM %s; infrastructure Ready=%s (not Kubernetes Ready)", role, guest.Domain, guest.ProviderReady)
			}
			if guest.ProvisioningFailure == nkedemo.TenantInstanceCreateFailed {
				message = role + ": provider reports instance lookup/create failure"
			}
			d.appendActivity(message)
		} else if guest.Available && previous.Available && guest.ConsoleChanged && guest.ConsoleBytes > previous.ConsoleBytes {
			d.appendActivity(role + ": console bytes grew; metadata activity is not guest health")
		}
	}
	d.guests, d.guestsObservedAt = event.Guests, time.Now()
}

func dashboardValidGuests(guests [2]nkedemo.TenantGuestObservation) bool {
	roles := [2]nkedemo.TenantGuestRole{nkedemo.TenantGuestControlPlane, nkedemo.TenantGuestWorker}
	for index, guest := range guests {
		if guest.Role != roles[index] || guest.ConsoleBytes < 0 || guest.ConsoleLastChange < 0 {
			return false
		}
		switch guest.ProvisioningFailure {
		case "", nkedemo.TenantInstanceCreateFailed:
		default:
			return false
		}
		if !guest.Available {
			if guest.Domain != "" && guest.Domain != nkedemo.TenantDomainUnknown || guest.ProviderReady != "" && guest.ProviderReady != nkedemo.TenantConditionUnknown || guest.ConsoleKnown || guest.ConsoleBytes != 0 || guest.ConsoleChanged || guest.ConsoleLastChange != 0 {
				return false
			}
			continue
		}
		switch guest.Domain {
		case nkedemo.TenantDomainUnknown, nkedemo.TenantDomainRunning, nkedemo.TenantDomainPaused, nkedemo.TenantDomainShutOff, nkedemo.TenantDomainCrashed:
		default:
			return false
		}
		switch guest.ProviderReady {
		case nkedemo.TenantConditionTrue, nkedemo.TenantConditionFalse, nkedemo.TenantConditionUnknown:
		default:
			return false
		}
		if !guest.ConsoleKnown && (guest.ConsoleBytes != 0 || guest.ConsoleChanged || guest.ConsoleLastChange != 0) || !guest.ConsoleChanged && guest.ConsoleLastChange != 0 {
			return false
		}
	}
	return true
}

func dashboardTenantSummary(s dashboardSnapshot, now time.Time) string {
	blocker := "Waiting for tenant kubeconfig"
	switch s.tenantStage {
	case "tenant-api":
		blocker = "Waiting for a successful tenant API query"
	case "tenant-nodes":
		blocker = "Waiting for both tenant nodes Ready"
		if s.registered == 2 && s.ready == 2 {
			blocker = "Both nodes Ready; workload and DNS checks pending"
		}
	case "tenant-workload":
		blocker = "Running workload and DNS checks"
	}
	if s.tenantStage == "tenant-kubeconfig" || s.tenantStage == "tenant-api" || s.tenantStage == "tenant-nodes" {
		for index, role := range [...]string{"Control plane", "Worker"} {
			if s.guests[index].ProvisioningFailure == nkedemo.TenantInstanceCreateFailed {
				blocker = role + ": instance lookup/create failed (provider report)"
				break
			}
		}
	}
	var checkpoints strings.Builder
	completed := 0
	for index, label := range [...]string{"Kubeconfig", "API", "Nodes", "Workload/DNS"} {
		state, _, _ := s.tenantStages[index].status(1)
		color, marker := "goldenrod", "○"
		switch state {
		case "observed":
			color, marker = "lightgreen", "✓"
			completed++
		case "running":
			color, marker = "lightcyan", "▶"
		case "failed":
			color, marker = "lightcoral", "✗"
		}
		fmt.Fprintf(&checkpoints, " [%s]%s %s[-]", color, marker, label)
	}
	lines := []string{fmt.Sprintf("[lightcyan::b]%s[-::-]", blocker), fmt.Sprintf("Checks %s %d/4%s", tview.Escape("["+strings.Repeat("#", completed)+strings.Repeat("-", 4-completed)+"]"), completed, checkpoints.String())}
	for index, role := range [...]string{"Control plane", "Worker"} {
		guest := s.guests[index]
		row := fmt.Sprintf("%-13s  VM unavailable; guest observation unknown", role)
		if guest.Available {
			console := "console unavailable"
			if guest.ConsoleKnown {
				console = fmt.Sprintf("console %s; baseline", dashboardConsoleSize(guest.ConsoleBytes))
				if guest.ConsoleChanged {
					console = fmt.Sprintf("console %s; growth age %s", dashboardConsoleSize(guest.ConsoleBytes), dashboardGuestAge(guest.ConsoleLastChange))
				}
			}
			row = fmt.Sprintf("%-13s VM %-8s Infra %s %s", role, guest.Domain, guest.ProviderReady, console)
		}
		if guest.ProvisioningFailure == nkedemo.TenantInstanceCreateFailed {
			domain := guest.Domain
			if domain == "" {
				domain = nkedemo.TenantDomainUnknown
			}
			row = fmt.Sprintf("[lightcoral]%-13s  instance lookup/create failed; VM observation %s[-]", role, domain)
		}
		lines = append(lines, row)
	}
	lines = append(lines, "VM/console activity and Infra status are not Kubernetes readiness.")
	nodes := "Kubernetes: registered unknown; Ready unknown (no node query yet)"
	if s.nodesObserved {
		nodes = fmt.Sprintf("Kubernetes: %d/2 registered · %d/2 Ready", s.registered, s.ready)
	}
	lines = append(lines, nodes)
	conditions := "NKE: Provisioning unknown; Provisioned unknown"
	if s.provisioning != "" || s.provisioned != "" {
		conditions = fmt.Sprintf("NKE: Provisioning=%s · Provisioned=%s", dashboardConditionLabel(s.provisioning), dashboardConditionLabel(s.provisioned))
	}
	lines = append(lines, conditions)
	lines = append(lines, fmt.Sprintf("State unchanged: %s · last wait report: %s ago", max(time.Duration(0), now.Sub(s.tenantChangedAt)).Truncate(time.Second), max(time.Duration(0), now.Sub(s.observedAt)).Truncate(time.Second)))
	guests := "Guest snapshot: not observed; guest progress unknown"
	if !s.guestsObservedAt.IsZero() {
		guests = fmt.Sprintf("Guest snapshot: %s old; growth ages measured at snapshot", max(time.Duration(0), now.Sub(s.guestsObservedAt)).Truncate(time.Second))
	}
	lines = append(lines, guests)
	lines = append(lines, fmt.Sprintf("Tenant budget: %s elapsed · Timeout remaining: %s (not an ETA)", (s.elapsed+max(time.Duration(0), now.Sub(s.observedAt))).Truncate(time.Second), max(time.Duration(0), s.deadline.Sub(now)).Truncate(time.Second)))
	return strings.Join(lines, "\n")
}

func dashboardConsoleSize(bytes int64) string {
	if bytes >= 1<<30 {
		return fmt.Sprintf("%d GiB", bytes/(1<<30))
	}
	if bytes >= 1<<20 {
		return fmt.Sprintf("%d MiB", bytes/(1<<20))
	}
	return fmt.Sprintf("%d KiB", bytes/1024)
}

func dashboardGuestAge(age time.Duration) string {
	if age >= 24*time.Hour {
		return "over 24h"
	}
	return age.Truncate(time.Second).String()
}

func dashboardConditionLabel(condition nkedemo.TenantConditionStatus) string {
	switch condition {
	case nkedemo.TenantConditionTrue, nkedemo.TenantConditionFalse, nkedemo.TenantConditionUnknown:
		return string(condition)
	default:
		return "Unknown"
	}
}

func (d *dashboard) snapshot() dashboardSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	copy := d.dashboardSnapshot
	copy.activity = append([]string(nil), d.activity...)
	copy.buildSteps = slices.Clone(d.buildSteps)
	copy.buildOutputs = slices.Clone(d.buildOutputs)
	copy.buildVertices = slices.Clone(d.buildVertices)
	copy.buildEdges = slices.Clone(d.buildEdges)
	copy.buildLogs = make(map[string]dashboardOSLogs, len(d.buildLogs))
	for operation, logs := range d.buildLogs {
		copy.buildLogs[operation] = logs
	}
	if len(d.partial) != 0 {
		line := []rune(terminalText(string(d.partial)))
		copy.activity = append(copy.activity, string(line[:min(len(line), dashboardRunes)]))
	}
	if len(copy.activity) > dashboardLines {
		copy.activity = copy.activity[len(copy.activity)-dashboardLines:]
	}
	return copy
}

func dashboardLogLocator(s dashboardSnapshot) string {
	if s.buildLogPath == "" {
		return "Details: retained private build and deployment logs"
	}
	return "Private build log: " + tview.Escape(terminalText(s.buildLogPath))
}

func (d *dashboard) input(event *tcell.EventKey) (cancel bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	if event.Key() == tcell.KeyCtrlC || event.Key() == tcell.KeyRune && (event.Rune() == 'q' || event.Rune() == 'Q') {
		d.confirmCancel = true
		return false
	}
	switch event.Key() {
	case tcell.KeyUp, tcell.KeyDown:
		if dashboardBuildActive(d.dashboardSnapshot) && len(d.buildSteps) > 0 {
			d.buildDetails, d.offset = true, 0
			selected := dashboardSelectedStep(d.dashboardSnapshot)
			order := dashboardBuildOrder(d.dashboardSnapshot)
			position := slices.Index(order, selected)
			if event.Key() == tcell.KeyUp {
				d.buildSelected = order[max(0, position-1)]
			} else {
				d.buildSelected = order[min(len(order)-1, position+1)]
			}
		}
	case tcell.KeyEnter:
		if dashboardBuildActive(d.dashboardSnapshot) {
			d.buildDetails = !d.buildDetails
			d.offset = 0
		}
	case tcell.KeyEscape:
		// Escape is navigation, never consent to cancel owned work. Repeated
		// Escape after closing details must be as harmless as the first press.
		d.buildDetails = false
		d.offset = 0
	case tcell.KeyTab:
		d.focus = (d.focus + 1) % 3
	case tcell.KeyPgUp:
		d.focus = 2
		if dashboardBuildActive(d.dashboardSnapshot) && d.buildDetails {
			d.offset = max(0, d.offset-10)
		} else {
			d.offset = min(dashboardLines, d.offset+10)
		}
	case tcell.KeyPgDn:
		d.focus = 2
		if dashboardBuildActive(d.dashboardSnapshot) && d.buildDetails {
			d.offset = min(dashboardLines, d.offset+10)
		} else {
			d.offset = max(0, d.offset-10)
		}
	case tcell.KeyEnd:
		d.buildDetails = false
		d.offset = 0
	}
	if event.Key() == tcell.KeyRune && event.Rune() == 'a' {
		d.buildDetails = false
		d.buildSelected = -1
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
			color, marker = "lightsalmon", "■"
		}
		if required == 2 && observed == 1 && state == "pending" {
			detail = "1/2 pending"
		} else if required == 2 && state == "running" {
			detail = fmt.Sprintf("%d/2 · %s", observed, detail)
		}
		fmt.Fprintf(&rows, "[%s]%s[-] %-23s %s\n", color, marker, label, detail)
	}
	siteTotal := 0
	for index, stage := range dashboardSiteStages {
		if index == 2 && !s.siteOVN {
			continue
		}
		siteTotal++
		required := 1
		if stage.operations[1] != "" {
			required = 2
		}
		write(stage.label, s.siteStages[index], required)
	}
	site := fmt.Sprintf("[lightcyan::b]Site stages · %d/%d checked[-::-]\n", checked, siteTotal) + rows.String()
	rows.Reset()
	checked = 0
	for index, label := range dashboardTenantStages {
		write(label, s.tenantStages[index], 1)
	}
	tenant := fmt.Sprintf("[lightcyan::b]Tenant stages · %d/%d checked[-::-]\n", checked, len(dashboardTenantStages)) + rows.String()
	return site + "\n" + tenant, siteTotal + len(dashboardTenantStages) + 3
}

func (d *dashboard) draw(screen tcell.Screen, now time.Time, canceling bool) {
	s := d.snapshot()
	width, height := screen.Size()
	content := max(0, height-5)
	screen.SetStyle(tcell.StyleDefault.Background(tcell.NewHexColor(0x1c2633)).Foreground(tcell.ColorLightSteelBlue))
	screen.Clear()
	d.header.SetText(fmt.Sprintf("[lightcyan::b] NKE · local ARM64 build & demo[-::-]    %s elapsed\n [goldenrod]UNQUALIFIED local diagnostic · not a signed Release[-]", max(time.Duration(0), now.Sub(s.started)).Truncate(time.Second)))
	if dashboardBuildActive(s) && !canceling {
		d.drawBuild(screen, s, now, width, height)
		return
	}
	d.phasesView.SetTitle(" Phase map ")
	d.phasesView.SetWrap(true)
	d.stateView.SetTitle(" Observed state ")
	d.activityView.SetWrap(false)
	var phases strings.Builder
	names := [...]string{"Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup"}
	colors := map[string]string{"pending": "goldenrod", "running": "lightcyan", "observed": "lightsteelblue", "complete": "lightgreen", "failed": "lightcoral", "stopped": "lightsalmon"}
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
		state = "[lightcoral::b]CANCELING · waiting for work owner[-::-]\n" + current
		if s.forceAvailable {
			if s.confirmForce {
				state += "\n\n[lightcoral::b]FORCE EXIT NOW?[-::-]\n" + tview.Escape("[y] Exit immediately   [n / Esc] Keep waiting")
			} else {
				state += "\n\n[lightcoral::b][ f ] FORCE EXIT[-::-] · confirmation required"
			}
			state += "\nOwned processes, VMs, containers and network changes may remain."
			if s.recoveryWork != "" {
				state += "\nRecovery workspace: " + tview.Escape(terminalText(s.recoveryWork))
			}
		} else {
			state += "\nCancellation requested; waiting for work to stop and its owner to return."
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
	tenantActive := !canceling && !s.observedAt.IsZero() && s.phases[3].state == "running"
	// Keep enough inner width for the role rows and observation clocks. A narrow
	// side-by-side pane wraps these rows and can hide the timeout at 24 lines.
	tenantStacked := tenantActive && width < 112
	if tenantActive {
		d.stateView.SetText(dashboardTenantSummary(s, now))
		if tenantStacked {
			var strip strings.Builder
			for index, name := range names {
				marker := "○"
				switch s.phases[index].state {
				case "complete", "observed":
					marker = "✓"
				case "running":
					marker = "▶"
				case "failed":
					marker = "✗"
				}
				fmt.Fprintf(&strip, "[%s]%s%s[-] ", colors[s.phases[index].state], name, marker)
			}
			d.header.SetText(d.header.GetText(false) + "\n " + strip.String())
		}
	}
	activity := s.activity
	d.activityView.SetTitle(" Activity · public diagnostics ")
	d.activityView.SetText(tview.Escape(strings.Join(activity, "\n")))
	help := " Tab focus · PgUp/PgDn activity · End live · q / Ctrl-C confirm cancel"
	if s.confirmCancel {
		help = " Cancel current work? Retention/cleanup follows the selected mode. y/N"
	}
	if canceling {
		help = " Canceling · waiting for work owner; retained resources need explicit cleanup"
		if s.forceAvailable {
			help += " · f force exit"
		}
		if s.confirmForce {
			help = " FORCE EXIT? Cleanup may be incomplete; resources may remain. y/N"
		}
	}
	d.footer.SetText("[lightcyan]" + help + "[-]\n " + dashboardLogLocator(s))
	if width > 90 && !tenantStacked {
		left, upper := width*3/10, min(12, content)
		if tenantActive {
			upper = min(14, content)
		}
		d.phasesView.SetRect(0, 3, left, content)
		d.stateView.SetRect(left, 3, width-left, upper)
		d.activityView.SetRect(left, 3+upper, width-left, content-upper)
	} else {
		// Reserve live status and a small activity window before allocating the
		// checklist. On short terminals, pending rows may clip; the current wait,
		// observation clocks and timeout must remain visible.
		stateHeight := min(10, max(0, content-4))
		if tenantActive {
			stateHeight = min(12, max(0, content-4))
		}
		phaseHeight := min(stageRows+5, max(0, content-stateHeight-4))
		if tenantActive && height <= 24 {
			phaseHeight = 0 // The complete phase strip is already in the header.
		}
		if tenantActive {
			d.stateView.SetRect(0, 3, width, stateHeight)
			d.phasesView.SetRect(0, 3+stateHeight, width, phaseHeight)
		} else {
			d.phasesView.SetRect(0, 3, width, phaseHeight)
			d.stateView.SetRect(0, 3+phaseHeight, width, stateHeight)
		}
		d.activityView.SetRect(0, 3+phaseHeight+stateHeight, width, content-phaseHeight-stateHeight)
	}
	d.header.SetRect(0, 0, width, min(3, height))
	d.footer.SetRect(0, max(0, height-2), width, min(2, height))
	_, _, _, activityHeight := d.activityView.GetInnerRect()
	d.activityView.ScrollTo(max(0, len(activity)-activityHeight-s.offset), 0)
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
