// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type diskPackageKey struct{ name, version, format, architecture string }

const (
	// Property names emitted and consumed by the NVKR inspection report.
	ContentClassProperty        = "nvkr:diskContentClass"
	PackageFormatProperty       = "nvkr:packageFormat"
	PackageArchitectureProperty = "nvkr:packageArchitecture"
	ContainedInProperty         = "nvkr:containedIn"
	ClassFilesystem             = "filesystem"
	ClassContainer              = "container"
	ClassDebianArchive          = "debian-archive"
)

// This class describes the actual observation operation, not installed origin.
// Native scanner fields are projected separately; they cannot author this key.
func withDiskContentClass(components []Component, class string) []Component {
	result := make([]Component, len(components))
	for index, component := range components {
		result[index] = cloneComponent(component)
		result[index].Properties = append(result[index].Properties, Property{Name: ContentClassProperty, Value: class})
		slices.SortFunc(result[index].Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
	}
	return result
}

func diskComponentClass(component Component) (string, error) {
	class := ""
	seen := false
	for _, property := range component.Properties {
		if property.Name != ContentClassProperty {
			continue
		}
		if seen {
			return "", e.New(e.CodeEnvelopeDecode, "disk component has ambiguous observation classes")
		}
		seen = true
		class = property.Value
	}
	switch class {
	case ClassFilesystem, ClassContainer, ClassDebianArchive:
		return class, nil
	case "":
		return "", e.New(e.CodeProvenanceMissing, "disk component has no owner-issued observation class")
	default:
		return "", e.New(e.CodeEnvelopeDecode, "disk component has an unsupported observation class")
	}
}

func diskComponentKey(component Component, platform string) (diskPackageKey, error) {
	key := diskPackageKey{name: component.Name, version: component.Version}
	for _, property := range component.Properties {
		switch property.Name {
		case PackageFormatProperty:
			key.format = property.Value
		case PackageArchitectureProperty:
			key.architecture = property.Value
		}
	}
	if key.name == "" || key.format == "" || (key.format == "deb" && key.version == "") {
		return diskPackageKey{}, e.New(e.CodeProvenanceMissing, "disk package lacks an exact semantic identity")
	}
	if key.format == "deb" && key.architecture != "all" && key.architecture != strings.TrimPrefix(platform, "linux/") {
		return diskPackageKey{}, e.New(e.CodeVerifyFailed, "disk Debian package has a missing or different consumer architecture")
	}
	return key, nil
}

// ComponentClass validates the descriptive operation class in a helper report.
// The host still authenticates the report's sandbox and exact subject binding.
func ComponentClass(component Component) (string, error) { return diskComponentClass(component) }

// ComponentPackageIdentity checks the semantic package fields in a report.
func ComponentPackageIdentity(component Component, platform string) (name, version, format, architecture string, err error) {
	key, err := diskComponentKey(component, platform)
	return key.name, key.version, key.format, key.architecture, err
}
