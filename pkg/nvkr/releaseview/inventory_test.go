// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestArtifactInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Description)
		code   e.Code
	}{
		{"chart index native", func(*Description) {}, ""},
		{"repository", func(d *Description) {
			d.Outputs[1].Reference = strings.Replace(d.Outputs[1].Reference, "product/", "image..name/", 1)
		}, e.CodeEngineCapability},
		{"host", func(d *Description) {
			d.Outputs[1].Reference = strings.Replace(d.Outputs[1].Reference, "registry.example", "registry_name.example", 1)
		}, e.CodeEngineCapability},
		{"missing archive", func(d *Description) { d.Outputs[1].Helm = nil }, e.CodeEngineCapability},
		{"empty", func(d *Description) { d.Outputs = nil }, e.CodeReleaseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			description, err := Describe(descriptionFixture(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&description)
			raw, err := ArtifactInventory(description)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code || len(raw) != 0 {
					t.Fatalf("output=%s error=%v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				APIVersion, Kind string
				Artifacts        []inventoryIdentity
			}
			if err = json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.APIVersion != "aicr.run/v1beta1" || got.Kind != "ArtifactInventory" || len(got.Artifacts) != 3 {
				t.Fatalf("inventory: %s", raw)
			}
			chart, native, index := got.Artifacts[0], got.Artifacts[1], got.Artifacts[2]
			if chart.Name != "chart" || chart.Type != "chart" || chart.Version != "8.2.1" || chart.ArchiveDigest != description.Outputs[1].Helm.DefaultDigest || chart.Platform != nil {
				t.Fatal(chart)
			}
			if native.Name != "image" || native.Type != "image" || native.Platform == nil || *native.Platform != (inventoryPlatform{"linux", "arm64"}) || native.Version != strings.ReplaceAll(native.ManifestDigest, ":", "-") {
				t.Fatal(native)
			}
			if index.Name != "multi" || index.Type != "image" || index.Platform != nil {
				t.Fatal(index)
			}
			for _, item := range got.Artifacts {
				if strings.Contains(item.Reference, "@") || !strings.HasPrefix(item.Reference, "oci://") {
					t.Fatal(item)
				}
			}
		})
	}
}
