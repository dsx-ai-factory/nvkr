// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiscoverFiltersByExactPlatformAndOutputType(t *testing.T) {
	both := []string{"linux/amd64", "linux/arm64"}
	cat := &Catalog{Components: map[string]Component{
		"zeta":     {Platforms: []string{"linux/arm64"}, OutputType: "host-package", Prebuilt: &Prebuilt{}},
		"alpha":    {Platforms: both, OutputType: "host-package", FromSource: true},
		"image":    {Platforms: both, OutputType: "oci-image", FromSource: true},
		"tool":     {Platforms: []string{"linux/amd64"}, OutputType: PinnedToolOutputFile, BuilderType: BuilderPinnedTool},
		"excluded": {Platforms: both, OutputType: "host-package"},
	}}
	for _, tc := range []struct {
		name, platform, outputType string
		want                       []string
	}{
		{"no filter lists everything sorted", "", "", []string{"alpha", "excluded", "image", "tool", "zeta"}},
		{"platform only", "linux/amd64", "", []string{"alpha", "excluded", "image", "tool"}},
		{"intersection", "linux/arm64", "host-package", []string{"alpha", "excluded", "zeta"}},
		{"pinned tool by exact type", "linux/amd64", PinnedToolOutputFile, []string{"tool"}},
		{"no match is empty", "linux/arm64", PinnedToolOutputFile, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := cat.Discover(tc.platform, tc.outputType)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range found {
				got = append(got, d.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	found, err := cat.Discover("", "")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, d := range found {
		modes[d.Name] = d.ProducerMode
	}
	want := map[string]string{"alpha": ProducerSource, "excluded": ProducerExcluded, "image": ProducerSource, "tool": ProducerPinnedTool, "zeta": ProducerPrebuilt}
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("producer modes %v, want %v", modes, want)
	}
}

func TestDiscoverRejectsUnknownSelectors(t *testing.T) {
	cat := &Catalog{}
	for _, tc := range []struct{ platform, outputType string }{
		{"arm64", ""}, {"linux/*", ""}, {"windows/amd64", ""}, {"", "package"}, {"", "Host-Package"},
	} {
		_, err := cat.Discover(tc.platform, tc.outputType)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
			t.Fatalf("Discover(%q, %q) error = %v, want CodeUsage", tc.platform, tc.outputType, err)
		}
	}
}
