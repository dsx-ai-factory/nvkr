// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"io"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

const (
	LaneStarted        = "lane-started"
	LaneFinished       = "lane-finished"
	LaneFailed         = "lane-failed"
	PhaseStarted       = "phase-started"
	PhaseFinished      = "phase-finished"
	PhaseFailed        = "phase-failed"
	DeploymentStarted  = "deployment-started"
	DeploymentFinished = "deployment-finished"
	DeploymentFailed   = "deployment-failed"
	TenantWaiting      = "tenant-waiting"
	TenantGuests       = "tenant-guests"
	NativeDeployment   = "native-deployment"
	OSBuildLogs        = "os-build-logs"
	BuildPlanSelected  = "build-plan-selected"
)

// Guest observations describe infrastructure and console activity only. They
// never establish guest installation, Kubernetes readiness or artifact proof.
type TenantGuestRole string
type TenantDomainState string
type TenantProvisioningFailure string

type TenantInstanceState string

const (
	TenantInstanceUnknown TenantInstanceState = "unknown"
	TenantInstanceReady   TenantInstanceState = "Ready"
	TenantInstanceError   TenantInstanceState = "Error"
)

const (
	TenantGuestControlPlane    TenantGuestRole           = "control-plane"
	TenantGuestWorker          TenantGuestRole           = "worker"
	TenantDomainUnknown        TenantDomainState         = "unknown"
	TenantDomainRunning        TenantDomainState         = "running"
	TenantDomainPaused         TenantDomainState         = "paused"
	TenantDomainShutOff        TenantDomainState         = "shut-off"
	TenantDomainCrashed        TenantDomainState         = "crashed"
	TenantInstanceCreateFailed TenantProvisioningFailure = "instance-create-failed"
)

type TenantGuestObservation struct {
	InstanceState TenantInstanceState
	Role          TenantGuestRole
	Available     bool
	Domain        TenantDomainState
	ProviderReady TenantConditionStatus
	// A source-owned provider failure can precede creation of an owned domain.
	// Empty means no mapped failure observation, never provisioning success.
	ProvisioningFailure TenantProvisioningFailure
	ConsoleKnown        bool
	ConsoleBytes        int64
	ConsoleChanged      bool
	// Age since this observer saw positive growth of the same console inode,
	// not a guest timestamp. Baseline, replacement and truncation are not growth.
	ConsoleLastChange time.Duration
}

// Process observations apply only to the opted-in public build launcher.
// Started proves cmd.Start returned; exited proves the owner joined the
// child and its private group. Neither describes descendant health.
const (
	ProcessStarted = "process-started"
	ProcessExited  = "process-exited"
)

// TenantConditionStatus is a descriptive NVKE API observation, not node readiness
// or NVKR evidence. Values follow the selected NVKE controller API model.
type TenantConditionStatus string

const (
	TenantConditionTrue    TenantConditionStatus = "True"
	TenantConditionFalse   TenantConditionStatus = "False"
	TenantConditionUnknown TenantConditionStatus = "Unknown"
)

func presentedTenantCondition(status TenantConditionStatus) TenantConditionStatus {
	switch status {
	case TenantConditionTrue, TenantConditionFalse, TenantConditionUnknown:
		return status
	default:
		return ""
	}
}

// ProgressEvent describes an observed operation boundary, never percentage,
// artifact verification, Release authority, arguments or private diagnostics.
type ProgressEvent struct {
	Kind            string
	Name            string
	Status          string
	Conformance     bool
	ExpectedNodes   int
	RegisteredNodes int
	ReadyNodes      int
	Provisioning    TenantConditionStatus
	Provisioned     TenantConditionStatus
	Elapsed         time.Duration
	Remaining       time.Duration
	Guests          [3]TenantGuestObservation
	BuildLogs       buildactivity.Record
	// Foreground-only descriptions; never expand the retained progress protocol.
	BuildPlan  *progress.Snapshot `json:"-"`
	BuildGraph *planview.Graph    `json:"-"`
}

// Project the selected Plan through its existing owners, never log text. A view
// failure leaves the presenter's unknown fallback and cannot prevent execution.
func reportBuildPlan(writer io.Writer, plan *factory.Plan, attempt, name string) {
	if _, ok := writer.(ProgressReporter); !ok {
		return
	}
	event := ProgressEvent{Kind: BuildPlanSelected, Name: name}
	defer func() { reportProgress(writer, event) }()
	monitor, err := factory.NewProgress(plan, attempt)
	if err != nil {
		return
	}
	document, err := planview.Inspect(plan)
	if err != nil {
		return
	}
	graph, err := document.GraphSnapshot("artifacts")
	if err != nil {
		return
	}
	snapshot := monitor.Snapshot()
	event.BuildPlan, event.BuildGraph = &snapshot, &graph
}

// Tenant polling reports interpreted observations, not successful subprocess
// exits. Repeat an unchanged wait at most once per 30 seconds; transitions are
// immediate. No private response, node name or condition message is exposed.
type tenantProgress struct {
	writer                  io.Writer
	started, deadline, last time.Time
	previous                ProgressEvent
	conditions              tenantConditions
	expectedNodes           int
}

func (p *tenantProgress) update(now time.Time, stage string, nodes []tenantNode) {
	expected := p.expectedNodes
	if expected == 0 {
		expected = nativeTenantNodeCount(false)
	}
	event := ProgressEvent{Kind: TenantWaiting, Name: stage, ExpectedNodes: expected, RegisteredNodes: len(nodes),
		Provisioning: presentedTenantCondition(p.conditions.Provisioning.Status),
		Provisioned:  presentedTenantCondition(p.conditions.Provisioned.Status)}
	for _, node := range nodes {
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				event.ReadyNodes++
				break
			}
		}
	}
	if event == p.previous && now.Sub(p.last) < 30*time.Second {
		return
	}
	p.previous, p.last = event, now
	event.Elapsed = max(time.Duration(0), now.Sub(p.started)).Truncate(time.Second)
	event.Remaining = max(time.Duration(0), p.deadline.Sub(now)).Truncate(time.Second)
	reportProgress(p.writer, event)
}

// ProgressReporter is optional on an existing progress writer. Report is called
// synchronously; presenters must return promptly and must not start work.
type ProgressReporter interface {
	Report(ProgressEvent)
}

func reportProgress(writer io.Writer, event ProgressEvent) {
	reporter, ok := writer.(ProgressReporter)
	if !ok {
		return
	}
	// Presentation is not an authority gate. A broken presenter cannot unwind
	// an owned child lifecycle or prevent restoration of dependent controls.
	defer func() { _ = recover() }()
	reporter.Report(event)
}

type progressBoundary struct {
	writer   io.Writer
	name     string
	finished string
	failed   string
	once     sync.Once
}

func beginProgress(writer io.Writer, name, started, finished, failed string) *progressBoundary {
	if !safePhase(name) {
		name = "command"
	}
	boundary := &progressBoundary{writer: writer, name: name, finished: finished, failed: failed}
	reportProgress(writer, ProgressEvent{Kind: started, Name: name, Status: "started"})
	return boundary
}

func (boundary *progressBoundary) finish(err error) {
	boundary.once.Do(func() {
		kind, status := boundary.finished, "finished"
		if err != nil {
			kind, status = boundary.failed, "failed"
		}
		reportProgress(boundary.writer, ProgressEvent{Kind: kind, Name: boundary.name, Status: status})
	})
}
