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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
