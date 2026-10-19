// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestCatalogStackReplacesOneProducerWithoutForkingCatalog(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	originalKubectl := base.Components["kubectl"]
	overlay := catalog.Overlay{
		APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
		Name: "trusted-containerd", RequiresCatalogDigest: baseDigest,
		Components: map[string]catalog.ComponentReplacement{
			"containerd": {
				Version: "2.0.5", SourceDateEpoch: 1_750_000_000,
				OutputType: base.Components["containerd"].OutputType,
				Prebuilt: &catalog.Prebuilt{
					URL: "https://supplier.example/containerd-{{.Arch}}",
					Digests: map[string]string{
						"amd64": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						"arm64": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					},
					Evidence: &catalog.ExternalEvidence{None: true},
				},
			},
		},
	}
	stack, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if stack.Digest == baseDigest || len(stack.Applied) != 1 {
		t.Fatalf("stack did not bind overlay: %#v", stack)
	}
	if got := stack.Catalog.Components["kubectl"]; got.Commit != originalKubectl.Commit || got.Prebuilt != nil {
		t.Fatalf("unrelated kubectl producer changed: %#v", got)
	}
	if got := stack.Catalog.Components["containerd"]; got.FromSource || got.Prebuilt == nil || got.Repo != "" {
		t.Fatalf("containerd was not replaced as a clean prebuilt lane: %#v", got)
	}

	resolved, err := buildgraph.Resolve(stack.Catalog, buildgraph.Selection{Components: []string{"containerd", "kubectl"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	foundPull := false
	for _, node := range graph.Nodes {
		if node.Component == "containerd" {
			if node.Kind != ir.KindPull {
				t.Fatalf("containerd node kind=%s, want pull", node.Kind)
			}
			foundPull = true
		}
	}
	if !foundPull {
		t.Fatal("resolved graph omitted supplier containerd")
	}
}

func TestCatalogStackSubstitutesExactToolchainForSelectedProducers(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	old := base.Components["kube-apiserver"].ToolchainImages["amd64"]
	newImage := "nvkr-toolchain-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	proxy := base.Components["kube-proxy"]
	overlay := catalog.Overlay{
		APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
		Name: "native-docker-toolchain", RequiresCatalogDigest: baseDigest,
		Components: map[string]catalog.ComponentReplacement{
			"kube-proxy": {Component: &proxy},
		},
		ToolchainSubstitutions: []catalog.ToolchainSubstitution{{
			Arch: "amd64", From: old, To: newImage,
			Components: []string{"kube-apiserver", "kube-proxy"},
		}},
	}
	stack, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if stack.Digest == baseDigest || len(stack.Applied) != 1 {
		t.Fatalf("toolchain substitution did not move stack identity: %#v", stack)
	}
	for _, name := range []string{"kube-apiserver", "kube-proxy"} {
		if got := stack.Catalog.Components[name].ToolchainImages["amd64"]; got != newImage {
			t.Fatalf("%s toolchain=%q, want %q", name, got, newImage)
		}
		if got := base.Components[name].ToolchainImages["amd64"]; got != old {
			t.Fatalf("base component %s was mutated: %q", name, got)
		}
	}
	if got := stack.Catalog.Components["kube-scheduler"].ToolchainImages["amd64"]; got != old {
		t.Fatalf("unselected component changed toolchain: %q", got)
	}
	resolved, err := buildgraph.Resolve(stack.Catalog, buildgraph.Selection{
		Components: []string{"kube-apiserver"}, Platform: "linux/amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch && node.VendorImage == newImage {
			found = true
		}
	}
	if !found {
		t.Fatal("compiled fetch did not select substituted toolchain")
	}
	baseResolved, err := buildgraph.Resolve(base, buildgraph.Selection{
		Components: []string{"kube-apiserver"}, Platform: "linux/amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseGraph, err := buildgraph.Compile(baseResolved)
	if err != nil {
		t.Fatal(err)
	}
	baseBuildDigest, err := baseGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	selectedBuildDigest, err := graph.Digest()
	if err != nil || selectedBuildDigest == baseBuildDigest {
		t.Fatalf("toolchain substitution did not change Build identity: %s, %s, %v",
			baseBuildDigest, selectedBuildDigest, err)
	}
}

func TestCatalogStackRejectsInvalidToolchainSubstitutions(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	old := base.Components["kube-apiserver"].ToolchainImages["amd64"]
	newImage := "nvkr-toolchain-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	valid := catalog.ToolchainSubstitution{Arch: "amd64", From: old, To: newImage,
		Components: []string{"kube-apiserver", "kube-proxy"}}
	for _, test := range []struct {
		name string
		edit func(*catalog.Overlay)
		code nvkrerr.Code
	}{
		{"stale pin", func(o *catalog.Overlay) {
			o.ToolchainSubstitutions[0].From = "nvkr-toolchain-go@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}, nvkrerr.CodeUnverifiedRef},
		{"unknown component", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].Components = []string{"missing"} }, nvkrerr.CodeUnverifiedRef},
		{"unordered components", func(o *catalog.Overlay) {
			o.ToolchainSubstitutions[0].Components = []string{"kube-proxy", "kube-apiserver"}
		}, nvkrerr.CodeInvalidCatalog},
		{"duplicate component", func(o *catalog.Overlay) {
			o.ToolchainSubstitutions[0].Components = []string{"kube-proxy", "kube-proxy"}
		}, nvkrerr.CodeInvalidCatalog},
		{"invalid destination", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].To = "latest" }, nvkrerr.CodeInvalidCatalog},
		{"invalid architecture", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].Arch = "bogus" }, nvkrerr.CodeInvalidCatalog},
		{"unchanged pin", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].To = old }, nvkrerr.CodeInvalidCatalog},
		{"empty components", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].Components = nil }, nvkrerr.CodeInvalidCatalog},
		{"invalid from scheme", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].From = "docker://" + old }, nvkrerr.CodeInvalidCatalog},
		{"invalid to scheme", func(o *catalog.Overlay) { o.ToolchainSubstitutions[0].To = "docker://" + newImage }, nvkrerr.CodeInvalidCatalog},
		{"empty overlay", func(o *catalog.Overlay) { o.ToolchainSubstitutions = nil }, nvkrerr.CodeInvalidCatalog},
		{"prebuilt target", func(o *catalog.Overlay) {
			o.Components = map[string]catalog.ComponentReplacement{"kube-apiserver": {
				Version: "1", SourceDateEpoch: 1, OutputType: base.Components["kube-apiserver"].OutputType,
				Prebuilt: &catalog.Prebuilt{Image: "supplier.example/component", Digests: map[string]string{
					"amd64": "sha256:" + strings.Repeat("a", 64), "arm64": "sha256:" + strings.Repeat("b", 64),
				}, Evidence: &catalog.ExternalEvidence{None: true}},
			}}
		}, nvkrerr.CodeUnverifiedRef},
		{"duplicate substitution", func(o *catalog.Overlay) { o.ToolchainSubstitutions = append(o.ToolchainSubstitutions, valid) }, nvkrerr.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
				Name: "invalid", RequiresCatalogDigest: baseDigest,
				ToolchainSubstitutions: []catalog.ToolchainSubstitution{valid}}
			test.edit(&overlay)
			stack, err := catalog.ApplyStack(base, overlay)
			if stack != nil || errorCode(err) != test.code {
				t.Fatalf("stack=%#v, error=%v, want code %s", stack, err, test.code)
			}
			if after, err := base.SnapshotDigest(); err != nil || after != baseDigest {
				t.Fatalf("failed substitution mutated base: %s, %v", after, err)
			}
		})
	}
}

