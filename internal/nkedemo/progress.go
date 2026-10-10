// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"io"
	"sync"
	"time"
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
)

// ProgressEvent describes an observed operation boundary, never percentage,
// artifact verification, Release authority, arguments or private diagnostics.
type ProgressEvent struct {
	Kind            string
	Name            string
	Status          string
	RegisteredNodes int
	ReadyNodes      int
	Elapsed         time.Duration
	Remaining       time.Duration
}

// Tenant polling reports interpreted observations, not successful subprocess
// exits. Repeat an unchanged wait at most once per 30 seconds; transitions are
// immediate. No private response, node name or condition message is exposed.
type tenantProgress struct {
	writer                  io.Writer
	started, deadline, last time.Time
	previous                ProgressEvent
}

func (p *tenantProgress) update(now time.Time, stage string, nodes []tenantNode) {
	event := ProgressEvent{Kind: TenantWaiting, Name: stage, RegisteredNodes: len(nodes)}
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
