// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The out-of-process projection pilot is a diagnostic: a top-level generation
// caller may select a same-module adapter process, core sends it the complete
// canonical Plan, and core admits the returned bundle with the existing exact
// validator before anything is written. Generated stages never consult the
// selection, so no generated job or worker launches an adapter.

const (
	projectionProtocol = "projection/v0-pilot"
	// The adapter emits members of the generated-run manifest contract.
	projectionGeneratorABI         = generatedManifestSchema
	projectionMemberMode           = uint32(0o644)
	maxProjectionPathDepth         = 8
	maxProjectionDescriptionBytes  = 4 << 10
	maxProjectionResponseBytes     = 64 << 20
	maxProjectionDiagnosticBytes   = 16 << 10
	defaultProjectionAdapterBudget = 2 * time.Minute
	// Base64 inflates the exact Plan bytes by 4/3 inside the request envelope.
	maxProjectionRequestBytes = 2 * MaxPlanBytes
)

// ProjectionAdapter names the out-of-process projection adapter a top-level
// generation caller selected. It only describes what to launch: core admits
// the adapter's description and every byte it emits.
type ProjectionAdapter struct {
	// Path is the absolute adapter executable; Args follow it in argv.
	Path string
	Args []string
	// Release is the exact adapter release core requires from Describe.
	Release string
	// Timeout bounds each adapter launch; zero selects two minutes.
	Timeout time.Duration
}

type projectionAdapterKey struct{}

// WithProjectionAdapter selects adapter for Render under ctx. It is the hidden
// diagnostic pilot selector for top-level generation only.
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

// projectionRequest is one adapter request: Describe when Emit is absent.
type projectionRequest struct {
	Protocol string                 `json:"protocol"`
	Emit     *projectionEmitRequest `json:"emit,omitempty"`
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

// renderProjected obtains a candidate bundle from the selected adapter and
// admits it exactly as core admits its own projection. It never writes files.
func renderProjected(ctx context.Context, plan *Plan, adapter ProjectionAdapter) (*GeneratedBundle, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "projection canceled")
	}
	if plan == nil {
		return nil, e.New(e.CodeInvalidCatalog, "projection requires a plan")
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
	layout := githubLayoutFor(plan)
	want := projectionDescription{Protocol: projectionProtocol, Release: adapter.Release, Engine: EngineGitHub,
		PlanSchema: PlanSchemaVersion, GeneratorABI: projectionGeneratorABI}
	var description projectionDescription
	if err := exchangeProjection(ctx, adapter, projectionRequest{Protocol: projectionProtocol}, &description, maxProjectionDescriptionBytes); err != nil {
		return nil, err
	}
	// Refuse a mismatched adapter before it receives any Plan bytes.
	if description != want {
		return nil, e.New(e.CodeEngineCapability, "projection adapter describes %+v; core requires %+v", description, want)
	}
	var emission projectionEmission
	request := projectionRequest{Protocol: projectionProtocol, Emit: &projectionEmitRequest{
		Plan: planBytes, GeneratorVersion: plan.GeneratorVersion, Layout: projectionLayoutFor(layout)}}
	if err := exchangeProjection(ctx, adapter, request, &emission, maxProjectionResponseBytes); err != nil {
		return nil, err
	}
	files, err := admitProjectionEmission(emission, layout, planDigest)
	if err != nil {
		return nil, err
	}
	bundle := newGeneratedBundle(EngineGitHub, layout, files, planDigest)
	if err := ValidateGitHubBundle(ctx, plan, bundle); err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, e.Wrap(e.CodePipelineDrift, err, "projection adapter bundle fails core admission")
	}
	return bundle, nil
}

// admitProjectionEmission checks the response shape before the exact validator
// compares its bytes: sorted unique safe members of the admitted mode and the
// core's own Plan digest and workflow path.
func admitProjectionEmission(emission projectionEmission, layout generationLayout, planDigest string) (map[string][]byte, error) {
	if emission.PlanDigest != planDigest {
		return nil, e.New(e.CodePipelineDrift, "projection adapter echoes planDigest %q, want %q", emission.PlanDigest, planDigest)
	}
	if emission.WorkflowPath != layout.workflowPath {
		return nil, e.New(e.CodePipelineDrift, "projection adapter workflow path %q is not the admitted %q", emission.WorkflowPath, layout.workflowPath)
	}
	if len(emission.Members) == 0 || len(emission.Members) > maxGeneratedDirEntries {
		return nil, e.New(e.CodePipelineDrift, "projection adapter emitted %d members", len(emission.Members))
	}
	files := make(map[string][]byte, len(emission.Members))
	for index, member := range emission.Members {
		if !safeGeneratedPath(member.Path) || strings.Count(member.Path, "/") > maxProjectionPathDepth {
			return nil, e.New(e.CodePipelineDrift, "projection adapter member path %q is unsafe", member.Path)
		}
		if index > 0 && member.Path <= emission.Members[index-1].Path {
			return nil, e.New(e.CodePipelineDrift, "projection adapter members are not sorted and unique at %q", member.Path)
		}
		if member.Mode != projectionMemberMode || int64(len(member.Bytes)) > maxGeneratedFileBytes {
			return nil, e.New(e.CodePipelineDrift, "projection adapter member %q has mode %o or is oversized", member.Path, member.Mode)
		}
		files[member.Path] = member.Bytes
	}
	return files, nil
}

