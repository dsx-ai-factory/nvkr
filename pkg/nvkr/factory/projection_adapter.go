// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// referenceProjection is the same-module reference projection plugin. It
// ports the in-binary renderer verbatim and never writes files.
// release is the plugin's own label.
type referenceProjection struct{ engine, release string }

func (plugin referenceProjection) describe(context.Context) (projectionDescription, error) {
	return projectionDescription{Protocol: projectionProtocol, Release: plugin.release, Engine: plugin.engine,
		PlanSchema: PlanSchemaVersion, GeneratorABI: projectionGeneratorABI}, nil
}

func (plugin referenceProjection) emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
	core, err := projectionCoreForEngine(plugin.engine)
	if err != nil {
		return projectionEmission{}, err
	}
	var plan Plan
	if err := canonical.UnmarshalExact(request.Plan, &plan); err != nil {
		return projectionEmission{}, e.Wrap(e.CodeUsage, err, "decoding projection Plan")
	}
	if plan.GeneratorVersion != request.GeneratorVersion {
		return projectionEmission{}, e.New(e.CodeUsage, "projection Plan generator %q is not the requested %q", plan.GeneratorVersion, request.GeneratorVersion)
	}
	if want := projectionLayoutFor(core.layout(&plan)); request.Layout != want {
		return projectionEmission{}, e.New(e.CodeEngineCapability, "projection layout %+v is not this Plan's %+v", request.Layout, want)
	}
	bundle, err := core.render(ctx, &plan)
	if err != nil {
		return projectionEmission{}, err
	}
	members := make([]projectionMember, 0, len(bundle.Files))
	for _, name := range sortedFileNames(bundle.Files) {
		members = append(members, projectionMember{Path: name, Mode: projectionMemberMode, Bytes: bundle.Files[name]})
	}
	return projectionEmission{PlanDigest: bundle.PlanDigest, WorkflowPath: bundle.WorkflowPath, Members: members}, nil
}
