// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// localVMPackerResolver realizes the exact runtime, daemon and already-local
// toolchain-image closure selected by one validated, relocated Plan. Command
// and Make producers remain closed until they have their own held-output adapter.
type localVMPackerResolver struct{}

type localVMPackerExecutor struct {
	inner       *pipeline.VMImagePackerExecutor
	native      map[string]*pipeline.PackerToolchain
	workspace   string
	subjects    map[string]struct{}
	sourceRoots map[string]string
	stdout      io.Writer
	stderr      io.Writer
	diagnostics *pipeline.PackerDiagnosticSink
	activity    *buildactivity.Sink
}

func (localVMPackerResolver) Admit(
	ctx context.Context,
	plan *Plan,
	session *pipeline.NodeSession,
	config vmDriverRunConfig,
) (result vmDriverExecutor, returnedErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local Packer resolver requires a context")
	}
	if plan == nil || session == nil {
		return nil, e.New(e.CodeEngineCapability,
			"local Packer resolver requires a validated Plan and producer session")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if config.Diagnostics == nil {
		return nil, e.New(e.CodeEngineCapability, "local Packer resolver requires an attempt-owned diagnostic capability")
	}
	_, artifacts := planIndex(plan)
	selections := make([]pipeline.VMImagePackerSelection, 0)
	subjects := make(map[string]struct{})
	sourceRoots := make(map[string]string)
	native := make(map[string]*pipeline.PackerToolchain)
	success := false
	defer func() {
		if !success {
			for _, toolchain := range native {
				if err := toolchain.Close(); err != nil {
					returnedErr = joinFactoryCleanupError(returnedErr, err, "closing rejected native Packer toolchain")
				}
			}
		}
	}()
	for _, selected := range topologicalFactoryNodes(plan.Graph) {
		if config.NodeID != "" && selected.ID != config.NodeID {
			continue
		}
		if selected.Kind != NodeBuild || selected.IRNode == "" {
			continue
		}
		build, instruction, err := plan.producerInstruction(selected)
		if err != nil {
			return nil, err
		}
		if instruction.BuilderType != ir.BuilderVMImage {
			continue
		}
		if instruction.Packer == nil || len(instruction.Command) != 0 {
			return nil, e.New(e.CodeEngineCapability,
				"vm-image command/Make execution requires an unimplemented held-output adapter")
		}
		if err := pipeline.ValidateVMImagePackerDiskExecution(instruction); err != nil {
			return nil, err
		}
		request, err := localVMRequestForPlan(plan, selected.ID)
		if err != nil {
			return nil, err
		}
		result, ok := artifacts["result/"+selected.BuildTarget+"/"+instruction.Component]
		if !ok || result.Path == "" {
			return nil, e.New(e.CodePipelineDrift,
				"VM-image producer %q has no planned result route", selected.ID)
		}
		if _, duplicate := subjects[request.Subject]; duplicate {
			return nil, e.New(e.CodePipelineDrift,
				"VM-image producer subject %q is selected more than once", request.Subject)
		}
		subjects[request.Subject] = struct{}{}
		sourceRoot, err := plannedPackerSourceRoot(build, selected, instruction, artifacts)
		if err != nil {
			return nil, err
		}
		sourceRoots[request.Subject] = sourceRoot
		selection := pipeline.VMImagePackerSelection{Subject: request.Subject, Node: instruction}
		_, hasNative := plan.Environment.Runtime.NativeToolchains[instruction.Toolchain]
		if err := validateLocalPackerHost(instruction, hasNative); err != nil {
			return nil, err
		}
		selection.ActivityOperation = packerActivityOperation(selected.ID, config.Activity != nil)
		selections = append(selections, selection)
	}
	if len(selections) == 0 {
		return nil, e.New(e.CodeEngineCapability,
			"local Packer resolver found no direct-Packer VM-image producer")
	}
	if config.Diagnostics == nil {
		return nil, e.New(e.CodeEngineCapability,
			"local Packer resolver requires an attempt-owned diagnostic capability")
	}
	networkAdmission, err := pipeline.AuthorizeVMImagePackerNetwork(
		plan.Environment, plan.EnvironmentDigest, selections,
	)
	if err != nil {
		return nil, err
	}
	// Pure Plan, host and network admission precede local toolchain I/O.
	nativeCount := 0
	for _, selection := range selections {
		if _, ok := plan.Environment.Runtime.NativeToolchains[selection.Node.Toolchain]; ok {
			nativeCount++
		}
	}
	if nativeCount != 0 && nativeCount != len(selections) {
		return nil, e.New(e.CodeEngineCapability, "one Packer admission cannot mix native and container toolchains")
	}
	for index := range selections {
		selection := &selections[index]
		locator, ok := plan.Environment.Runtime.NativeToolchains[selection.Node.Toolchain]
		if !ok {
			continue
		}
		toolchain := native[selection.Node.Toolchain]
		if toolchain == nil {
			toolchain, err = openNativePackerBundle(ctx, locator.Layout, locator.Bundle, selection.Node.Toolchain, selection.Node.ToolchainPlatform)
			if err != nil {
				return nil, err
			}
			native[selection.Node.Toolchain] = toolchain
		}
		variables := make([]pipeline.PackerVariable, 0, len(selection.Node.Packer.Variables))
		for _, variable := range selection.Node.Packer.Variables {
			variables = append(variables, pipeline.PackerVariable(variable))
		}
		if _, err := toolchain.NativeVariables(ctx, variables); err != nil {
			return nil, err
		}
		selection.Toolchain = toolchain
	}
	admitted, err := session.AdmitVMImagePacker(ctx, selections, networkAdmission, config.Runtime)
	if err != nil {
		return nil, err
	}
	success = true
	return &localVMPackerExecutor{
		native: native, workspace: config.Workspace,
		inner: admitted, subjects: subjects, sourceRoots: sourceRoots,
		stdout: config.Stdout, stderr: config.Stderr, diagnostics: config.Diagnostics,
		activity: config.Activity,
	}, nil
}

