package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"sigs.k8s.io/yaml"
)

const (
	maxHelmValuesBytes   = 8 << 20
	maxHelmRenderedValue = 4 << 10
)

// errHelmValueTooLarge aborts template execution once a rendered value exceeds
// maxHelmRenderedValue.
var errHelmValueTooLarge = errors.New("rendered value is too large")

type boundedWriter struct{ buf strings.Builder }

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > maxHelmRenderedValue {
		return 0, errHelmValueTooLarge
	}
	return w.buf.Write(p)
}

// chartHelmSpec returns the single helm.values declaration among the tarball
// inputs, or nil when none declares one.
func chartHelmSpec(build BuildPlan, inputs []string, artifacts map[string]Artifact) (*catalog.HelmSpec, error) {
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
	return spec, nil
}

// renderHelmValues renders the chart component's values templates against the
// selected Environment. The template context is closed:
// Environment.Publication.RepositoryPrefix (present only when the Environment
// publishes), Arch, Platform and Output. Any other reference fails closed.
func renderHelmValues(environment authored.Environment, build BuildPlan, inputs []string, artifacts map[string]Artifact) ([]HelmValueOverride, error) {
	spec, err := chartHelmSpec(build, inputs, artifacts)
	if err != nil || spec == nil {
		return nil, err
	}
	arch := target.Arch(build.Platform)
	if arch == "" {
		return nil, e.New(e.CodeInvalidCatalog, "Helm output %q has unsupported platform %q", build.Name, build.Platform)
	}
	env := map[string]any{}
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
		var rendered boundedWriter
		if err := tmpl.Execute(&rendered, data); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "Helm output %q value %q requires an Environment field that is not set or renders too large", build.Name, value.Path)
		}
		text := rendered.buf.String()
		if text == "" || strings.ContainsFunc(text, unicode.IsControl) {
			return nil, e.New(e.CodeInvalidCatalog, "Helm output %q value %q must render non-empty text without control characters", build.Name, value.Path)
		}
		overrides = append(overrides, HelmValueOverride{Path: value.Path, Value: text})
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Path < overrides[j].Path })
	return overrides, nil
}

// setHelmValues replaces existing string scalars only: a typo'd path, a missing
// intermediate mapping, or a non-string target (mapping, sequence, bool, number,
// null) is an error rather than a silent type change.
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
		if _, isString := existing.(string); !isString {
			return e.New(e.CodeInvalidCatalog, "Helm values path %q is %T in the chart values, not a string scalar", override.Path, existing)
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
