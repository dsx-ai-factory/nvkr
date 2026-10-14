// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// This private completion holds the actual package-issued archive against the
// original Run and exact named invocation. It supplies neither inspected package
// metadata nor final-output SBOM/provenance, terminal verification or publication.
type debianRunProducerCompletion struct {
	archive     pipeline.DebianProducerArchive
	description pipeline.DebianProducerDescription
	startedOn   time.Time
	finishedOn  time.Time
}

// executeBoundProducer consumes the native scheduler's original exclusive binding.
// Its cross-package adapter admits the complete graph and native runtime before
// work and retains only the actual producer-issued completion.
func (state *debianRunState) executeBoundProducer(ctx context.Context, result *LocalRunResult,
	plan *Plan, output LifecycleOutputPlan, runtimeName string, stdout, stderr io.Writer,
) (returnErr error) {
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return err
	}
	defer finish()
	var original Plan
	if err := canonical.UnmarshalExact(state.plan, &original); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading original Debian producer Plan")
	}
	var required LifecycleOutputPlan
	if err := canonical.UnmarshalExact(state.outputs[output.ArtifactID], &required); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading original named Debian output")
	}
	_, artifacts := planIndex(&original)
	selected, ok := artifacts[required.ArtifactID]
	if !ok {
		return e.New(e.CodeVerifyFailed, "original Debian output has no producer route")
	}
	var producer Node
	for _, node := range original.Graph.Nodes {
		if node.ID == selected.Producer {
			producer = node
			break
		}
	}
	build, instruction, err := original.producerInstruction(producer)
	if err != nil {
		return err
	}
	sourceRoot, err := plannedPackerSourceRoot(build, producer, instruction, artifacts)
	if err != nil {
		return err
	}
	sourceRoot, err = relocatedFactoryPath(sourceRoot, state.workspace)
	if err != nil {
		return err
	}
	state.mu.Lock()
	_, installed := state.executors[required.ArtifactID]
	state.mu.Unlock()
	if !installed {
		if err := state.admitBoundProducer(owned, result, &original, required, runtimeName); err != nil {
			return err
		}
	}
	state.mu.Lock()
	binding, ok := state.executors[required.ArtifactID]
	state.mu.Unlock()
	if !ok {
		return e.New(e.CodeVerifyFailed, "original Debian executor was not installed")
	}
	startedOn := time.Now().UTC()
	archive, description, err := binding.executor.RunBoundContext(owned, &binding.invocation, pipeline.DebianProducerRun{SourceRoot: sourceRoot, Stdout: stdout, Stderr: stderr})
	finishedOn := time.Now().UTC()
	transferred := false
	defer func() {
		if !transferred && archive != nil {
			returnErr = errors.Join(returnErr, archive.Close())
		}
	}()
	if err != nil {
		return err
	}
	if description == nil || archive == nil {
		return e.New(e.CodeProvenanceMissing, "original Debian producer returned no retained completion")
	}
	observed, err := archive.DescribeContext(owned)
	if err != nil {
		return err
	}
	if err := binding.executor.RequireArchiveContext(owned, required.ArtifactID, archive, observed.InputDigest); err != nil {
		return err
	}
	raw, err := canonical.Marshal(instruction)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "encoding original Debian instruction")
	}
	actual, err := canonical.Marshal(observed.Instruction)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "encoding retained Debian instruction")
	}
	if observed.Subject != required.ArtifactID || observed.BuildDigest != required.BuildDigest || !bytes.Equal(raw, actual) ||
		!canonical.IsDigest(observed.InputDigest) || !canonical.IsDigest(observed.Digest) || observed.Size <= 0 || observed.Size > pipeline.MaxDebianPackageBytes ||
		description.Component != instruction.Component || description.OutputType != instruction.OutputType || description.Qualification != pipeline.ArtifactUnqualified ||
		description.InputDigest != observed.InputDigest || description.SHA256 != observed.Digest || description.Size != observed.Size {
		return e.New(e.CodeVerifyFailed, "Debian completion differs from original instruction/source/bytes")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := owned.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "transferring original Debian completion")
	}
	if state.closed {
		return e.New(e.CodeEngineCapability, "original Debian Run closed before producer transfer")
	}
	if _, exists := state.completions[required.ArtifactID]; exists {
		return e.New(e.CodeVerifyFailed, "named Debian output already completed")
	}
	if state.completions == nil {
		state.completions = make(map[string]debianRunProducerCompletion)
	}
	state.completions[required.ArtifactID] = debianRunProducerCompletion{archive: *archive, description: *observed, startedOn: startedOn, finishedOn: finishedOn}
	transferred = true
	return nil
}

func (state *debianRunState) admitBoundProducer(ctx context.Context, result *LocalRunResult, plan *Plan, output LifecycleOutputPlan, runtimeName string) error {
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return err
	}
	defer finish()
	_, artifacts := planIndex(plan)
	producer := planNode(plan, artifacts[output.ArtifactID].Producer)
	build, instruction, err := plan.producerInstruction(producer)
	if err != nil {
		return err
	}
	executor, err := state.session.AdmitDebianProducer(owned, build.Graph, output.BuildDigest, output.ArtifactID, instruction.ID, runtimeName)
	if err != nil {
		return err
	}
	return state.bindExecutor(owned, result, plan, output, executor)
}
