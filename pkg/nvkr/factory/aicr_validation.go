// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func validateAICRProducer(ctx context.Context, n ir.Node, result *pipeline.ArtifactResult, tool, runtime string, access pipeline.ImageAccess, stdout, stderr io.Writer) error {
	validated := false
	defer func() {
		if !validated {
			_ = os.RemoveAll(result.TransportPath)
		}
	}()
	temporary, err := os.MkdirTemp(filepath.Dir(result.TransportPath), ".nvkr-aicr-snapshot-")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating AICR snapshot")
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	snapshot := filepath.Join(temporary, "layout")
	if _, err := delivery.ImportObservedBundleLayout(ctx, snapshot, result.TransportPath, result.SHA256); err != nil {
		return err
	}
	descriptor, archive, err := delivery.ReadArtifact(ctx, snapshot)
	if err != nil {
		return err
	}
	if descriptor.ArtifactType != delivery.MediaAICRArtifact {
		return e.New(e.CodeEngineCapability, "layout has no admitted post-render validator")
	}
	if err := pipeline.ValidateAICRBundle(ctx, n, archive, pipeline.RunOptions{Ctx: ctx, Runtime: runtime, ToolExecutable: tool, ImageAccess: access, Stdout: stdout, Stderr: stderr}); err != nil {
		return err
	}
	result.AICRValidation = &pipeline.AICRValidation{ToolDigest: n.Digest, IndexDigest: result.SHA256, ManifestDigest: descriptor.Digest, LayerDigest: pipeline.FileDigest(archive)}
	if err := observeAICRValidation(ctx, n, result.TransportPath, *result); err != nil {
		return err
	}
	validated = true
	return nil
}

func observeAICRValidation(ctx context.Context, n ir.Node, source string, result pipeline.ArtifactResult) error {
	binding := result.AICRValidation
	descriptor, err := delivery.ObservedBundleManifest(ctx, source, result.SHA256)
	if err != nil {
		return err
	}
	if binding == nil || binding.ToolDigest != n.Digest || result.InputDigest != n.Digest || binding.IndexDigest != result.SHA256 || binding.ManifestDigest != descriptor.Digest {
		return e.New(e.CodeVerifyFailed, "layout result omits its exact AICR validation binding")
	}
	actual, archive, err := delivery.ReadArtifact(ctx, source)
	if err != nil {
		return err
	}
	if actual.ArtifactType != delivery.MediaAICRArtifact || actual.Digest != binding.ManifestDigest || pipeline.FileDigest(archive) != binding.LayerDigest {
		return e.New(e.CodeVerifyFailed, "layout closure differs from AICR validation")
	}
	return nil
}
