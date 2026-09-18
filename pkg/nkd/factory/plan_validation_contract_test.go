package factory

import (
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPlanRejectsToolOperationsOutsideSelectedCatalogPins(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan, int, int)
	}{
		{"URL", func(p *Plan, n, _ int) { p.Graph.Nodes[n].URL = "https://attacker.invalid/cosign.tar.gz" }},
		{"archive digest", func(p *Plan, n, _ int) { p.Graph.Nodes[n].Digest = "sha256:" + strings.Repeat("a", 64) }},
		{"executable digest", func(p *Plan, n, _ int) { p.Graph.Nodes[n].ExecutableDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"executable path", func(p *Plan, n, _ int) { p.Graph.Nodes[n].Executable = "bin/other" }},
		{"self-consistent raw replacement", func(p *Plan, n, _ int) {
			node := &p.Graph.Nodes[n]
			node.URL = "https://attacker.invalid/cosign"
			node.ToolFormat = ToolFormatRaw
			node.Digest = "sha256:" + strings.Repeat("c", 64)
			node.ExecutableDigest = node.Digest
		}},
		{"artifact path", func(p *Plan, _, a int) { p.Graph.Artifacts[a].Path = ".nkd/factory/tools/other/cosign" }},
		{"artifact name", func(p *Plan, _, a int) { p.Graph.Artifacts[a].Name = "other" }},
		{"extra acquisition", func(p *Plan, n, a int) {
			node, artifact := p.Graph.Nodes[n], p.Graph.Artifacts[a]
			node.ID, artifact.ID, artifact.Producer = "tool/other", "tool/other", "tool/other"
			node.Outputs = []string{artifact.ID}
			artifact.Name, artifact.Path = "other", ".nkd/factory/tools/other/cosign"
			p.Graph.Nodes = append(p.Graph.Nodes, node)
			p.Graph.Artifacts = append(p.Graph.Artifacts, artifact)
		}},
		{"missing acquisition", func(p *Plan, n, a int) {
			p.Graph.Nodes = slices.Delete(p.Graph.Nodes, n, n+1)
			p.Graph.Artifacts = slices.Delete(p.Graph.Artifacts, a, a+1)
			for i := range p.Graph.Nodes {
				p.Graph.Nodes[i].Inputs = slices.DeleteFunc(p.Graph.Nodes[i].Inputs, func(id string) bool { return id == "tool/cosign" })
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
			n := slices.IndexFunc(plan.Graph.Nodes, func(node Node) bool { return node.ID == "tool/cosign" })
			a := slices.IndexFunc(plan.Graph.Artifacts, func(artifact Artifact) bool { return artifact.ID == "tool/cosign" })
			if n < 0 || a < 0 {
				t.Fatal("fixture omitted the selected cosign tool")
			}
			tc.edit(plan, n, a)
			slices.SortFunc(plan.Graph.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
			slices.SortFunc(plan.Graph.Artifacts, func(a, b Artifact) int { return strings.Compare(a.ID, b.ID) })
			assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
		})
	}
}

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
		{"missing base catalog", func(p *Plan) { p.CatalogSnapshot = nil }, e.CodeUnverifiedRef, "base catalog snapshot"},
		{"substituted base catalog", func(p *Plan) { p.CatalogSnapshot.Metadata.Name = "other" }, e.CodeUnverifiedRef, "catalogDigest"},
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

func TestPlanToolPathsAllowOnlyOwnerRelocation(t *testing.T) {
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	relocated, err := relocateFactoryPlan(plan, ".nkd/custom-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := relocated.IdentityDigest(); err != nil || got != digest {
		t.Fatalf("relocated identity = %s, %v; want %s", got, err, digest)
	}
	raw, err := canonical.Marshal(relocated)
	if err != nil {
		t.Fatal(err)
	}
	var restored Plan
	if err := canonical.UnmarshalExact(raw, &restored); err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t, restored.Validate(), e.CodePipelineDrift)
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
