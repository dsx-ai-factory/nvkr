package factory

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCompiledHelmOverridesSurviveRehydrationAndRejectDrift(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["runc"]
	component.OutputType = "tarball"
	component.ArtifactPath = filepath.ToSlash(filepath.Join(component.WriteDir, "chart.tar"))
	component.Helm = &catalog.HelmConfig{
		Repository: &catalog.RepositoryConfig{Name: "published-chart"},
		Values: &catalog.ValuesConfig{Overrides: []catalog.ValueOverride{
			{JSONPath: ".image.tag", ValueFromComponent: &catalog.ComponentValueRef{Component: "runc", Template: "{{.Tag}}"}},
			{JSONPath: ".image.registry", ValueFromEnvironment: "{{.publication.repositoryPrefix}}"},
		}},
	}
	base.Components["runc"] = component
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("helm-overrides", "chart", "ubuntu-24.04")
	definition.Outputs[0].Format = authored.OutputHelm
	definition.Outputs[0].Components = []string{"runc"}
	environment.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example", Anonymous: true}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	compiled := routeContractNode(t, plan, "package/chart")
	wantValues := []HelmValueOverride{
		{JSONPath: ".image.registry", Value: "ghcr.io/example"},
		{JSONPath: ".image.tag", Value: component.Tag},
	}
	if compiled.HelmRepository != "published-chart" || !reflect.DeepEqual(compiled.HelmValues, wantValues) {
		t.Fatalf("compiled Helm configuration = %#v", compiled)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Node)
	}{
		{name: "unchanged"},
		{name: "changed value", mutate: func(node *Node) { node.HelmValues[0].Value = "ghcr.io/other" }},
		{name: "removed values", mutate: func(node *Node) { node.HelmValues = nil }},
		{name: "changed repository", mutate: func(node *Node) { node.HelmRepository = "other-chart" }},
		{name: "removed repository", mutate: func(node *Node) { node.HelmRepository = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var restored Plan
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(routeContractNode(t, &restored, "package/chart"))
				assertFactoryErrorCode(t, restored.Validate(), e.CodePipelineDrift)
			} else if err := restored.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func helmValuesTestPlan(postgres catalog.Component) Plan {
	return Plan{Builds: []BuildPlan{{Name: "helm-test", ResolvedComponents: []catalog.NamedComponent{
		{Name: "postgres", Component: postgres},
	}}}}
}

func TestResolveHelmValueOverridesRendersAndSortsBySource(t *testing.T) {
	postgres := catalog.Component{FromSource: true, Tag: "postgresql/17.1.2", Commit: "c", Helm: &catalog.HelmConfig{Values: &catalog.ValuesConfig{
		Overrides: []catalog.ValueOverride{
			{JSONPath: ".image.tag", ValueFromComponent: &catalog.ComponentValueRef{Component: "postgres", Template: "{{.ModuleTag}}"}},
			{JSONPath: ".image.repository", Value: "docker.io/library/postgres"},
			{JSONPath: ".image.registry", ValueFromEnvironment: "{{.publication.repositoryPrefix}}"},
		},
	}}}
	plan := helmValuesTestPlan(postgres)
	artifacts := map[string]Artifact{
		"artifact/postgres": {ID: "artifact/postgres", Kind: "tarball", BuildTarget: "helm-test", Component: "postgres"},
	}
	delivery := authored.Environment{Publication: &authored.Publication{RepositoryPrefix: "ghcr.io/example"}}
	overrides, err := resolveHelmValueOverrides(plan, artifacts, delivery, []string{"artifact/postgres"})
	if err != nil {
		t.Fatal(err)
	}
	want := []HelmValueOverride{
		{JSONPath: ".image.registry", Value: "ghcr.io/example"},
		{JSONPath: ".image.repository", Value: "docker.io/library/postgres"},
		{JSONPath: ".image.tag", Value: "17.1.2"},
	}
	if len(overrides) != len(want) {
		t.Fatalf("overrides = %#v", overrides)
	}
	for i, o := range overrides {
		if o != want[i] {
			t.Fatalf("overrides[%d] = %#v, want %#v", i, o, want[i])
		}
	}
}

func TestResolveHelmValueOverridesNoValuesIsANoop(t *testing.T) {
	plan := helmValuesTestPlan(catalog.Component{FromSource: true})
	artifacts := map[string]Artifact{"artifact/postgres": {ID: "artifact/postgres", Kind: "tarball", BuildTarget: "helm-test", Component: "postgres"}}
	overrides, err := resolveHelmValueOverrides(plan, artifacts, authored.Environment{}, []string{"artifact/postgres"})
	if err != nil || overrides != nil {
		t.Fatalf("overrides = %#v, err = %v", overrides, err)
	}
}

func TestResolveHelmRepositoryOverride(t *testing.T) {
	artifacts := map[string]Artifact{"artifact/postgres": {ID: "artifact/postgres", Kind: "tarball", BuildTarget: "helm-test", Component: "postgres"}}
	inputs := []string{"artifact/postgres"}

	t.Run("no helm config", func(t *testing.T) {
		plan := helmValuesTestPlan(catalog.Component{FromSource: true})
		if got := resolveHelmRepositoryOverride(plan, artifacts, inputs); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("values only, no repository override", func(t *testing.T) {
		plan := helmValuesTestPlan(catalog.Component{FromSource: true, Helm: &catalog.HelmConfig{Values: &catalog.ValuesConfig{
			Overrides: []catalog.ValueOverride{{JSONPath: ".image.tag", Value: "x"}},
		}}})
		if got := resolveHelmRepositoryOverride(plan, artifacts, inputs); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
	t.Run("repository override set", func(t *testing.T) {
		plan := helmValuesTestPlan(catalog.Component{FromSource: true, Helm: &catalog.HelmConfig{Repository: &catalog.RepositoryConfig{Name: "postgresql"}}})
		if got := resolveHelmRepositoryOverride(plan, artifacts, inputs); got != "postgresql" {
			t.Fatalf("got %q, want %q", got, "postgresql")
		}
	})
}

func TestResolveHelmValueOverridesFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   *catalog.ValuesConfig
		delivery authored.Environment
	}{
		{
			name: "missing publication",
			values: &catalog.ValuesConfig{Overrides: []catalog.ValueOverride{
				{JSONPath: ".image.registry", ValueFromEnvironment: "{{.publication.repositoryPrefix}}"},
			}},
		},
		{
			name: "unknown missingkey",
			values: &catalog.ValuesConfig{Overrides: []catalog.ValueOverride{
				{JSONPath: ".image.registry", ValueFromEnvironment: "{{.publication.unknownField}}"},
			}},
			delivery: authored.Environment{Publication: &authored.Publication{RepositoryPrefix: "ghcr.io/example"}},
		},
		{
			name: "unknown component reference",
			values: &catalog.ValuesConfig{Overrides: []catalog.ValueOverride{
				{JSONPath: ".image.tag", ValueFromComponent: &catalog.ComponentValueRef{Component: "missing", Template: "{{.Tag}}"}},
			}},
		},
		{
			name: "duplicate jsonpath",
			values: &catalog.ValuesConfig{Overrides: []catalog.ValueOverride{
				{JSONPath: ".image.tag", Value: "a"},
				{JSONPath: ".image.tag", Value: "b"},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := helmValuesTestPlan(catalog.Component{FromSource: true, Tag: "postgresql/17.1.2", Helm: &catalog.HelmConfig{Values: test.values}})
			artifacts := map[string]Artifact{"artifact/postgres": {ID: "artifact/postgres", Kind: "tarball", BuildTarget: "helm-test", Component: "postgres"}}
			_, err := resolveHelmValueOverrides(plan, artifacts, test.delivery, []string{"artifact/postgres"})
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		})
	}
}
