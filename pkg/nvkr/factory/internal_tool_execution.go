// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"maps"
	"os"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func (plan *Plan) internalToolForTarget(target string) (InternalToolPlan, bool) {
	for _, tool := range plan.InternalTools {
		if tool.BuildTarget == target {
			return tool, true
		}
	}
	return InternalToolPlan{}, false
}

// admitInternalToolEvidence rejects absent, revoked or foreign production before
// an evidence operation can perform filesystem or subprocess work.
func (owner *LocalRunResult) admitInternalToolEvidence(ctx context.Context, plan *Plan, node Node) error {
	selected, internal := plan.internalToolForTarget(node.BuildTarget)
	if !internal {
		return e.New(e.CodePipelineDrift, "internal evidence has no selected tool")
	}
	if owner == nil || (node.Kind != NodeVerify && node.Kind != NodeSBOM) {
		return e.New(e.CodeEngineCapability, "internal tool evidence requires its original Run dispatcher")
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	if identity != owner.PlanDigest {
		return e.New(e.CodeVerifyFailed, "internal tool evidence belongs to another Plan")
	}
	owner.diskMu.Lock()
	closed := owner.diskClosed || owner.diskClosing
	tool := owner.internalExecutables[selected.Name]
	owner.diskMu.Unlock()
	if closed || tool == nil {
		return e.New(e.CodeGateUnmet, "internal tool evidence has no live production owner")
	}
	if _, err := tool.BindingContext(ctx); err != nil {
		return err
	}
	owner.diskMu.Lock()
	completed := owner.internalToolEvidence[selected.Name][node.Kind] != ""
	owner.diskMu.Unlock()
	if completed {
		return e.New(e.CodeVerifyFailed, "internal tool evidence repeats a completed operation")
	}
	return nil
}

// Evidence is retained only by the dispatcher after its concrete operation
// succeeds. Re-reading documents can check freshness but cannot issue this state.
func (owner *LocalRunResult) recordInternalToolEvidence(ctx context.Context, plan *Plan, node Node) error {
	if err := owner.admitInternalToolEvidence(ctx, plan, node); err != nil {
		return err
	}
	selected, _ := plan.internalToolForTarget(node.BuildTarget)
	_, artifacts := planIndex(plan)
	if err := validateInternalToolEvidence(ctx, plan, selected, node, artifacts); err != nil {
		return err
	}
	digest, err := digestFactoryPath(ctx, artifacts[node.Outputs[0]].Path)
	if err != nil {
		return err
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing || owner.internalExecutables[selected.Name] == nil {
		return e.New(e.CodeGateUnmet, "internal tool evidence has no live production owner")
	}
	if owner.internalToolEvidence == nil {
		owner.internalToolEvidence = make(map[string]map[string]string)
	}
	if owner.internalToolEvidence[selected.Name] == nil {
		owner.internalToolEvidence[selected.Name] = make(map[string]string)
	}
	if owner.internalToolEvidence[selected.Name][node.Kind] != "" {
		return e.New(e.CodeVerifyFailed, "internal tool evidence repeats a completed operation")
	}
	owner.internalToolEvidence[selected.Name][node.Kind] = digest
	return nil
}

func validateInternalToolEvidence(ctx context.Context, plan *Plan, selected InternalToolPlan, node Node, artifacts map[string]Artifact) error {
	if len(node.Outputs) != 1 {
		return e.New(e.CodePipelineDrift, "internal tool evidence has ambiguous outputs")
	}
	output := artifacts[node.Outputs[0]]
	if node.Kind == NodeSBOM {
		if output.ID != selected.SBOMArtifactID {
			return e.New(e.CodePipelineDrift, "internal tool SBOM changes its Plan output")
		}
		_, err := sbomPublicationObjects(ctx, plan, output, artifacts, func(path string) (string, error) { return path, nil })
		return err
	}
	if node.Kind != NodeVerify || output.ID != selected.ObservationArtifactID {
		return e.New(e.CodePipelineDrift, "internal tool terminal evidence changes its Plan output")
	}
	raw, err := readSmallFactoryFile(ctx, output.Path, "internal tool terminal observation")
	if err != nil {
		return err
	}
	var observed factoryObservation
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &observed); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding internal tool terminal observation")
	}
	if observed.SchemaVersion != "nvkr.dev/factory-observation/v2" || observed.BuildDigest != selected.BuildDigest || observed.BuildTarget != selected.BuildTarget || observed.Qualification != TierStaticUnqualified || len(observed.Inputs) != len(node.Inputs) {
		return e.New(e.CodeVerifyFailed, "internal tool terminal observation changes its producer closure")
	}
	for i, input := range observed.Inputs {
		planned := artifacts[node.Inputs[i]]
		if input.ArtifactID != planned.ID || input.Kind != planned.Kind {
			return e.New(e.CodeVerifyFailed, "internal tool terminal input is unbound")
		}
		digest, err := digestFactoryPath(ctx, planned.Path)
		if err != nil {
			return err
		}
		if input.Digest != digest {
			return e.New(e.CodeVerifyFailed, "internal tool terminal input changed")
		}
	}
	return nil
}

