// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func bindReleaseContent(plan *factory.Plan, run *Run) (*Run, error) {
	if run == nil || run.Status != RunComplete {
		return nil, e.New(e.CodeGateUnmet, "content binding requires a complete Run")
	}
	contentSubject, err := delivery.ContentDigest(run.Observed)
	if err != nil {
		return nil, err
	}
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = cloneArtifacts(run.Observed)
	copyRun.ContentSubject = contentSubject
	if err := factory.ValidateLifecycleOutputs(plan.LifecycleOutputs, run.Observed); err != nil {
		return nil, err
	}
	return &copyRun, nil
}
