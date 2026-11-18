// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"maps"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// The public LocalRunResult fields are descriptions. Retain both original and
// relocated Plan identities privately with each concrete runtime production.
type runtimeProductionPlan struct {
	identity string
	current  string
}

func runtimePlanBinding(plan *Plan) (runtimeProductionPlan, error) {
	identity, err := plan.IdentityDigest()
	if err != nil {
		return runtimeProductionPlan{}, err
	}
	current, err := plan.Digest()
	if err != nil {
		return runtimeProductionPlan{}, err
	}
	return runtimeProductionPlan{identity: identity, current: current}, nil
}

func (owner *LocalRunResult) admitInternalRuntime(ctx context.Context, plan *Plan, selected InternalToolPlan) error {
	if selected.Format != definition.OutputOCI {
		return e.New(e.CodeEngineCapability, "internal runtime requires its OCI Plan selection")
	}
	return owner.admitInternalProduction(ctx, plan, selected)
}

func (owner *LocalRunResult) retainInternalRuntime(ctx context.Context, plan *Plan, selected InternalToolPlan, runtime *pipeline.ProducedRuntime) error {
	if err := owner.admitInternalRuntime(ctx, plan, selected); err != nil {
		return err
	}
	binding, err := runtime.BindingContext(ctx)
	if err != nil {
		return err
	}
	if binding.BuildDigest != selected.BuildDigest || binding.Platform != selected.Platform || binding.Component != selected.Component {
		return e.New(e.CodeVerifyFailed, "internal runtime differs from its selected producer")
	}
	planned, err := runtimePlanBinding(plan)
	if err != nil {
		return err
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing {
		return e.New(e.CodeGateUnmet, "internal runtime Run owner is closed")
	}
	if owner.internalExecutables[selected.Name] != nil || owner.internalRuntimes[selected.Name] != nil {
		return e.New(e.CodeVerifyFailed, "internal runtime %q has repeated production", selected.Name)
	}
	if owner.internalRuntimes == nil {
		owner.internalRuntimes = make(map[string]*pipeline.ProducedRuntime)
	}
	if owner.internalRuntimePlans == nil {
		owner.internalRuntimePlans = make(map[string]runtimeProductionPlan)
	}
	owner.internalRuntimes[selected.Name] = runtime
	owner.internalRuntimePlans[selected.Name] = planned
	return nil
}

// internalRuntimeProduction is available to the evidence operation before its
// own completion. It never admits a consumer; borrowInternalRuntime joins the
// completed terminal observation and SBOM separately.
func (owner *LocalRunResult) internalRuntimeProduction(ctx context.Context, plan *Plan, name string) (*pipeline.ProducedRuntime, InternalToolPlan, error) {
	if ctx == nil || owner == nil || plan == nil {
		return nil, InternalToolPlan{}, e.New(e.CodeUsage, "runtime observation requires context, Plan and original Run")
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return nil, InternalToolPlan{}, err
	}
	if identity != owner.PlanDigest {
		return nil, InternalToolPlan{}, e.New(e.CodeVerifyFailed, "runtime observation changes its Plan")
	}
	var selected InternalToolPlan
	for _, candidate := range plan.InternalTools {
		if candidate.Name == name {
			selected = candidate
		}
	}
	if selected.Name == "" || selected.Format != definition.OutputOCI {
		return nil, InternalToolPlan{}, e.New(e.CodeEngineCapability, "runtime observation lacks its exact OCI producer selection")
	}
	owner.diskMu.Lock()
	closed := owner.diskClosed || owner.diskClosing
	runtime := owner.internalRuntimes[name]
	planned := owner.internalRuntimePlans[name]
	owner.diskMu.Unlock()
	if closed || runtime == nil || planned.identity == "" {
		return nil, InternalToolPlan{}, e.New(e.CodeGateUnmet, "runtime observation has no live production")
	}
	current, err := runtimePlanBinding(plan)
	if err != nil {
		return nil, InternalToolPlan{}, err
	}
	if current != planned {
		return nil, InternalToolPlan{}, e.New(e.CodeVerifyFailed, "runtime observation changes its original production Plan")
	}
	binding, err := runtime.BindingContext(ctx)
	if err != nil {
		return nil, InternalToolPlan{}, err
	}
	if binding.BuildDigest != selected.BuildDigest || binding.Component != selected.Component || binding.Platform != selected.Platform {
		return nil, InternalToolPlan{}, e.New(e.CodeVerifyFailed, "runtime observation substitutes its selected producer")
	}
	_, artifacts := planIndex(plan)
	observed, err := observeProducerPayload(ctx, plan, artifacts[selected.ArtifactID], artifacts)
	if err != nil {
		return nil, InternalToolPlan{}, err
	}
	if observed.SHA256 != binding.Digest || observed.Size != binding.Size {
		return nil, InternalToolPlan{}, e.New(e.CodeVerifyFailed, "internal runtime result changed after production")
	}
	recorded, err := readProducerResult(ctx, artifacts[selected.ResultArtifactID].Path)
	if err != nil {
		return nil, InternalToolPlan{}, err
	}
	if err := runtime.AuthenticateResultContext(ctx, recorded); err != nil {
		return nil, InternalToolPlan{}, err
	}
	return runtime, selected, nil
}

func (owner *LocalRunResult) borrowInternalRuntime(ctx context.Context, plan *Plan, name string) (*pipeline.ProducedRuntime, error) {
	runtime, selected, err := owner.internalRuntimeProduction(ctx, plan, name)
	if err != nil {
		return nil, err
	}
	owner.diskMu.Lock()
	evidence := maps.Clone(owner.internalToolEvidence[name])
	owner.diskMu.Unlock()
	if err := validateCompletedInternalToolEvidence(ctx, plan, selected, evidence); err != nil {
		return nil, err
	}
	if _, err := runtime.BindingContext(ctx); err != nil {
		return nil, err
	}
	return runtime, nil
}
