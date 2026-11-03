// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ServeProjectionAdapter is the same-module reference GitHub projection adapter
// of the out-of-process pilot. It reads one request frame from in, answers
// Describe or Emit with one frame on out and exits; it ports the in-binary
// renderer verbatim and never writes files. release is the adapter's own label.
func ServeProjectionAdapter(ctx context.Context, release string, in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, 4+maxProjectionRequestBytes+1))
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "reading projection request")
	}
	var request projectionRequest
	if err := decodeProjectionFrame(raw, &request, e.CodeUsage); err != nil {
		return err
	}
	if request.Protocol != projectionProtocol {
		return e.New(e.CodeEngineCapability, "projection protocol %q is unsupported; this adapter speaks %s", request.Protocol, projectionProtocol)
	}
	var response any = projectionDescription{Protocol: projectionProtocol, Release: release, Engine: EngineGitHub,
		PlanSchema: PlanSchemaVersion, GeneratorABI: projectionGeneratorABI}
	if request.Emit != nil {
		if response, err = emitProjection(ctx, *request.Emit); err != nil {
			return err
		}
	}
	frame, err := encodeProjectionFrame(response)
	if err != nil {
		return err
	}
	_, err = out.Write(frame)
	return err
}

func emitProjection(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
	var plan Plan
	if err := canonical.UnmarshalExact(request.Plan, &plan); err != nil {
		return projectionEmission{}, e.Wrap(e.CodeUsage, err, "decoding projection Plan")
	}
	if plan.GeneratorVersion != request.GeneratorVersion {
		return projectionEmission{}, e.New(e.CodeUsage, "projection Plan generator %q is not the requested %q", plan.GeneratorVersion, request.GeneratorVersion)
	}
	if want := projectionLayoutFor(githubLayoutFor(&plan)); request.Layout != want {
		return projectionEmission{}, e.New(e.CodeEngineCapability, "projection layout %+v is not this Plan's %+v", request.Layout, want)
	}
	bundle, err := RenderGitHub(ctx, &plan)
	if err != nil {
		return projectionEmission{}, err
	}
	members := make([]projectionMember, 0, len(bundle.Files))
	for _, name := range sortedFileNames(bundle.Files) {
		members = append(members, projectionMember{Path: name, Mode: projectionMemberMode, Bytes: bundle.Files[name]})
	}
	return projectionEmission{PlanDigest: bundle.PlanDigest, WorkflowPath: bundle.WorkflowPath, Members: members}, nil
}
