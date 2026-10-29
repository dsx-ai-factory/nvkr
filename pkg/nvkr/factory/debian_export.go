// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"reflect"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// ExportWorkspaceDebianPayload copies one exact named raw package from its
// ordinary producer result. This authenticates local bytes and their declared
// producer only; it grants no execution qualification or publication authority.
func ExportWorkspaceDebianPayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan,
	workspace string, destination io.Writer,
) (result pipeline.ArtifactResult, returnErr error) {
	if ctx == nil || plan == nil || workspace == "" || destination == nil {
		return result, e.New(e.CodeUsage, "raw package export requires a context, Plan, workspace and destination")
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "raw package export canceled")
	}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	matched := false
	for _, output := range plan.LifecycleOutputs {
		if reflect.DeepEqual(output, expected) && output.Kind == authored.OutputDebianPackage {
			matched = true
		}
	}
	if !matched || len(expected.Components) != 1 || expected.ImageIndex != nil {
		return result, e.New(e.CodeVerifyFailed, "raw package export changes the exact named output")
	}
	localized, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		return result, err
	}
	nodes, artifacts := planIndex(localized)
	payload := artifacts[expected.ArtifactID]
	record := artifacts["result/"+expected.Name+"/"+expected.Components[0]]
	build, producer, err := localized.producerInstruction(nodes[payload.Producer])
	if err != nil {
		return result, err
	}
	if build.Name != expected.Name || build.BuildDigest != expected.BuildDigest || producer.Kind != ir.KindBuild ||
		producer.OutputType != "host-package" || producer.Component != expected.Components[0] || payload.Kind != producer.OutputType ||
		payload.BuildTarget != build.Name || payload.Component != producer.Component || record.Kind != "producer-result" ||
		record.BuildTarget != build.Name || record.Component != producer.Component || record.Producer != payload.Producer {
		return result, e.New(e.CodeVerifyFailed, "raw package export has no exact producer payload/result pair")
	}
	// Bound file I/O before reopening the producer record. The subsequent
	// authenticated result and held snapshot recheck both identity and length.
	info, err := os.Lstat(payload.Path)
	if err != nil {
		return result, e.Wrap(e.CodeVerifyFailed, err, "opening raw package output")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > pipeline.MaxBuildMaterialBytes {
		return result, e.New(e.CodeEngineCapability, "raw package output is not one bounded regular file")
	}
	result, err = pipeline.AuthenticateResultForConsumption(ctx, producer, payload.Path, record.Path)
	if err != nil {
		return pipeline.ArtifactResult{}, err
	}
	if result.AbsPath == "" || result.TransportPath != "" || result.Size <= 0 || result.Size > pipeline.MaxBuildMaterialBytes {
		return pipeline.ArtifactResult{}, e.New(e.CodeEngineCapability, "raw package output is not one bounded regular file")
	}
	held, err := heldfile.OpenContext(ctx, result.AbsPath, heldfile.Options{
		Label: "raw-package-" + expected.Name, MaxBytes: pipeline.MaxBuildMaterialBytes, ExpectedDigest: result.SHA256,
	})
	if err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodeVerifyFailed, err, "holding raw package output")
	}
	defer func() {
		returnErr = joinFactoryCleanupError(returnErr, held.Close(), "closing raw package output")
		if returnErr != nil {
			result = pipeline.ArtifactResult{}
		}
	}()
	if held.Size() != result.Size {
		return result, e.New(e.CodeVerifyFailed, "held raw package size differs from its producer result")
	}
	written, err := held.WriteToContext(ctx, destination)
	if err != nil || written != result.Size {
		if ctx.Err() != nil {
			return result, e.Wrap(e.CodeTimeout, ctx.Err(), "copying raw package output")
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		return result, e.Wrap(e.CodeVerifyFailed, err, "copying raw package output")
	}
	return result, nil
}