func TestToolchainSubstitutionJSONContract(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../../testdata/catalogs/toolchain-substitution.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture exercises the script's wire shape; bind its predecessor to this
	// test catalog rather than duplicating that catalog's changing identity.
	overlay.RequiresCatalogDigest, err = base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kube-apiserver", "kube-proxy"} {
		if stack.Catalog.Components[name].ToolchainImages["amd64"] != overlay.ToolchainSubstitutions[0].To {
			t.Fatalf("%s did not receive JSON-authored toolchain substitution", name)
		}
	}
}

func TestCatalogStackRejectsReplayAndUnknownReplacement(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := catalog.ComponentReplacement{
		Version: "1", SourceDateEpoch: 1_750_000_000, OutputType: "host-package",
		Prebuilt: &catalog.Prebuilt{URL: "https://supplier.example/tool", Digests: map[string]string{
			"amd64": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}, Evidence: &catalog.ExternalEvidence{None: true}},
	}
	_, err = catalog.ApplyStack(base, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion,
		Kind: catalog.OverlayKind, Name: "replay", RequiresCatalogDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Components: map[string]catalog.ComponentReplacement{"kubectl": replacement}})
	if errorCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("replay code=%s err=%v", errorCode(err), err)
	}
	baseDigest, _ := base.SnapshotDigest()
	_, err = catalog.ApplyStack(base, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion,
		Kind: catalog.OverlayKind, Name: "unknown", RequiresCatalogDigest: baseDigest,
		Components: map[string]catalog.ComponentReplacement{"not-real": replacement}})
	if errorCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("unknown code=%s err=%v", errorCode(err), err)
	}
}

