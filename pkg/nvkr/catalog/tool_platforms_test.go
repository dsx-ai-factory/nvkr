// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func platformToolFixture() (ToolSource, Pin) {
	source := ToolSource{Format: "raw", URL: "https://example.test/{{.Tag}}/{{.OS}}/{{.Arch}}/tool", Platforms: []string{"darwin/arm64", "linux/amd64", "linux/arm64"}, ExecutablePath: "tool"}
	pin := Pin{Tag: "v1.2.3", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1}
	for _, platform := range source.Platforms {
		rendered, _ := renderPlatformTool(source, pin, platform)
		pin.Tools = append(pin.Tools, ToolPin{Format: "raw", URL: rendered.URL, Platform: platform, ExecutablePath: "tool", ArchiveDigest: "sha256:" + strings.Repeat("c", 64), ExecutableDigest: "sha256:" + strings.Repeat("c", 64)})
	}
	return source, pin
}

func TestPlatformToolDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ToolSource)
		code   e.Code
	}{
		{"three platforms", func(*ToolSource) {}, ""},
		{"both forms", func(s *ToolSource) { s.Platform = "linux/amd64" }, e.CodeInvalidCatalog},
		{"empty", func(s *ToolSource) { s.Platforms = []string{} }, e.CodeInvalidCatalog},
		{"unsorted", func(s *ToolSource) { slices.Reverse(s.Platforms) }, e.CodeInvalidCatalog},
		{"duplicate", func(s *ToolSource) { s.Platforms = []string{"linux/arm64", "linux/arm64"} }, e.CodeInvalidCatalog},
		{"unsupported", func(s *ToolSource) { s.Platforms = []string{"windows/amd64"} }, e.CodeInvalidCatalog},
		{"same asset", func(s *ToolSource) { s.URL = "https://example.test/tool" }, e.CodeInvalidCatalog},
		{"unknown token", func(s *ToolSource) { s.URL = "https://example.test/{{.Architecture}}" }, e.CodeInvalidCatalog},
		{"unsafe member", func(s *ToolSource) { s.ExecutablePath = "../{{.Arch}}" }, e.CodeInvalidCatalog},
		{"members and companions", func(s *ToolSource) {
			s.Format = "tar-gzip"
			s.ExecutablePath = "{{.OS}}-{{.Arch}}/tool"
			s.Companions = []string{"{{.OS}}-{{.Arch}}/tool.sig"}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := platformToolFixture()
			tc.change(&s)
			err := validateToolSource(s)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if pinErrorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestPlatformToolLockCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Pin)
		code   e.Code
	}{
		{"complete", func(*Pin) {}, ""},
		{"missing", func(p *Pin) { p.Tools = p.Tools[:2] }, e.CodeUnpinnedSource},
		{"duplicate", func(p *Pin) { p.Tools[1] = p.Tools[0] }, e.CodeInvalidCatalog},
		{"both forms", func(p *Pin) { x := p.Tools[0]; p.Tool = &x }, e.CodeInvalidCatalog},
		{"wrong URL", func(p *Pin) { p.Tools[1].URL = "https://example.test/substituted" }, e.CodeUnpinnedSource},
		{"wrong version", func(p *Pin) { p.Tag = "v1.2.4" }, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := platformToolFixture()
			tc.change(&p)
			err := checkToolPin(&s, p)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if pinErrorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestPlatformToolsResolveAllOrNone(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "second fails"}[fail], func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail && strings.Contains(r.URL.Path, "linux/amd64") {
					http.Error(w, "missing", http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(r.URL.Path))
			}))
			defer server.Close()
			source, pin := platformToolFixture()
			source.URL = server.URL + "/{{.Tag}}/{{.OS}}/{{.Arch}}/tool"
			pins, err := resolvePlatformTools(t.Context(), source, pin, server.Client())
			if fail {
				if pinErrorCode(err) != e.CodeFetchFailed || pins != nil {
					t.Fatalf("partial result after failure: %v %v", pins, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(pins) != 3 {
				t.Fatalf("incomplete result: %v", pins)
			}
			pin.Tools = pins
			if err := checkToolPin(&source, pin); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlatformToolSelection(t *testing.T) {
	_, pin := platformToolFixture()
	cat := &Catalog{ToolsByPlatform: map[string][]ToolPin{"tool": pin.Tools}}
	for _, platform := range []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		t.Run(platform, func(t *testing.T) {
			selected, err := cat.ToolForPlatform("tool", platform)
			if platform == "windows/amd64" {
				if pinErrorCode(err) != e.CodeEngineCapability {
					t.Fatal(err)
				}
				return
			}
			if err != nil || selected.Platform != platform {
				t.Fatalf("wrong platform: %v %v", selected, err)
			}
		})
	}
}

func TestLoadPlatformExecutableCatalog(t *testing.T) {
	source, pin := platformToolFixture()
	registry := map[string]any{"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "sources": map[string]any{"upstream": map[string]any{"repo": "https://github.com/example/tool", "constraint": "1.2.3", "tool": source}}, "components": map[string]any{"tool": map[string]any{"source": "upstream", "platforms": source.Platforms, "outputType": "host-package", "prebuilt": map[string]any{"releaseExecutable": true, "evidence": map[string]any{"none": true}}}}}
	lock := map[string]any{"apiVersion": "nvkr.dev/lock/v1", "kind": "PinLock", "pins": map[string]Pin{"upstream": pin}}
	registryBytes, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: registryBytes, LockFile: lockBytes})
	if err != nil {
		t.Fatal(err)
	}
	for i, platform := range source.Platforms {
		selected, err := cat.Components["tool"].ForPlatform(platform)
		if err != nil {
			t.Fatal(err)
		}
		if selected.Prebuilt.Executable == nil || selected.Prebuilt.Executable.URL != pin.Tools[i].URL || selected.Prebuilt.Executables != nil {
			t.Fatalf("wrong native projection: %+v", selected)
		}
		tool, err := cat.ToolForPlatform("upstream", platform)
		if err != nil || tool.URL != pin.Tools[i].URL {
			t.Fatalf("wrong utility selection: %+v %v", tool, err)
		}
	}
	if _, err := cat.SnapshotDigest(); err != nil {
		t.Fatal(err)
	}
}
