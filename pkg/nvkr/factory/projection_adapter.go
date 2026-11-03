// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// referenceProjection is the same-module reference GitHub projection plugin. It
// ports the in-binary renderer verbatim, never writes files, and serves both
// transports unchanged. release is the plugin's own label.
type referenceProjection struct{ release string }

func (plugin referenceProjection) describe(context.Context) (projectionDescription, error) {
	return projectionDescription{Protocol: projectionProtocol, Release: plugin.release, Engine: EngineGitHub,
		PlanSchema: PlanSchemaVersion, GeneratorABI: projectionGeneratorABI}, nil
}

func (referenceProjection) emit(ctx context.Context, request projectionEmitRequest) (projectionEmission, error) {
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

// ServeProjectionAdapter serves the reference projection plugin as an
// out-of-process adapter: it reads one request frame from in, answers Describe
// or Emit with one frame on out and exits. It owns in and out for the call. A
// canceled ctx stops it before any I/O. Cancellation during the read or write
// sets that stream's deadline or, when it has none, closes it, and the call
// returns CodeTimeout only after the interrupted read or write returns. A
// blocking descriptor that neither call interrupts stays bounded by its owner:
// the projection host closes the adapter's stdin after one frame and owns the
// adapter's process group.
func ServeProjectionAdapter(ctx context.Context, release string, in io.Reader, out io.Writer) error {
	return serveProjection(ctx, referenceProjection{release: release}, in, out)
}

// serveProjection carries one framed request to any contract implementation.
func serveProjection(ctx context.Context, plugin projectionPlugin, in io.Reader, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "projection request canceled")
	}
	var raw []byte
	err := interruptStreamOnCancel(ctx, in, func() (err error) {
		raw, err = io.ReadAll(io.LimitReader(in, 4+maxProjectionRequestBytes+1))
		return err
	})
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "projection request canceled")
	}
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
	var response any
	if request.Emit == nil {
		response, err = plugin.describe(ctx)
	} else {
		response, err = plugin.emit(ctx, *request.Emit)
	}
	if err != nil {
		return err
	}
	frame, err := encodeProjectionFrame(response)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "projection response canceled")
	}
	err = interruptStreamOnCancel(ctx, out, func() error {
		_, err := out.Write(frame)
		return err
	})
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "projection response canceled")
	}
	if err != nil {
		return e.Wrap(e.CodeUnavailable, err, "writing projection response")
	}
	return nil
}

// interruptStreamOnCancel runs one blocking read or write on stream in the
// caller's goroutine. Cancellation sets the stream's deadline or, failing that,
// closes it; the interruption is joined before return, so nothing outlives the call.
func interruptStreamOnCancel(ctx context.Context, stream any, operation func() error) error {
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		if deadline, ok := stream.(interface{ SetDeadline(time.Time) error }); ok && deadline.SetDeadline(time.Now()) == nil {
			return
		}
		if closer, ok := stream.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	err := operation()
	if !stop() {
		<-interrupted
	}
	return err
}
