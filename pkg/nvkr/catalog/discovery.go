// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"slices"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// Producer modes reported by component discovery. "acquired" is a component
// delivered from a prebuilt URL or image (the authored "prebuilt" lane).
const (
	ProducerSource     = "source"
	ProducerAcquired   = "acquired"
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
	types := []string{PinnedToolOutputFile, PinnedToolOutputOCILayout}
	for name := range knownOutputSet {
		types = append(types, name)
	}
	sort.Strings(types)
	return e.New(e.CodeUsage, "output type %q is not one of %s", outputType, strings.Join(types, ", "))
}

// ValidatePlatform admits only platforms a catalog can declare: the container
// targets and the darwin/arm64 executable the loader also accepts.
func ValidatePlatform(platform string) error {
	if target.ValidateExecutablePlatform(platform) == nil {
		return nil
	}
	return e.New(e.CodeUsage, "platform %q is not one of linux/amd64, linux/arm64, darwin/arm64", platform)
}

// ValidateSelectors checks the discovery selectors without a catalog. Empty
// means no selector.
func ValidateSelectors(platform, outputType string) error {
	if platform != "" {
		if err := ValidatePlatform(platform); err != nil {
			return err
		}
	}
	if outputType != "" {
		return ValidateOutputType(outputType)
	}
	return nil
}

// Discover lists the components matching every nonempty selector, sorted by
// name. The catalog is already fully validated by loading, so filtering never
// hides an invalid component. Matching is exact, never a prefix or wildcard.
func (c *Catalog) Discover(platform, outputType string) ([]Discovery, error) {
	if err := ValidateSelectors(platform, outputType); err != nil {
		return nil, err
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
		return ProducerAcquired
	default:
		return ProducerExcluded
	}
}
