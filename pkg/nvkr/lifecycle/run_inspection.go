// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// MaxRunRecordBytes bounds metadata inspection, not execution admission.
const MaxRunRecordBytes int64 = 4 << 20

// RunInspection is a safe detached metadata view. It authenticates no payload,
// current availability, release policy, runtime health or executable capability.
type RunInspection struct {
	SchemaVersion     string `json:"schemaVersion"`
	Authority         string `json:"authority"`
	Attempt           string `json:"attempt,omitempty"`
	PlanDigest        string `json:"planDigest,omitempty"`
	Lane              string `json:"lane,omitempty"`
	Status            string `json:"status"`
	FailureCode       string `json:"failureCode,omitempty"`
	RecordAvailable   bool   `json:"recordAvailable"`
	CleanupIncomplete bool   `json:"cleanupIncomplete"`
	// Interrupted remains false: stored metadata cannot exclude a live owner.
	Interrupted bool                 `json:"interrupted"`
	Outputs     []InspectedRunOutput `json:"outputs"`
}

// InspectedRunOutput describes declared metadata, never current retained bytes.
type InspectedRunOutput struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Digest string `json:"digest,omitempty"`
}

// InspectRun snapshots canonical lifecycle.json and the sibling attempt marker
// through held descriptors without following symlinks in any path component.
// The bounded regular record must have exact canonical bytes and valid frozen
// output bindings. Returned fields omit raw failure messages and output locators;
// inspection never opens payloads or modifies the attempt's storage.
// Missing records and records from a differently named attempt directory are
// rejected. Neither record absence nor marker presence establishes interruption
// or owner liveness; a marker only describes possible incomplete disk cleanup.
func InspectRun(ctx context.Context, recordPath string) (RunInspection, error) {
	if ctx == nil {
		return RunInspection{}, e.New(e.CodeUsage, "Run inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return RunInspection{}, e.Wrap(e.CodeTimeout, err, "Run inspection canceled")
	}
	if recordPath == "" || filepath.Base(recordPath) != "lifecycle.json" {
		return RunInspection{}, e.New(e.CodeUsage, "Run inspection requires an explicit lifecycle.json record")
	}
	root, err := openRunInspectionDirectory(ctx, filepath.Dir(recordPath))
	if err != nil {
		return RunInspection{}, err
	}
	defer root.Close()
	view := RunInspection{SchemaVersion: "nvkr.dev/run-inspection/v1", Authority: "description-only", Status: "unavailable",
		Outputs: []InspectedRunOutput{}}
	marker, _, markerErr := securepath.OpenChild(root, diskAttemptMarker, false)
	if markerErr == nil {
		view.CleanupIncomplete = true
		_ = marker.Close()
	} else if !os.IsNotExist(markerErr) {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "cannot safely inspect the local attempt marker")
	}
	source, info, err := securepath.OpenChild(root, "lifecycle.json", false)
	if os.IsNotExist(err) {
		if canceled := ctx.Err(); canceled != nil {
			return RunInspection{}, e.Wrap(e.CodeTimeout, canceled, "Run inspection canceled")
		}
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record is unavailable; owner liveness and interruption are unknown")
	}
	if err != nil {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record must be a regular file without symlinks")
	}
	defer source.Close()
	if info.Size() <= 0 || info.Size() > MaxRunRecordBytes {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record exceeds its metadata size bound or is empty")
	}
	held, err := heldfile.FromReaderContext(ctx, io.NewSectionReader(source, 0, info.Size()),
		heldfile.Options{Label: "run-inspection", MaxBytes: MaxRunRecordBytes})
	if err != nil {
		return RunInspection{}, runInspectionReadError(ctx, err)
	}
	defer func() { _ = held.Close() }()
	after, err := source.Stat()
	if err != nil || !securepath.SameMetadata(info, after) || held.Size() != info.Size() {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record changed while being inspected")
	}
	raw, err := held.ReadAllContext(ctx, MaxRunRecordBytes)
	if err != nil {
		return RunInspection{}, runInspectionReadError(ctx, err)
	}
	var run Run
	if err := canonical.UnmarshalExact(raw, &run); err != nil {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record is not exact canonical metadata")
	}
	if err := validateInspectedRun(&run); err != nil {
		return RunInspection{}, err
	}
	if run.Attempt != filepath.Base(root.Name()) {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "Run record does not match its attempt directory")
	}
	// The marker can be created before an immutable record becomes visible. A
	// second descriptor-relative sample avoids describing that new record without
	// its in-progress cleanup marker. Presence is conservative metadata only.
	marker, _, markerErr = securepath.OpenChild(root, diskAttemptMarker, false)
	if markerErr == nil {
		view.CleanupIncomplete = true
		_ = marker.Close()
	} else if !os.IsNotExist(markerErr) {
		return RunInspection{}, e.New(e.CodeVerifyFailed, "cannot safely inspect the local attempt marker")
	}
	if err := ctx.Err(); err != nil {
		return RunInspection{}, e.Wrap(e.CodeTimeout, err, "Run inspection canceled")
	}
	view.Attempt, view.PlanDigest, view.Lane, view.Status = run.Attempt, run.PlanDigest, run.Lane, run.Status
	view.RecordAvailable = true
	if run.Failure != nil {
		view.FailureCode = run.Failure.Code
	}
	observed := make(map[string]Artifact, len(run.Observed))
	for _, output := range run.Observed {
		observed[output.Name] = output
	}
	for _, required := range run.Expected {
		output := InspectedRunOutput{Name: required.Name, Kind: required.Kind, State: "required"}
		if artifact, exists := observed[required.Name]; exists {
			output.State, output.Digest = "observed", artifact.Digest
		}
		view.Outputs = append(view.Outputs, output)
	}
	return view, nil
}

