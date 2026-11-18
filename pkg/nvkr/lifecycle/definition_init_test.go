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
	return filepath.Join(initRepoRoot(t), "inputs", "catalogs", "tools")
}

func TestInitDefinitionAuthorsOneSortedExecutableOutput(t *testing.T) {
	request := DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "my-tools", Catalog: initOperatorCatalog(t), Platform: "linux/arm64", Components: []string{"kubectl-operator", "helm-operator"}}
	got, err := InitDefinition(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outputs) != 1 {
		t.Fatalf("want one output, got %+v", got.Outputs)
	}
	output := got.Outputs[0]
	if output.Name != "my-tools" || output.Format != OutputExecutable || output.Platform != "linux/arm64" || !reflect.DeepEqual(output.Components, []string{"helm-operator", "kubectl-operator"}) {
		t.Fatalf("unexpected output: %+v", output)
	}
	if filepath.IsAbs(got.Catalog.Base) || got.SBOMTool != "" || got.CosignTool != "" || len(got.Catalog.Overlays) != 0 {
		t.Fatalf("init added more than the requested selection: %+v", got)
	}
	if !reflect.DeepEqual(request.Components, []string{"kubectl-operator", "helm-operator"}) {
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

func TestInitDefinitionRelativeCatalogStaysRelative(t *testing.T) {
	want := "inputs/catalogs/tools"
	root := initRepoRoot(t)
	t.Chdir(root)
	got, err := InitDefinition(t.Context(), DefinitionInit{Root: root, Destination: "starter.yaml", Name: "my-tools", Catalog: "inputs/catalogs/tools", Platform: "linux/arm64", Components: []string{"helm-operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Catalog.Base != want {
		t.Fatalf("catalog base = %q, want %q", got.Catalog.Base, want)
	}
}

// The emitted file prepares from its declared destination, independently of
// the working directory used when authoring it.
func TestInitDefinitionContractPreparesFromAnotherDirectory(t *testing.T) {
	root := initRepoRoot(t)
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
	authored, err := InitDefinition(t.Context(), DefinitionInit{Root: root, Destination: path, Name: "my-tools", Catalog: initOperatorCatalog(t), Platform: "linux/arm64", Components: []string{"helm-operator", "kubectl-operator"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := RenderDefinition(authored)
	if err != nil {
		t.Fatal(err)
	}
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
	if len(expected) != 1 || expected[0].Name != "my-tools" || !reflect.DeepEqual(expected[0].Components, []string{"helm-operator", "kubectl-operator"}) {
		t.Fatalf("prepared run does not match the request: %+v", expected)
	}
}

func TestInitDefinitionRefusesBeforeAuthoring(t *testing.T) {
	operator := initOperatorCatalog(t)
	ovn := filepath.Join(initRepoRoot(t), "inputs", "catalogs", "ovn", "upstream")
	for _, tc := range []struct {
		name    string
		request DefinitionInit
		code    e.Code
	}{
		{"missing name", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Catalog: operator, Platform: "linux/arm64", Components: []string{"helm-operator"}}, e.CodeUsage},
		{"missing catalog", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Platform: "linux/arm64", Components: []string{"helm-operator"}}, e.CodeUsage},
		{"missing platform", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Components: []string{"helm-operator"}}, e.CodeUsage},
		{"no components", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Platform: "linux/arm64"}, e.CodeUsage},
		{"duplicate component", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Platform: "linux/arm64", Components: []string{"helm-operator", "helm-operator"}}, e.CodeUsage},
		{"non-canonical name", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "My_Tools", Catalog: operator, Platform: "linux/arm64", Components: []string{"helm-operator"}}, e.CodeInvalidCatalog},
		{"unknown component", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Platform: "linux/arm64", Components: []string{"nope"}}, e.CodeInvalidCatalog},
		{"invalid platform", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Platform: "linux/riscv64", Components: []string{"helm-operator"}}, e.CodeInvalidCatalog},
		{"unsupported platform", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: operator, Platform: "darwin/arm64", Components: []string{"helm-operator"}}, e.CodeEngineCapability},
		{"missing catalog directory", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: filepath.Join(initRepoRoot(t), "absent"), Platform: "linux/arm64", Components: []string{"helm-operator"}}, e.CodeInvalidCatalog},
		{"incomplete material selection", DefinitionInit{Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: ovn, Platform: "linux/arm64", Components: []string{"nvke-cni-ovn"}}, e.CodeInvalidCatalog},
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
func TestInitDefinitionExplicitPolicyPreservesProducerFormats(t *testing.T) {
	root := initRepoRoot(t)
	for _, tc := range []struct {
		name, catalog string
		components    []string
	}{
		{"raw", "inputs/catalogs/tools", []string{"clusterctl-installer-local"}},
		{"tarball", "inputs/catalogs/tools", []string{"nico-sim-runtime-assets-nicosim"}},
		{"oci-image", "testdata/examples/layered-catalog", []string{"pause"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InitDefinition(t.Context(), DefinitionInit{Environment: filepath.Join(root, "inputs/environments/local-conformance-tools-arm64.yaml"), Root: initRepoRoot(t), Destination: "starter.yaml", Name: "t", Catalog: filepath.Join(root, tc.catalog), Platform: "linux/arm64", Components: tc.components})
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
