// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func runtimeSBOMFixture(t *testing.T) (*BOM, *pipeline.RuntimeContents) {
	t.Helper()
	digest := func(value string) string { return "sha256:" + strings.Repeat(value, 64) }
	component := catalog.NamedComponent{Name: "runtime", Component: catalog.Component{
		BuilderType: catalog.BuilderMkosi, FromSource: true, OutputType: "oci-image", Tag: "v1",
		Repo: "https://example.invalid/mkosi", Commit: strings.Repeat("1", 40), TreeHash: strings.Repeat("2", 40)}}
	boms, index, err := PerArtifact(&buildgraph.Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{component}}, digest("a"), map[string]string{"runtime": digest("b")})
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Validate(); err != nil {
		t.Fatal(err)
	}
	contents := &pipeline.RuntimeContents{Binding: pipeline.ProducedRuntimeBinding{BuildDigest: digest("a"), Component: "runtime", Platform: "linux/amd64",
		Digest: digest("b"), LayerDigest: digest("c")}, RecipeDigest: digest("d"),
		Roots: []pipeline.RuntimePackageRoot{{Name: "runtime", Distribution: "ubuntu", Release: "noble", Snapshot: "20261109T000000Z", ReportDigest: digest("e"),
			Packages: []mkosi.PackagePin{{Name: "base-files", Version: "1.0", Architecture: "amd64", Digest: digest("f"), Size: 42, RepositoryDigest: digest("0")}}}},
		Files: []pipeline.RuntimeFile{{Path: "/nvkr/tools/packer", Material: "packer", Digest: digest("1"), Size: 123}}}
	return boms["runtime"], contents
}

func TestRuntimeSBOMSeparatesAssemblySourceAndPackageInputs(t *testing.T) {
	base, contents := runtimeSBOMFixture(t)
	for _, property := range base.Metadata.Component.Properties {
		if property.Name == "nvkr:fromSource" && property.Value != "false" || property.Name == "nvkr:toolchain" || property.Name == "nvkr:repo" {
			t.Fatalf("runtime misstates assembled packages as compiled source: %+v", property)
		}
	}
	bom, err := withRuntimeContents(base, contents)
	if err != nil {
		t.Fatal(err)
	}
	if len(bom.Components) != 2 || len(bom.Dependencies) != 1 || len(bom.Dependencies[0].DependsOn) != 2 {
		t.Fatalf("incomplete runtime inventory: %+v", bom)
	}
	for _, component := range bom.Components {
		if component.Type == "library" && len(component.Hashes) != 0 {
			t.Fatal("package archive hash was claimed as installed-content hash")
		}
		if component.Type == "file" && (component.Name != "/nvkr/tools/packer" || component.Hashes[0].Content != strings.Repeat("1", 64)) {
			t.Fatal("projected material lost measured bytes")
		}
	}
	if len(base.Components) != 0 {
		t.Fatal("inventory mutated original composition")
	}
	if _, err := bom.Marshal(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSBOMRetainsRepeatedPackagesAcrossRootsDeterministically(t *testing.T) {
	base, contents := runtimeSBOMFixture(t)
	other := contents.Roots[0]
	other.Name, other.ReportDigest = "inspection", "sha256:"+strings.Repeat("2", 64)
	contents.Roots = append(contents.Roots, other)
	first, err := withRuntimeContents(base, contents)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(contents.Roots)
	second, err := withRuntimeContents(base, contents)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || len(first.Components) != 2 {
		t.Fatal("root ordering changed inventory or repeated a package")
	}
	for _, component := range first.Components {
		if component.Type == "library" && len(component.Properties) != 10 {
			t.Fatal("same package lost one root's retained inputs")
		}
	}
}

func TestRuntimeSBOMRejectsUnownedAndSubstitutedSubjects(t *testing.T) {
	base, contents := runtimeSBOMFixture(t)
	for _, tool := range []*pipeline.ProducedRuntime{nil, {}} {
		_, err := WithRuntimeContents(t.Context(), base, tool, "/must-not-create")
		assertDependencyCode(t, err, e.CodeGateUnmet)
	}
	_, err := WithDependencies(base, []Component{{Type: "library", Name: "made-up"}}, MkosiInstalledMethod, "")
	assertDependencyCode(t, err, e.CodeEngineCapability)
	for _, field := range []string{"build", "component", "subject", "type"} {
		t.Run(field, func(t *testing.T) {
			candidate := cloneBOM(base)
			switch field {
			case "build":
				for i := range candidate.Metadata.Props {
					if candidate.Metadata.Props[i].Name == "nvkr:buildDigest" {
						candidate.Metadata.Props[i].Value = "sha256:" + strings.Repeat("3", 64)
					}
				}
			case "component":
				for i := range candidate.Metadata.Props {
					if candidate.Metadata.Props[i].Name == "nvkr:artifact" {
						candidate.Metadata.Props[i].Value = "other"
					}
				}
			case "subject":
				candidate.Metadata.Component.Hashes[0].Content = strings.Repeat("3", 64)
			case "type":
				candidate.Metadata.Component.Type = "application"
			}
			_, err := withRuntimeContents(candidate, contents)
			assertDependencyCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestRuntimeSBOMSerializedEvidenceRequiresOriginalBindings(t *testing.T) {
	base, contents := runtimeSBOMFixture(t)
	bom, err := withRuntimeContents(base, contents)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nvkr:runtimeLayerDigest", "nvkr:runtimeRecipeDigest", "nvkr:mkosi:runtime:reportDigest"} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneBOM(bom)
			candidate.Metadata.Props = slices.DeleteFunc(candidate.Metadata.Props, func(property Property) bool { return property.Name == name })
			assertDependencyCode(t, candidate.Validate(), e.CodeProvenanceMissing)
		})
	}
}

func TestRuntimeSBOMRejectsFalseSerializedInventory(t *testing.T) {
	base, contents := runtimeSBOMFixture(t)
	bom, err := withRuntimeContents(base, contents)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*BOM)
		code   e.Code
	}{
		{"archive hash as installed content", func(b *BOM) {
			for i := range b.Components {
				if b.Components[i].Type == "library" {
					b.Components[i].Hashes = []Hash{{Alg: "SHA-256", Content: strings.Repeat("a", 64)}}
				}
			}
		}, e.CodeVerifyFailed},
		{"different report", func(b *BOM) {
			for i := range b.Components {
				for j := range b.Components[i].Properties {
					if strings.HasSuffix(b.Components[i].Properties[j].Name, ":reportDigest") {
						b.Components[i].Properties[j].Value = "sha256:" + strings.Repeat("3", 64)
					}
				}
			}
		}, e.CodeVerifyFailed},
		{"missing package inputs", func(b *BOM) {
			for i := range b.Components {
				if b.Components[i].Type == "library" {
					b.Components[i].Properties = nil
				}
			}
		}, e.CodeProvenanceMissing},
		{"file traversal", func(b *BOM) {
			for i := range b.Components {
				if b.Components[i].Type == "file" {
					b.Components[i].Name = "/nvkr/../outside"
				}
			}
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneBOM(bom)
			tc.change(candidate)
			assertDependencyCode(t, candidate.Validate(), tc.code)
		})
	}
}
