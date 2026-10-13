// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func readDescription(ctx context.Context, path string, max int64) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, e.New(e.CodeUsage, "canonical reporting description path required")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "test-report-description", MaxBytes: max})
	if err != nil {
		if canceled := checkContext(ctx); canceled != nil {
			return nil, canceled
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding reporting description")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, max)
	if err != nil {
		if canceled := checkContext(ctx); canceled != nil {
			return nil, canceled
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading reporting description")
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

// LoadInputs reads bounded independently authored coordinates, expectations and
// source admission. They are descriptions, never restored authenticated receipts.
func LoadInputs(ctx context.Context, path string) ([]Input, error) {
	raw, err := readDescription(ctx, path, MaxIndexBytes)
	if err != nil {
		return nil, err
	}
	var inputs []Input
	if err := canonical.UnmarshalExact(raw, &inputs); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) > MaxInputs {
		return nil, e.New(e.CodeUsage, "report inputs require 1 through %d entries", MaxInputs)
	}
	return inputs, nil
}
func LoadTestGridDestination(ctx context.Context, path string) (TestGridDestination, error) {
	raw, err := readDescription(ctx, path, 16384)
	if err != nil {
		return TestGridDestination{}, err
	}
	var destination TestGridDestination
	if err := canonical.UnmarshalExact(raw, &destination); err != nil {
		return TestGridDestination{}, err
	}
	return destination, destination.Validate()
}

type ExportResult struct {
	Output            string `json:"output"`
	Format            string `json:"format"`
	Observations      int    `json:"observations"`
	TestGridOutput    string `json:"testGridOutput,omitempty"`
	Complete          bool   `json:"complete"`
	Published         bool   `json:"published"`
	ReportCommitted   bool   `json:"reportCommitted"`
	TestGridCommitted bool   `json:"testGridCommitted"`
}

// ExportFiles loads bounded independent configuration, authenticates every source
// and commits local generated reports. A failure after the first export returns
// its partial result explicitly; no service or external destination is written.
func ExportFiles(ctx context.Context, inputsPath, format, out, gridTargetPath, gridOut string, options testobservation.RegistryOptions) (*ExportResult, error) {
	return exportFilesWith(ctx, inputsPath, format, out, gridTargetPath, gridOut, options, stockVerify)
}

func exportFilesWith(ctx context.Context, inputsPath, format, out, gridTargetPath, gridOut string, options testobservation.RegistryOptions, verify verifyFunc) (*ExportResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if (format != "json" && format != "markdown") || out == "" || (gridTargetPath == "") != (gridOut == "") {
		return nil, e.New(e.CodeUsage, "report requires json/markdown format, explicit output and paired TestGrid target/output")
	}
	var destination TestGridDestination
	if gridTargetPath != "" {
		var err error
		destination, err = LoadTestGridDestination(ctx, gridTargetPath)
		if err != nil {
			return nil, err
		}
	}
	inputs, err := LoadInputs(ctx, inputsPath)
	if err != nil {
		return nil, err
	}
	report, err := ingestWith(ctx, inputs, options, verify)
	if err != nil {
		return nil, err
	}
	defer func() { _ = report.Close() }()
	index, err := report.Index(ctx)
	if err != nil {
		return nil, err
	}
	// Admit and materialize every requested projection before output staging.
	var raw []byte
	if format == "json" {
		raw, err = report.JSON(ctx)
	} else {
		var rendered string
		rendered, err = report.Markdown(ctx)
		raw = []byte(rendered)
	}
	if err != nil {
		return nil, err
	}
	var projection TestGridProjection
	if gridTargetPath != "" {
		projection, err = report.TestGrid(ctx, destination)
		if err != nil {
			return nil, err
		}
	}
	if gridOut != "" {
		a, _ := filepath.Abs(out)
		b, _ := filepath.Abs(gridOut)
		if a == b {
			return nil, e.New(e.CodeUsage, "report and TestGrid outputs must be distinct")
		}
	}
	for _, path := range []string{out, gridOut} {
		if path != "" {
			if err := admitOutput(ctx, path); err != nil {
				return nil, err
			}
		}
	}
	result := &ExportResult{Format: format, Observations: len(index.Entries)}
	if err := report.exportLocalFileWithCommit(ctx, out, raw, func() { result.Output = out; result.ReportCommitted = true }); err != nil {
		return result, err
	}
	if gridTargetPath != "" {
		if err := report.exportGridProjection(ctx, projection, gridOut, func() { result.TestGridOutput = gridOut; result.TestGridCommitted = true }); err != nil {
			return result, err
		}
	}
	result.Complete = true
	return result, nil
}
func admitOutput(ctx context.Context, path string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return e.New(e.CodeImmutableViolation, "report output already exists")
	} else if !os.IsNotExist(err) {
		return e.Wrap(e.CodeVerifyFailed, err, "checking report output")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "resolving report output")
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(absolute))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "admitting report output parent")
	}
	if err := parent.Close(); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "closing admitted output parent")
	}
	return nil
}
