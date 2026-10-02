package factory

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

const maxHelmValuesBytes = 8 << 20

// renderHelmValues renders the chart component's values templates against the
// selected Environment. The template context is closed: Environment.Name,
// Environment.Publication.RepositoryPrefix (present only when the Environment
// publishes), Arch, Platform and Output. Any other reference fails closed.
func renderHelmValues(environment authored.Environment, build BuildPlan, inputs []string, artifacts map[string]Artifact) ([]HelmValueOverride, error) {
	var spec *catalog.HelmSpec
	for _, id := range inputs {
		input := artifacts[id]
		if input.Kind != "tarball" {
			continue
		}
		for _, component := range build.ResolvedComponents {
			if component.Name == input.Component && component.Helm != nil {
				if spec != nil {
					return nil, e.New(e.CodeInvalidCatalog, "Helm output %q selects more than one chart with values", build.Name)
				}
				spec = component.Helm
			}
		}
	}
	if spec == nil {
		return nil, nil
	}
	_, arch, _ := strings.Cut(build.Platform, "/")
	env := map[string]any{"Name": environment.Metadata.Name}
	if environment.Publication != nil {
		env["Publication"] = map[string]any{"RepositoryPrefix": environment.Publication.RepositoryPrefix}
	}
	data := map[string]any{"Environment": env, "Arch": arch, "Platform": build.Platform, "Output": build.Name}
	overrides := make([]HelmValueOverride, 0, len(spec.Values))
	for _, value := range spec.Values {
		tmpl, err := template.New(value.Path).Option("missingkey=error").Parse(value.Value)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "Helm output %q value %q", build.Name, value.Path)
		}
		var rendered strings.Builder
		if err := tmpl.Execute(&rendered, data); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "Helm output %q value %q requires an Environment field that is not set", build.Name, value.Path)
		}
		overrides = append(overrides, HelmValueOverride{Path: value.Path, Value: rendered.String()})
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Path < overrides[j].Path })
	return overrides, nil
}

// setHelmValues replaces existing scalars only: a typo'd path, a missing
// intermediate mapping, or an attempt to replace a whole mapping is an error.
func setHelmValues(values map[string]any, overrides []HelmValueOverride) error {
	for _, override := range overrides {
		segments := strings.Split(override.Path, ".")
		cursor := values
		for _, segment := range segments[:len(segments)-1] {
			next, ok := cursor[segment].(map[string]any)
			if !ok {
				return e.New(e.CodeInvalidCatalog, "Helm values path %q: %q is not a mapping in the chart values", override.Path, segment)
			}
			cursor = next
		}
		last := segments[len(segments)-1]
		existing, ok := cursor[last]
		if !ok {
			return e.New(e.CodeInvalidCatalog, "Helm values path %q does not exist in the chart values", override.Path)
		}
		if _, isMapping := existing.(map[string]any); isMapping {
			return e.New(e.CodeInvalidCatalog, "Helm values path %q is a mapping, not a scalar", override.Path)
		}
		cursor[last] = override.Value
	}
	return nil
}

// rewriteHelmValues applies overrides to an unbound chart's values.yaml.
func rewriteHelmValues(ctx context.Context, chartRoot string, overrides []HelmValueOverride) error {
	if len(overrides) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "applying Helm values")
	}
	path := filepath.Join(chartRoot, "values.yaml")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxHelmValuesBytes {
		return e.New(e.CodeVerifyFailed, "Helm chart requires a regular values.yaml of at most %d bytes", maxHelmValuesBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Helm values")
	}
	var values map[string]any
	if err := yaml.UnmarshalStrict(raw, &values); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding Helm values")
	}
	if err := setHelmValues(values, overrides); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding Helm values")
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm values")
	}
	return nil
}