func (owner *LocalRunResult) borrowInternalExecutable(ctx context.Context, plan *Plan, name string) (*pipeline.ProducedExecutable, error) {
	if ctx == nil || owner == nil || plan == nil {
		return nil, e.New(e.CodeUsage, "internal tool consumption requires context, Plan and original Run")
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	if identity != owner.PlanDigest {
		return nil, e.New(e.CodeVerifyFailed, "internal tool consumer changes its Plan")
	}
	var selected InternalToolPlan
	for _, tool := range plan.InternalTools {
		if tool.Name == name {
			selected = tool
		}
	}
	if selected.Name == "" {
		return nil, e.New(e.CodePipelineDrift, "internal tool consumer has no selected producer")
	}
	owner.diskMu.Lock()
	closed := owner.diskClosed || owner.diskClosing
	tool := owner.internalExecutables[name]
	evidence := maps.Clone(owner.internalToolEvidence[name])
	owner.diskMu.Unlock()
	if closed || tool == nil {
		return nil, e.New(e.CodeGateUnmet, "internal tool consumer has no live production")
	}
	binding, err := tool.BindingContext(ctx)
	if err != nil {
		return nil, err
	}
	if binding.BuildDigest != selected.BuildDigest || binding.Platform != selected.Platform || binding.Component != selected.Component {
		return nil, e.New(e.CodeVerifyFailed, "internal tool consumer substitutes its selected producer")
	}
	nodes, artifacts := planIndex(plan)
	result, err := observeProducerPayload(ctx, plan, artifacts[selected.ArtifactID], artifacts)
	if err != nil {
		return nil, err
	}
	if result.SHA256 != binding.Digest || result.Size != binding.Size {
		return nil, e.New(e.CodeVerifyFailed, "internal tool result changed after production")
	}
	if err := tool.AuthenticateResultContext(ctx, result); err != nil {
		return nil, err
	}
	for _, id := range []string{selected.ObservationArtifactID, selected.SBOMArtifactID} {
		artifact := artifacts[id]
		node := nodes[artifact.Producer]
		if evidence[node.Kind] == "" {
			return nil, e.New(e.CodeProvenanceMissing, "required internal tool evidence has not completed")
		}
		if err := validateInternalToolEvidence(ctx, plan, selected, node, artifacts); err != nil {
			return nil, err
		}
		digest, err := digestFactoryPath(ctx, artifact.Path)
		if err != nil {
			return nil, err
		}
		if digest != evidence[node.Kind] {
			return nil, e.New(e.CodeVerifyFailed, "internal tool evidence changed after completion")
		}
	}
	if _, err := tool.BindingContext(ctx); err != nil {
		return nil, err
	}
	return tool, nil
}

func (owner *LocalRunResult) openDiskEvidenceRuntime(ctx context.Context, plan *Plan, runtimeExecutable string, workspace *os.File, diagnostics *diskdiagnostic.Collector) (*sbom.DiskInspectionRuntime, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "disk inspection requires a Plan")
	}
	name := plan.Environment.DiskInspectionProfiles[runtime.GOOS+"/"+runtime.GOARCH].HelperTool
	var tool *pipeline.ProducedExecutable
	if name != "" {
		var err error
		tool, err = owner.borrowInternalExecutable(ctx, plan, name)
		if err != nil {
			return nil, err
		}
	}
	return openDiskEvidenceRuntimeWithTool(ctx, plan, runtimeExecutable, workspace, diagnostics, tool)
}

