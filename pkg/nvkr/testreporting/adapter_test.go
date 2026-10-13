// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func TestReportingLoadsOnlyBoundedExactCanonicalDescriptions(t *testing.T) {
	input, _ := fixture(t, "adapter", "pass", "workflow/main", FirstParty)
	raw, err := canonical.Marshal([]Input{input})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "inputs.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(t.Context(), path)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("loaded inputs %+v, %v", inputs, err)
	}
	for _, kind := range []string{"whitespace", "unknown field", "symlink", "oversize", "empty"} {
		t.Run(kind, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "inputs.json")
			data := raw
			switch kind {
			case "whitespace":
				data = append(append([]byte(nil), raw...), '\n')
			case "unknown field":
				data = []byte(`[{"unknown":true}]`)
			case "empty":
				data = []byte(`[]`)
			case "symlink":
				if err := os.Symlink(path, candidate); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				data = []byte(`[]`)
			}
			if kind != "symlink" {
				if err := os.WriteFile(candidate, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "oversize" {
				if err := os.Truncate(candidate, MaxIndexBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			got, err := LoadInputs(t.Context(), candidate)
			if got != nil {
				t.Fatal("invalid description became receipt inputs")
			}
			if kind == "empty" {
				code(t, err, e.CodeUsage)
			} else {
				code(t, err, e.CodeVerifyFailed)
			}
		})
	}
}
func TestReportingExportAdmissionAndCancellationBeforeInputs(t *testing.T) {
	for _, tc := range []struct {
		format, target, gridOut string
		ctx                     context.Context
		want                    e.Code
	}{
		{"invalid", "", "", t.Context(), e.CodeUsage},
		{"json", "missing-target", "", t.Context(), e.CodeUsage},
		{"json", "", "unexpected-grid-out", t.Context(), e.CodeUsage},
		{"json", "", "", nil, e.CodeUsage},
	} {
		out := filepath.Join(t.TempDir(), "report.json")
		got, err := ExportFiles(tc.ctx, "/nonexistent-input", tc.format, out, tc.target, tc.gridOut, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}})
		if got != nil {
			t.Fatal("admission returned partial success")
		}
		code(t, err, tc.want)
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("admission touched output")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := filepath.Join(t.TempDir(), "report.json")
	got, err := ExportFiles(ctx, "/nonexistent-input", "json", out, "", "", testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}})
	if got != nil {
		t.Fatal("canceled export returned result")
	}
	code(t, err, e.CodeTimeout)
}
func TestReportingImmutableMarkdownExport(t *testing.T) {
	input, verified := fixture(t, "markdown", "pass", "workflow/main", FirstParty)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	out := filepath.Join(t.TempDir(), "report.md")
	if err := report.ExportMarkdown(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := report.Markdown(t.Context())
	if err != nil || string(raw) != expected {
		t.Fatal("Markdown export changed verified projection")
	}
	code(t, report.ExportMarkdown(t.Context(), out), e.CodeImmutableViolation)
}
