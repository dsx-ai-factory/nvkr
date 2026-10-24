// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/internal/tuitheme"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	dashboardLines = 80
	dashboardRunes = 4096
	dashboardSteps = 512
)

type dashboardPhase struct{ state, detail string }

const dashboardConformancePhase = 6

type dashboardStage struct {
	state   string
	started time.Time
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
// milestones. Each completed observation belongs to exactly one operation.
type dashboardSiteStage struct {
	label     string
	operation string
}

var dashboardSiteStages = [...]dashboardSiteStage{
	{"Site guest Ready (NICo)", "native-site-guest"},
	{"Site packages prepared", "native-site-preparation"},
	{"NVKE site bootstrap", "host-nvke-bootstrap"},
	{"Cluster API installed", "host-site-capi"},
	{"Site stack installed", "host-nvke-deploy"},
	{"CAPI SPIFFE authentication", "host-site-spiffe-exec"},
	{"Site host identity handoff", "native-site-identity-handoff"},
	{"NVKE API accessible", "check-05-install-site-controller"},
}

var dashboardTenantStages = [...]string{"Tenant kubeconfig", "Tenant API query", "Nodes Ready", "Workload and DNS"}

type dashboardSnapshot struct {
	started, observedAt, deadline time.Time
	elapsed                       time.Duration
	phases                        [dashboardConformancePhase + 1]dashboardPhase
	activity                      []string
	operatorPage                  string
	tenant, current               string
	focus, offset                 int
	registered, ready             int
	expectedNodes                 int
	nodesObserved                 bool
	confirmCancel                 bool
	confirmForce, forceAvailable  bool
	currentStarted                time.Time
	tenantChangedAt               time.Time
	tenantStage                   string
	provisioning, provisioned     nvkedemo.TenantConditionStatus
	guests                        [3]nvkedemo.TenantGuestObservation
	guestsObservedAt              time.Time
	siteStages                    [len(dashboardSiteStages)]dashboardStage
	conformanceSelected           bool
	tenantStages                  [len(dashboardTenantStages)]dashboardStage
	buildSteps                    []dashboardBuildStep
	buildPreview, buildTruncated  bool
	buildChangedAt                time.Time
	buildOutputs                  []string
	buildRequired                 int
	buildRun                      string
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
	palette := tuitheme.Colors(false)
	d := &dashboard{dashboardSnapshot: dashboardSnapshot{started: time.Now(), tenant: "Tenant readiness has not been observed.", buildSelected: -1, buildDetails: true}}
	for i := range d.phases {
		d.phases[i] = dashboardPhase{"pending", "No operation observed yet"}
	}
	pane := func(title string) *tview.TextView {
		view := tview.NewTextView().SetDynamicColors(true).SetTextColor(palette.Foreground).SetWrap(true)
		view.SetBackgroundColor(palette.Background).SetBorder(true).SetBorderColor(palette.Border).SetTitle(title).SetTitleColor(palette.Accent)
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
	if strings.HasPrefix(line, "[log +") || strings.HasPrefix(line, "[build +") {
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
	message, ok := strings.CutPrefix(line, "==> nvke-node-non-fde.qemu.nvke-non-fde: ")
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
	case "tenant-provisioning":
		return 3, "Provision QCOW2 control plane and worker"
	case "tenant-cni-readiness":
		return 4, "Wait for tenant Calico node agents before DNS checks"
	case "tenant-dns-readiness":
		return 4, "Wait for CoreDNS and node-local DNS rollout before the smoke test"
	case "native-conformance":
		return dashboardConformancePhase, "Kubernetes conformance validation and original report capture"
	case "tenant-cleanup":
		return 5, "Clean up the owned tenant"
	case "tenant-diagnostics":
		return 5, "Collect bounded private guest diagnostics"
	case "tenant-delete":
		return 5, "Ask NVKE to delete the owned tenant"
	case "tenant-deletion-wait":
		return 5, "Wait for NVKE to confirm tenant deletion"
	case "network-cleanup", "registry-cleanup", "owned-node-cleanup":
		return 5, "Restore owned demo resources"
	}
	if name == "native-site-ready" {
		return 2, "Prepare and check the native site"
	}
	for _, stage := range dashboardSiteStages {
		if name == stage.operation {
			return 2, stage.label
		}
	}
	return -1, ""
}

func (stage *dashboardStage) observe(state string, now time.Time) {
	if stage.state == "failed" || stage.state == "observed" && state != "failed" {
		return
	}
	if state == "running" && stage.started.IsZero() {
		stage.started = now
	}
	stage.state = state
}

func (stage dashboardStage) status() string {
	if stage.state == "" {
		return "pending"
	}
	return stage.state
}

func (d *dashboard) observeSiteStage(event nvkedemo.ProgressEvent, state string) string {
	for index, stage := range dashboardSiteStages {
		if event.Name == stage.operation {
			d.siteStages[index].observe(state, time.Now())
			return stage.label
		}
	}
	return ""
}

func stopDashboardStages(stages []dashboardStage, state string) {
	for index := range stages {
		if stages[index].state == "running" {
			stages[index].state = state
		}
	}
}

func (d *dashboard) Report(event nvkedemo.ProgressEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if event.Name == "tenant-provisioning" && event.Kind == nvkedemo.DeploymentStarted {
		d.resetTenantObservation()
	}
	switch event.Kind {
	case nvkedemo.BuildPlanSelected:
		d.observeBuildPlan(event)
		return
	case nvkedemo.NativeDeployment:
		if event.ExpectedNodes != 0 && (requestedTenantNodes(event.ExpectedNodes) == 0 || event.Conformance && event.ExpectedNodes != 3) {
			return
		}
		d.conformanceSelected = event.Conformance
		if event.ExpectedNodes != 0 {
			d.expectedNodes = event.ExpectedNodes
		}
		return
	}
	if event.Kind == nvkedemo.OSBuildLogs {
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
	if event.Kind == nvkedemo.ProcessStarted || event.Kind == nvkedemo.ProcessExited {
		if event.Name == "build-all" || event.Name == "native-tools-run" {
			if event.Kind == nvkedemo.ProcessExited {
				d.buildProcess = "exited"
			} else if d.buildProcess != "exited" {
				d.buildProcess = "started"
			}
		}
		return
	}
	if event.Kind == nvkedemo.TenantWaiting {
		d.tenantObservation(event)
		return
	}
	if event.Kind == nvkedemo.TenantGuests {
		d.tenantGuestObservation(event)
		return
	}
	state := ""
	switch event.Kind {
	case nvkedemo.LaneStarted, nvkedemo.PhaseStarted, nvkedemo.DeploymentStarted:
		state = "running"
	case nvkedemo.LaneFinished, nvkedemo.PhaseFinished, nvkedemo.DeploymentFinished:
		state = "observed"
	case nvkedemo.LaneFailed, nvkedemo.PhaseFailed, nvkedemo.DeploymentFailed:
		state = "failed"
	default:
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
	if index == dashboardConformancePhase {
		d.conformanceSelected = true
		if state == "running" {
			// A retained tenant may run another explicit suite after an outcome.
			d.phases[index] = dashboardPhase{}
		}
	}
	if event.Kind == nvkedemo.PhaseStarted || event.Kind == nvkedemo.PhaseFinished || event.Kind == nvkedemo.PhaseFailed {
		if stageLabel := d.observeSiteStage(event, state); stageLabel != "" {
			label = stageLabel
		}
	}
	if event.Kind == nvkedemo.LaneFinished && event.Name == "artifacts" || event.Kind == nvkedemo.PhaseFinished && (event.Name == "installer-preparation" || index == dashboardConformancePhase) {
		state = "complete"
	}
	phaseState := state
	if index == 2 && state == "observed" {
		phaseState = "running"
	}
	if event.Name == "native-site-ready" && event.Kind == nvkedemo.PhaseFinished {
		phaseState = "complete"
	}
	if d.phases[index].state != "failed" && (d.phases[index].state != "complete" || phaseState == "failed") {
		d.phases[index] = dashboardPhase{phaseState, label}
	}
	d.current = label
	if state == "running" {
		d.currentStarted = time.Now()
	} else {
		d.currentStarted = time.Time{}
	}
	d.appendActivity(state + ": " + label)
	if event.Name == "tenant-provisioning" && event.Kind == nvkedemo.DeploymentFinished {
		if d.tenantStages[3].state == "running" {
			d.tenantStages[3].observe("observed", time.Now())
		}
		d.phases[3] = dashboardPhase{"complete", "Owning tenant operation completed"}
		d.phases[4] = dashboardPhase{"complete", "Workload and DNS checks completed"}
	}
	if event.Name == "tenant-provisioning" && event.Kind == nvkedemo.DeploymentFailed {
		// The owner reports every termination here, including cancellation.
		// It does not establish that an unfinished readiness check failed.
		stopDashboardStages(d.tenantStages[:], "stopped")
	}
	if event.Name == "tenant-provisioning" && event.Kind == nvkedemo.DeploymentFailed && d.phases[4].state == "running" {
		d.phases[4] = dashboardPhase{"stopped", "Tenant operation ended; workload/DNS completion unconfirmed"}
	}
}

func (d *dashboard) tenantObservation(event nvkedemo.ProgressEvent) {
	expected := requestedTenantNodes(event.ExpectedNodes)
	if expected == 0 {
		return
	}
	if event.RegisteredNodes < 0 || event.RegisteredNodes > expected || event.ReadyNodes < 0 || event.ReadyNodes > event.RegisteredNodes || event.Elapsed < 0 || event.Remaining < 0 {
		return
	}
	message := ""
	stage := 0
	now := time.Now()
	previousRegistered, previousReady := d.registered, d.ready
	d.expectedNodes = expected
	switch event.Name {
	case "tenant-kubeconfig":
		d.tenantStages[0].observe("running", now)
		message = "Waiting for NVKE to publish tenant kubeconfig. Guest boot and node readiness are not yet observed."
	case "tenant-api":
		stage = 1
		if d.tenantStages[0].state == "running" {
			d.tenantStages[0].observe("observed", now)
		}
		d.tenantStages[1].observe("running", now)
		message = "Kubeconfig available. Waiting for a successful tenant API node query; readiness is not yet observed."
	case "tenant-nodes":
		stage = 2
		if d.tenantStages[1].state == "running" {
			d.tenantStages[1].observe("observed", now)
		}
		d.tenantStages[2].observe("running", now)
		d.registered, d.ready, d.nodesObserved = event.RegisteredNodes, event.ReadyNodes, true
		message = fmt.Sprintf("QCOW2 tenant: %d/%d registered · %d/%d Ready. Waiting for control plane and workers.", event.RegisteredNodes, expected, event.ReadyNodes, expected)
		if event.RegisteredNodes == expected && event.ReadyNodes == expected {
			d.tenantStages[2].observe("observed", now)
			message = fmt.Sprintf("QCOW2 tenant: %d/%d registered · %d/%d Ready. Workload and DNS checks still required.", expected, expected, expected, expected)
		}
	case "tenant-cloud-init":
		stage = 2
		message = "Checking actual host cloud-init completion on the Ready tenant nodes; workload and DNS checks remain pending."
	case "tenant-workload":
		stage = 3
		d.tenantStages[3].observe("running", now)
		message = "Running workload and cluster DNS checks. Successful completion is still required."
		d.phases[4] = dashboardPhase{"running", "Check workload execution and cluster DNS"}
	default:
		return
	}
	conditionChanged := false
	for _, condition := range []struct {
		observed nvkedemo.TenantConditionStatus
		stored   *nvkedemo.TenantConditionStatus
	}{{event.Provisioning, &d.provisioning}, {event.Provisioned, &d.provisioned}} {
		switch condition.observed {
		case nvkedemo.TenantConditionTrue, nvkedemo.TenantConditionFalse, nvkedemo.TenantConditionUnknown:
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
				activity += fmt.Sprintf(" · %d/%d registered · %d/%d Ready", d.registered, expected, d.ready, expected)
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
func (d *dashboard) tenantGuestObservation(event nvkedemo.ProgressEvent) {
	if !dashboardValidGuests(event.Guests) {
		return
	}
	for index, role := range tenantGuestRoles(event.ExpectedNodes) {
		guest, previous := event.Guests[index], d.guests[index]
		if guest.Available != previous.Available || guest.Domain != previous.Domain || guest.ProviderReady != previous.ProviderReady || guest.ProvisioningFailure != previous.ProvisioningFailure || guest.InstanceState != previous.InstanceState {
			message := role + ": guest observation unavailable"
			if guest.Available {
				message = fmt.Sprintf("%s: NICo %s; Infra Ready=%s; VM %s", role, guest.InstanceState, guest.ProviderReady, guest.Domain)
			}
			if guest.ProvisioningFailure == nvkedemo.TenantInstanceCreateFailed {
				message = role + ": provider reports instance lookup/create failure"
			}
			d.appendActivity(message)
		} else if guest.Available && previous.Available && guest.ConsoleChanged && guest.ConsoleBytes > previous.ConsoleBytes {
			d.appendActivity(role + ": console bytes grew; metadata activity is not guest health")
		}
	}
	d.guests, d.guestsObservedAt = event.Guests, time.Now()
}

func dashboardValidGuests(guests [3]nvkedemo.TenantGuestObservation) bool {
	roles := [3]nvkedemo.TenantGuestRole{nvkedemo.TenantGuestControlPlane, nvkedemo.TenantGuestWorker, nvkedemo.TenantGuestWorker}
	for index, guest := range guests {
		if index == 2 && guest == (nvkedemo.TenantGuestObservation{}) {
			continue
		}
		if guest.Role != roles[index] || guest.ConsoleBytes < 0 || guest.ConsoleLastChange < 0 {
			return false
		}
		switch guest.ProvisioningFailure {
		case "", nvkedemo.TenantInstanceCreateFailed:
		default:
			return false
		}
		switch guest.InstanceState {
		case "", nvkedemo.TenantInstanceUnknown, nvkedemo.TenantInstanceReady, nvkedemo.TenantInstanceError:
		default:
			return false
		}
		if !guest.Available {
			if guest.Domain != "" && guest.Domain != nvkedemo.TenantDomainUnknown || guest.ProviderReady != "" && guest.ProviderReady != nvkedemo.TenantConditionUnknown || guest.ConsoleKnown || guest.ConsoleBytes != 0 || guest.ConsoleChanged || guest.ConsoleLastChange != 0 || guest.InstanceState != "" && guest.InstanceState != nvkedemo.TenantInstanceUnknown {
				return false
			}
			continue
		}
		switch guest.Domain {
		case nvkedemo.TenantDomainUnknown, nvkedemo.TenantDomainRunning, nvkedemo.TenantDomainPaused, nvkedemo.TenantDomainShutOff, nvkedemo.TenantDomainCrashed:
		default:
			return false
		}
		switch guest.ProviderReady {
		case nvkedemo.TenantConditionTrue, nvkedemo.TenantConditionFalse, nvkedemo.TenantConditionUnknown:
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
	expected := requestedTenantNodes(s.expectedNodes)
	if expected == 0 {
		expected = 2
	}
	blocker := "Waiting for tenant kubeconfig"
	switch s.tenantStage {
	case "tenant-api":
		blocker = "Waiting for a successful tenant API query"
	case "tenant-nodes":
		blocker = "Waiting for both tenant nodes Ready"
		if expected == 3 {
			blocker = "Waiting for all three tenant nodes Ready"
		}
		if s.registered == expected && s.ready == expected {
			blocker = "Both nodes Ready; workload and DNS checks pending"
			if expected == 3 {
				blocker = "All three nodes Ready; workload and DNS checks pending"
			}
		}
	case "tenant-cloud-init":
		blocker = "Checking host cloud-init on the tenant nodes"
	case "tenant-workload":
		blocker = "Running workload and DNS checks"
	}
	if s.tenantStage == "tenant-kubeconfig" || s.tenantStage == "tenant-api" || s.tenantStage == "tenant-nodes" || s.tenantStage == "tenant-cloud-init" {
		for index, role := range tenantGuestRoles(s.expectedNodes) {
			if s.guests[index].ProvisioningFailure == nvkedemo.TenantInstanceCreateFailed {
				blocker = role + ": instance lookup/create failed (provider report)"
				break
			}
		}
	}
	var checkpoints strings.Builder
	completed := 0
	for index, label := range [...]string{"Kubeconfig", "API", "Nodes", "Workload/DNS"} {
		state := s.tenantStages[index].status()
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
	for index, role := range tenantGuestRoles(s.expectedNodes) {
		guest := s.guests[index]
		var row string
		switch {
		case guest.ProvisioningFailure == nvkedemo.TenantInstanceCreateFailed:
			domain := guest.Domain
			if domain == "" {
				domain = nvkedemo.TenantDomainUnknown
			}
			row = fmt.Sprintf("[lightcoral]%-13s  instance lookup/create failed; VM observation %s[-]", role, domain)
		case !guest.Available:
			row = fmt.Sprintf("%-13s  VM state unobserved; provider observation unavailable", role)
		default:
			vm := "unobserved"
			if guest.Domain != nvkedemo.TenantDomainUnknown {
				vm = string(guest.Domain)
			}
			row = fmt.Sprintf("%-13s NICo %s Infra Ready=%s; VM %s", role, guest.InstanceState, guest.ProviderReady, vm)
			if guest.ConsoleKnown {
				row += fmt.Sprintf("; console %s", dashboardConsoleSize(guest.ConsoleBytes))
			}
		}
		lines = append(lines, row)
	}
	lines = append(lines, "Kubernetes node readiness is reported separately below.")
	nodes := "Kubernetes: registered unknown; Ready unknown (no node query yet)"
	if s.nodesObserved {
		nodes = fmt.Sprintf("Kubernetes: %d/%d registered · %d/%d Ready", s.registered, expected, s.ready, expected)
	}
	lines = append(lines, nodes)
	conditions := "NVKE: Provisioning unknown; Provisioned unknown"
	if s.provisioning != "" || s.provisioned != "" {
		conditions = fmt.Sprintf("NVKE: Provisioning=%s · Provisioned=%s", dashboardConditionLabel(s.provisioning), dashboardConditionLabel(s.provisioned))
	}
	lines = append(lines, conditions)
	lines = append(lines, fmt.Sprintf("State unchanged: %s · last wait report: %s ago", max(time.Duration(0), now.Sub(s.tenantChangedAt)).Truncate(time.Second), max(time.Duration(0), now.Sub(s.observedAt)).Truncate(time.Second)))
	guests := "Provider/VM snapshot: not observed; guest progress unknown"
	if !s.guestsObservedAt.IsZero() {
		guests = fmt.Sprintf("Provider/VM snapshot: %s old; unknown fields remain unobserved", max(time.Duration(0), now.Sub(s.guestsObservedAt)).Truncate(time.Second))
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

func dashboardConditionLabel(condition nvkedemo.TenantConditionStatus) string {
	switch condition {
	case nvkedemo.TenantConditionTrue, nvkedemo.TenantConditionFalse, nvkedemo.TenantConditionUnknown:
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
	write := func(label string, stage dashboardStage) {
		state := stage.status()
		color, marker := "goldenrod", "○"
		detail := state
		switch state {
		case "observed":
			checked++
			color, marker = "lightgreen", "✓"
		case "running":
			color, marker = "lightcyan", "▶"
			if !stage.started.IsZero() {
				detail = max(time.Duration(0), now.Sub(stage.started)).Truncate(time.Second).String()
			}
		case "failed":
			color, marker = "lightcoral", "✗"
		case "stopped":
			color, marker = "lightsalmon", "■"
		}
		fmt.Fprintf(&rows, "[%s]%s[-] %-23s %s\n", color, marker, label, detail)
	}
	for index, stage := range dashboardSiteStages {
		write(stage.label, s.siteStages[index])
	}
	site := fmt.Sprintf("[lightcyan::b]Native site stages · %d/%d checked[-::-]\n", checked, len(dashboardSiteStages)) + rows.String()
	rows.Reset()
	checked = 0
	for index, label := range dashboardTenantStages {
		write(label, s.tenantStages[index])
	}
	tenant := fmt.Sprintf("[lightcyan::b]Tenant stages · %d/%d checked[-::-]\n", checked, len(dashboardTenantStages)) + rows.String()
	return site + "\n" + tenant, len(dashboardSiteStages) + len(dashboardTenantStages) + 3
}

func (d *dashboard) draw(screen tcell.Screen, now time.Time, canceling bool) {
	s := d.snapshot()
	width, height := screen.Size()
	content := max(0, height-5)
	palette := tuitheme.Colors(false)
	screen.SetStyle(tcell.StyleDefault.Background(palette.Background).Foreground(palette.Foreground))
	screen.Clear()
	d.header.SetText(fmt.Sprintf("[lightcyan::b] NVKE · local ARM64 build & demo[-::-]    %s elapsed\n [goldenrod]UNQUALIFIED local diagnostic · not a signed Release[-]", max(time.Duration(0), now.Sub(s.started)).Truncate(time.Second)))
	if s.operatorPage != "" && !canceling && !s.confirmCancel && !s.confirmForce {
		d.activityView.SetTitle(" Recorded kubectl walkthrough · actual observations ")
		d.activityView.SetWrap(false)
		d.activityView.SetText(tview.Escape(s.operatorPage))
		d.activityView.SetRect(0, 3, width, content)
		d.activityView.ScrollToBeginning()
		d.header.SetRect(0, 0, width, min(3, height))
		d.footer.SetText(" Recorded observations · pages advance automatically\n q / Ctrl-C confirm cancel; lifetime follows the selected mode")
		d.footer.SetRect(0, max(0, height-2), width, min(2, height))
		d.activityView.Draw(screen)
		d.header.Draw(screen)
		d.footer.Draw(screen)
		return
	}
	if dashboardBuildActive(s) && !canceling {
		d.drawBuild(screen, s, now, width, height)
		return
	}
	d.phasesView.SetTitle(" Phase map ")
	d.phasesView.SetWrap(true)
	d.stateView.SetTitle(" Observed state ")
	d.activityView.SetWrap(false)
	var phases strings.Builder
	namedPhases := dashboardNamedPhases(s)
	colors := map[string]string{"pending": "goldenrod", "running": "lightcyan", "observed": "lightsteelblue", "complete": "lightgreen", "failed": "lightcoral", "stopped": "lightsalmon"}
	for index, named := range namedPhases {
		phase := named.phase
		if width <= 90 {
			fmt.Fprintf(&phases, "[%s::b]%s[-::-] %s", colors[phase.state], named.name, phase.state)
			if index%3 == 2 {
				phases.WriteByte('\n')
			} else {
				phases.WriteString(" · ")
			}
		} else {
			fmt.Fprintf(&phases, "[%s::b]%-14s[-::-] %s\n", colors[phase.state], named.name, phase.state)
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
	} else if s.phases[dashboardConformancePhase].state == "running" {
		state += "\nWorkload/DNS completed. Conformance validation is preparing artifacts/tools or executing the suite on this same tenant. Test progress and original report outcome remain pending."
	} else {
		state += "\n" + tview.Escape(s.tenant)
	}
	if !canceling && s.nodesObserved {
		state += fmt.Sprintf("\nLast node query: %d/%d registered · %d/%d Ready", s.registered, requestedTenantNodes(s.expectedNodes), s.ready, requestedTenantNodes(s.expectedNodes))
	}
	if !canceling && (s.provisioning != "" || s.provisioned != "") {
		conditions := []string{}
		if s.provisioning != "" {
			conditions = append(conditions, "Provisioning="+string(s.provisioning))
		}
		if s.provisioned != "" {
			conditions = append(conditions, "Provisioned="+string(s.provisioned))
		}
		state += "\nLast NVKE conditions: " + strings.Join(conditions, " · ")
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
			for _, named := range namedPhases {
				marker := "○"
				switch named.phase.state {
				case "complete", "observed":
					marker = "✓"
				case "running":
					marker = "▶"
				case "failed":
					marker = "✗"
				}
				fmt.Fprintf(&strip, "[%s]%s%s[-] ", colors[named.phase.state], named.name, marker)
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
		color := palette.Border
		if index == s.focus {
			color = palette.Accent
		}
		view.SetBorderColor(color)
		view.Draw(screen)
	}
	d.header.Draw(screen)
	d.footer.Draw(screen)
	// The event-loop owner calls Show and handles terminal failures.
}

// A site-scoped Go caller may create another tenant after natural deletion.
// Keep the site observations, but never reuse a previous tenant's readiness or
// suite/cleanup outcome for that fresh operation.
func (d *dashboard) resetTenantObservation() {
	d.tenantStages = [len(dashboardTenantStages)]dashboardStage{}
	d.guests = [3]nvkedemo.TenantGuestObservation{}
	d.observedAt, d.deadline, d.tenantChangedAt, d.guestsObservedAt, d.tenantActivityAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	d.elapsed, d.registered, d.ready = 0, 0, 0
	d.nodesObserved = false
	d.tenantStage, d.provisioning, d.provisioned = "", "", ""
	d.tenant = "Tenant readiness has not been observed."
	for _, index := range []int{3, 4, 5, dashboardConformancePhase} {
		d.phases[index] = dashboardPhase{"pending", "No operation observed for this tenant yet"}
	}
}

type dashboardNamedPhase struct {
	name  string
	phase dashboardPhase
}

func dashboardNamedPhases(s dashboardSnapshot) []dashboardNamedPhase {
	var result []dashboardNamedPhase
	for _, phase := range []struct {
		index int
		name  string
	}{{0, "Prepare"}, {1, "Artifacts"}, {2, "Site"}, {3, "Tenant"}, {4, "Workload/DNS"}, {dashboardConformancePhase, "Conformance"}, {5, "Cleanup"}} {
		if phase.index == dashboardConformancePhase && !s.conformanceSelected {
			continue
		}
		result = append(result, dashboardNamedPhase{phase.name, s.phases[phase.index]})
	}
	return result
}
