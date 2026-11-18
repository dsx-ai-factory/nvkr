// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeuse"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// issueRuntimeUse joins the original production and completed evidence to one
// exact consumer. Only the owning Run can reach this issuer; serialized results,
// public LocalRunResult fields and caller-supplied callbacks cannot restore it.
func (owner *LocalRunResult) issueRuntimeUse(ctx context.Context, plan *Plan, consumerID string) (*pipeline.ProducedRuntime, *runtimeuse.Receipt, error) {
	if ctx == nil || owner == nil || plan == nil {
		return nil, nil, e.New(e.CodeUsage, "runtime use requires context, Plan and original Run owner")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "runtime use canceled")
	}
	request, err := localVMRequestForPlan(plan, consumerID)
	if err != nil {
		return nil, nil, err
	}
	nodes, _ := planIndex(plan)
	consumer := nodes[consumerID]
	_, instruction, err := plan.producerInstruction(consumer)
	if err != nil {
		return nil, nil, err
	}
	if instruction.Runtime == nil || !slices.Equal(consumer.InternalTools, []string{instruction.Runtime.Tool}) {
		return nil, nil, e.New(e.CodeEngineCapability, "runtime use requires its exact planned producer selection")
	}
	selection := *instruction.Runtime
	runtime, err := owner.borrowInternalRuntime(ctx, plan, selection.Tool)
	if err != nil {
		return nil, nil, err
	}
	production, err := runtime.BindingContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	planned, err := runtimePlanBinding(plan)
	if err != nil {
		return nil, nil, err
	}
	owner.diskMu.Lock()
	observation := owner.internalToolEvidence[selection.Tool][NodeVerify]
	sbom := owner.internalToolEvidence[selection.Tool][NodeSBOM]
	owner.diskMu.Unlock()
	binding := runtimeuse.Binding{PlanDigest: planned.identity, Subject: request.Subject, Purpose: "packer-build",
		Runtime: selection, Production: production, Observation: observation, SBOM: sbom}
	authenticate := func(callCtx context.Context) error {
		current, err := runtimePlanBinding(plan)
		if err != nil {
			return err
		}
		if current != planned {
			return e.New(e.CodeVerifyFailed, "runtime use changed its original Plan")
		}
		live, err := owner.borrowInternalRuntime(callCtx, plan, selection.Tool)
		if err != nil {
			return err
		}
		if live != runtime {
			return e.New(e.CodeVerifyFailed, "runtime use changed its original production owner")
		}
		actual, err := live.BindingContext(callCtx)
		if err != nil {
			return err
		}
		owner.diskMu.Lock()
		unchanged := !owner.diskClosing && !owner.diskClosed &&
			owner.internalToolEvidence[selection.Tool][NodeVerify] == observation && owner.internalToolEvidence[selection.Tool][NodeSBOM] == sbom
		owner.diskMu.Unlock()
		if actual != production || !unchanged {
			return e.New(e.CodeVerifyFailed, "runtime use changed its production or evidence")
		}
		return nil
	}
	receipt, err := runtimeuse.Issue(ctx, binding, instruction, authenticate)
	if err != nil {
		return nil, nil, err
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing || owner.internalRuntimes[selection.Tool] != runtime {
		receipt.Close()
		return nil, nil, e.New(e.CodeGateUnmet, "runtime use lost its original Run owner")
	}
	if owner.internalRuntimeUses[request.Subject] != nil {
		receipt.Close()
		return nil, nil, e.New(e.CodeGateUnmet, "runtime consumer has already received its execution receipt")
	}
	if owner.internalRuntimeUses == nil {
		owner.internalRuntimeUses = make(map[string]*runtimeuse.Receipt)
	}
	owner.internalRuntimeUses[request.Subject] = receipt
	return runtime, receipt, nil
}
