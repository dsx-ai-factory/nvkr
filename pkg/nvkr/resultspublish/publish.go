// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package resultspublish

import (
	"bytes"
	"context"
	"errors"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// MaxReadBytes bounds a read-back. Publish refuses to write a larger object, so a
// larger existing object can never match.
const MaxReadBytes = qualificationoutcome.MaxBytes

// ErrExists reports that Create found an object already at its path.
var ErrExists = errors.New("an object already exists at this path")

// ObjectStore is a create-only object store. Its errors are coded and name no
// bucket or credential.
type ObjectStore interface {
	// Create writes data at path only if nothing is there, and otherwise returns
	// ErrExists without changing the object.
	Create(ctx context.Context, path string, data []byte) error
	// Read returns the object at path, refusing one larger than maxBytes.
	Read(ctx context.Context, path string, maxBytes int64) ([]byte, error)
}

type object struct {
	path string
	data []byte
}

// Publish writes every outcome under group and definition, in publish order
// (qualificationoutcome.SortOutcomes). For each outcome it writes the record, then
// the TestGrid build's started.json, artifacts/junit.xml and finished.json, so a
// build is complete only once finished.json exists. Writes are create-only. An
// object that already exists must equal what the outcome projects, so a second or
// resumed pass writes only what is missing; a different object is
// CodeImmutableViolation. Every outcome is projected before anything is written,
// and Publish stops at the first error.
func Publish(ctx context.Context, store ObjectStore, outcomes []qualificationoutcome.Outcome, group, definition string) error {
	if ctx == nil || store == nil {
		return e.New(e.CodeUsage, "publishing requires context and a store")
	}
	if len(outcomes) == 0 {
		return e.New(e.CodeUsage, "publishing requires an outcome")
	}
	ordered := slices.Clone(outcomes)
	qualificationoutcome.SortOutcomes(ordered)
	var objects []object
	for _, outcome := range ordered {
		projected, err := project(outcome, group, definition)
		if err != nil {
			return err
		}
		objects = append(objects, projected...)
	}
	for _, item := range objects {
		if err := put(ctx, store, item); err != nil {
			return err
		}
	}
	return nil
}

func project(outcome qualificationoutcome.Outcome, group, definition string) ([]object, error) {
	at, err := layoutFor(outcome, group, definition)
	if err != nil {
		return nil, err
	}
	record, err := outcome.Marshal()
	if err != nil {
		return nil, err
	}
	started, err := startedJSON(outcome, at, group, definition)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding started.json")
	}
	junit, err := junitXML(outcome, definition)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding junit.xml")
	}
	finished, err := finishedJSON(outcome, at, group, definition)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding finished.json")
	}
	objects := []object{
		{at.record, record},
		{at.build + "started.json", started},
		{at.build + "artifacts/junit.xml", junit},
		{at.build + "finished.json", finished},
	}
	// A retry reads each object back within MaxReadBytes, so a larger one could
	// never complete its build. An outcome bounds its own size, not its JUnit's.
	for _, item := range objects {
		if len(item.data) > MaxReadBytes {
			return nil, e.New(e.CodeVerifyFailed, "%s would exceed %d bytes", item.path, MaxReadBytes)
		}
	}
	return objects, nil
}

func put(ctx context.Context, store ObjectStore, item object) error {
	err := store.Create(ctx, item.path, item.data)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrExists) {
		return coded(err, "writing %s", item.path)
	}
	existing, err := store.Read(ctx, item.path, MaxReadBytes)
	if err != nil {
		return coded(err, "reading back %s", item.path)
	}
	if !bytes.Equal(existing, item.data) {
		return e.New(e.CodeImmutableViolation, "%s already exists with different content", item.path)
	}
	return nil
}

// coded keeps a store's coded error and gives an uncoded one the transient code.
func coded(err error, format string, args ...any) error {
	var known *e.Error
	if errors.As(err, &known) {
		return err
	}
	return e.Wrap(e.CodeUnavailable, err, format, args...)
}
