// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func platformToolFixture() (ToolSource, Pin) {
	source := ToolSource{Format: "raw", URL: "https://example.test/{{.Tag}}/{{.OS}}-{{.Arch}}", Platforms: []string{"linux/amd64", "linux/arm64"}, ExecutablePath: "bin/{{.Arch}}/tool"}
	pin := Pin{Tag: "v1.2.3"}
	for _, platform := range source.Platforms {
		selected := platformSource(source, platform)
		url, _ := renderRef("tool URL", selected.URL, pin)
		pin.Tools = append(pin.Tools, ToolPin{Format: selected.Format, URL: url, Platform: platform, ExecutablePath: selected.ExecutablePath, ArchiveDigest: goodArtifactDigest, ExecutableDigest: goodArtifactDigest})
	}
	return source, pin
}

func TestPlatformToolPinContracts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ToolSource, *Pin)
		code e.Code
	}{
		{"complete", func(*ToolSource, *Pin) {}, ""},
		{"both declarations", func(s *ToolSource, _ *Pin) { s.Platform = "linux/amd64" }, e.CodeInvalidCatalog},
		{"empty declarations", func(s *ToolSource, _ *Pin) { s.Platforms = []string{} }, e.CodeInvalidCatalog},
		{"unsorted declarations", func(s *ToolSource, _ *Pin) { s.Platforms[0], s.Platforms[1] = s.Platforms[1], s.Platforms[0] }, e.CodeInvalidCatalog},
		{"unknown platform", func(s *ToolSource, _ *Pin) { s.Platforms[1] = "linux/any" }, e.CodeInvalidCatalog},
		{"same asset", func(s *ToolSource, _ *Pin) { s.URL = "https://example.test/tool"; s.ExecutablePath = "tool" }, e.CodeInvalidCatalog},
		{"unknown member token", func(s *ToolSource, _ *Pin) { s.ExecutablePath = "{{.Unknown}}" }, e.CodeInvalidCatalog},
		{"missing entry", func(_ *ToolSource, p *Pin) { p.Tools = p.Tools[:1] }, e.CodeUnpinnedSource},
		{"extra entry", func(_ *ToolSource, p *Pin) {
			extra := p.Tools[0]
			extra.Platform = "darwin/arm64"
			p.Tools = append([]ToolPin{extra}, p.Tools...)
		}, e.CodeInvalidCatalog},
		{"duplicate entry", func(_ *ToolSource, p *Pin) { p.Tools[1] = p.Tools[0] }, e.CodeInvalidCatalog},
		{"both lock forms", func(_ *ToolSource, p *Pin) { p.Tool = &p.Tools[0] }, e.CodeInvalidCatalog},
		{"stale tag", func(_ *ToolSource, p *Pin) { p.Tag = "v2.0.0" }, e.CodeUnpinnedSource},
		{"wrong executable", func(_ *ToolSource, p *Pin) { p.Tools[1].ExecutablePath = "wrong" }, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, pin := platformToolFixture()
			tc.edit(&source, &pin)
			err := checkToolPin(&source, pin)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCatalogContractCode(t, err, tc.code)
		})
	}
}

func TestPlatformToolSelectionAndLegacyBytes(t *testing.T) {
	source, pin := platformToolFixture()
	cat := &Catalog{Sources: map[string]Source{"tool": {Tool: &source}}, ToolPlatforms: map[string][]ToolPin{"tool": pin.Tools}}
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			tool, err := cat.SelectTool("tool", platform)
			if platform == "darwin/arm64" {
				assertCatalogContractCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || tool.Platform != platform {
				t.Fatalf("selection = %+v, %v", tool, err)
			}
			legacy := platformSource(source, platform)
			legacyPin := Pin{Tag: pin.Tag, Tool: &tool}
			if err := checkToolPin(&legacy, legacyPin); err != nil {
				t.Fatal(err)
			}
			before, err := canonical.Marshal(legacyPin)
			if err != nil {
				t.Fatal(err)
			}
			cloned := clonePin(legacyPin)
			after, err := canonical.Marshal(cloned)
			if err != nil || string(before) != string(after) {
				t.Fatalf("legacy bytes changed: %s / %s, %v", before, after, err)
			}
		})
	}
	copy := clonePin(pin)
	copy.Tools[0].URL = "https://changed.test/tool"
	if reflect.DeepEqual(copy, pin) || copy.Tools[0].URL == pin.Tools[0].URL {
		t.Fatal("clone aliases pins")
	}
}

func TestPlatformToolResolutionIsAllOrNothing(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "second platform unavailable"}[failSecond], func(t *testing.T) {
			var requested []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested = append(requested, r.URL.Path)
				if failSecond && strings.HasSuffix(r.URL.Path, "arm64") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(r.URL.Path))
			}))
			defer server.Close()
			source, old := platformToolFixture()
			source.URL = server.URL + "/{{.Tag}}/{{.OS}}-{{.Arch}}"
			got, err := resolveSourceToolPinsWithClient(t.Context(), source, old, server.Client())
			if failSecond {
				assertCatalogContractCode(t, err, e.CodeFetchFailed)
				if !reflect.DeepEqual(got, Pin{}) {
					t.Fatal("partial lock escaped failed resolution")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := checkToolPin(&source, got); err != nil {
					t.Fatal(err)
				}
				if got.Tools[0].ExecutableDigest == got.Tools[1].ExecutableDigest {
					t.Fatal("platform bytes reused")
				}
			}
			if len(requested) != 2 {
				t.Fatalf("requests = %v", requested)
			}
			if old.Tools[0].URL == server.URL+"/v1.2.3/linux-amd64" {
				t.Fatal("existing pin mutated")
			}
		})
	}
}
