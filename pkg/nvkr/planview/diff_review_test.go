// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestCompareGraphExecutionFields(t *testing.T) {
	before := inspectPlan(t, comparisonPlan(t, nil))
	after := inspectPlan(t, comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
		env.RunsOn = "ubuntu-24.04"
	}))
	comparison, err := Compare(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Builds) != 0 || comparison.UnchangedBuilds != len(before.plan.Builds) {
		t.Fatalf("runner change altered Build identities: %+v", comparison.Builds)
	}
	found := false
	for _, change := range comparison.Changes {
		if !strings.HasPrefix(change.Path, "/graph/nodes/@id=") || !strings.HasSuffix(change.Path, "/runsOn") {
			continue
		}
		found = true
		if change.Category != "environment and execution" {
			t.Errorf("%s categorized as %q", change.Path, change.Category)
		}
	}
	if !found {
		t.Fatal("missing graph runner changes")
	}
}

func TestComparisonRejectsCopiedGraphExecution(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*factory.Node)
	}{
		{"requirements", func(node *factory.Node) { node.Requirements = &execution.Requirements{TimeoutSeconds: 1} }},
		{"executionBinding", func(node *factory.Node) { node.ExecutionBinding = &execution.Binding{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := comparisonPlan(t, nil)
			for i := range plan.Graph.Nodes {
				if plan.Graph.Nodes[i].Kind != factory.NodeBuild {
					continue
				}
				test.edit(&plan.Graph.Nodes[i])
				doc, err := Inspect(plan)
				if doc != nil {
					t.Fatal("copied producer metadata admitted")
				}
				assertCode(t, err, e.CodePipelineDrift)
				return
			}
			t.Fatal("fixture contains no build node")
		})
	}
}

func TestCategoryUsesSchemaFieldBoundaries(t *testing.T) {
	const producer = "/builds/@name=node-image/executionGraph/nodes/@id=build-image"
	for _, test := range []struct{ path, want string }{
		{"/builds/@name=agent/resolvedComponents", "catalog inputs"},
		{"/builds/@name=agent/resolvedComponents/@Name=containerd", "catalog inputs"},
		{"/builds/@name=agent/catalogSnapshot", "catalog inputs"},
		{"/builds/@name=agent/appliedOverlays", "catalog inputs"},
		{"/catalogSnapshot", "catalog inputs"},
		{"/appliedOverlays", "catalog inputs"},
		{"/graph/nodes", "operation bindings"},
		{"/graph/nodes/@id=build/requirements", "environment and execution"},
		{"/graph/nodes/@id=build/executionBinding", "environment and execution"},
		{"/graph/nodes/@id=build/requirements/timeoutSeconds", "environment and execution"},
		{"/graph/nodes/@id=build/executionBinding/class/runsOn", "environment and execution"},
		{"/graph/artifacts", "artifact bindings"},
		{"/builds/@name=agent/buildDigest", "derived identities"},
		{"/environment/runtime/variables/buildDigest", "environment and execution"},
		{producer + "/packer/variables/buildDigest", "producer instructions"},
		{producer + "/packer/variables/catalogSnapshot", "producer instructions"},
		{producer + "/packer/variables/resolvedComponents", "producer instructions"},
		{producer + "/packer/variables/appliedOverlaysExtra", "producer instructions"},
		{producer + "/packer/materialManifest/kinds/requirements", "producer instructions"},
		{producer + "/packer/materialManifest/kinds/executionBinding", "producer instructions"},
	} {
		t.Run(test.path, func(t *testing.T) {
			if got := category(test.path); got != test.want {
				t.Fatalf("category=%q, want %q", got, test.want)
			}
		})
	}
}

func TestCompareWholeResolvedComponents(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(strconv.FormatBool(removed), func(t *testing.T) {
			left, right := map[string]any{}, map[string]any{"resolvedComponents": []any{map[string]any{"Name": "example"}}}
			if removed {
				left, right = right, left
			}
			comparison := Comparison{}
			comparison.compare("/builds/@name=example", left, true, right, true)
			if len(comparison.Changes) != 1 || comparison.Changes[0].Path != "/builds/@name=example/resolvedComponents" || comparison.Changes[0].Category != "catalog inputs" {
				t.Fatalf("whole collection changes=%+v", comparison.Changes)
			}
			if (comparison.Changes[0].After == nil) != removed {
				t.Fatal("addition/removal was not retained")
			}
		})
	}
}

func TestCompactPreservesUTF8Prefix(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"ASCII", strings.Repeat("a", 200)},
		{"two byte rune crosses boundary", strings.Repeat("a", 138) + "é" + strings.Repeat("z", 50)},
		{"supplementary rune crosses boundary", strings.Repeat("a", 137) + "🙂" + strings.Repeat("z", 50)},
		{"three byte rune ends at boundary", strings.Repeat("a", 136) + "界" + strings.Repeat("z", 50)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			displayed, _, shortened := strings.Cut(compact(raw), " … (")
			if !shortened {
				t.Fatal("long value was not abbreviated")
			}
			prefix, err := strconv.Unquote(displayed)
			if err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(prefix) || !strings.HasPrefix(string(raw), prefix) || len(prefix) > 140 {
				t.Fatalf("invalid abbreviated prefix: %q", prefix)
			}
			if len(prefix) < 140 && utf8.RuneStart(raw[len(prefix)+1]) {
				t.Fatalf("prefix dropped a complete rune: %q", prefix)
			}
			if !strings.Contains(compact(raw), strconv.Itoa(len(raw))+" bytes)") {
				t.Fatal("original byte count lost")
			}
		})
	}
}

