package catalog

import (
	"strings"
	"testing"
)

func valuesComponent(config *ValuesConfig) Component {
	component := goodComponent()
	component.Helm = &HelmConfig{Values: config}
	return component
}

func TestValuesConfigValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		config   *ValuesConfig
		wantText string
	}{
		{
			name: "valid literal",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.repository", Value: "docker.io/library/postgres"},
			}},
		},
		{
			name: "valid environment reference",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.registry", ValueFromEnvironment: "{{.publication.repositoryPrefix}}"},
			}},
		},
		{
			name: "valid component reference",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.tag", ValueFromComponent: &ComponentValueRef{Component: "postgres", Template: "{{.Tag}}"}},
			}},
		},
		{
			name:     "no overrides",
			config:   &ValuesConfig{},
			wantText: "requires at least one override",
		},
		{
			name: "missing jsonpath",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{Value: "x"},
			}},
			wantText: "invalid jsonpath",
		},
		{
			name: "jsonpath without leading dot",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: "image.tag", Value: "x"},
			}},
			wantText: "invalid jsonpath",
		},
		{
			name: "no value source",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.tag"},
			}},
			wantText: "requires exactly one of value, valueFromEnvironment, or valueFromComponent",
		},
		{
			name: "ambiguous value sources",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.tag", Value: "x", ValueFromEnvironment: "{{.y}}"},
			}},
			wantText: "requires exactly one of value, valueFromEnvironment, or valueFromComponent",
		},
		{
			name: "valueFromComponent missing component",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.tag", ValueFromComponent: &ComponentValueRef{Template: "{{.Tag}}"}},
			}},
			wantText: "requires a component and template",
		},
		{
			name: "valueFromComponent missing template",
			config: &ValuesConfig{Overrides: []ValueOverride{
				{JSONPath: ".image.tag", ValueFromComponent: &ComponentValueRef{Component: "postgres"}},
			}},
			wantText: "requires a component and template",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := base(valuesComponent(test.config)).Validate()
			if test.wantText == "" {
				if err != nil {
					t.Fatalf("expected valid values config: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestValuesConfigRequiresFromSourceComponent(t *testing.T) {
	component := Component{Platforms: []string{"linux/amd64"}, ExclusionReason: "not distributed", Helm: &HelmConfig{Values: &ValuesConfig{Overrides: []ValueOverride{
		{JSONPath: ".image.tag", Value: "x"},
	}}}}
	err := base(component).Validate()
	if err == nil || !strings.Contains(err.Error(), "helm config requires a from-source component") {
		t.Fatalf("error = %v, want from-source requirement", err)
	}
}

func TestHelmConfigRequiresValues(t *testing.T) {
	component := goodComponent()
	component.Helm = &HelmConfig{}
	err := base(component).Validate()
	if err == nil || !strings.Contains(err.Error(), "helm config requires values") {
		t.Fatalf("error = %v, want helm-requires-values", err)
	}
}

func TestHelmConfigAllowsRepositoryAloneWithoutValues(t *testing.T) {
	component := goodComponent()
	component.Helm = &HelmConfig{Repository: &RepositoryConfig{Name: "postgresql"}}
	if err := base(component).Validate(); err != nil {
		t.Fatalf("expected a repository-only helm config to validate: %v", err)
	}
}

func TestHelmConfigRejectsEmptyRepositoryName(t *testing.T) {
	component := goodComponent()
	component.Helm = &HelmConfig{Repository: &RepositoryConfig{Name: "  "}}
	err := base(component).Validate()
	if err == nil || !strings.Contains(err.Error(), "helm repository override requires a name") {
		t.Fatalf("error = %v, want repository-name requirement", err)
	}
}
