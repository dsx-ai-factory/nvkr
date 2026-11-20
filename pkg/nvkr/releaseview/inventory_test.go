// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
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

func TestArtifactInventorySelection(t *testing.T) {
	description, err := Describe(descriptionFixture(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	full, err := ArtifactInventory(description)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/artifact-inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full, golden) {
		t.Fatal("unfiltered inventory changed")
	}
	var original struct {
		Artifacts []json.RawMessage `json:"artifacts"`
	}
	if err := json.Unmarshal(full, &original); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		selected, want []string
		code           e.Code
	}{
		{"single", []string{"image"}, []string{"image"}, ""},
		{"multiple sorted", []string{"multi", "chart"}, []string{"chart", "multi"}, ""},
		{"duplicate", []string{"image", "image"}, []string{"image"}, ""},
		{"unknown", []string{"image", "missing"}, nil, e.CodeUsage},
		{"empty name", []string{""}, nil, e.CodeUsage},
		{"unsupported", []string{"tools"}, nil, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := ArtifactInventory(description, tc.selected...)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code || len(raw) != 0 || !strings.Contains(err.Error(), tc.selected[len(tc.selected)-1]) {
					t.Fatalf("output=%s err=%v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Artifacts []json.RawMessage `json:"artifacts"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range got.Artifacts {
				var item inventoryIdentity
				if err := json.Unmarshal(entry, &item); err != nil {
					t.Fatal(err)
				}
				names = append(names, item.Name)
				if !slices.ContainsFunc(original.Artifacts, func(candidate json.RawMessage) bool { return bytes.Equal(entry, candidate) }) {
					t.Fatalf("entry changed: %s", entry)
				}
			}
			if !slices.Equal(names, tc.want) {
				t.Fatalf("names=%v want=%v", names, tc.want)
			}
		})
	}
}