func TestComparisonPreservesExactUnicodeValues(t *testing.T) {
	old := strings.Repeat("a", 138) + "é" + strings.Repeat("z", 50)
	changed := old + "after"
	withValue := func(value string) *Document {
		return inspectPlan(t, comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
			env.Runtime.Variables = map[string]string{"message": value}
		}))
	}
	comparison, err := Compare(withValue(old), withValue(changed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := comparison.Render("json")
	if err != nil {
		t.Fatal(err)
	}
	var restored Comparison
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(comparison, &restored) {
		t.Fatal("JSON comparison changed full values")
	}
	for _, render := range []func() ([]byte, error){comparison.Summary, func() ([]byte, error) { return comparison.Render("text") }} {
		text, err := render()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(text), `\xc3`) {
			t.Fatalf("text introduced partial UTF-8 byte: %s", text)
		}
	}
}

func TestComparisonCategoryRulesMatchSchema(t *testing.T) {
	for _, rule := range comparisonCategories {
		t.Run(rule.path, func(t *testing.T) {
			schema := reflect.TypeFor[factory.Plan]()
			for _, segment := range strings.Split(strings.TrimPrefix(rule.path, "/"), "/") {
				for schema.Kind() == reflect.Pointer {
					schema = schema.Elem()
				}
				if schema.Kind() == reflect.Slice {
					if !strings.HasPrefix(segment, "@") || !strings.HasSuffix(segment, "=") {
						t.Fatalf("collection lacks named selector: %s", segment)
					}
					schema = schema.Elem()
					key := strings.TrimSuffix(strings.TrimPrefix(segment, "@"), "=")
					field := serializedFieldType(t, schema, key)
					if field.Kind() != reflect.String {
						t.Fatalf("selector %s is not a string", segment)
					}
					continue
				}
				schema = serializedFieldType(t, schema, segment)
			}
		})
	}
}

func serializedFieldType(t *testing.T, schema reflect.Type, name string) reflect.Type {
	t.Helper()
	if schema.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct at %s", schema, name)
	}
	for i := 0; i < schema.NumField(); i++ {
		field := schema.Field(i)
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == name {
			return field.Type
		}
	}
	t.Fatalf("%s has no serialized field %q", schema, name)
	return nil
}

func TestComparisonEqualAndSharedTextHeader(t *testing.T) {
	before := inspectPlan(t, comparisonPlan(t, nil))
	for _, test := range []struct {
		name   string
		change func(*catalog.Catalog, *definition.Definition, *definition.Environment)
		equal  bool
	}{
		{"same Plan", nil, true},
		{"execution only", func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
			env.RunsOn = "ubuntu-24.04"
		}, false},
		{"logical input", func(cat *catalog.Catalog, _ *definition.Definition, _ *definition.Environment) {
			c := cat.Components["containerd"]
			c.Commit = strings.Repeat("b", 40)
			c.TreeHash = strings.Repeat("c", 40)
			cat.Components["containerd"] = c
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			comparison, err := Compare(before, inspectPlan(t, comparisonPlan(t, test.change)))
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Equal() != test.equal {
				t.Fatalf("Equal()=%t, want %t", comparison.Equal(), test.equal)
			}
			summary, err := comparison.Summary()
			if err != nil {
				t.Fatal(err)
			}
			details, err := comparison.Render("text")
			if err != nil {
				t.Fatal(err)
			}
			summaryHeader, _, _ := strings.Cut(string(summary), "\nChanged fields:")
			detailHeader, _, _ := strings.Cut(string(details), "\nChanged fields:")
			if summaryHeader != detailHeader {
				t.Fatal("text report headers disagree")
			}
			if strings.Contains(summaryHeader, "No changes.") != test.equal {
				t.Fatal("text identity outcome disagrees with Equal")
			}
		})
	}
	var missing *Comparison
	if missing.Equal() {
		t.Fatal("nil comparison reported equality")
	}
}

func TestCompareRuntimeVariableNamesAreNotIdentityFields(t *testing.T) {
	for _, key := range []string{"buildDigest", "catalogDigest", "definitionDigest", "environmentDigest"} {
		t.Run(key, func(t *testing.T) {
			withValue := func(value string) *Document {
				return inspectPlan(t, comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
					env.Runtime.Variables = map[string]string{key: value}
				}))
			}
			comparison, err := Compare(withValue("before"), withValue("after"))
			if err != nil {
				t.Fatal(err)
			}
			if len(comparison.Builds) != 0 {
				t.Fatal("runtime variable changed logical Build identity")
			}
			path := "/environment/runtime/variables/" + key
			for _, change := range comparison.Changes {
				if change.Path != path {
					continue
				}
				if change.Category != "environment and execution" {
					t.Fatalf("runtime variable %s categorized as %q", key, change.Category)
				}
				return
			}
			t.Fatalf("missing runtime variable path %s", path)
		})
	}
}
