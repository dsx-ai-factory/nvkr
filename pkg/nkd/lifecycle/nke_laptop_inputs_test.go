// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"reflect"
	"testing"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
)

func TestLaptopARM64DefinitionMembership(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "definitions", "nke-laptop-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]authored.DefinitionOutput{}
	parts := map[string]authored.DefinitionOutput{}
	for _, tc := range []struct {
		file  string
		count int
		disk  bool
	}{
		{"definition.json", 21, true},
		{"definition-components.json", 20, false},
		{"definition-os.json", 1, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			d, err := authored.LoadDefinition(t.Context(), filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Outputs) != tc.count {
				t.Fatalf("outputs=%d want %d", len(d.Outputs), tc.count)
			}
			hasDisk := false
			for _, output := range d.Outputs {
				if output.Platform != "linux/arm64" {
					t.Fatalf("foreign platform: %+v", output)
				}
				if output.Format == authored.OutputDiskImage {
					hasDisk = true
				}
				if tc.file == "definition.json" {
					all[output.Name] = output
				} else {
					if _, exists := parts[output.Name]; exists {
						t.Fatalf("duplicate split output %s", output.Name)
					}
					parts[output.Name] = output
				}
			}
			if hasDisk != tc.disk {
				t.Fatalf("disk=%t want %t", hasDisk, tc.disk)
			}
		})
	}
	if !reflect.DeepEqual(all, parts) {
		t.Fatal("component and OS selections do not exactly partition the combined set")
	}
	env, err := authored.LoadEnvironment(t.Context(), filepath.Join(root, "environment-local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if env.Publication != nil || env.Signing != nil || env.GitHubARC != "" || env.UtilityPlatform != "linux/arm64" || env.RuntimeBinary() != "podman" {
		t.Fatalf("local example grants an unintended route: %+v", env)
	}
}
