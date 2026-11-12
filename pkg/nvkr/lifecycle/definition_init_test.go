// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func initRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func initOperatorCatalog(t *testing.T) string {
	t.Helper()
	return filepath.Join(initRepoRoot(t), "inputs", "catalogs", "tools", "operator-arm64")
}

func TestInitDefinitionAuthorsOneSortedExecutableOutput(t *testing.T) {
	request := DefinitionInit{Name: "my-tools", Catalog: initOperatorCatalog(t), Platform: "linux/arm64", Components: []string{"kubectl", "helm"}}
	got, err := InitDefinition(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("want one output, got %+v", got.Outputs)
	}
	output := got.Outputs[0]
	if output.Name != "my-tools" || output.Format != OutputExecutable || output.Platform != "linux/arm64" || !reflect.DeepEqual(output.Components, []string{"helm", "kubectl"}) {
		t.Fatalf("unexpected output: %+v", output)
	}
	if !filepath.IsAbs(got.Catalog.Base) || got.SBOMTool != "" || got.CosignTool != "" || len(got.Catalog.Overlays) != 0 {
		t.Fatalf("init added more than the requested selection: %+v", got)
	}
	if !reflect.DeepEqual(request.Components, []string{"kubectl", "helm"}) {
		t.Fatalf("init reordered the caller's components: %v", request.Components)
	}
	first, err := RenderDefinition(got)
	if err != nil {
		t.Fatal(err)
	}
	again, err := InitDefinition(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderDefinition(again)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("generation is not deterministic: %v\n%s\n%s", err, first, second)
	}
}

func TestInitDefinitionRelativeCatalogBecomesAbsolute(t *testing.T) {
	want := initOperatorCatalog(t)
	t.Chdir(initRepoRoot(t))
	got, err := InitDefinition(t.Context(), DefinitionInit{Name: "my-tools", Catalog: "inputs/catalogs/tools/operator-arm64", Platform: "linux/arm64", Components: []string{"helm"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Catalog.Base != want {
		t.Fatalf("catalog base = %q, want %q", got.Catalog.Base, want)
	}
}

// The emitted file loads and prepares from any directory, because its catalog
// locator does not depend on where the file is placed.
func TestInitDefinitionContractPreparesFromAnotherDirectory(t *testing.T) {
	root := initRepoRoot(t)
	authored, err := InitDefinition(t.Context(), DefinitionInit{Name: "my-tools", Catalog: initOperatorCatalog(t), Platform: "linux/arm64", Components: []string{"helm", "kubectl"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RenderDefinition(authored)
	if err != nil {
		t.Fatal(err)
	}
	// Preparation reads only inputs under the working tree, so "elsewhere" is a
	// scratch directory in the repository rather than the catalog's directory.
	scratch := filepath.Join(root, ".nvkr")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere, err := os.MkdirTemp(scratch, "definition-init-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(elsewhere) })
	path := filepath.Join(elsewhere, "definition.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path, err = filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, authored) {
		t.Fatalf("round trip changed the Definition:\n%+v\n%+v", loaded, authored)
	}
	prepared, err := PrepareFiles(t.Context(), path, "inputs/environments/local-conformance-tools-arm64.yaml", "test", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	expected := prepared.Description().Expected
	if len(expected) != 1 || expected[0].Name != "my-tools" || !reflect.DeepEqual(expected[0].Components, []string{"helm", "kubectl"}) {
		t.Fatalf("prepared run does not match the request: %+v", expected)
	}
}

func TestInitDefinitionRefusesBeforeAuthoring(t *testing.T) {
	operator := initOperatorCatalog(t)
	ovn := filepath.Join(initRepoRoot(t), "inputs", "catalogs", "ovn-upstream")
	for _, tc := range []struct {
		name    string
		request DefinitionInit
		code    e.Code
	}{
		{"missing name", DefinitionInit{Catalog: operator, Platform: "linux/arm64", Components: []string{"helm"}}, e.CodeUsage},
		{"missing catalog", DefinitionInit{Name: "t", Platform: "linux/arm64", Components: []string{"helm"}}, e.CodeUsage},
		{"missing platform", DefinitionInit{Name: "t", Catalog: operator, Components: []string{"helm"}}, e.CodeUsage},
		{"no components", DefinitionInit{Name: "t", Catalog: operator, Platform: "linux/arm64"}, e.CodeUsage},
		{"duplicate component", DefinitionInit{Name: "t", Catalog: operator, Platform: "linux/arm64", Components: []string{"helm", "helm"}}, e.CodeUsage},
		{"non-canonical name", DefinitionInit{Name: "My_Tools", Catalog: operator, Platform: "linux/arm64", Components: []string{"helm"}}, e.CodeInvalidCatalog},
		{"unknown component", DefinitionInit{Name: "t", Catalog: operator, Platform: "linux/arm64", Components: []string{"nope"}}, e.CodeInvalidCatalog},
		{"invalid platform", DefinitionInit{Name: "t", Catalog: operator, Platform: "linux/riscv64", Components: []string{"helm"}}, e.CodeInvalidCatalog},
		{"unsupported platform", DefinitionInit{Name: "t", Catalog: operator, Platform: "linux/amd64", Components: []string{"helm"}}, e.CodeEngineCapability},
		{"missing catalog directory", DefinitionInit{Name: "t", Catalog: filepath.Join(t.TempDir(), "absent"), Platform: "linux/arm64", Components: []string{"helm"}}, e.CodeInvalidCatalog},
		{"incomplete material selection", DefinitionInit{Name: "t", Catalog: ovn, Platform: "linux/arm64", Components: []string{"nvke-cni-ovn"}}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InitDefinition(t.Context(), tc.request)
			assertCode(t, err, tc.code)
			if !reflect.DeepEqual(got, Definition{}) {
				t.Fatalf("refused request returned a Definition: %+v", got)
			}
		})
	}
}

// A run bundles any producer output type into an executable output (the OVN
// graphs bundle an OCI image), so init admits the same selections and adds no
// output-type rule of its own.
func TestInitDefinitionAdmitsWhatARunAdmits(t *testing.T) {
	root := initRepoRoot(t)
	for _, tc := range []struct {
		name, catalog string
		components    []string
	}{
		{"raw", "inputs/catalogs/tools/installer-local-arm64", []string{"clusterctl"}},
		{"tarball", "inputs/catalogs/nicosim", []string{"nico-sim-runtime-assets"}},
		{"oci-image", "testdata/examples/layered-catalog", []string{"pause"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InitDefinition(t.Context(), DefinitionInit{Name: "t", Catalog: filepath.Join(root, tc.catalog), Platform: "linux/arm64", Components: tc.components})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRenderDefinitionRefusesAnInvalidDefinition(t *testing.T) {
	if raw, err := RenderDefinition(Definition{}); err == nil || raw != nil {
		t.Fatalf("invalid Definition rendered: %q, %v", raw, err)
	}
}
