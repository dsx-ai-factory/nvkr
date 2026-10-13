// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// EmitFiles loads bounded descriptive inputs, delegates capture/derivation and
// returns the emitted semantic identity. Inputs are not authenticated receipts.
func EmitFiles(ctx context.Context, root, descriptionPath, selectionPath, out string) (string, error) {
	raw, err := readInput(ctx, descriptionPath, MaxObservationBytes)
	if err != nil {
		return "", err
	}
	var observation Observation
	if err := canonical.UnmarshalExact(raw, &observation); err != nil {
		return "", err
	}
	raw, err = readInput(ctx, selectionPath, MaxObservationBytes)
	if err != nil {
		return "", err
	}
	var files []FileInput
	if err := canonical.UnmarshalExact(raw, &files); err != nil {
		return "", err
	}
	if err := Emit(ctx, root, observation, files, out); err != nil {
		return "", err
	}
	bundle, err := Open(ctx, out)
	if err != nil {
		return "", err
	}
	defer func() { _ = bundle.Close() }()
	return bundle.Digest(), nil
}

func LoadExpected(ctx context.Context, path string) (Expected, error) {
	raw, err := readInput(ctx, path, MaxObservationBytes)
	if err != nil {
		return Expected{}, err
	}
	var expected Expected
	if err := canonical.UnmarshalExact(raw, &expected); err != nil {
		return Expected{}, err
	}
	return expected, expected.Validate()
}

func LoadLocator(ctx context.Context, path string) (Locator, error) {
	raw, err := readInput(ctx, path, 16384)
	if err != nil {
		return Locator{}, err
	}
	return DecodeLocator(raw)
}

func readInput(ctx context.Context, path string, max int64) ([]byte, error) {
	if err := captureContext(ctx); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, e.New(e.CodeUsage, "observation input path is required")
	}
	file, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "observation-input", MaxBytes: max})
	if err != nil {
		return nil, captureError(ctx, err, "holding observation input")
	}
	defer func() { _ = file.Close() }()
	raw, err := file.ReadAllContext(ctx, max)
	if err != nil {
		return nil, captureError(ctx, err, "reading observation input")
	}
	return raw, nil
}

// Render describes recorded scope and results, keeping integrity and test outcome separate.
// This presentation never issues authority or treats an inaccessible file as verified.
func Render(observation Observation, verification Verification) string {
	trust := "unsigned self-consistency"
	if verification.SignerAuthenticated {
		trust = "authenticated signer observation"
	}
	return fmt.Sprintf("Trust: %s\nObservation: %s\nSuite: %s\nRole: %s\nCoverage: %s\nTests: %d total, %d passed, %d failed, %d errors, %d skipped\nExecution: %s; cleanup: %s\nRecorded required-case result passing: %t\nEnvironment facts are recorded declarations/API observations, not authenticated installed-byte proof.\n",
		trust, verification.ObservationDigest, strconv.Quote(observation.Suite.Name), strconv.Quote(observation.Environment.Role), outputNames(observation.Subjects), observation.Summary.Total, observation.Summary.Passed, observation.Summary.Failed, observation.Summary.Errors, observation.Summary.Skipped, observation.Execution.Status, observation.Cleanup.Status, verification.Passing)
}

func outputNames(subjects []Subject) string {
	names := make([]string, len(subjects))
	for i, subject := range subjects {
		names[i] = strconv.Quote(subject.Output) + " (" + strconv.Quote(subject.Platform) + ")"
	}
	return strings.Join(names, ", ")
}
