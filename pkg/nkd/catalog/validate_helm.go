package catalog

import (
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// validValuesJSONPath admits only a leading-dot path whose dotted segments are
// all non-empty. An empty leading, intermediate, or trailing segment (".", "..",
// ".a..b", ".a.") would otherwise reach the package-time resolver and address an
// empty key in values.yaml instead of failing closed at catalog load.
func validValuesJSONPath(path string) bool {
	if !strings.HasPrefix(path, ".") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "."), ".")
	for _, segment := range segments {
		if segment == "" {
			return false
		}
	}
	return true
}

// validateValuesConfig requires each override to name a JSONPath and exactly one
// value source, so a package-time resolver never has to guess authorial intent
// or silently prefer one source over another.
func validateValuesConfig(name string, config *ValuesConfig) error {
	if len(config.Overrides) == 0 {
		return e.New(e.CodeInvalidCatalog, "component %q: values requires at least one override", name)
	}
	for i, override := range config.Overrides {
		if !validValuesJSONPath(override.JSONPath) {
			return e.New(e.CodeInvalidCatalog, "component %q: values override[%d] has an invalid jsonpath %q", name, i, override.JSONPath)
		}
		sources := 0
		if override.Value != "" {
			sources++
		}
		if override.ValueFromEnvironment != "" {
			sources++
		}
		if override.ValueFromComponent != nil {
			sources++
			if override.ValueFromComponent.Component == "" || override.ValueFromComponent.Template == "" {
				return e.New(e.CodeInvalidCatalog,
					"component %q: values override[%d] valueFromComponent requires a component and template", name, i)
			}
		}
		if sources != 1 {
			return e.New(e.CodeInvalidCatalog,
				"component %q: values override[%d] requires exactly one of value, valueFromEnvironment, or valueFromComponent", name, i)
		}
	}
	return nil
}
