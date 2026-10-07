// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// authenticateAcquiredImagePayload shares the frozen pull/result boundary with
// packaging. It returns descriptive observations, not execution qualification.
func authenticateAcquiredImagePayload(ctx context.Context, plan *Plan, input Artifact, artifacts map[string]Artifact) (pipeline.ArtifactResult, error) {
	fail := func() (pipeline.ArtifactResult, error) {
		return pipeline.ArtifactResult{}, e.New(e.CodeProvenanceMissing, "acquired image requires its exact frozen pull payload and result")
	}
	if ctx == nil || plan == nil {
		return pipeline.ArtifactResult{}, e.New(e.CodeUsage, "acquired image authentication requires context and Plan")
	}
	if err := ctx.Err(); err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodeTimeout, err, "acquired image authentication canceled")
	}
	if err := plan.Validate(); err != nil {
		return pipeline.ArtifactResult{}, err
	}
	nodes, planned := planIndex(plan)
	declared, found := planned[input.ID]
	if !found || declared != input || artifacts[input.ID] != input || input.Kind != "oci-image" {
		return fail()
	}
	producer, found := nodes[input.Producer]
	if !found || producer.Kind != NodeAcquire || producer.BuildTarget != input.BuildTarget || !containsString(producer.Outputs, input.ID) {
		return fail()
	}
	build, instruction, err := plan.producerInstruction(producer)
	if err != nil {
		return pipeline.ArtifactResult{}, err
	}
	if instruction.Kind != ir.KindPull || instruction.OutputType != "oci-image" || instruction.Component != input.Component {
		return fail()
	}
	prebuilt := false
	for _, component := range build.ResolvedComponents {
		if component.Name == input.Component {
			prebuilt = !component.FromSource && component.Prebuilt != nil && component.OutputType == "oci-image"
		}
	}
	recordID := "result/" + input.BuildTarget + "/" + input.Component
	record, found := artifacts[recordID]
	if !prebuilt || !found || planned[recordID] != record || record.ID != recordID ||
		record.Kind != "producer-result" || record.Component != input.Component || record.BuildTarget != input.BuildTarget ||
		record.Producer != producer.ID || !containsString(producer.Outputs, record.ID) {
		return fail()
	}
	observed, err := pipeline.AuthenticateResultForConsumption(ctx, instruction, input.Path, record.Path)
	if err != nil {
		return pipeline.ArtifactResult{}, err
	}
	if observed.InputDigest != "" || observed.MaterialManifestDigest != "" {
		return fail()
	}
	return observed, nil
}

// acquiredImageSBOMBinding additionally requires the frozen SBOM input edges.
func acquiredImageSBOMBinding(ctx context.Context, plan *Plan, node Node, payload Artifact,
	artifacts map[string]Artifact, subject string,
) (string, error) {
	if ctx == nil || plan == nil {
		return "", e.New(e.CodeUsage, "acquired image inspection requires context and Plan")
	}
	nodes, _ := planIndex(plan)
	recordID := "result/" + payload.BuildTarget + "/" + payload.Component
	if !reflect.DeepEqual(nodes[node.ID], node) || node.Kind != NodeSBOM || payload.BuildTarget != node.BuildTarget ||
		!containsString(node.Inputs, payload.ID) || !containsString(node.Inputs, recordID) || !canonical.IsDigest(subject) {
		return "", e.New(e.CodeProvenanceMissing, "acquired image inspection changes its frozen SBOM inputs")
	}
	observed, err := authenticateAcquiredImagePayload(ctx, plan, payload, artifacts)
	if err != nil {
		return "", err
	}
	if observed.SHA256 != subject {
		return "", e.New(e.CodeProvenanceMissing, "acquired image SBOM substitutes its native manifest subject")
	}
	for _, build := range plan.Builds {
		if build.Name == node.BuildTarget {
			return build.Platform, nil
		}
	}
	return "", e.New(e.CodePipelineDrift, "acquired image inspection has no frozen build platform")
}

type acquiredImageSBOMArchive struct {
	root, path, digest string
}