func TestCatalogStackPreservesProducerContract(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*catalog.Component)
	}{
		{"targets", func(c *catalog.Component) { c.Platforms = []string{"linux/amd64"} }},
		{"output type", func(c *catalog.Component) { c.OutputType = "raw" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			component := base.Components["kubectl"]
			test.edit(&component)
			overlay := catalog.Overlay{
				APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
				Name: "changed-contract", RequiresCatalogDigest: digest,
				Components: map[string]catalog.ComponentReplacement{"kubectl": {Component: &component}},
			}
			stack, err := catalog.ApplyStack(base, overlay)
			if stack != nil || errorCode(err) != nvkrerr.CodeInvalidCatalog {
				t.Fatalf("changed producer contract produced stack=%+v, err=%v", stack, err)
			}
			if after, err := base.SnapshotDigest(); err != nil || after != digest {
				t.Fatalf("rejected replacement mutated base catalog: %s, %v", after, err)
			}
		})
	}
}

func TestLoadOverlayContextIsStrictAndNoFollow(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := base.SnapshotDigest()
	dir := t.TempDir()
	path := filepath.Join(dir, "supplier.yaml")
	raw := fmt.Sprintf(`apiVersion: %s
kind: %s
name: supplier
requiresCatalogDigest: %s
components:
  containerd:
    version: "2.0.5"
    sourceDateEpoch: 1750000000
    outputType: oci-image
    prebuilt:
      image: supplier.example/containerd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      digests:
        amd64: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      evidence:
        none: true
`, catalog.OverlayAPIVersion, catalog.OverlayKind, digest)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(context.Background(), path)
	if err != nil || overlay.Name != "supplier" {
		t.Fatalf("load overlay: %#v %v", overlay, err)
	}
	for _, field := range []string{"roles: [worker]", "category: os", "component: {category: os}", "component: {roles: [worker]}"} {
		t.Run("removed "+field, func(t *testing.T) {
			changed := strings.Replace(raw, "    version:", "    "+field+"\n    version:", 1)
			if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := catalog.LoadOverlayContext(context.Background(), path)
			if errorCode(err) != nvkrerr.CodeInvalidCatalog || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("retired classification did not fail strict decoding: %v", err)
			}
		})
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadOverlayContext(context.Background(), link); errorCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("symlink code=%s err=%v", errorCode(err), err)
	}
}

func errorCode(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
