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
	const start = "    --arg overlay \"../../catalogs/node-images/variants/diagnostic-${arch}-kvm.yaml\" '\n"
	_, remainder, found := strings.Cut(string(script), start)
	if !found {
		t.Fatal("OS freeze projection invocation is missing")
	}
	projection, _, found := strings.Cut(remainder, "' \\\n  >\"${frozen_definition}\"")
	if !found {
		t.Fatal("OS freeze projection output is missing")
	}
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	for _, arch := range []string{"amd64", "arm64"} {
		for _, change := range []string{"unchanged", "foreign catalog", "output override", "foreign overlay"} {
			t.Run(arch+"/"+change, func(t *testing.T) {
				original, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "inputs/definitions/diagnostics/os-"+arch+".yaml"))
				if err != nil {
					t.Fatal(err)
				}
				selected := original.Clone()
				switch change {
				case "foreign catalog":
					selected.Catalog.Base = "../../catalogs/node-images/release"
				case "output override":
					selected.Outputs[0].Catalog = &definition.Catalog{Base: selected.Catalog.Base}
				case "foreign overlay":
					selected.Catalog.Overlays = []string{"../../catalogs/node-images/variants/nvidia-tcg.json"}
				}
				raw, err := json.Marshal(selected)
				if err != nil {
					t.Fatal(err)
				}
				command := exec.CommandContext(t.Context(), "jq", "--arg", "catalog", original.Catalog.Base,
					"--arg", "overlay", "../../catalogs/node-images/variants/diagnostic-"+arch+"-kvm.yaml", projection)
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
	}
}
