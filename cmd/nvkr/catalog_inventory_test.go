// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCatalogRootListAndFind(t *testing.T) {
	root := t.TempDir()
	for _, leaf := range []string{"one", "two"} {
		dir := filepath.Join(root, leaf)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{catalog.RegistryFile, catalog.LockFile} {
			raw, err := os.ReadFile(filepath.Join(layeredCatalogPath(t), file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
		want    []string
	}{
		{"list", nil, []string{`catalog: "one"`, `catalog: "two"`, `"kubectl"`}},
		{"find", []string{"kubectl"}, []string{`catalog: "one"`, `catalog: "two"`, "70d3cc986aa8221cd1dfb1121852688902d3bf53"}},
		{"list", []string{"--output-type", "host-package"}, []string{`"kubectl"`, `"kubelet"`}},
	} {
		t.Run(tc.command+strings.Join(tc.args, "-"), func(t *testing.T) {
			output, err := runCatalogCommand(t, append([]string{"catalog", tc.command, "--data", root}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %q in %s", want, output)
				}
			}
		})
	}
	output, err := runCatalogCommand(t, "catalog", "find", "--data", root, "--format", "json", "kubectl")
	if err != nil {
		t.Fatal(err)
	}
	var entries []catalog.InventoryCatalog
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "one" || entries[1].Path != "two" {
		t.Fatalf("locations lost: %s", output)
	}
	for _, args := range [][]string{
		{"catalog", "find", "--data", root, "missing"},
		{"catalog", "list", "--data", root, "--output-type", "disk-image"},
	} {
		output, err := runCatalogCommand(t, args...)
		assertLifecycleCommandCode(t, err, e.CodeNoMatch)
		if output != "" {
			t.Fatalf("failure emitted output: %s", output)
		}
	}
}

func TestCatalogFindRejectsUsageBeforeIO(t *testing.T) {
	for _, args := range [][]string{{}, {"one", "two"}, {"--format", "invalid", "one"}} {
		output, err := runCatalogCommand(t, append([]string{"catalog", "find", "--data", "does-not-exist"}, args...)...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if output != "" {
			t.Fatalf("usage emitted output: %s", output)
		}
	}
}

func TestCatalogInventoryRejectsInvalidRoot(t *testing.T) {
	for _, root := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		for _, command := range []string{"list", "find", "index"} {
			args := []string{"catalog", command, "--data", root}
			if command == "find" {
				args = append(args, "kubectl")
			}
			output, err := runCatalogCommand(t, args...)
			assertLifecycleCommandCode(t, err, e.CodeInvalidCatalog)
			if output != "" {
				t.Fatalf("invalid root emitted output: %s", output)
			}
		}
	}
}
