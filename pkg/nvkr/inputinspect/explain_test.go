// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package inputinspect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func write(t *testing.T, root, name string, data []byte) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{catalog.RegistryFile, catalog.LockFile} {
		raw, err := os.ReadFile(filepath.Join("../../../testdata/examples/layered-catalog", name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, root, "catalog/"+name, raw)
	}
	def := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: "tiny"}, Catalog: definition.Catalog{Base: "catalog"}, Outputs: []definition.DefinitionOutput{{Name: "pause", Platform: "linux/amd64", Format: definition.OutputOCI, Components: []string{"pause"}}}}
	writeJSON(t, root, "definition.json", def)
	return root
}

func writeJSON(t *testing.T, root, name string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, name, raw)
}

func code(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func TestExplainDirectAndReverseDeterministic(t *testing.T) {
	root := fixture(t)
	for _, tc := range []struct {
		file, kind      string
		direct, reverse int
	}{
		{"definition.json", "Definition", 2, 0},
		{"catalog/registry.yaml", "SourceBuildRegistry", 1, 3},
		{"catalog/pins.lock.yaml", "GeneratedLock", 1, 1},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			got, err := Explain(t.Context(), root, filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != tc.kind || len(got.References) != tc.direct || len(got.Consumers) != tc.reverse {
				t.Fatalf("report: %+v", got)
			}
			again, err := Explain(t.Context(), root, filepath.Join(root, tc.file))
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("nondeterministic: %v", err)
			}
			if !strings.Contains(got.Scope, "within this root") {
				t.Fatal("missing scope")
			}
		})
	}
}

func imported(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, "catalog/registry.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	components := reg["components"].(map[string]any)
	fragment := map[string]any{"apiVersion": "nvkr.dev/catalog-fragment/v1", "kind": "CatalogFragment", "components": map[string]any{"shared-pause": components["pause"]}}
	delete(components, "pause")
	fragRaw, err := yaml.Marshal(fragment)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "shared.yaml", fragRaw)
	reg["imports"] = []any{map[string]any{"path": "../shared.yaml", "sha256": fmt.Sprintf("%x", sha256.Sum256(fragRaw)), "components": map[string]string{"pause": "shared-pause"}}}
	writeJSON(t, root, "catalog/registry.yaml", reg)
}

func TestExplainFragmentMappingsAndHashFailure(t *testing.T) {
	root := fixture(t)
	imported(t, root)
	got, err := Explain(t.Context(), root, filepath.Join(root, "shared.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Consumers) != 1 || got.Consumers[0].Mappings["pause"] != "shared-pause" {
		t.Fatalf("missing mapping: %+v", got)
	}
	f, err := os.OpenFile(filepath.Join(root, "shared.yaml"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n# changed\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Explain(t.Context(), root, filepath.Join(root, "shared.yaml"))
	code(t, err, e.CodeInvalidCatalog)
}

func overlay(t *testing.T, root string, selected bool) {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	pause := base.Components["pause"]
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "alternate", RequiresCatalogDigest: digest, Components: map[string]catalog.ComponentReplacement{"pause": {Component: &pause}}}
	writeJSON(t, root, "overlay.json", overlay)
	if selected {
		def, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.json"))
		if err != nil {
			t.Fatal(err)
		}
		def.Catalog.Overlays = []string{"overlay.json"}
		writeJSON(t, root, "definition.json", def)
	}
}

func TestExplainOverlayBindingAndConsumers(t *testing.T) {
	root := fixture(t)
	overlay(t, root, true)
	got, err := Explain(t.Context(), root, filepath.Join(root, "overlay.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.References) != 1 || got.References[0].Path != "catalog/registry.yaml" || len(got.Consumers) != 2 {
		t.Fatalf("overlay report: %+v", got)
	}
}

func TestExplainRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		file   string
		want   e.Code
	}{
		{"missing reference", func(t *testing.T, r string) {
			def, err := definition.LoadDefinition(t.Context(), filepath.Join(r, "definition.json"))
			if err != nil {
				t.Fatal(err)
			}
			def.Catalog.Base = "absent"
			writeJSON(t, r, "definition.json", def)
		}, "definition.json", e.CodeInvalidCatalog},
		{"unknown schema", func(t *testing.T, r string) {
			write(t, r, "unknown.yaml", []byte("apiVersion: nvkr.dev/unknown/v99\nkind: Unknown\n"))
		}, "unknown.yaml", e.CodeInvalidCatalog},
		{"unknown component", func(t *testing.T, r string) {
			def, err := definition.LoadDefinition(t.Context(), filepath.Join(r, "definition.json"))
			if err != nil {
				t.Fatal(err)
			}
			def.Outputs[0].Components = []string{"absent"}
			writeJSON(t, r, "definition.json", def)
		}, "definition.json", e.CodeInvalidCatalog},
		{"unmatched predecessor", func(t *testing.T, r string) {
			overlay(t, r, false)
			p := filepath.Join(r, "overlay.json")
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var v catalog.Overlay
			if err = json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			v.RequiresCatalogDigest = "sha256:" + strings.Repeat("a", 64)
			writeJSON(t, r, "overlay.json", v)
		}, "overlay.json", e.CodeUnverifiedRef},
		{"ambiguous predecessor", func(t *testing.T, r string) {
			overlay(t, r, false)
			for _, n := range []string{catalog.RegistryFile, catalog.LockFile} {
				raw, err := os.ReadFile(filepath.Join(r, "catalog", n))
				if err != nil {
					t.Fatal(err)
				}
				write(t, r, "duplicate/"+n, raw)
			}
		}, "overlay.json", e.CodeUnverifiedRef},
		{"symlink", func(t *testing.T, r string) {
			if err := os.Symlink("definition.json", filepath.Join(r, "linked.json")); err != nil {
				t.Fatal(err)
			}
		}, "definition.json", e.CodeInvalidCatalog},
		{"composite cycle", func(t *testing.T, r string) {
			writeJSON(t, r, "composite.json", definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "cycle"}, Members: []definition.CompositeMember{{Name: "self", Definition: "composite.json", Digest: "sha256:" + strings.Repeat("a", 64)}}})
		}, "composite.json", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t)
			tc.mutate(t, root)
			_, err := Explain(t.Context(), root, filepath.Join(root, tc.file))
			code(t, err, tc.want)
		})
	}
}

func TestExplainCanceledBeforeIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Explain(ctx, "/absent", "/absent/file")
	code(t, err, e.CodeTimeout)
}

func TestExplainEnvironmentAndComposite(t *testing.T) {
	root := fixture(t)
	raw, err := os.ReadFile("../../../demos/hello/environment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "environment.yaml", raw)
	def, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := def.Digest()
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, root, "composite.json", definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "joined"}, Members: []definition.CompositeMember{{Name: "tiny", Definition: "definition.json", Digest: digest}}})
	for _, name := range []string{"environment.yaml", "composite.json"} {
		if _, err := Explain(t.Context(), root, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
}
