// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// localQualificationInputs is validated descriptive input, not a receipt. A
// completed record and a reproducible Plan do not authenticate payload bytes,
// producer execution, signatures or qualification. The local owner must still
// independently capture and check every output before issuing any authority.
type localQualificationInputs struct {
	plan                            *factory.Plan
	run                             Run
	planBytes, runBytes, inputBytes []byte
}

// InspectLocalRunInputs rederives a completed local Run's frozen metadata from
// its captured compiler inputs. The returned Plan and Run are detached,
// descriptive values, not receipts. No payload is opened or accepted; consumers
// must still verify the selected artifact bytes through the owning modules.
func InspectLocalRunInputs(ctx context.Context, attemptDirectory string) (*factory.Plan, Run, error) {
	inputs, err := loadLocalQualificationInputs(ctx, attemptDirectory)
	if err != nil {
		return nil, Run{}, err
	}
	return inputs.plan, inputs.run, nil
}

// loadLocalQualificationInputs snapshots metadata under one held attempt
// directory, rejects an incomplete commit, and rederives every required slot
// from the original compiler reads. It never consults the current checkout.
func loadLocalQualificationInputs(ctx context.Context, workspace string) (*localQualificationInputs, error) {
	if ctx == nil || workspace == "" {
		return nil, e.New(e.CodeUsage, "local qualification requires a context and explicit attempt directory")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local qualification input loading canceled")
	}
	root, err := openRunInspectionDirectory(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := requireLocalQualificationCommit(root); err != nil {
		return nil, err
	}
	rawRun, err := readLocalQualificationMetadata(ctx, root, "lifecycle.json", MaxRunRecordBytes)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := canonical.UnmarshalExact(rawRun, &run); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "local qualification requires exact canonical Run metadata")
	}
	if err := validateInspectedRun(&run); err != nil {
		return nil, err
	}
	if run.Status != RunComplete || run.Failure != nil || run.Lane != factory.TierLocalExecution.String() || run.ContentSubject == "" {
		return nil, e.New(e.CodeGateUnmet, "local qualification requires a complete local Run with content identity")
	}
	if run.Attempt != filepath.Base(filepath.Clean(workspace)) {
		return nil, e.New(e.CodeVerifyFailed, "local qualification Run differs from its attempt directory")
	}
	// Base64 expansion of the bounded 32 MiB compiler-read closure, plus its
	// bounded path/directory records, fits within this transport limit.
	rawInputs, err := readLocalQualificationMetadata(ctx, root, "compile-inputs.json", 64<<20)
	if err != nil {
		return nil, err
	}
	runner, _, err := securepath.OpenChild(root, ".runner", true)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "opening original local Plan directory")
	}
	defer runner.Close()
	rawPlan, err := readLocalQualificationMetadata(ctx, runner, "plan.json", factory.MaxPlanBytes)
	if err != nil {
		return nil, err
	}
	replayed, err := replayLocalQualificationInputs(ctx, rawPlan, rawRun, rawInputs)
	if err != nil {
		return nil, err
	}
	// Reject commits that began while metadata was being read. No directory
	// or serialized status is used as a claim about producer liveness.
	if err := requireLocalQualificationCommit(root); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local qualification input loading canceled")
	}
	return replayed, nil
}

// replayLocalQualificationInputs validates transported metadata without a producer
// workspace. Its result is descriptive until signatures and payloads authenticate.
func replayLocalQualificationInputs(ctx context.Context, rawPlan, rawRun, rawInputs []byte) (*localQualificationInputs, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local input replay requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local input replay canceled")
	}
	if len(rawPlan) == 0 || int64(len(rawPlan)) > factory.MaxPlanBytes || len(rawRun) == 0 || int64(len(rawRun)) > MaxRunRecordBytes || len(rawInputs) == 0 || len(rawInputs) > 64<<20 {
		return nil, e.New(e.CodeVerifyFailed, "local replay metadata is missing or oversized")
	}
	rawPlan, rawRun, rawInputs = bytes.Clone(rawPlan), bytes.Clone(rawRun), bytes.Clone(rawInputs)
	var run Run
	if err := canonical.UnmarshalExact(rawRun, &run); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "local qualification requires exact canonical Run metadata")
	}
	if err := validateInspectedRun(&run); err != nil {
		return nil, err
	}
	if run.Status != RunComplete || run.Failure != nil || run.Lane != factory.TierLocalExecution.String() || run.ContentSubject == "" {
		return nil, e.New(e.CodeGateUnmet, "local qualification requires a complete local Run with content identity")
	}
	var inputs factory.InputSnapshot
	if err := canonical.UnmarshalExact(rawInputs, &inputs); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "local qualification requires exact captured compiler inputs")
	}
	var original factory.Plan
	if err := canonical.UnmarshalExact(rawPlan, &original); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding original local Plan")
	}
	if err := original.Validate(); err != nil {
		return nil, err
	}
	compiled, err := factory.CompileSnapshot(ctx, inputs, original.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	want, err := compiled.Digest()
	if err != nil {
		return nil, err
	}
	got, err := original.Digest()
	if err != nil {
		return nil, err
	}
	if got != want || got != run.PlanDigest {
		return nil, e.New(e.CodePipelineDrift, "local Run Plan differs from its original captured inputs")
	}
	prepared, err := PrepareRun(compiled.Definition, compiled.Environment, compiled, run.Attempt)
	if err != nil {
		return nil, err
	}
	// Compare all frozen fields, including producer result mappings. Merely
	// repeating the Plan digest in a caller-written Run does not establish them.
	description := run
	description.Lane, description.Status = prepared.Lane, prepared.Status
	description.Observed, description.ContentSubject = nil, ""
	wantRun, err := canonical.Marshal(prepared)
	if err != nil {
		return nil, err
	}
	gotRun, err := canonical.Marshal(description)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(wantRun, gotRun) {
		return nil, e.New(e.CodePipelineDrift, "local Run changes its recompiled frozen input or output bindings")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local input replay canceled")
	}
	return &localQualificationInputs{plan: compiled, run: run, planBytes: rawPlan, runBytes: rawRun, inputBytes: rawInputs}, nil
}

func requireLocalQualificationCommit(root *os.File) error {
	marker, _, err := securepath.OpenChild(root, diskAttemptMarker, false)
	if err == nil {
		_ = marker.Close()
		return e.New(e.CodeImmutableViolation, "local attempt has an incomplete disk commit or cleanup")
	}
	if !os.IsNotExist(err) {
		return e.Wrap(e.CodeImmutableViolation, err, "checking local attempt commit marker")
	}
	return nil
}

func readLocalQualificationMetadata(ctx context.Context, root *os.File, name string, limit int64) ([]byte, error) {
	source, info, err := securepath.OpenChild(root, name, false)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening local qualification metadata %s", name)
	}
	defer source.Close()
	if info.Size() <= 0 || info.Size() > limit {
		return nil, e.New(e.CodeVerifyFailed, "local qualification metadata %s is empty or oversized", name)
	}
	held, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(source, 0, info.Size()), heldfile.Options{Label: "local-qualification-metadata", MaxBytes: limit})
	if err != nil {
		return nil, runInspectionReadError(ctx, err)
	}
	defer func() { _ = held.Close() }()
	after, err := source.Stat()
	if err != nil || !securepath.SameMetadata(info, after) || held.Size() != info.Size() {
		return nil, e.New(e.CodeVerifyFailed, "local qualification metadata changed while reading %s", name)
	}
	raw, err := held.ReadAllContext(ctx, limit)
	if err != nil {
		return nil, runInspectionReadError(ctx, err)
	}
	return raw, nil
}
