// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CaptureWorkspaceOrdinaryPublication captures all non-disk outputs and their
// required inventories from a retained workspace. It rechecks producer bytes,
// packages and evidence through the normal publication assembly owners. The
// resulting diagnostic publication retains private bytes, with no invocation,
// signature or execution claim. The caller must bind its exact output set to
// the original Run, capture disks separately, and close the returned owner.
// The returned digest identifies the held diagnostic root, not a registry location.
// As with local execution, workspace is a canonical repository-relative path.
func CaptureWorkspaceOrdinaryPublication(ctx context.Context, plan *Plan, workspace string) (*delivery.Publication, string, error) {
	if ctx == nil || plan == nil || workspace == "" {
		return nil, "", e.New(e.CodeUsage, "retained publication requires context, Plan and workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", e.Wrap(e.CodeTimeout, err, "retained publication capture canceled")
	}
	if err := plan.Validate(); err != nil {
		return nil, "", err
	}
	if err := validateLocalWorkspace(workspace, plan); err != nil {
		return nil, "", err
	}
	relocated, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		return nil, "", err
	}
	_, artifacts := planIndex(relocated)
	return captureRetainedOrdinaryPublication(ctx, relocated, artifacts)
}

func captureRetainedOrdinaryPublication(ctx context.Context, plan *Plan, artifacts map[string]Artifact) (owner *delivery.Publication, rootDigest string, err error) {
	var selected []LifecycleOutputPlan
	var packages []Artifact
	builds := map[string]bool{}
	for _, output := range plan.LifecycleOutputs {
		switch output.Kind {
		case PackageDiskImage:
			continue // Raw local disks have a separate retained transport owner.
		case "executable", "oci", PackageHelm, PackageAPT:
		default:
			return nil, "", e.New(e.CodeEngineCapability, "unsupported retained output format %q", output.Kind)
		}
		selected = append(selected, output)
		packages = append(packages, artifacts[output.ArtifactID])
		builds[output.Name] = true
		if output.ImageIndex != nil {
			for _, child := range output.ImageIndex.Children {
				builds[child.BuildTarget] = true
			}
		}
	}
	if len(selected) == 0 {
		return nil, "", e.New(e.CodeEngineCapability, "retained publication has no ordinary outputs")
	}
	// Build the visibility list from the frozen graph, not caller-selected IDs.
	// Disk inventories are excluded here; unrelated SBOMs cannot fill an
	// ordinary output's exact build/component inventory slot.
	node := Node{ID: "retained-ordinary-publication"}
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.Kind != "sbom-set" || builds[artifact.BuildTarget] {
			node.Inputs = append(node.Inputs, artifact.ID)
		}
	}
	outputs, err := lifecyclePublicationOutputsForSelection(ctx, plan, node, artifacts, selected)
	if err != nil {
		return nil, "", err
	}
	evidence, err := releaseEvidenceObjectsForOutputs(ctx, plan, node, artifacts, selected)
	if err != nil {
		return nil, "", err
	}
	content, err := delivery.ContentDigest(outputs)
	if err != nil {
		return nil, "", err
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return nil, "", err
	}
	stage, err := os.MkdirTemp("", "nvkr-retained-publication-*")
	if err != nil {
		return nil, "", e.Wrap(e.CodeImmutableViolation, err, "creating retained publication scratch")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing retained publication paths"))
		}
		if err == nil && ctx.Err() != nil {
			err = e.Wrap(e.CodeTimeout, ctx.Err(), "retained publication capture canceled")
		}
		if err != nil {
			if owner != nil {
				err = errors.Join(err, owner.Close())
			}
			owner, rootDigest = nil, ""
		}
	}()
	output := Artifact{Path: filepath.Join(stage, "layout"), Producer: node.ID}
	objects, cleanup, err := collectPublicationObjects(ctx, plan, output, packages, evidence)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	if err := bindPublicationOutputObjects(ctx, plan, node, artifacts, objects, outputs); err != nil {
		return nil, "", err
	}
	metadata := &delivery.ReleaseMetadata{DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest,
		PlanDigest: identity, ContentSubject: content, Composition: plan.Composition.Clone()}
	result, err := delivery.BuildPublicationRoot(ctx, output.Path, delivery.PublicationRequest{Release: metadata, Objects: objects})
	if err != nil {
		return nil, "", err
	}
	owner, err = delivery.OpenPublication(ctx, output.Path, result.RootDigest)
	return owner, result.RootDigest, err
}
