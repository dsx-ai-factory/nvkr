// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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
		{"unknown URL token", func(s *ToolSource, _ *Pin) { s.URL = "https://example.test/{{.Os}}/{{.Arch}}" }, e.CodeInvalidCatalog},
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

func TestPlatformToolComponentConsumers(t *testing.T) {
	source, pin := platformToolFixture()
	source.Format, source.Companions = "tar-gzip", []string{"evidence/{{.Arch}}.sig"}
	for i := range pin.Tools {
		arch := strings.TrimPrefix(pin.Tools[i].Platform, "linux/")
		pin.Tools[i].Format = source.Format
		pin.Tools[i].ExecutableDigest = "sha256:" + strings.Repeat(string(rune('b'+i)), 64)
		pin.Tools[i].Companions = []ToolCompanion{{Path: "evidence/" + arch + ".sig", Digest: "sha256:" + strings.Repeat(string(rune('d'+i)), 64)}}
	}
	sources := map[string]Source{"shared": {Repo: "https://example.test/tool", Tool: &source}}
	lock := &PinLock{Pins: map[string]Pin{"shared": pin}}
	for _, consumer := range []string{"release executable", "pinned tool"} {
		for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/arm64"} {
			t.Run(consumer+"/"+platform, func(t *testing.T) {
				raw := rawComponent{Platforms: []string{platform}}
				if consumer == "release executable" {
					raw.Source, raw.Prebuilt = "shared", &Prebuilt{ReleaseExecutable: true, Evidence: &ExternalEvidence{None: true}}
				} else {
					raw.BuilderType, raw.Tool = BuilderPinnedTool, "shared"
				}
				got, err := resolveComponent("consumer", raw, sources, lock)
				if platform == "darwin/arm64" {
					assertCatalogContractCode(t, err, e.CodeEngineCapability)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := pin.Tools[0]
				if platform == "linux/arm64" {
					want = pin.Tools[1]
				}
				selected := got.ToolExecutable
				if consumer == "release executable" {
					if got.Prebuilt == nil {
						t.Fatal("missing release executable")
					}
					selected = got.Prebuilt.Executable
					arch := strings.TrimPrefix(platform, "linux/")
					if got.Prebuilt.URL != want.URL || !reflect.DeepEqual(got.Prebuilt.Digests, map[string]string{arch: want.ExecutableDigest}) {
						t.Fatalf("release transport/member binding = %+v", got.Prebuilt)
					}
				}
				if selected == nil || !reflect.DeepEqual(*selected, want) {
					t.Fatalf("selected pin = %+v, want %+v", selected, want)
				}
			})
		}
	}
}

func TestPlatformToolSnapshotIdentity(t *testing.T) {
	cat, err := LoadContext(t.Context(), Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	// The pre-platform snapshot contains these exact fields, including omission of
	// empty optional fields. Keep this contract independent of catalogSnapshot.
	legacy := map[string]any{
		"schemaVersion": "nvkr.dev/catalog-snapshot/v3", "apiVersion": cat.APIVersion,
		"kind": cat.Kind, "metadata": cat.Metadata, "sources": cat.Sources, "components": cat.Components,
	}
	if len(cat.Actions) > 0 {
		legacy["actions"] = cat.Actions
	}
	if len(cat.Tools) > 0 {
		legacy["tools"] = cat.Tools
	}
	want, err := canonical.Digest(canonical.KindCatalogSnapshot, legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []map[string][]ToolPin{nil, {}} {
		cat.ToolPlatforms = empty
		got, err := cat.SnapshotDigest()
		if err != nil || got != want {
			t.Fatalf("legacy snapshot = %s, want %s: %v", got, want, err)
		}
	}
	source, pin := platformToolFixture()
	cat.Sources["platform-tool"] = Source{Repo: "https://example.test/tool", Tool: &source}
	cat.ToolPlatforms = map[string][]ToolPin{"platform-tool": pin.Tools}
	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for i := range pin.Tools {
		t.Run(pin.Tools[i].Platform, func(t *testing.T) {
			changed := cloneToolPins(pin.Tools)
			changed[i].ExecutableDigest = "sha256:" + strings.Repeat("e", 64)
			changed[i].ArchiveDigest = changed[i].ExecutableDigest
			cat.ToolPlatforms["platform-tool"] = changed
			after, err := cat.SnapshotDigest()
			if err != nil || after == before {
				t.Fatalf("platform-only change retained snapshot %s: %v", after, err)
			}
		})
	}
}

func releasePlatformToolFixture() (ToolSource, Pin) {
	source := ToolSource{Format: "raw", URL: "https://example.test/{{.Tag}}/{{.OS}}/{{.Arch}}/tool", Platforms: []string{"darwin/arm64", "linux/amd64", "linux/arm64"}, ExecutablePath: "tool"}
	pin := Pin{Tag: "v1.2.3", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDateEpoch: 1}
	for _, platform := range source.Platforms {
		rendered := platformSource(source, platform)
		rendered.URL, _ = renderRef("tool URL", rendered.URL, pin)
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
			s, _ := releasePlatformToolFixture()
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
			s, p := releasePlatformToolFixture()
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
			source, pin := releasePlatformToolFixture()
			source.URL = server.URL + "/{{.Tag}}/{{.OS}}/{{.Arch}}/tool"
			resolved, err := resolveSourceToolPinsWithClient(t.Context(), source, pin, server.Client())
			pins := resolved.Tools
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
	_, pin := releasePlatformToolFixture()
	cat := &Catalog{ToolPlatforms: map[string][]ToolPin{"tool": pin.Tools}}
	for _, platform := range []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		t.Run(platform, func(t *testing.T) {
			selected, err := cat.SelectTool("tool", platform)
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
	source, pin := releasePlatformToolFixture()
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
		tool, err := cat.SelectTool("upstream", platform)
		if err != nil || tool.URL != pin.Tools[i].URL {
			t.Fatalf("wrong utility selection: %+v %v", tool, err)
		}
	}
	if _, err := cat.SnapshotDigest(); err != nil {
		t.Fatal(err)
	}
}