func (owner *LocalRunResult) diskEvidenceProfile(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, inspection *sbom.DiskInspectionRuntime) (definition.DiskInspectionProfile, error) {
	profile, err := diskEvidenceProfile(ctx, plan, inspection)
	if err != nil || profile.HelperTool == "" {
		return profile, err
	}
	consumer := planNode(plan, expected.Name+"/sbom")
	if consumer.Kind != NodeSBOM || len(consumer.InternalTools) != 1 || consumer.InternalTools[0] != profile.HelperTool {
		return definition.DiskInspectionProfile{}, e.New(e.CodePipelineDrift, "inspection has no exact planned internal helper use")
	}
	tool, err := owner.borrowInternalExecutable(ctx, plan, profile.HelperTool)
	if err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	if _, err := inspection.AuthenticateProducedTool(ctx, profile.HelperTool, tool); err != nil {
		return definition.DiskInspectionProfile{}, err
	}
	return profile, nil
}

// retainInternalExecutable transfers the concrete producer's capability into
// the original Run. A serialized Plan or result cannot populate this ownership.
func (owner *LocalRunResult) retainInternalExecutable(ctx context.Context, plan *Plan, selected InternalToolPlan, executable *pipeline.ProducedExecutable) error {
	if err := owner.admitInternalExecutable(ctx, plan, selected); err != nil {
		return err
	}
	binding, err := executable.BindingContext(ctx)
	if err != nil {
		return err
	}
	if binding.BuildDigest != selected.BuildDigest || binding.Platform != selected.Platform || binding.Component != selected.Component {
		return e.New(e.CodeVerifyFailed, "internal executable differs from its selected producer")
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing {
		return e.New(e.CodeGateUnmet, "internal tool Run owner is closed")
	}
	if owner.internalExecutables[selected.Name] != nil {
		return e.New(e.CodeVerifyFailed, "internal tool %q has repeated production", selected.Name)
	}
	if owner.internalExecutables == nil {
		owner.internalExecutables = make(map[string]*pipeline.ProducedExecutable)
	}
	owner.internalExecutables[selected.Name] = executable
	return nil
}

func (owner *LocalRunResult) admitInternalExecutable(ctx context.Context, plan *Plan, selected InternalToolPlan) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "internal tool production requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "internal tool production canceled")
	}
	if owner == nil || plan == nil {
		return e.New(e.CodeVerifyFailed, "internal tool requires its original Run owner")
	}
	expected, ok := plan.internalToolForTarget(selected.BuildTarget)
	if !ok || expected != selected {
		return e.New(e.CodePipelineDrift, "internal tool changes its exact Plan selection")
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	if identity != owner.PlanDigest {
		return e.New(e.CodeVerifyFailed, "internal tool belongs to another Plan")
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing {
		return e.New(e.CodeGateUnmet, "internal tool Run owner is closed")
	}
	if owner.internalExecutables[selected.Name] != nil {
		return e.New(e.CodeVerifyFailed, "internal tool %q has repeated production", selected.Name)
	}
	return nil
}
