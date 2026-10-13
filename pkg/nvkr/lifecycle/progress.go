// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

// NewProgress describes a prepared Run without exposing its executable Plan.
// The monitor remains descriptive and cannot alter frozen requirements.
func NewProgress(prepared *PreparedRun) (*progress.Monitor, error) {
	if prepared == nil || prepared.plan == nil || prepared.run == nil {
		return nil, e.New(e.CodeUsage, "progress requires a prepared Run")
	}
	monitor, err := factory.NewProgress(prepared.plan, prepared.run.Attempt)
	if err != nil {
		return nil, err
	}
	description := monitor.Snapshot()
	// A prepared local Run is pending and static-unqualified, even if its Plan
	// also describes a hosted lane. Only the execution's returned Run supplies
	// its resulting local observation lane.
	description.Lane = prepared.run.Lane
	return progress.New(description), nil
}

func finishProgress(monitor *progress.Monitor, run *Run, err error, dry bool) {
	if monitor == nil {
		return
	}
	status, lane := "unavailable", ""
	var observed []string
	if run != nil {
		status, lane = run.Status, run.Lane
		for _, output := range run.Observed {
			observed = append(observed, output.Name)
		}
	}
	code := ""
	if err != nil {
		code = string(e.CodeInternal)
		var coded *e.Error
		if errors.As(err, &coded) {
			code = string(coded.Code)
		}
	}
	// A returned live Run is issued only after its owning record commit. Do not
	// add filesystem probes on rejected admission merely for presentation.
	monitor.Finish(status, lane, observed, code, run != nil && !dry,
		run != nil && run.Status == RunComplete && err != nil)
}
