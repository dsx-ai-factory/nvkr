// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"slices"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// Producer modes reported by component discovery.
const (
	ProducerSource     = "source"
	ProducerPrebuilt   = "prebuilt"
	ProducerPinnedTool = "pinned-tool"
	ProducerExcluded   = "excluded"
)

// Discovery is the deterministic, descriptive projection of one catalog
// component. Platforms are the catalog-declared targets, not a statement about
// the current host or about a successful build.
type Discovery struct {
	Name         string   `json:"name"`
	Platforms    []string `json:"platforms"`
	OutputType   string   `json:"outputType"`
	ProducerMode string   `json:"producerMode"`
}

// ValidateOutputType admits only output types a catalog can declare.
func ValidateOutputType(outputType string) error {
	if knownOutputSet[outputType] || outputType == PinnedToolOutputFile || outputType == PinnedToolOutputOCILayout {
		return nil
	}
	return e.New(e.CodeUsage, "output type %q is not one of disk-image, file, host-package, oci-image, oci-layout, raw, tarball", outputType)
}

// Discover lists the components matching every nonempty selector, sorted by
// name. The catalog is already fully validated by loading, so filtering never
// hides an invalid component. Matching is exact, never a prefix or wildcard.
func (c *Catalog) Discover(platform, outputType string) ([]Discovery, error) {
	if platform != "" {
		if target.ValidatePlatform(platform) != nil {
			return nil, e.New(e.CodeUsage, "--platform %q must be linux/amd64 or linux/arm64", platform)
		}
	}
	if outputType != "" {
		if err := ValidateOutputType(outputType); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(c.Components))
	for name := range c.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	found := make([]Discovery, 0, len(names))
	for _, name := range names {
		component := c.Components[name]
		if platform != "" && !slices.Contains(component.Platforms, platform) {
			continue
		}
		if outputType != "" && component.OutputType != outputType {
			continue
		}
		found = append(found, Discovery{
			Name:         name,
			Platforms:    append([]string{}, component.Platforms...),
			OutputType:   component.OutputType,
			ProducerMode: component.producerMode(),
		})
	}
	return found, nil
}

func (c Component) producerMode() string {
	switch {
	case c.BuilderType == BuilderPinnedTool:
		return ProducerPinnedTool
	case c.FromSource:
		return ProducerSource
	case c.Prebuilt != nil:
		return ProducerPrebuilt
	default:
		return ProducerExcluded
	}
}
