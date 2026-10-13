// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReleaseExportCLIAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"consumer format", []string{"--format", "nvke-release"}, e.CodeUsage},
		{"timeout", []string{"--timeout", "0s"}, e.CodeUsage},
		{"negative timeout", []string{"--timeout", "-1s"}, e.CodeUsage},
		{"platform selection", []string{"--platform", "linux/amd64"}, e.CodeUsage},
		{"plan", []string{"--plan", "/must-not-open"}, e.CodeUsage},
		{"mapping", []string{"--mapping", "/must-not-open"}, e.CodeUsage},
		{"unpublished workspace", []string{"--workspace", "/must-not-open"}, e.CodeUsage},
		{"output selection", []string{"--output", "controller"}, e.CodeUsage},
		{"local registry options", []string{"--anonymous"}, e.CodeUsage},
		{"positional argument", []string{"unexpected"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			args := []string{"nvkr", "release", "export", "--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64), "--layout", "/must-not-open"}
			err := app.RunContext(t.Context(), append(args, tc.args...))
			if cliErrorCode(err) != tc.code || out.Len() != 0 {
				t.Fatalf("invalid export options reached inspection or returned a description: %v, %q", err, out.String())
			}
		})
	}
}

func TestReleaseExportCLIReferenceOnlyAdmission(t *testing.T) {
	for _, reference := range []string{"", "registry.example.test/releases:latest", "registry.example.test/releases@sha256:bad"} {
		t.Run(reference, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", reference})
			if cliErrorCode(err) != e.CodeUnverifiedRef || out.Len() != 0 {
				t.Fatalf("reference-only export should reach immutable-reference admission without other required flags: %v, %q", err, out.String())
			}
		})
	}
}

func TestReleaseExportCLIMatchesCompleteBrowseJSON(t *testing.T) {
	fixture := releaseBrowsePayloadFixture(t, "arm64")
	inspection, err := delivery.Inspect(t.Context(), fixture.layout)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product/releases@" + inspection.RootDigest
	var exported []byte
	for _, command := range []string{"export", "browse"} {
		var out bytes.Buffer
		app := newApp()
		app.Writer = &out
		args := []string{"nvkr", "release", command, "--reference", reference, "--layout", fixture.layout}
		if command == "browse" {
			args = append(args, "--format", "json")
		}
		if err := app.RunContext(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		var document struct {
			SchemaVersion string `json:"schemaVersion"`
			TotalOutputs  int    `json:"totalOutputs"`
			Outputs       []struct {
				Name string `json:"name"`
			} `json:"outputs"`
		}
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if document.SchemaVersion != "nvkr.dev/release-description/v1" || document.TotalOutputs != 2 || len(document.Outputs) != 2 ||
			document.Outputs[0].Name != "node-image" || document.Outputs[1].Name != "tools" {
			t.Fatalf("%s omitted non-NVKE outputs or versioned schema: %s", command, out.String())
		}
		if command == "export" {
			exported = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(exported, out.Bytes()) {
			t.Fatal("export and unfiltered JSON browsing disagree on the release description")
		}
	}
}
