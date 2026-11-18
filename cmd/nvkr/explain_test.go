// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/inputinspect"
)

func TestExplainCommand(t *testing.T) {
	root := t.TempDir()
	raw, err := os.ReadFile("../../demos/hello/environment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "environment.yaml")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want e.Code
	}{
		{"plain", []string{"--root", root, file}, ""},
		{"json", []string{"--root", root, "--json", file}, ""},
		{"missing root", []string{file}, e.CodeUsage},
		{"extra file", []string{"--root", root, file, file}, e.CodeUsage},
		{"json tui", []string{"--root", root, "--json", "--ui=tui", file}, e.CodeUsage},
		{"missing file", []string{"--root", root, filepath.Join(root, "absent.yaml")}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			err := app.RunContext(t.Context(), append([]string{"nvkr", "explain"}, tc.args...))
			if tc.want != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.want {
					t.Fatalf("got %v want %s", err, tc.want)
				}
				if out.Len() != 0 {
					t.Fatalf("partial output: %s", out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "json" {
				var got inputinspect.Report
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Kind != "Environment" {
					t.Fatalf("JSON: %s %v", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "Scanned root:") {
				t.Fatalf("no scope: %s", out.String())
			}
		})
	}
}
