// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"io"
	"sync"
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
)

// ProgressEvent describes an observed operation boundary, never percentage,
// artifact verification, Release authority, arguments or private diagnostics.
type ProgressEvent struct {
	Kind   string
	Name   string
	Status string
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