// Descriptive labels must not narrow executable Plan admission. A producer can
// have a valid node ID outside the finite telemetry vocabulary; in that case
// omit its optional observer without changing its execution selection.
func packerActivityOperation(nodeID string, enabled bool) string {
	if enabled && (buildactivity.Record{Version: buildactivity.Version, Operation: nodeID}).Validate() == nil {
		return nodeID
	}
	return ""
}

// plannedPackerSourceRoot resolves the source route already authored into the
// validated Plan. It does not reconstruct that route from the result-directory
// layout, so changing an unrelated result path cannot redirect source access.
func plannedPackerSourceRoot(build BuildPlan, selected Node, instruction ir.Node, artifacts map[string]Artifact) (string, error) {
	if build.Graph == nil {
		return "", e.New(e.CodePipelineDrift, "VM-image producer %q has no build graph", selected.ID)
	}
	byID := make(map[string]ir.Node, len(build.Graph.Nodes))
	for _, node := range build.Graph.Nodes {
		byID[node.ID] = node
	}
	var sourceID string
	for _, dependency := range instruction.Needs {
		if byID[dependency].Kind != ir.KindFetch {
			continue
		}
		if sourceID != "" {
			return "", e.New(e.CodePipelineDrift,
				"VM-image producer %q has more than one planned source route", selected.ID)
		}
		sourceID = "source/" + selected.BuildTarget + "/" + dependency
	}
	if sourceID == "" {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q has no planned source route", selected.ID)
	}
	source, ok := artifacts[sourceID]
	suffix := "/" + instruction.SourceDest
	if !ok || source.Path == "" || !strings.HasSuffix(source.Path, suffix) {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q source route does not bind destination %q", selected.ID, instruction.SourceDest)
	}
	root := strings.TrimSuffix(source.Path, suffix)
	if root == "" {
		return "", e.New(e.CodePipelineDrift,
			"VM-image producer %q has an empty planned source root", selected.ID)
	}
	return root, nil
}

func (executor *localVMPackerExecutor) RunPacker(
	ctx context.Context,
	runtime *VMRuntime,
	subject string,
	node ir.Node,
	materials *vmPackerMaterials,
) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
	if executor == nil || executor.inner == nil || runtime == nil || materials == nil ||
		materials.manifest == nil {
		return nil, nil, e.New(e.CodeEngineCapability,
			"local Packer execution requires admitted runtime and material capabilities")
	}
	if _, ok := executor.subjects[subject]; !ok || subject == "" {
		return nil, nil, e.New(e.CodePipelineDrift,
			"local Packer subject %q was not admitted", subject)
	}
	sourceRoot, ok := executor.sourceRoots[subject]
	if !ok || sourceRoot == "" {
		return nil, nil, e.New(e.CodePipelineDrift,
			"local Packer subject %q has no admitted source route", subject)
	}
	return executor.inner.Run(ctx, subject, node, pipeline.VMImagePackerRun{
		Activity:   executor.activity,
		SourceRoot: sourceRoot, Workspace: executor.workspace,
		Manifest:    materials.manifest,
		Materials:   materials.inputs,
		Runtime:     runtime.inner,
		Diagnostics: executor.diagnostics,
		Stdout:      executor.stdout,
		Stderr:      executor.stderr,
	})
}

func (executor *localVMPackerExecutor) Close() error {
	if executor == nil || executor.inner == nil {
		return nil
	}
	closeErr := executor.inner.Close()
	for _, toolchain := range executor.native {
		closeErr = errors.Join(closeErr, toolchain.Close())
	}
	return closeErr
}

// validateLocalPackerHost is the shared pure platform/network gate used by local
// preflight and the resolver before opening native bundle bytes.
func validateLocalPackerHost(node ir.Node, native bool) error {
	if runtime.GOOS != "linux" && !native {
		return e.New(e.CodeEngineCapability, "local VM execution requires Linux container tools or a verified native toolchain bundle; host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if native && (node.ToolchainPlatform != runtime.GOOS+"/"+runtime.GOARCH || node.Network != ir.NetworkDefault) {
		return e.New(e.CodeEngineCapability, "native VM execution requires the selected actual host and explicit producer-declared networking")
	}
	return nil
}