// openRunInspectionDirectory walks from the filesystem root using no-follow
// directory descriptors. The caller owns the returned descriptor and must close it.
func openRunInspectionDirectory(ctx context.Context, path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, e.New(e.CodeUsage, "cannot resolve Run record directory")
	}
	root, _, err := securepath.OpenDirectory(string(filepath.Separator))
	if err != nil {
		return nil, e.New(e.CodeVerifyFailed, "cannot open Run record directory")
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			_ = root.Close()
			return nil, e.Wrap(e.CodeTimeout, err, "Run inspection canceled")
		}
		next, _, openErr := securepath.OpenChild(root, part, true)
		_ = root.Close()
		if openErr != nil {
			if os.IsNotExist(openErr) {
				return nil, e.New(e.CodeVerifyFailed, "Run record directory is unavailable")
			}
			return nil, e.New(e.CodeVerifyFailed, "Run record directories must exist without symlinks")
		}
		root = next
	}
	return root, nil
}

// runInspectionReadError preserves cancellation classification while mapping
// other bounded-read failures to the record-verification boundary.
func runInspectionReadError(ctx context.Context, _ error) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "Run inspection canceled")
	}
	return e.New(e.CodeVerifyFailed, "reading bounded Run metadata failed")
}

// validateInspectedRun checks only recorded descriptions: known vocabularies,
// frozen output bindings, and any declared content identity. It reuses the owning
// slot join instead of treating a canonical document as payload authentication.
func validateInspectedRun(run *Run) error {
	if err := validateRun(run); err != nil {
		return e.New(e.CodeVerifyFailed, "Run record has an invalid frozen description")
	}
	switch run.Lane {
	case LaneUnqualified, LanePipeline, factory.TierLocalExecution.String():
	default:
		return e.New(e.CodeVerifyFailed, "Run record has an unknown evidence lane")
	}
	for _, output := range run.Expected {
		switch output.Kind {
		case OutputExecutable, OutputOCI, OutputAPT, OutputHelm, OutputDiskImage:
		default:
			return e.New(e.CodeVerifyFailed, "Run record has an unknown output format")
		}
	}
	if run.Failure != nil {
		known := false
		for _, code := range e.AllCodes {
			known = known || string(code) == run.Failure.Code
		}
		if !known {
			return e.New(e.CodeVerifyFailed, "Run record has an unknown failure code")
		}
	}
	if run.Status != RunComplete {
		if len(run.Observed) != 0 || run.ContentSubject != "" {
			return e.New(e.CodeVerifyFailed, "unfinished Run record carries completed output metadata")
		}
		return nil
	}
	// Reuse the owning exact-slot join without authenticating payloads. This catches
	// duplicate, substituted and sibling observations that validateRun alone cannot.
	pending := *run
	pending.Status, pending.Observed, pending.Failure, pending.ContentSubject = RunPending, nil, nil, ""
	bound, err := bindObserved(&pending, run.Observed)
	if err != nil || bound.Status != RunComplete {
		return e.New(e.CodeVerifyFailed, "Run record observations do not match its required outputs")
	}
	if run.ContentSubject != "" {
		content, err := delivery.ContentDigest(run.Observed)
		if err != nil || content != run.ContentSubject {
			return e.New(e.CodeVerifyFailed, "Run record content identity does not match its output descriptions")
		}
	}
	return nil
}
