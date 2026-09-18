package factory

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPlanRejectsSubstitutedInputsAndBuildBindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Plan)
		code    e.Code
		message string
	}{
		{"missing generator", func(p *Plan) { p.GeneratorVersion = " " }, e.CodeInvalidCatalog, "generator"},
		{"missing environment path", func(p *Plan) { p.EnvironmentPath = "" }, e.CodeInvalidCatalog, "source paths"},
		{"escaping definition path", func(p *Plan) { p.DefinitionPath = "../definition.yaml" }, e.CodeInvalidCatalog, "source paths"},

		{"bad definition identity", func(p *Plan) { p.DefinitionDigest = "latest" }, e.CodeUnverifiedRef, "definitionDigest"},
		{"bad environment identity", func(p *Plan) { p.EnvironmentDigest = "latest" }, e.CodeUnverifiedRef, "environmentDigest"},
		{"partial lifecycle identity", func(p *Plan) { p.DefinitionDigest = "sha256:" + strings.Repeat("a", 64); p.EnvironmentDigest = "" }, e.CodeUnverifiedRef, "definitionDigest"},
		{"bad catalog identity", func(p *Plan) { p.CatalogDigest = "latest" }, e.CodeUnpinnedSource, "catalog identity"},
		{"substituted environment", func(p *Plan) { p.Environment.RunsOn = "ubuntu-22.04" }, e.CodeUnverifiedRef, "environmentDigest"},
		{"empty builds", func(p *Plan) { p.Builds = nil }, e.CodeUnverifiedRef, "output slots"},
		{"duplicate build", func(p *Plan) { p.Builds = append(p.Builds, p.Builds[0]) }, e.CodeUnverifiedRef, "output slots"},
		{"missing build graph", func(p *Plan) { p.Builds[0].Graph = nil }, e.CodeInvalidCatalog, "invalid"},
		{"missing build catalog", func(p *Plan) { p.Builds[0].CatalogDigest = "" }, e.CodeUnpinnedSource, "catalog digest"},
		{"substituted build identity", func(p *Plan) { p.Builds[0].BuildDigest = "sha256:" + strings.Repeat("a", 64) }, e.CodeUnverifiedRef, "graph identity"},
		{"substituted build ID", func(p *Plan) { p.Builds[0].Platform = "linux/arm64" }, e.CodeUnverifiedRef, "graph identity"},
		{"empty selection", func(p *Plan) { p.Builds[0].Components = nil }, e.CodeInvalidCatalog, "selects no components"},
		{"unordered selection", func(p *Plan) { p.Builds[0].Components = []string{"z", "a"} }, e.CodeInvalidCatalog, "not canonical"},
		{"duplicate selection", func(p *Plan) {
			components := p.Builds[0].Components
			p.Builds[0].Components = append([]string{components[0]}, components...)
		}, e.CodeInvalidCatalog, "not canonical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := buildOnlyPlan(t, DefaultRunsOn)
			if err := plan.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			tc.edit(plan)
			err := plan.Validate()
			assertFactoryErrorCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong rejection boundary: %v; want %q", err, tc.message)
			}
		})
	}
}

func TestPlanRejectsStaleRuntimeAfterEnvironmentIdentityChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan)
	}{
		{"runner", func(plan *Plan) { plan.Environment.RunsOn = "ubuntu-24.04" }},
		{"utility container", func(plan *Plan) { plan.Environment.UtilityContainer = "golang@sha256:" + strings.Repeat("a", 64) }},
		{"utility platform", func(plan *Plan) { plan.Environment.UtilityPlatform = "linux/arm64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := buildOnlyPlan(t, DefaultRunsOn)
			tc.edit(plan)
			digest, err := plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest = digest
			assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
		})
	}
}
