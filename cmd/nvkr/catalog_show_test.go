// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

func TestCatalogShowReportsTheLoadedSourcePin(t *testing.T) {
	data, err := filepath.Abs(filepath.Join("..", "..", "inputs", "catalogs", "nvke", "module-tags"))
	if err != nil {
		t.Fatal(err)
	}
	before := catalogFileBytes(t, data)
	output, err := runCatalogCommand(t, "catalog", "show", "--data", data, "--format", "json", "nvkectl")
	if err != nil {
		t.Fatal(err)
	}
	var description catalog.ComponentDescription
	if err := json.Unmarshal([]byte(output), &description); err != nil {
		t.Fatalf("output is not one JSON description: %v\n%s", err, output)
	}
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(data))
	if err != nil {
		t.Fatal(err)
	}
	want := loaded.Components["nvkectl"]
	if description.Mode != catalog.ProducerModeSource || description.OutputType != "host-package" ||
		description.Source == nil || description.Source.Commit != want.Commit || description.Source.TreeHash != want.TreeHash ||
		!slices.Equal(description.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Fatalf("wrong description: %+v", description)
	}
	text, err := runCatalogCommand(t, "catalog", "show", "--data", data, "nvkectl")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"mode: source", "output type: host-package", "source commit: " + want.Commit, "platforms: linux/amd64, linux/arm64"} {
		if !strings.Contains(text, line+"\n") {
			t.Fatalf("text lacks %q:\n%s", line, text)
		}
	}
	if after := catalogFileBytes(t, data); !maps.Equal(before, after) {
		t.Fatal("catalog show changed catalog files")
	}
}

func TestCatalogShowRejectsUsageBeforeCatalogIO(t *testing.T) {
	for _, args := range [][]string{
		{"--data", "must-not-be-read"},
		{"--data", "must-not-be-read", "a", "b"},
		{"--data", "must-not-be-read", "--format", "yaml", "a"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			output, err := runCatalogCommand(t, append([]string{"catalog", "show"}, args...)...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if output != "" {
				t.Fatalf("usage failure emitted output: %s", output)
			}
		})
	}
	output, err := runCatalogCommand(t, "catalog", "show", "--data", layeredCatalogPath(t), "missing")
	assertLifecycleCommandCode(t, err, e.CodeInvalidCatalog)
	if output != "" {
		t.Fatalf("unknown component emitted output: %s", output)
	}
}

func catalogFileBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		files[path] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCatalogShowResultViewShowsEveryPin(t *testing.T) {
	description := catalog.ComponentDescription{Catalog: "fixture", Name: "bundle", Mode: catalog.ProducerModePinnedTool, Platforms: []string{"linux/arm64"},
		Tool: &catalog.DescribedToolPin{Source: "aicr", Tag: "v1.2.0", DescribedExecutable: catalog.DescribedExecutable{
			Format: "tar-gzip", URL: "https://example.test/aicr.tar.gz", Platform: "linux/arm64", ExecutablePath: "aicr",
			ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64),
		}}}
	display := catalogComponentDescriptionDisplay(description)
	values := map[string]string{}
	for _, field := range display.Fields {
		values[field.Label] = field.Value
	}
	for label, want := range map[string]string{
		"Tool format": "tar-gzip", "Tool platform": "linux/arm64", "Tool executable path": "aicr",
		"Tool archive digest": description.Tool.ArchiveDigest, "Tool executable digest": description.Tool.ExecutableDigest,
	} {
		if values[label] != want {
			t.Fatalf("result view %q = %q; want %q", label, values[label], want)
		}
	}
	if len(display.Fields) != len(description.Fields())+1 {
		t.Fatalf("result view shows %d fields; the description has %d plus the closure count", len(display.Fields), len(description.Fields()))
	}
}

func TestCatalogShowUsageErrorsUseTheResultView(t *testing.T) {
	for _, args := range [][]string{
		{"--data", "must-not-be-read"},
		{"--data", "must-not-be-read", "--format", "yaml", "component"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			shown := false
			app := catalogPresentationApp(true, func(snapshot progress.Snapshot) runpresenter.Result {
				shown = true
				if snapshot.FailureCode != string(e.CodeUsage) {
					t.Errorf("usage failure not shown: %+v", snapshot)
				}
				return runpresenter.Result{}
			})
			output, err := runCLIWithCapturedStdout(t, app, append([]string{"nvkr", "catalog", "show", "--ui=tui"}, args...)...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if !shown || len(output) != 0 {
				t.Fatal("usage failure bypassed the result view or printed output")
			}
		})
	}
}
