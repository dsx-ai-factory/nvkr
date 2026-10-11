// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"

	"github.com/NVIDIA/nke-recipes/internal/buildactivity"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type vmAdmission interface {
	Execute(context.Context, *Plan, string, func(context.Context, *VMRuntime) error) error
	Close() error
}

// vmDriverResolver authenticates and holds the run's exact process/runtime,
// daemon and toolchain-image closure before the workspace is mutated. The
// returned executor is bound to the exact Plan, NodeSession and runtime.
type vmDriverResolver interface {
	Admit(context.Context, *Plan, *pipeline.NodeSession, vmDriverRunConfig) (vmDriverExecutor, error)
}

type vmDriverRunConfig struct {
	NodeID      string // Optional exact node within a complete hosted Plan.
	Runtime     string
	Stdout      io.Writer
	Stderr      io.Writer
	Diagnostics *pipeline.PackerDiagnosticSink
	Activity    *buildactivity.Sink
}

// vmDriverExecutor is deliberately private and run-owned. Its methods are
// useful only inside the callback of a Plan-bound VM admission; it is not a
// serializable or caller-constructible execution authority.
type vmDriverExecutor interface {
	RunPacker(context.Context, *VMRuntime, string, ir.Node, *vmPackerMaterials) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error)
	Close() error
}

type unavailableVMDriverResolver struct{}

func (unavailableVMDriverResolver) Admit(
	context.Context,
	*Plan,
	*pipeline.NodeSession,
	vmDriverRunConfig,
) (vmDriverExecutor, error) {
	return nil, e.New(e.CodeEngineCapability,
		"local VM execution requires a Plan-bound realized toolchain capability")
}

// dispatchAdmittedVMProducer performs driver selection only after Execute has
// revalidated and consumed the exact Plan/node admission. The executor is called
// inside the authored deadline. The run-owned executor has already authenticated
// and retained the exact runtime, daemon and OCI toolchain closure, but performs
// source, material and output work only in this callback.
func dispatchAdmittedVMProducer(
	ctx context.Context, admission vmAdmission, plan *Plan, node Node,
	executor vmDriverExecutor, owner *LocalRunResult,
) (artifact *pipeline.PackerArtifact, result *pipeline.ArtifactResult, returnErr error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "VM driver dispatch requires a context")
	}
	if plan == nil {
		return nil, nil, e.New(e.CodeUsage, "VM driver dispatch requires a Plan")
	}
	if admission == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "VM driver dispatch requires runtime admission")
	}
	if executor == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "VM driver dispatch requires a toolchain executor")
	}
	_, selected, err := plan.producerInstruction(node)
	if err != nil {
		return nil, nil, err
	}
	if selected.Disk == nil || selected.Disk.Encryption != "none" {
		return nil, nil, e.New(e.CodeEngineCapability,
			"vm-image producer %q requires a typed non-public encryption secret channel", selected.Component)
	}
	if selected.BuilderType != ir.BuilderVMImage || selected.Packer == nil || len(selected.Command) != 0 {
		return nil, nil, e.New(e.CodeEngineCapability,
			"vm-image producer %q requires an unimplemented held-output command adapter", selected.Component)
	}
	returnErr = admission.Execute(ctx, plan, node.ID, func(runCtx context.Context, runtime *VMRuntime) error {
		request, err := localVMRequestForPlan(plan, node.ID)
		if err != nil {
			return err
		}
		if err := runtime.requireSubject(request.Subject); err != nil {
			return err
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return err
		}
		if instruction.BuilderType != ir.BuilderVMImage {
			return e.New(e.CodeEngineCapability,
				"Plan node %q is not a vm-image producer", node.ID)
		}
		if instruction.Packer == nil || len(instruction.Command) != 0 ||
			instruction.Disk == nil || instruction.Disk.Encryption != "none" {
			return e.New(e.CodePipelineDrift,
				"vm-image producer %q changed its admitted Packer contract", instruction.Component)
		}
		materials, materialErr := observeVMPackerMaterials(runCtx, plan, node)
		if materialErr != nil {
			return materialErr
		}
		artifact, result, err = executor.RunPacker(runCtx, runtime, request.Subject, instruction, materials)
		if err == nil && artifact == nil {
			err = e.New(e.CodeVerifyFailed,
				"vm-image producer %q returned no held disk artifact", instruction.Component)
		}
		if err == nil && result == nil {
			err = e.New(e.CodeVerifyFailed,
				"vm-image producer %q returned no descriptive result", instruction.Component)
		}
		if err == nil && owner != nil {
			err = owner.recordProducerConsumption(runCtx, plan, node, instruction, result, materials.observedProducerMaterials)
		}
		err = joinFactoryCleanupError(err, materials.Close(), "closing VM Packer material capabilities")
		if err != nil {
			return err
		}
		return nil
	})
	if returnErr != nil && artifact != nil {
		returnErr = closeRejectedVMArtifact(returnErr, artifact)
		artifact = nil
		result = nil
	}
	return artifact, result, returnErr
}

type vmArtifactCloser interface {
	Close() error
}

func closeRejectedVMArtifact(primary error, artifact vmArtifactCloser) error {
	if artifact == nil {
		return primary
	}
	closeErr := artifact.Close()
	if closeErr == nil {
		return primary
	}
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(primary, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, errors.Join(primary, closeErr),
		"closing rejected VM driver artifact")
}

// validateVMDriverPlan keeps every encrypted vm-image form closed until the
// source contract and runtime provide a typed non-public secret channel. It also
// keeps command/Make authoring disconnected until a held-output adapter exists.
// Public literal variables and source-owned defaults cannot grant either
// authority.
func validateVMDriverPlan(plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "VM driver validation requires a Plan")
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeBuild || node.IRNode == "" {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return err
		}
		if instruction.BuilderType != ir.BuilderVMImage {
			continue
		}
		if instruction.Disk == nil || instruction.Disk.Encryption != "none" {
			return e.New(e.CodeEngineCapability,
				"vm-image execution requires a typed non-public encryption secret channel")
		}
		if instruction.Packer == nil || len(instruction.Command) != 0 {
			return e.New(e.CodeEngineCapability,
				"vm-image command/Make execution requires an unimplemented held-output adapter")
		}
	}
	return nil
}
