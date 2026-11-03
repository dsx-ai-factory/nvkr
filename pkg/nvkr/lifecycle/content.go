// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func bindReleaseContent(plan *factory.Plan, run *Run) (*Run, error) {
	return bindReleaseContentOutputs(plan.LifecycleOutputs, run)
}

func bindReleaseContentOutputs(outputs []factory.LifecycleOutputPlan, run *Run) (*Run, error) {
	if run == nil || run.Status != RunComplete {
		return nil, e.New(e.CodeGateUnmet, "content binding requires a complete Run")
	}
	contentSubject, err := delivery.ContentDigest(run.Observed)
	if err != nil {
		return nil, err
	}
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Composition = run.Composition.Clone()
	copyRun.Observed = cloneArtifacts(run.Observed)
	copyRun.ContentSubject = contentSubject
	if err := factory.ValidateLifecycleOutputs(outputs, run.Observed); err != nil {
		return nil, err
	}
	return &copyRun, nil
}
