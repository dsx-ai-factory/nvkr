// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func runtimeContentsFixture(t *testing.T, change func(map[string]string, *material.MkosiManifest)) (*ProducedRuntime, *mkosi.Spec, string) {
	t.Helper()
	spec, _ := mkosiAcquisitionFixture()
	spec.Files = []mkosi.File{{Material: "packer", Path: "/nvkr/tools/packer", Mode: 0o444}}
	recipe, err := canonical.Marshal(spec.Logical())
	if err != nil {
		t.Fatal(err)
	}
	manifest := material.MkosiManifest{ManifestVersion: 1,
		Config:   material.MkosiConfig{Name: "runtime", Distribution: "ubuntu", Release: "noble", Architecture: "x86-64", OutputFormat: "directory"},
		Packages: []material.InstalledPackage{{Type: "deb", Name: "base-files", Version: "1.0", Architecture: "amd64"}}}
	files := map[string]string{"nvkr/mkosi/recipe.json": string(recipe), "nvkr/mkosi/mkosi.conf": spec.Configuration,
		"nvkr/tools/packer": "original projected tool", `usr/lib/systemd/system/system-systemd\x2dcryptsetup.slice`: "literal backslash"}
	if change != nil {
		change(files, &manifest)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files["nvkr/mkosi/runtime.manifest.json"] = string(raw)
	node, result, options, _ := runtimeReceiptFixtureFiles(t, files)
	node.Mkosi = spec
	tool, err := captureExecutedRuntime(t.Context(), "linux/amd64", "sha256:"+strings.Repeat("4", 64), node, result, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tool.Close(); err != nil {
			t.Error(err)
		}
	})
	return tool, spec, FileDigest(raw)
}

func TestRuntimeContentsBindsOriginalReportAndFileBytes(t *testing.T) {
	tool, spec, report := runtimeContentsFixture(t, nil)
	// The caller cannot edit the retained recipe after production capture.
	spec.Roots[0].Inputs.Packages[0].Version = "2.0"
	contents, err := tool.InspectContents(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(contents.Roots) != 1 || contents.Roots[0].ReportDigest != report || len(contents.Roots[0].Packages) != 1 || contents.Roots[0].Packages[0].Version != "1.0" ||
		len(contents.Files) != 1 || contents.Files[0].Digest != FileDigest([]byte("original projected tool")) || contents.Files[0].Material != "packer" || contents.RecipeDigest == "" {
		t.Fatalf("inventory lost installed-content binding: %+v", contents)
	}
	binding, err := tool.BindingContext(t.Context())
	if err != nil || contents.Binding != binding {
		t.Fatalf("inventory lost exact production binding: %v", err)
	}
	contents.Roots[0].Packages[0].Version = "3.0"
	again, err := tool.InspectContents(t.Context(), t.TempDir())
	if err != nil || again.Roots[0].Packages[0].Version != "1.0" {
		t.Fatalf("returned description changed producer state: %v", err)
	}
}

func TestRuntimeContentsRejectsEmbeddedSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]string, *material.MkosiManifest)
	}{
		{"recipe", func(files map[string]string, _ *material.MkosiManifest) { files["nvkr/mkosi/recipe.json"] = "{}" }},
		{"configuration", func(files map[string]string, _ *material.MkosiManifest) { files["nvkr/mkosi/mkosi.conf"] += "\n" }},
		{"missing file material", func(files map[string]string, _ *material.MkosiManifest) { delete(files, "nvkr/tools/packer") }},
		{"missing package", func(_ map[string]string, report *material.MkosiManifest) { report.Packages = nil }},
		{"extra package", func(_ map[string]string, report *material.MkosiManifest) {
			report.Packages = append(report.Packages, report.Packages[0])
		}},
		{"version", func(_ map[string]string, report *material.MkosiManifest) { report.Packages[0].Version = "2.0" }},
		{"architecture", func(_ map[string]string, report *material.MkosiManifest) { report.Packages[0].Architecture = "arm64" }},
		{"release", func(_ map[string]string, report *material.MkosiManifest) { report.Config.Release = "resolute" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, _, _ := runtimeContentsFixture(t, tc.change)
			scratch := t.TempDir()
			contents, err := tool.InspectContents(t.Context(), scratch)
			if contents != nil || code(err) != e.CodeVerifyFailed {
				t.Fatalf("substitution returned content: %+v, %v", contents, err)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed inventory retained extraction: %v", err)
			}
		})
	}
}

func TestRuntimeContentsRejectsMissingAuthorityBeforeStaging(t *testing.T) {
	tool, _, _ := runtimeContentsFixture(t, nil)
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*ProducedRuntime{nil, {}, tool} {
		contents, err := candidate.InspectContents(t.Context(), "/must-not-create")
		if contents != nil || code(err) != e.CodeGateUnmet {
			t.Fatalf("unowned inventory reached staging: %+v, %v", contents, err)
		}
	}
	live, _, _ := runtimeContentsFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	contents, err := live.InspectContents(ctx, "/must-not-create")
	if contents != nil || code(err) != e.CodeTimeout {
		t.Fatalf("canceled inventory reached staging: %+v, %v", contents, err)
	}
}
