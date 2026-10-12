// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func diskBuildProvenance(plan *Plan) bool {
	return plan != nil && plan.Environment.DiskEvidence == sbom.DiskBuildProvenance
}

func diskBuildDocuments(build BuildPlan, hashes map[string]string) (map[string]*sbom.BOM, error) {
	documents, _, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: build.Platform, Components: build.ResolvedComponents}, build.BuildDigest, hashes)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(documents) {
		documents[name], err = sbom.ForDiskBuildProvenance(documents[name], hashes[name])
		if err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func validateDiskBuildDocument(plan *Plan, output, component, subject string, document *sbom.BOM) error {
	if err := sbom.ValidateDiskBuildProvenance(document); err != nil {
		return err
	}
	for _, build := range plan.Builds {
		if build.Name != output {
			continue
		}
		for _, source := range build.ResolvedComponents {
			if source.Name != component {
				continue
			}
			selected := build
			selected.ResolvedComponents = []catalog.NamedComponent{source}
			expected, err := diskBuildDocuments(selected, map[string]string{component: subject})
			if err != nil {
				return err
			}
			want, err := expected[component].Digest()
			if err != nil {
				return err
			}
			got, err := document.Digest()
			if err != nil {
				return err
			}
			if want != got {
				return e.New(e.CodeVerifyFailed, "disk producer evidence differs from exact source, subject or scope")
			}
			return nil
		}
	}
	return e.New(e.CodeProvenanceMissing, "disk producer evidence lacks its planned component")
}

// packageDiskBuildProvenance authenticates the original held QCOW2 and every
// transitive material handoff. It does not manufacture an inspection receipt.
func (owner *LocalRunResult) packageDiskBuildProvenance(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, output Artifact) (returnErr error) {
	if !diskBuildProvenance(plan) || plan.Qualification != TierPipelineAttested || plan.Environment.GitHubARC != "native-kvm-v1" {
		return e.New(e.CodeEngineCapability, "disk build provenance requires its explicit hosted policy")
	}
	platform, _, err := reconciledDiskPackageBinding(plan, expected, output)
	if err != nil {
		return err
	}
	trace, err := owner.observeDiskMaterials(ctx, plan, expected)
	if err != nil {
		return err
	}
	view := trace.Description()
	hashes := make(map[string]string, len(view.Members))
	for _, member := range view.Members {
		hashes[member.Component] = member.Digest
	}
	var build BuildPlan
	for _, candidate := range plan.Builds {
		if candidate.Name == expected.Name {
			build = candidate
		}
	}
	documents, err := diskBuildDocuments(build, hashes)
	if err != nil {
		return err
	}
	evidence, err := prepareReconciledEvidence(ctx, plan, expected, documents[view.Subject.Component], documents)
	if err != nil {
		return err
	}
	workspace := plan.workspace
	if workspace == "" {
		workspace = filepath.Join(".nvkr", "factory")
	}
	capture := &reconciledDiskCapture{ctx: ctx, parent: filepath.Dir(output.Path), workspace: workspace,
		size: view.Subject.Size, metadataBytes: int64(len(expected.Components)+4) * sbom.MaxDocumentBytes}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, capture.close(), "close disk build carrier") }()
	observed, err := owner.ExportDiskLifecyclePayload(ctx, plan, expected, capture)
	if err != nil {
		return err
	}
	if observed.Digest != view.Subject.Digest || observed.Size != view.Subject.Size || observed.Format != "qcow2" || observed.Encryption != "none" {
		return e.New(e.CodeVerifyFailed, "disk export changed its traced producer subject")
	}
	if err := capture.finish(); err != nil {
		return err
	}
	_, err = publishReconciledDiskPackage(ctx, workspace, output.Path, evidence, capture.close,
		func(ctx context.Context, path string) (delivery.Result, error) {
			return delivery.BuildDiskArtifact(ctx, path, delivery.DiskArtifactRequest{Path: capture.path,
				Digest: observed.Digest, Size: observed.Size, Platform: platform, Format: "qcow2", Encryption: "none", ArtifactType: output.MediaType})
		})
	return err
}
