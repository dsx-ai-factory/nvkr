package factory

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

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
