// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/urfave/cli/v2"
)

func TestQualificationCLIManifestOutcome(t *testing.T) {
	for _, tc := range []struct {
		name                string
		qualificationPassed bool
		code                e.Code
	}{
		{"issued", true, ""},
		{"qualification failed", false, e.CodeGateUnmet},
		{"manifest readback failed", true, e.CodeSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			var expected error
			if tc.code != "" {
				expected = e.New(tc.code, "refused")
			}
			app := &cli.App{Writer: &output, Commands: []*cli.Command{cmdRunQualifyWithPublishers(
				func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error) {
					t.Fatal("manifest request used qualification-only publisher")
					return nil, nil
				},
				func(ctx context.Context, path string, options lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
					calls++
					if ctx == nil || path != "trusted.yaml" || options.WorkspaceRoot != "private-workspace" {
						t.Fatal("manifest invocation changed independent policy or runtime options")
					}
					return &releasemanifest.RunResult{Qualification: &qualification.Publication{Successful: tc.qualificationPassed}, Successful: expected == nil}, expected
				},
			)}}
			err := app.Run([]string{"nvkr", "qualify", "--policy", "trusted.yaml", "--workspace", "private-workspace", "--issue-manifest"})
			if err != expected || calls != 1 {
				t.Fatalf("lost terminal outcome: calls=%d err=%v", calls, err)
			}
			var result releasemanifest.RunResult
			if err := canonical.UnmarshalExact(bytes.TrimSpace(output.Bytes()), &result); err != nil {
				t.Fatal(err)
			}
			if result.Successful != (expected == nil) || result.Qualification == nil || result.Qualification.Successful != tc.qualificationPassed {
				t.Fatal("manifest diagnostics lost qualification outcome")
			}
		})
	}
}

func TestQualificationCLIManifestWriterFailurePreservesGateError(t *testing.T) {
	gate := e.New(e.CodeSignatureInvalid, "manifest readback refused")
	app := &cli.App{Writer: qualificationFailWriter{}, Commands: []*cli.Command{cmdRunQualifyWithPublishers(nil,
		func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			return &releasemanifest.RunResult{Qualification: &qualification.Publication{Successful: true}}, gate
		},
	)}}
	err := app.Run([]string{"nvkr", "qualify", "--policy", "trusted.yaml", "--issue-manifest"})
	if !errors.Is(err, gate) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure hid original gate error: %v", err)
	}
}

type qualificationFailWriter struct{}

func (qualificationFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
