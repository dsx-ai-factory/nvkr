// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// maintainedPlan loads the recorded M2 Plan independently of workflow selection.
func maintainedPlan(t *testing.T) *factory.Plan {
	t.Helper()
	plan, err := factory.LoadPlanContext(t.Context(), "../../../testdata/plans/nvke-m2.json")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func inspectPlan(t *testing.T, plan *factory.Plan) *Document {
	t.Helper()
	doc, err := Inspect(plan)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
func assertCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v, want %s", err, want)
	}
}

func TestInspectionPreservesCanonicalIdentityAndOwnsSnapshot(t *testing.T) {
	plan := maintainedPlan(t)
	original, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	doc := inspectPlan(t, plan)
	for _, test := range []struct {
		name, format string
		pretty       bool
	}{{"canonical", "json", false}, {"pretty", "json", true}, {"text", "text", false}} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := doc.Render(PlanOptions{Format: test.format, Pretty: test.pretty})
			if err != nil {
				t.Fatal(err)
			}
			again, err := doc.Render(PlanOptions{Format: test.format, Pretty: test.pretty})
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatalf("unstable view: %v", err)
			}
			if test.format == "json" {
				if !test.pretty && !bytes.Equal(raw, original) {
					t.Fatal("canonical output differs from stored bytes")
				}
				var decoded factory.Plan
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				digest, err := decoded.Digest()
				if err != nil || digest != doc.digest {
					t.Fatalf("view changed subject: %s %v", digest, err)
				}
				if test.pretty && canonical.UnmarshalExact(raw, &decoded) == nil {
					t.Fatal("pretty view became an accepted canonical Plan")
				}
			} else {
				for _, want := range []string{"Producer instructions", "Artifact operations", "Publication routes", "controller-chart", "cwe", "argv:", "image bindings:", "no runtime observations"} {
					if !strings.Contains(string(raw), want) {
						t.Fatalf("view omits %q", want)
					}
				}
			}
		})
	}
	plan.Builds[0].Graph.Nodes[0].Kind = "substituted"
	plan.Definition.Outputs[0].Name = "substituted"
	copyBytes, _ := doc.Render(PlanOptions{Format: "json"})
	copyBytes[0] = '!'
	still, _ := doc.Render(PlanOptions{Format: "json"})
	if !bytes.Equal(still, original) {
		t.Fatal("caller changed retained snapshot")
	}
}

func TestInspectionRejectsInvalidCompletePlans(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*factory.Plan)
		code   e.Code
	}{
		{"stale definition", func(p *factory.Plan) { p.Definition.Metadata.Description = "replaced" }, e.CodeUnverifiedRef},
		{"dangling edge", func(p *factory.Plan) { p.Graph.Nodes[0].Inputs = []string{"absent"} }, e.CodePipelineDrift},
		{"producer substitution", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Command = []string{"arbitrary"} }, e.CodeUnverifiedRef},
		{"route substitution", func(p *factory.Plan) {
			target := p.Targets["release"]
			target.Ref = "example.invalid/release"
			p.Targets["release"] = target
		}, e.CodeUnverifiedRef},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := maintainedPlan(t)
			test.mutate(p)
			doc, err := Inspect(p)
			if doc != nil {
				t.Fatal("invalid plan produced a view")
			}
			assertCode(t, err, test.code)
		})
	}
	doc, err := Inspect(nil)
	if doc != nil {
		t.Fatal("nil plan produced a view")
	}
	assertCode(t, err, e.CodeUsage)
}

func TestLoadRejectsAlternateBytesAndUnsafeFiles(t *testing.T) {
	doc := inspectPlan(t, maintainedPlan(t))
	pretty, _ := doc.Render(PlanOptions{Format: "json", Pretty: true})
	for _, test := range []struct {
		name    string
		raw     []byte
		symlink bool
	}{
		{"pretty", pretty, false}, {"trailing newline", append(bytes.Clone(doc.raw), '\n'), false}, {"unknown version", bytes.ReplaceAll(doc.raw, []byte(factory.PlanSchemaVersion), []byte("nvkr.dev/run-plan/v99")), false},
		{"unknown field", append([]byte(`{"unknown":true,`), doc.raw[1:]...), false}, {"duplicate key", append([]byte(`{"name":"duplicate",`), doc.raw[1:]...), false},
		{"symlink", doc.raw, true}, {"oversize", bytes.Repeat([]byte(" "), int(factory.MaxPlanBytes+1)), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.symlink {
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			got, err := Load(t.Context(), path)
			if got != nil {
				t.Fatal("unsafe input emitted view")
			}
			want := e.CodePipelineDrift
			if test.name == "unknown version" {
				want = e.CodeInvalidCatalog
			}
			assertCode(t, err, want)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Load(ctx, "missing.json")
	assertCode(t, err, e.CodeTimeout)
}

func TestViewOptionsFailClosed(t *testing.T) {
	doc := inspectPlan(t, maintainedPlan(t))
	for _, options := range []PlanOptions{{Format: "yaml"}, {Format: "text", Pretty: true}} {
		raw, err := doc.Render(options)
		if raw != nil {
			t.Fatal("invalid format emitted bytes")
		}
		assertCode(t, err, e.CodeUsage)
	}
	for _, options := range []GraphOptions{{View: "missing", Format: "json"}, {View: "complete", Format: "svg"}} {
		raw, err := doc.Graph(options)
		if raw != nil {
			t.Fatal("invalid graph emitted bytes")
		}
		assertCode(t, err, e.CodeUsage)
	}
	for _, doc := range []*Document{nil, {}} {
		_, err := doc.Render(PlanOptions{Format: "json"})
		assertCode(t, err, e.CodeUsage)
		_, err = doc.Graph(GraphOptions{View: "complete", Format: "json"})
		assertCode(t, err, e.CodeUsage)
	}
}

func comparisonPlan(t *testing.T, change func(*catalog.Catalog, *definition.Definition, *definition.Environment)) *factory.Plan {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	def := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: "view-test"}, Catalog: definition.Catalog{Base: "catalog"}, Outputs: []definition.DefinitionOutput{
		{Name: "agent", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"containerd"}},
		{Name: "utility", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"crictl"}},
	}}
	environment := definition.Environment{APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind, Metadata: definition.Metadata{Name: "test"}, RunsOn: "ubuntu-latest", UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64"}
	if change != nil {
		change(base, &def, &environment)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), def, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