// prepareAcquiredImageSBOMArchive authenticates acquisition before any private
// workspace, imports the unchanged native manifest closure, and packages only
// that checked snapshot. Its archive hash is scan transport, never the BOM subject.
func prepareAcquiredImageSBOMArchive(ctx context.Context, plan *Plan, node Node, payload Artifact,
	artifacts map[string]Artifact, subject string,
) (archive *acquiredImageSBOMArchive, returnErr error) {
	platform, err := acquiredImageSBOMBinding(ctx, plan, node, payload, artifacts, subject)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "nkd-acquired-image-sbom-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private acquired image inspection workspace")
	}
	archive = &acquiredImageSBOMArchive{root: root}
	owned := archive
	failed := true
	defer func() {
		if failed {
			returnErr = joinFactoryCleanupError(returnErr, owned.close(), "closing failed acquired image inspection")
			archive = nil
		}
	}()
	snapshot := filepath.Join(root, "image")
	inspection, err := delivery.ImportAcquiredContainerImage(ctx, payload.Path, subject, platform, snapshot)
	if err != nil {
		return nil, err
	}
	if inspection.ManifestDigest != subject {
		return nil, e.New(e.CodeVerifyFailed, "acquired image inspection substitutes its upstream native manifest")
	}
	archive.path = filepath.Join(snapshot, "inspection.tar")
	if err := transportarchive.Pack(ctx, archive.path, snapshot, []string{"blobs", "index.json", "oci-layout"}); err != nil {
		return nil, err
	}
	archive.digest, err = digestFactoryPath(ctx, archive.path)
	if err != nil {
		return nil, err
	}
	failed = false
	return archive, nil
}

func (archive *acquiredImageSBOMArchive) close() error {
	if archive == nil || archive.root == "" {
		return nil
	}
	if err := os.RemoveAll(archive.root); err != nil {
		return e.Wrap(e.CodeInternal, err, "removing private acquired image inspection workspace")
	}
	archive.root = ""
	return nil
}

func inspectAcquiredImageDependencies(ctx context.Context, plan *Plan, node Node, payload Artifact,
	artifacts map[string]Artifact, subject string, scanner sbom.Scanner,
) (inventory sbom.DependencyInventory, returnErr error) {
	if ctx == nil {
		return sbom.DependencyInventory{}, e.New(e.CodeUsage, "acquired image inspection requires context")
	}
	if err := ctx.Err(); err != nil {
		return sbom.DependencyInventory{}, e.Wrap(e.CodeTimeout, err, "acquired image inspection canceled before admission")
	}
	if !canonical.IsDigest(scanner.Digest) || scanner.Path == "" {
		return sbom.DependencyInventory{}, e.New(e.CodeProvenanceMissing, "acquired image inspection requires the pinned scanner")
	}
	if plan == nil {
		return sbom.DependencyInventory{}, e.New(e.CodeUsage, "acquired image inspection requires its frozen Plan")
	}
	_, planned := planIndex(plan)
	toolID := "tool/" + plan.Definition.SBOMTool
	tool, found := artifacts[toolID]
	if !found || tool.Kind != "pipeline-tool" || planned[toolID] != tool || !containsString(node.Inputs, toolID) ||
		scanner.Path != tool.Path || scanner.Digest != toolExecutableDigest(plan, plan.Definition.SBOMTool) {
		return sbom.DependencyInventory{}, e.New(e.CodeVerifyFailed, "acquired image inspection substitutes its frozen scanner")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return sbom.DependencyInventory{}, e.Wrap(e.CodeEngineCapability, err, "acquired image inspection requires held scanner execution")
	}
	archive, err := prepareAcquiredImageSBOMArchive(ctx, plan, node, payload, artifacts, subject)
	if err != nil {
		return sbom.DependencyInventory{}, err
	}
	defer func() {
		if err := archive.close(); err != nil {
			inventory = sbom.DependencyInventory{}
			returnErr = joinFactoryCleanupError(returnErr, err, "closing acquired image inspection")
		}
	}()
	inventory, err = sbom.InspectDependencies(ctx, archive.path, archive.digest, "oci-image", scanner)
	if err != nil {
		return sbom.DependencyInventory{}, err
	}
	// Re-read the original result and closure after scanning. Neither an import
	// nor the temporary archive can waive the selected acquisition binding.
	if _, err := acquiredImageSBOMBinding(ctx, plan, node, payload, artifacts, subject); err != nil {
		return sbom.DependencyInventory{}, err
	}
	return inventory, nil
}
