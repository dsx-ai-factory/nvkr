// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"

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
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding reporting description")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, max)
	if err != nil {
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
	Output         string `json:"output"`
	Format         string `json:"format"`
	Observations   int    `json:"observations"`
	TestGridOutput string `json:"testGridOutput,omitempty"`
	Complete       bool   `json:"complete"`
	Published      bool   `json:"published"`
}

// ExportFiles loads bounded independent configuration, authenticates every source
// and commits local generated reports. A failure after the first export returns
// its partial result explicitly; no service or external destination is written.
func ExportFiles(ctx context.Context, inputsPath, format, out, gridTargetPath, gridOut string, options testobservation.RegistryOptions) (*ExportResult, error) {
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
	report, err := Ingest(ctx, inputs, options)
	if err != nil {
		return nil, err
	}
	defer func() { _ = report.Close() }()
	index, err := report.Index(ctx)
	if err != nil {
		return nil, err
	}
	result := &ExportResult{Output: out, Format: format, Observations: len(index.Entries), Published: false}
	if format == "json" {
		err = report.ExportIndex(ctx, out)
	} else {
		err = report.ExportMarkdown(ctx, out)
	}
	if err != nil {
		return result, err
	}
	if gridTargetPath != "" {
		result.TestGridOutput = gridOut
		if err := report.ExportTestGrid(ctx, destination, gridOut); err != nil {
			return result, err
		}
	}
	result.Complete = true
	return result, nil
}
