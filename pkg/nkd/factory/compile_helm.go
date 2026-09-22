package factory

import (
	"sort"
	"strings"
	"text/template"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// helmComponentTemplateData is the valueFromComponent template context: a named
// component's resolved build identity, mirroring catalog's own stampData/refData
// field set (pkg/nkd/catalog/resolve.go) so authors can rely on the same {{.Tag}}-style
// tokens they already use in versionStamp/prebuilt templates.
type helmComponentTemplateData struct {
	Tag, ModuleTag, Version, Commit, TreeHash string
}

// resolveHelmValueOverrides renders a Helm package's owning catalog component's
// ValuesConfig into a fully-resolved, sorted override list, once, at compile time.
// A nil/no-values component yields nil (no packaging-time mutation). Rendering
// happens here — not at execute time — so the isolated executor never needs
// catalog access or a template engine, mirroring how buildCommand/artifactPath
// are resolved at compile time from the same ResolvedComponents.
func resolveHelmValueOverrides(plan Plan, artifacts map[string]Artifact, delivery authored.Environment, inputs []string) ([]HelmValueOverride, error) {
	owner, buildComponents, component := findOwningHelmComponent(plan, artifacts, inputs)
	if component == nil || component.Helm == nil || component.Helm.Values == nil {
		return nil, nil
	}
	overrides := make([]HelmValueOverride, 0, len(component.Helm.Values.Overrides))
	for _, override := range component.Helm.Values.Overrides {
		value := override.Value
		switch {
		case override.ValueFromEnvironment != "":
			rendered, err := renderHelmEnvironmentTemplate(owner.Component, override.ValueFromEnvironment, delivery.Publication)
			if err != nil {
				return nil, err
			}
			value = rendered
		case override.ValueFromComponent != nil:
			source := findNamedComponent(buildComponents, override.ValueFromComponent.Component)
			if source == nil {
				return nil, e.New(e.CodeInvalidCatalog,
					"component %q values override references unknown or unbuilt component %q", owner.Component, override.ValueFromComponent.Component)
			}
			rendered, err := renderHelmComponentTemplate(owner.Component, override.ValueFromComponent.Template, *source)
			if err != nil {
				return nil, err
			}
			value = rendered
		}
		overrides = append(overrides, HelmValueOverride{JSONPath: override.JSONPath, Value: value})
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].JSONPath < overrides[j].JSONPath })
	for i := 1; i < len(overrides); i++ {
		if overrides[i].JSONPath == overrides[i-1].JSONPath {
			return nil, e.New(e.CodeInvalidCatalog, "component %q declares values override %q more than once", owner.Component, overrides[i].JSONPath)
		}
	}
	return overrides, nil
}

// findOwningHelmComponent locates the source-archive artifact feeding a Helm
// package and its owning catalog component (with its build target's other
// resolved components, for valueFromComponent cross-references). A component
// with no Helm config (nil result) means the caller applies no override/default.
func findOwningHelmComponent(plan Plan, artifacts map[string]Artifact, inputs []string) (Artifact, []catalog.NamedComponent, *catalog.NamedComponent) {
	var owner Artifact
	for _, id := range inputs {
		if artifact := artifacts[id]; artifact.Kind == "tarball" && artifact.BuildTarget != "" {
			owner = artifact
			break
		}
	}
	if owner.ID == "" {
		return Artifact{}, nil, nil
	}
	var buildComponents []catalog.NamedComponent
	for _, build := range plan.Builds {
		if build.Name == owner.BuildTarget {
			buildComponents = build.ResolvedComponents
			break
		}
	}
	return owner, buildComponents, findNamedComponent(buildComponents, owner.Component)
}

// resolveHelmRepositoryOverride returns the owning catalog component's
// `.helm.repository.name` override, or "" when unset — the caller then
// defaults to the chart's own Chart.yaml name at execute time.
func resolveHelmRepositoryOverride(plan Plan, artifacts map[string]Artifact, inputs []string) string {
	_, _, component := findOwningHelmComponent(plan, artifacts, inputs)
	if component == nil || component.Helm == nil || component.Helm.Repository == nil {
		return ""
	}
	return component.Helm.Repository.Name
}

func findNamedComponent(components []catalog.NamedComponent, name string) *catalog.NamedComponent {
	for i := range components {
		if components[i].Name == name {
			return &components[i]
		}
	}
	return nil
}

func renderHelmComponentTemplate(component, tmpl string, source catalog.NamedComponent) (string, error) {
	data := helmComponentTemplateData{
		Tag: source.Tag, ModuleTag: catalog.StripTagPrefix(source.Tag),
		Version: catalog.NormalizeVersion(source.Tag), Commit: source.Commit, TreeHash: source.TreeHash,
	}
	return renderHelmValueTemplate(component, tmpl, data)
}

func renderHelmEnvironmentTemplate(component, tmpl string, publication *authored.Publication) (string, error) {
	if publication == nil {
		return "", e.New(e.CodeInvalidCatalog,
			"component %q values override references the environment, but this delivery declares no publication", component)
	}
	context := map[string]any{"publication": map[string]any{"repositoryPrefix": publication.RepositoryPrefix}}
	return renderHelmValueTemplate(component, tmpl, context)
}

func renderHelmValueTemplate(component, tmpl string, data any) (string, error) {
	t, err := template.New(component).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "component %q values override template", component)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "component %q values override template", component)
	}
	return b.String(), nil
}
