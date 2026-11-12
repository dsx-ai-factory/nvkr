// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

// The factory issues scope only after authenticating every build task and its
// exact carriers. This adapter borrows that live scope; descriptions alone do
// not enter the shared artifact-check gate as authenticated provider evidence.
type tektonQualificationSource struct {
	ctx         context.Context
	task        *factory.TektonEvidenceTask
	planDigest  string
	description qualificationorigin.Description
}

func newTektonQualificationSource(ctx context.Context, task *factory.TektonEvidenceTask, plan *factory.Plan) (*tektonQualificationSource, error) {
	run, err := task.RunContext(ctx)
	if err != nil {
		return nil, err
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	actual, err := task.Plan(ctx)
	if err != nil {
		return nil, err
	}
	want, err := actual.IdentityDigest()
	if err != nil {
		return nil, err
	}
	if digest != want {
		return nil, e.New(e.CodePipelineDrift, "Tekton qualification substituted its authenticated Plan")
	}
	description := qualificationorigin.Description{Provider: "tekton", InvocationID: run.InvocationID(), BuilderID: run.BuilderID()}
	if err := description.Validate(); err != nil {
		return nil, err
	}
	return &tektonQualificationSource{ctx: ctx, task: task, planDigest: digest, description: description}, nil
}

func (source *tektonQualificationSource) Require() error {
	if source == nil || source.task == nil {
		return e.New(e.CodeProvenanceMissing, "Tekton qualification requires live provider authority")
	}
	return source.task.Require(source.ctx)
}

func (source *tektonQualificationSource) Description() (qualificationorigin.Description, error) {
	if err := source.Require(); err != nil {
		return qualificationorigin.Description{}, err
	}
	return source.description, nil
}

func (source *tektonQualificationSource) requirePlan(plan *factory.Plan) error {
	if err := source.Require(); err != nil {
		return err
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	if digest != source.planDigest {
		return e.New(e.CodePipelineDrift, "Tekton qualification differs from the authenticated Plan")
	}
	return nil
}
