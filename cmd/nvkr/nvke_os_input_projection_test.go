// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func TestOSFrozenInputProjectionPreservesSelection(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("OS wrapper requires jq")
	}
	script, err := os.ReadFile(demoScript(t, "nvke-os-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "  jq --arg catalog \"${definition_catalog}\" --argjson overlays \"${definition_overlays}\" '\n"
	_, remainder, found := strings.Cut(string(script), start)
	if !found {
		t.Fatal("OS freeze projection invocation is missing")
	}
	projection, _, found := strings.Cut(remainder, "' \\\n  >\"${frozen_definition}\"")
	if !found {
		t.Fatal("OS freeze projection output is missing")
	}
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	for _, selection := range []struct {
		name       string
		definition string
		catalog    string
		overlays   []string
	}{
		{
			name:       "26.04/amd64",
			definition: "os-amd64.yaml",
			catalog:    "../../catalogs/node-images/diagnostic",
			overlays:   []string{},
		},
		{
			name:       "26.04/arm64",
			definition: "os-arm64.yaml",
			catalog:    "../../catalogs/node-images/diagnostic",
			overlays:   []string{"../../catalogs/node-images/variants/diagnostic-arm64-kvm.yaml"},
		},
		{
			name:       "24.04/amd64",
			definition: "ubuntu24/definition-amd64.yaml",
			catalog:    "../../../catalogs/node-images/release",
			overlays: []string{
				"../../../catalogs/node-images/variants/native-toolchains.yaml",
				"../../../catalogs/node-images/variants/ubuntu24.json",
			},
		},
		{
			name:       "24.04/arm64",
			definition: "ubuntu24/definition-arm64.yaml",
			catalog:    "../../../catalogs/node-images/release",
			overlays: []string{
				"../../../catalogs/node-images/variants/native-toolchains.yaml",
				"../../../catalogs/node-images/variants/ubuntu24.json",
			},
		},
	} {
		t.Run(selection.name, func(t *testing.T) {
			original, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "inputs/definitions/diagnostics", selection.definition))
			if err != nil {
				t.Fatal(err)
			}
			if original.Catalog.Base != selection.catalog || !slices.Equal(original.Catalog.Overlays, selection.overlays) {
				t.Fatal("maintained Definition does not select the expected ordered catalog stack")
			}
			overlays, err := json.Marshal(selection.overlays)
			if err != nil {
				t.Fatal(err)
			}
			changes := []string{"unchanged", "foreign catalog", "output override", "foreign overlay"}
			if len(selection.overlays) > 0 {
				changes = append(changes, "missing overlays")
			}
			if len(selection.overlays) > 1 {
				changes = append(changes, "missing first overlay", "missing last overlay", "reordered overlays")
			}
			for _, change := range changes {
				t.Run(change, func(t *testing.T) {
					selected := original.Clone()
					switch change {
					case "foreign catalog":
						selected.Catalog.Base = "foreign/catalog"
					case "output override":
						selected.Outputs[0].Catalog = &definition.Catalog{Base: selected.Catalog.Base}
					case "foreign overlay":
						selected.Catalog.Overlays = []string{"../../catalogs/node-images/variants/nvidia-tcg.json"}
					case "missing overlays":
						selected.Catalog.Overlays = nil
					case "missing first overlay":
						selected.Catalog.Overlays = selected.Catalog.Overlays[1:]
					case "missing last overlay":
						selected.Catalog.Overlays = selected.Catalog.Overlays[:len(selected.Catalog.Overlays)-1]
					case "reordered overlays":
						slices.Reverse(selected.Catalog.Overlays)
					}
					raw, err := json.Marshal(selected)
					if err != nil {
						t.Fatal(err)
					}
					command := exec.CommandContext(t.Context(), "jq", "--arg", "catalog", selection.catalog,
						"--argjson", "overlays", string(overlays), projection)
					command.Stdin = bytes.NewReader(raw)
					output, err := command.Output()
					if change != "unchanged" {
						if err == nil || len(output) != 0 {
							t.Fatal("unsupported catalog selection produced frozen inputs")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					frozen, err := definition.DecodeDefinition(output)
					if err != nil {
						t.Fatal(err)
					}
					if frozen.Catalog.Base != "catalog" || len(frozen.Catalog.Overlays) != len(original.Catalog.Overlays) {
						t.Fatal("frozen Definition lost its catalog stack")
					}
					for i, overlay := range original.Catalog.Overlays {
						if frozen.Catalog.Overlays[i] != filepath.Base(overlay) {
							t.Fatal("frozen Definition did not select its copied overlay")
						}
					}
					frozen.Catalog = original.Catalog
					if !reflect.DeepEqual(frozen, original) {
						t.Fatal("freezing catalog locations changed the producer selection")
					}
				})
			}
		})
	}
}
