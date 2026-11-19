// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMaintainedPackerSourcesKeepAgentVendoringSeparate(t *testing.T) {
	root := "../../../inputs/catalogs/node-images"
	base, err := LoadContext(t.Context(), Dir(filepath.Join(root, "release")))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockContext(t.Context(), filepath.Join(root, "release"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lock.Pins["nvke-packer-source"], lock.Pins["nvke-os"]) {
		t.Fatal("Packer source split changed the pinned NVKE revision")
	}
	if len(base.Sources["nvke-packer-source"].VendorCommand) != 0 ||
		!strings.Contains(strings.Join(base.Sources["nvke-os"].VendorCommand, "\n"), "go mod vendor") {
		t.Fatal("Packer and agent source vendoring are not independent")
	}
	for _, tc := range []struct {
		series   string
		overlays []string
	}{
		{"26.04", []string{"native-toolchains.yaml"}},
		{"24.04", []string{"native-toolchains.yaml", "ubuntu24.json"}},
	} {
		t.Run(tc.series, func(t *testing.T) {
			var overlays []Overlay
			for _, name := range tc.overlays {
				overlay, err := LoadOverlayContext(t.Context(), filepath.Join(root, "variants", name))
				if err != nil {
					t.Fatal(err)
				}
				overlays = append(overlays, overlay)
			}
			stack, err := ApplyStack(base, overlays...)
			if err != nil {
				t.Fatal(err)
			}
			agent := stack.Catalog.Components["nvke-agent"]
			if !strings.Contains(strings.Join(agent.VendorCommand, "\n"), "go mod vendor") {
				t.Fatal("agent producer lost its vendored dependency closure")
			}
			for _, name := range []string{"nvke-node-fde-amd64", "nvke-node-image-amd64"} {
				packer := stack.Catalog.Components[name]
				if len(packer.VendorCommand) != 0 || packer.Commit != agent.Commit || packer.TreeHash != agent.TreeHash {
					t.Fatalf("%s must use the same pinned source without agent vendoring", name)
				}
				if packer.Packer == nil || packer.Packer.Variables["ubuntu_series"] != tc.series ||
					packer.Materials["image-inputs"] != "nvke-image-inputs" ||
					stack.Catalog.Components["nvke-image-inputs"].Materials["agent"] != "nvke-agent" {
					t.Fatalf("%s lost its separately built agent package material", name)
				}
			}
			for _, name := range []string{"nvke-node-fde-arm64", "nvke-node-image"} {
				if len(stack.Catalog.Components[name].VendorCommand) == 0 {
					t.Fatalf("%s changed the deferred arm64 source closure", name)
				}
			}
		})
	}
}