// launchProjectionAdapter runs one adapter exchange. Boundary tests trap it.
var launchProjectionAdapter = runProjectionAdapter

func exchangeProjection(ctx context.Context, adapter ProjectionAdapter, request projectionRequest, response any, limit int) error {
	frame, err := encodeProjectionFrame(request)
	if err != nil {
		return err
	}
	raw, err := launchProjectionAdapter(ctx, adapter, frame, limit)
	if err != nil {
		return err
	}
	return decodeProjectionFrame(raw, response, e.CodePipelineDrift)
}

// runProjectionAdapter launches the adapter as an owned process group with an
// empty environment and a private empty working directory, buffers its whole
// bounded response and reaps it on completion, timeout or cancellation. Exit
// status and stderr are diagnostics only; the host selects every error code.
func runProjectionAdapter(ctx context.Context, adapter ProjectionAdapter, request []byte, limit int) ([]byte, error) {
	if !filepath.IsAbs(adapter.Path) {
		return nil, e.New(e.CodeEngineCapability, "projection adapter path %q is not absolute", adapter.Path)
	}
	budget := adapter.Timeout
	if budget <= 0 {
		budget = defaultProjectionAdapterBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	scratch, err := os.MkdirTemp("", "nvkr-projection-")
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating projection adapter scratch directory")
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	cmd := exec.CommandContext(ctx, adapter.Path, adapter.Args...)
	cmd.Dir, cmd.Env = scratch, []string{}
	cmd.Stdin = bytes.NewReader(request)
	stdout := &projectionBuffer{limit: 4 + limit}
	stderr := &projectionBuffer{limit: maxProjectionDiagnosticBytes, truncate: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = ownedcmd.Run(ctx, cmd)
	switch {
	case ctx.Err() != nil:
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "projection adapter did not finish")
	case stdout.overflow:
		return nil, e.New(e.CodePipelineDrift, "projection adapter response exceeds %d bytes", limit)
	case err != nil:
		return nil, e.Wrap(e.CodeEngineCapability, err, "projection adapter refused: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// projectionBuffer keeps at most limit bytes. A full stdout buffer fails the
// write so the adapter cannot stream unbounded output; stderr truncates. The
// buffer stays unexported so io.Copy cannot bypass Write through ReadFrom.
type projectionBuffer struct {
	buffer   bytes.Buffer
	limit    int
	truncate bool
	overflow bool
}

func (b *projectionBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *projectionBuffer) String() string { return b.buffer.String() }

func (b *projectionBuffer) Write(raw []byte) (int, error) {
	room := b.limit - b.buffer.Len()
	if len(raw) <= room {
		return b.buffer.Write(raw)
	}
	b.overflow = true
	_, _ = b.buffer.Write(raw[:max(room, 0)])
	if b.truncate {
		return len(raw), nil
	}
	return 0, e.New(e.CodePipelineDrift, "projection adapter output exceeds %d bytes", b.limit)
}

// encodeProjectionFrame writes a 4-byte big-endian length then canonical JSON.
func encodeProjectionFrame(message any) ([]byte, error) {
	raw, err := canonical.Marshal(message)
	if err != nil {
		return nil, err
	}
	return append(binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(raw)), uint32(len(raw))), raw...), nil
}

// decodeProjectionFrame admits exactly one complete frame of canonical JSON in
// the strict selected schema; unknown fields and trailing bytes are errors.
func decodeProjectionFrame(frame []byte, out any, code e.Code) error {
	if len(frame) < 4 || uint64(len(frame)-4) != uint64(binary.BigEndian.Uint32(frame)) {
		return e.New(code, "projection frame is truncated or has trailing bytes")
	}
	raw := frame[4:]
	if err := canonical.UnmarshalExact(raw, out); err != nil {
		return e.Wrap(code, err, "decoding projection frame")
	}
	if again, err := canonical.Marshal(out); err != nil || !bytes.Equal(again, raw) {
		return e.New(code, "projection frame is not canonical JSON")
	}
	return nil
}
