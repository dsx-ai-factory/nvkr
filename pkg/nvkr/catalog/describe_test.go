// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// describeCatalog is three levels deep: image needs agent and runtime, and
// agent needs runtime too, so runtime is a shared material.
func describeCatalog(t *testing.T) *Catalog {
	t.Helper()
	agent := goodComponent()
	agent.Materials = map[string]string{"runtime": "runtime"}
	cat := &Catalog{
		Metadata: Metadata{Name: "describe-fixture"},
		Components: map[string]Component{
			"agent":   agent,
			"image":   vmMaterialComponent(map[string]string{"agent-package": "agent", "runtime-package": "runtime"}),
			"runtime": goodPrebuilt(),
			"unused":  goodComponent(),
		},
	}
	if err := cat.Validate(); err != nil {
		t.Fatalf("valid describe fixture: %v", err)
	}
	return cat
}

func TestDescribeMaterialClosure(t *testing.T) {
	description, err := describeCatalog(t).Describe("image")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(description.Closure, []string{"agent", "runtime"}) {
		t.Fatalf("closure lists each material once, sorted, and nothing unreachable: %v", description.Closure)
	}
	wantEdges := []DescribedMaterialBinding{
		{Consumer: "agent", Binding: "runtime", Component: "runtime"},
		{Consumer: "image", Binding: "agent-package", Component: "agent"},
		{Consumer: "image", Binding: "runtime-package", Component: "runtime"},
	}
	if !slices.Equal(description.Edges, wantEdges) {
		t.Fatalf("edges keep every binding of the shared material: %+v", description.Edges)
	}
	if !slices.Equal(description.Materials, wantEdges[1:]) {
		t.Fatalf("direct materials: %+v", description.Materials)
	}
	if description.Mode != ProducerModeSource || description.Source == nil || description.Source.Commit != goodCommit ||
		description.Source.TreeHash != goodTree || description.OutputType != "disk-image" {
		t.Fatalf("wrong source identity: %+v", description)
	}

	leaf, err := describeCatalog(t).Describe("runtime")
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Mode != ProducerModeAcquisition || leaf.Acquisition == nil || leaf.Source != nil || leaf.Tool != nil ||
		leaf.Acquisition.URL != goodPrebuilt().Prebuilt.URL || leaf.Acquisition.Digests["amd64"] != goodArtifactDigest {
		t.Fatalf("wrong acquisition identity: %+v", leaf)
	}
	if len(leaf.Closure) != 0 || len(leaf.Edges) != 0 {
		t.Fatalf("a leaf has no materials: %+v", leaf)
	}
}

func TestDescribePinnedTool(t *testing.T) {
	cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(pinnedToolRegistry), LockFile: []byte(pinnedToolLock)})
	if err != nil {
		t.Fatal(err)
	}
	for name, closure := range map[string][]string{"recipe": {}, "bundle": {"recipe"}} {
		description, err := cat.Describe(name)
		if err != nil {
			t.Fatal(err)
		}
		if description.Mode != ProducerModePinnedTool || description.Source != nil || description.Acquisition != nil {
			t.Fatalf("%s: a pinned-tool producer is neither excluded, source nor acquired: %+v", name, description)
		}
		want := DescribedToolPin{
			Source: "aicr", Tag: "v1.2.0-rc1", Format: "tar-gzip",
			URL:      "https://github.com/NVIDIA/aicr/releases/download/v1.2.0-rc1/aicr_1.2.0-rc1_linux_arm64.tar.gz",
			Platform: "linux/arm64", ExecutablePath: "aicr", ArchiveDigest: goodArtifactDigest,
			ExecutableDigest: "sha256:" + pinnedToolExecutableHex,
		}
		if description.Tool == nil || *description.Tool != want {
			t.Fatalf("%s: wrong tool pin: %+v", name, description.Tool)
		}
		if !slices.Equal(description.Closure, closure) {
			t.Fatalf("%s: closure %v; want %v", name, description.Closure, closure)
		}
		raw, err := description.Render("json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "AICR_LOG_FORMAT") || strings.Contains(string(raw), "--accelerator") {
			t.Fatalf("%s: description exposes tool environment or arguments: %s", name, raw)
		}
	}
}

func TestDescribeFailsClosed(t *testing.T) {
	cyclic := describeCatalog(t)
	runtime := goodComponent()
	runtime.Materials = map[string]string{"image": "image"}
	cyclic.Components["runtime"] = runtime

	malformed := describeCatalog(t)
	unpinned := malformed.Components["agent"]
	unpinned.Commit = "main"
	malformed.Components["agent"] = unpinned

	for _, test := range []struct {
		name      string
		catalog   *Catalog
		component string
		code      e.Code
	}{
		{"unknown component", describeCatalog(t), "missing", e.CodeInvalidCatalog},
		{"dependency cycle", cyclic, "image", e.CodeInvalidCatalog},
		{"malformed catalog", malformed, "unused", e.CodeUnpinnedSource},
		{"nil catalog", nil, "image", e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.catalog.Describe(test.component)
			if got := pinErrorCode(err); got != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
		})
	}
	if _, err := (ComponentDescription{}).Render("yaml"); pinErrorCode(err) != e.CodeUsage {
		t.Fatalf("unknown format: %v", err)
	}
}

func TestDescribeRenderIsDeterministicAndEscaped(t *testing.T) {
	cat := describeCatalog(t)
	cat.Components["excluded"] = Component{Platforms: []string{"linux/amd64"}, ExclusionReason: "not\x1b[2J distributed\nhere"}
	for _, format := range []string{"text", "json"} {
		first, err := cat.Describe("image")
		if err != nil {
			t.Fatal(err)
		}
		a, err := first.Render(format)
		if err != nil {
			t.Fatal(err)
		}
		for range 20 {
			again, err := cat.Describe("image")
			if err != nil {
				t.Fatal(err)
			}
			b, err := again.Render(format)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("%s output is not deterministic:\n%s\n%s", format, a, b)
			}
		}
	}

	excluded, err := cat.Describe("excluded")
	if err != nil {
		t.Fatal(err)
	}
	if excluded.Mode != ProducerModeExcluded {
		t.Fatalf("mode %q", excluded.Mode)
	}
	text, err := excluded.Render("text")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(text, '\x1b') || !strings.Contains(string(text), `exclusion reason: not\x1b[2J distributed\nhere`) {
		t.Fatalf("text does not escape control characters:\n%q", text)
	}
	raw, err := excluded.Render("json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded ComponentDescription
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.ExclusionReason != "not\x1b[2J distributed\nhere" {
		t.Fatalf("JSON does not round-trip: %v %+v", err, decoded)
	}
}

func TestDescribeIsDetached(t *testing.T) {
	cat := describeCatalog(t)
	description, err := cat.Describe("runtime")
	if err != nil {
		t.Fatal(err)
	}
	description.Platforms[0] = "changed"
	description.Acquisition.Digests["amd64"] = "changed"
	if cat.Components["runtime"].Platforms[0] != "linux/amd64" || cat.Components["runtime"].Prebuilt.Digests["amd64"] != goodArtifactDigest {
		t.Fatal("description shares storage with the catalog")
	}
}
