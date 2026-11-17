// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"maps"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BuildToolToken denotes the executable supplied by the producer's explicit tool
// binding. It never searches PATH or selects a tool from the Environment.
const BuildToolToken = "{{.BuildTool}}"

func resolveBuildTool(name string, c *Component, sources map[string]Source, lock *PinLock) error {
	if len(c.BuildTool) == 0 {
		return nil
	}
	c.BuildToolPins = make(map[string]ToolPin, len(c.BuildTool))
	for _, platform := range slices.Sorted(maps.Keys(c.BuildTool)) {
		sourceName := c.BuildTool[platform]
		source, exists := sources[sourceName]
		if !exists || source.Tool == nil {
			return e.New(e.CodeUnpinnedSource, "component %q build tool %q lacks its source declaration", name, sourceName)
		}
		pin, exists := lock.Pins[sourceName]
		if !exists || pin.Tool == nil && len(pin.Tools) == 0 {
			return e.New(e.CodeUnpinnedSource, "component %q build tool %q lacks its lock pin", name, sourceName)
		}
		if err := checkToolPin(source.Tool, pin); err != nil {
			return err
		}
		tool, err := selectToolPin(pin, platform)
		if err != nil {
			return err
		}
		c.BuildToolPins[platform] = tool
	}
	return validateBuildTool(name, *c)
}

func validateBuildTool(name string, c Component) error {
	selected := len(c.BuildTool) != 0
	if !selected && len(c.BuildToolPins) != 0 {
		return e.New(e.CodeInvalidCatalog, "component %q has build-tool pins without a selection", name)
	}
	if selected {
		if !c.FromSource || (c.BuilderType != "" && c.BuilderType != BuilderOCIHermetic) ||
			(c.BuildNetwork != "" && c.BuildNetwork != "none") || c.WriteDir == "" {
			return e.New(e.CodeEngineCapability, "component %q build tool requires an isolated offline OCI source build", name)
		}
		if len(c.BuildTool) != len(c.Platforms) || len(c.BuildToolPins) != len(c.Platforms) {
			return e.New(e.CodeUnpinnedSource, "component %q build tool requires exactly its native platform pins", name)
		}
		for _, platform := range slices.Sorted(maps.Keys(c.BuildTool)) {
			source := c.BuildTool[platform]
			if source == "" || !slices.Contains(c.Platforms, platform) || (platform != "linux/amd64" && platform != "linux/arm64") {
				return e.New(e.CodeInvalidCatalog, "component %q has an unsupported build-tool selection for %q", name, platform)
			}
			pin, exists := c.BuildToolPins[platform]
			if !exists || pin.Platform != platform {
				return e.New(e.CodeUnpinnedSource, "component %q lacks its exact %s build-tool pin", name, platform)
			}
			if err := validateToolPin(pin); err != nil {
				return err
			}
			if len(pin.Companions) != 0 || (pin.Format != "raw" && pin.Format != "tar-gzip") {
				return e.New(e.CodeEngineCapability, "component %q build tool requires one raw or tar-gzip executable without companions", name)
			}
		}
	}
	for _, platform := range c.Platforms {
		command := c.BuildCommand
		if override, exists := c.BuildCommandByPlatform[platform]; exists {
			command = override
		}
		used := false
		for _, argument := range command {
			used = used || strings.Contains(argument, BuildToolToken)
		}
		if used != selected {
			return e.New(e.CodeInvalidCatalog, "component %q %s command must use exactly its declared build-tool binding", name, platform)
		}
	}
	return nil
}
