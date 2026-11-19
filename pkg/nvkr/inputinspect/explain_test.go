// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package inputinspect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
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
		{"catalog/pins.lock.yaml", "PinLock", 1, 1},
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

func TestExplainDerivedOutputsDoNotSelectCatalog(t *testing.T) {
	root := fixture(t)
	def, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.json"))
	if err != nil {
		t.Fatal(err)
	}
	def.Outputs = append(def.Outputs, definition.DefinitionOutput{
		Name: "release-inventory", Platform: "linux/amd64", Format: definition.OutputBundle,
		Derived: &derivedoutput.Selection{Encoding: derivedoutput.InventoryV1, Outputs: []string{"pause"}},
	})
	writeJSON(t, root, "definition.json", def)
	for _, tc := range []struct {
		file            string
		direct, reverse int
	}{
		{"definition.json", 2, 0},
		{"catalog/registry.yaml", 1, 3},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got, err := Explain(t.Context(), root, filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(got.References) != tc.direct || len(got.Consumers) != tc.reverse {
				t.Fatalf("source-less output changed catalog references: %+v", got)
			}
			if tc.file == "definition.json" && !strings.Contains(got.Summary, "2 named outputs") {
				t.Fatalf("derived output missing from summary: %s", got.Summary)
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

func TestExplainIgnoresOtherSchemas(t *testing.T) {
	root := fixture(t)
	for i, field := range []string{"apiVersion", "schemaVersion"} {
		writeJSON(t, root, fmt.Sprintf("other-%d.json", i), map[string]any{field: "nvkr.dev/toolchain-refresh/v1"})
	}
	if _, err := Explain(t.Context(), root, filepath.Join(root, "definition.json")); err != nil {
		t.Fatal(err)
	}
	_, err := Explain(t.Context(), root, filepath.Join(root, "other-0.json"))
	code(t, err, e.CodeInvalidCatalog)
}

func TestExplainRejectsEscapeBeforeReadingFragment(t *testing.T) {
	original := fixture(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "inputs")
	if err := os.Rename(original, root); err != nil {
		t.Fatal(err)
	}
	write(t, parent, "outside.yaml", []byte("not a fragment"))
	raw, err := os.ReadFile(filepath.Join(root, "catalog/registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	reg["imports"] = []any{map[string]any{"path": "../../outside.yaml", "sha256": strings.Repeat("a", 64), "components": map[string]string{"extra": "x"}}}
	writeJSON(t, root, "catalog/registry.yaml", reg)
	_, err = Explain(t.Context(), root, filepath.Join(root, "definition.json"))
	code(t, err, e.CodeInvalidCatalog)
	if !strings.Contains(err.Error(), "leaves --root") {
		t.Fatalf("read escaped input before admission: %v", err)
	}
}

func TestExplainPolicyKinds(t *testing.T) {
	root := fixture(t)
	raw, err := os.ReadFile("../../../inputs/policies/nvke-qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "policy.yaml", raw)
	key := func() string {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	local := lifecycle.LocalQualificationPolicy{SchemaVersion: lifecycle.LocalQualificationPolicySchema, Lane: factory.TierLocalExecution.String(), DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, Platform: "linux/amd64", CosignDigest: digest, BuilderKey: key(), SmokeKey: key(), QualifierKey: key(), Smoke: lifecycle.QualificationSmokePolicy{Images: map[string]string{"linux/amd64": "registry.example.test/smoke@" + digest}, Tools: map[string]string{"tool": "helm-version"}}}
	writeJSON(t, root, "local-policy.json", local)
	for _, tc := range []struct{ file, kind string }{{"policy.yaml", "QualificationPolicy"}, {"local-policy.json", "LocalQualificationPolicy"}} {
		got, err := Explain(t.Context(), root, filepath.Join(root, tc.file))
		if err != nil || got.Kind != tc.kind {
			t.Fatalf("policy %s: %+v %v", tc.file, got, err)
		}
		t.Run(tc.kind+" captured before parent replacement", func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			r := t.TempDir()
			write(t, r, "sub/policy.json", raw)
			s := &scan{ctx: t.Context(), root: r, docs: map[string]*document{}}
			if err := s.walk("sub/policy.json"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(r, "sub"), filepath.Join(r, "old")); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			write(t, outside, "policy.json", []byte("invalid outside policy"))
			if err := os.Symlink(outside, filepath.Join(r, "sub")); err != nil {
				t.Fatal(err)
			}
			if err := s.admit("sub/policy.json"); err != nil {
				t.Fatalf("reopened policy after scan: %v", err)
			}
		})
	}
}

func TestExplainCompositeMismatch(t *testing.T) {
	root := fixture(t)
	writeJSON(t, root, "composite.json", definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "joined"}, Members: []definition.CompositeMember{{Name: "tiny", Definition: "definition.json", Digest: "sha256:" + strings.Repeat("a", 64)}}})
	_, err := Explain(t.Context(), root, filepath.Join(root, "composite.json"))
	code(t, err, e.CodeUnverifiedRef)
}

func TestExplainOrderedOverlayChain(t *testing.T) {
	root := fixture(t)
	overlay(t, root, true)
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(root, "overlay.json"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := *first.Components["pause"].Component
	replacement.Tag = "v2.0.0"
	first.Components["pause"] = catalog.ComponentReplacement{Component: &replacement}
	writeJSON(t, root, "overlay.json", first)
	applied, err := catalog.ApplyStack(base, first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Name = "second"
	second.RequiresCatalogDigest = applied.Digest
	replacement.Tag = "v3.0.0"
	second.Components = map[string]catalog.ComponentReplacement{"pause": {Component: &replacement}}
	writeJSON(t, root, "second.json", second)
	def, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.json"))
	if err != nil {
		t.Fatal(err)
	}
	def.Catalog.Overlays = []string{"overlay.json", "second.json"}
	writeJSON(t, root, "definition.json", def)
	got, err := Explain(t.Context(), root, filepath.Join(root, "definition.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.References[1].Path != "overlay.json" || got.References[2].Path != "second.json" {
		t.Fatalf("reordered: %+v", got.References)
	}
	// An unselected sibling can bind to an intermediate selected state.
	third := first
	third.Name = "third"
	third.RequiresCatalogDigest = applied.Digest
	thirdReplacement := replacement
	thirdReplacement.Tag = "v4.0.0"
	third.Components = map[string]catalog.ComponentReplacement{"pause": {Component: &thirdReplacement}}
	writeJSON(t, root, "third.json", third)
	if got, err := Explain(t.Context(), root, filepath.Join(root, "third.json")); err != nil || got.References[0].Path != "overlay.json" {
		t.Fatalf("intermediate predecessor: %+v %v", got, err)
	}
	// Remove explicit selection: the same chain must resolve by its distinct states.
	def.Catalog.Overlays = nil
	writeJSON(t, root, "definition.json", def)
	got, err = Explain(t.Context(), root, filepath.Join(root, "second.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.References) != 1 || got.References[0].Path != "overlay.json" {
		t.Fatalf("wrong predecessor: %+v", got)
	}
}

type cancelDuringScan struct {
	context.Context
	remaining atomic.Int32
	cancel    context.CancelFunc
}

func (c *cancelDuringScan) Err() error {
	if c.remaining.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestExplainCancellationDuringScan(t *testing.T) {
	root := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	counted := &cancelDuringScan{Context: ctx, cancel: cancel}
	counted.remaining.Store(12)
	got, err := Explain(counted, root, filepath.Join(root, "definition.json"))
	code(t, err, e.CodeTimeout)
	if got.Kind != "" {
		t.Fatalf("partial report: %+v", got)
	}
}

func TestExplainRootBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"malformed neighbor", func(t *testing.T, r string) { write(t, r, "malformed.yaml", []byte("kind: [")) }},
		{"aggregate bytes", func(t *testing.T, r string) {
			for i := 0; i < 9; i++ {
				write(t, r, fmt.Sprintf("large-%d.json", i), []byte(`{"padding":"`+strings.Repeat("x", (4<<20)-32)+`"}`))
			}
		}},
		{"depth", func(t *testing.T, r string) { write(t, r, strings.Repeat("deep/", 18)+"file.txt", nil) }},
		{"entries", func(t *testing.T, r string) {
			for i := 0; i < maxEntries; i++ {
				write(t, r, fmt.Sprintf("entry-%d.txt", i), nil)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixture(t)
			tc.setup(t, r)
			got, err := Explain(t.Context(), r, filepath.Join(r, "definition.json"))
			code(t, err, e.CodeInvalidCatalog)
			if got.Kind != "" {
				t.Fatalf("partial report: %+v", got)
			}
		})
	}
}
