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
		{"bad compile identity", func(p *Plan) { p.CompileSpecDigest = "latest" }, e.CodeUnpinnedSource, "compile-spec"},
		{"bad definition identity", func(p *Plan) { p.DefinitionDigest = "latest" }, e.CodeUnverifiedRef, "definitionDigest"},
		{"bad environment identity", func(p *Plan) { p.EnvironmentDigest = "latest" }, e.CodeUnverifiedRef, "environmentDigest"},
		{"partial lifecycle identity", func(p *Plan) { p.DefinitionDigest = "sha256:" + strings.Repeat("a", 64); p.EnvironmentDigest = "" }, e.CodeUnverifiedRef, "together"},
		{"bad catalog identity", func(p *Plan) { p.CatalogDigest = "latest" }, e.CodeUnpinnedSource, "catalog/execution-settings"},
		{"substituted environment", func(p *Plan) { p.ExecutionSettings.RunsOn = "ubuntu-22.04" }, e.CodeUnverifiedRef, "executionSettingsDigest"},
		{"empty builds", func(p *Plan) { p.Builds = nil }, e.CodeInvalidCatalog, "no builds"},
		{"duplicate build", func(p *Plan) { p.Builds = append(p.Builds, p.Builds[0]) }, e.CodeInvalidCatalog, "repeated"},
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
