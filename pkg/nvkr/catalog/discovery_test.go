// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"reflect"
	"strings"
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
	want := map[string]string{"alpha": ProducerSource, "excluded": ProducerExcluded, "image": ProducerSource, "tool": ProducerPinnedTool, "zeta": ProducerAcquired}
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

func TestDiscoverAdmitsEveryDeclarablePlatformAndOutputType(t *testing.T) {
	cat := &Catalog{Components: map[string]Component{
		"mac":     {Platforms: []string{"darwin/arm64"}, OutputType: "tarball", Prebuilt: &Prebuilt{}},
		"layout":  {Platforms: []string{"linux/arm64"}, OutputType: PinnedToolOutputOCILayout, BuilderType: BuilderPinnedTool},
		"disk":    {Platforms: []string{"linux/amd64"}, OutputType: "disk-image", FromSource: true},
		"rawdisk": {Platforms: []string{"linux/amd64"}, OutputType: "raw", FromSource: true},
	}}
	for _, tc := range []struct{ platform, outputType, want string }{
		{"darwin/arm64", "", "mac"},
		{"", "tarball", "mac"},
		{"", PinnedToolOutputOCILayout, "layout"},
		{"", "disk-image", "disk"},
		{"", "raw", "rawdisk"},
	} {
		found, err := cat.Discover(tc.platform, tc.outputType)
		if err != nil || len(found) != 1 || found[0].Name != tc.want {
			t.Fatalf("Discover(%q, %q) = %v, %v; want only %s", tc.platform, tc.outputType, found, err, tc.want)
		}
	}
	if found, err := (&Catalog{}).Discover("darwin/arm64", ""); err != nil || len(found) != 0 {
		t.Fatalf("a declarable platform no component declares must be an empty match, got %v, %v", found, err)
	}
}

func TestSelectorDiagnosticsNameEveryAdmittedValue(t *testing.T) {
	err := ValidateOutputType("package")
	if err == nil {
		t.Fatal("unknown output type admitted")
	}
	for name := range knownOutputSet {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("diagnostic omits admitted type %q: %v", name, err)
		}
	}
	for _, name := range []string{PinnedToolOutputFile, PinnedToolOutputOCILayout} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("diagnostic omits admitted type %q: %v", name, err)
		}
	}
	if err := ValidatePlatform("windows/amd64"); err == nil || !strings.Contains(err.Error(), "darwin/arm64") || strings.Contains(err.Error(), "--platform") {
		t.Fatalf("platform diagnostic should list the admitted set without flag syntax: %v", err)
	}
}
