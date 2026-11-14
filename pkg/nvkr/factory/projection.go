// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The projection pilot is a diagnostic: a top-level generation caller may
// select the reference projection plugin through one plugin contract,
// reached in-process. Core sends the plugin the
// complete canonical Plan and admits the returned bundle with the existing
// exact validator before anything is written. Generated stages never consult
// the selection, so no generated job or worker reaches a selected plugin.

const (
	projectionProtocol = "projection/v0-pilot"
	// The adapter emits members of the generated-run manifest contract.
	projectionGeneratorABI = generatedManifestSchema
	projectionMemberMode   = uint32(0o644)
	maxProjectionPathDepth = 8
)

// ProjectionTransport selects how core reaches the projection plugin. The
// plugin contract and core's admission of its results do not depend on it.
type ProjectionTransport string

const (
	// ProjectionInProcess calls the built-in reference plugin directly.
	ProjectionInProcess ProjectionTransport = "in-process"
)

// ProjectionAdapter is the projection plugin a top-level generation caller
// selected and the transport that reaches it. It only describes the plugin:
// core admits the plugin's description and every byte it emits.
type ProjectionAdapter struct {
	Transport ProjectionTransport
	// Release is the exact plugin release core requires from Describe.
	Release string
}

type projectionAdapterKey struct{}

// WithProjectionAdapter selects adapter for Render under ctx. It is the hidden
// diagnostic pilot selector for top-level generation only. Core admits every
// emitted byte through the existing exact validator.
func WithProjectionAdapter(ctx context.Context, adapter ProjectionAdapter) context.Context {
	return context.WithValue(ctx, projectionAdapterKey{}, adapter)
}

// ProjectionAdapterSelected reports whether ctx selects a projection adapter.
func ProjectionAdapterSelected(ctx context.Context) bool {
	_, ok := selectedProjectionAdapter(ctx)
	return ok
}

func selectedProjectionAdapter(ctx context.Context) (ProjectionAdapter, bool) {
	if ctx == nil {
		return ProjectionAdapter{}, false
	}
	adapter, ok := ctx.Value(projectionAdapterKey{}).(ProjectionAdapter)
	return adapter, ok
}

// projectionPlugin is the in-process reference projection contract.
type projectionPlugin interface {
	describe(ctx context.Context) (projectionDescription, error)
	emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error)
}

// plugin resolves the selected transport to the contract implementation.
func (adapter ProjectionAdapter) plugin(engine string) (projectionPlugin, error) {
	switch adapter.Transport {
	case ProjectionInProcess:
		return builtinProjection(engine, adapter.Release), nil
	default:
		return nil, e.New(e.CodeEngineCapability, "projection transport %q is unsupported (expected %s)",
			adapter.Transport, ProjectionInProcess)
	}
}

// builtinProjection registers the in-binary reference plugin for the
// in-process transport. Tests wrap it to count contract calls.
var builtinProjection = func(engine, release string) projectionPlugin {
	return referenceProjection{engine: engine, release: release}
}

type projectionEmitRequest struct {
	Plan             []byte           `json:"plan"`
	GeneratorVersion string           `json:"generatorVersion"`
	Layout           projectionLayout `json:"layout"`
}

type projectionLayout struct {
	Workflow string `json:"workflow"`
	Manifest string `json:"manifest"`
	Plan     string `json:"plan"`
}

type projectionDescription struct {
	Protocol     string `json:"protocol"`
	Release      string `json:"release"`
	Engine       string `json:"engine"`
	PlanSchema   string `json:"planSchema"`
	GeneratorABI string `json:"generatorABI"`
}

type projectionEmission struct {
	PlanDigest   string             `json:"planDigest"`
	WorkflowPath string             `json:"workflowPath"`
	Members      []projectionMember `json:"members"`
}

type projectionMember struct {
	Path  string `json:"path"`
	Mode  uint32 `json:"mode"`
	Bytes []byte `json:"bytes"`
}

func projectionLayoutFor(layout generationLayout) projectionLayout {
	return projectionLayout{Workflow: layout.workflowPath, Manifest: layout.manifestPath, Plan: layout.planPath}
}

// renderProjected obtains a candidate bundle from the selected plugin and
// admits it exactly as core admits its own projection, whatever the transport.
// It never writes files. release is the plugin release the selection requires.
func renderProjected(ctx context.Context, plan *Plan, engine string, plugin projectionPlugin, release string) (*GeneratedBundle, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "projection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "projection canceled")
	}
	if plan == nil {
		return nil, e.New(e.CodeInvalidCatalog, "projection requires a plan")
	}
	core, err := projectionCoreForEngine(engine)
	if err != nil {
		return nil, err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	planBytes, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if int64(len(planBytes)) > MaxPlanBytes {
		return nil, e.New(e.CodeEngineCapability, "complete Plan exceeds the %d-byte projection request bound", MaxPlanBytes)
	}
	layout := core.layout(plan)
	want := projectionDescription{Protocol: projectionProtocol, Release: release, Engine: core.engine,
		PlanSchema: PlanSchemaVersion, GeneratorABI: projectionGeneratorABI}
	description, err := plugin.describe(ctx)
	if err != nil {
		return nil, err
	}
	// Refuse a mismatched plugin before it receives any Plan bytes.
	if description != want {
		return nil, e.New(e.CodeEngineCapability, "projection plugin describes %+v; core requires %+v", description, want)
	}
	emission, err := plugin.emit(ctx, projectionEmitRequest{
		Plan: planBytes, GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayoutFor(layout)})
	if err != nil {
		return nil, err
	}
	files, err := admitProjectionEmission(emission, layout, planDigest)
	if err != nil {
		return nil, err
	}
	bundle := newGeneratedBundle(core.engine, layout, files, planDigest)
	if err := core.validate(ctx, plan, bundle); err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, e.Wrap(e.CodePipelineDrift, err, "projection plugin bundle fails core admission")
	}
	return bundle, nil
}

// admitProjectionEmission checks the response shape before the exact validator
// compares its bytes: sorted unique safe members of the admitted mode and the
// core's own Plan digest and workflow path.
func admitProjectionEmission(emission projectionEmission, layout generationLayout, planDigest string) (map[string][]byte, error) {
	if emission.PlanDigest != planDigest {
		return nil, e.New(e.CodePipelineDrift, "projection plugin echoes planDigest %q, want %q", emission.PlanDigest, planDigest)
	}
	if emission.WorkflowPath != layout.workflowPath {
		return nil, e.New(e.CodePipelineDrift, "projection plugin workflow path %q is not the admitted %q", emission.WorkflowPath, layout.workflowPath)
	}
	if len(emission.Members) == 0 || len(emission.Members) > maxGeneratedDirEntries {
		return nil, e.New(e.CodePipelineDrift, "projection plugin emitted %d members", len(emission.Members))
	}
	files := make(map[string][]byte, len(emission.Members))
	for index, member := range emission.Members {
		if !safeGeneratedPath(member.Path) || strings.Count(member.Path, "/") > maxProjectionPathDepth {
			return nil, e.New(e.CodePipelineDrift, "projection plugin member path %q is unsafe", member.Path)
		}
		if index > 0 && member.Path <= emission.Members[index-1].Path {
			return nil, e.New(e.CodePipelineDrift, "projection plugin members are not sorted and unique at %q", member.Path)
		}
		if member.Mode != projectionMemberMode || int64(len(member.Bytes)) > maxGeneratedFileBytes {
			return nil, e.New(e.CodePipelineDrift, "projection plugin member %q has mode %o or is oversized", member.Path, member.Mode)
		}
		files[member.Path] = member.Bytes
	}
	return files, nil
}
