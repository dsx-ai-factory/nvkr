// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func (source ToolSource) supportsPlatform(platform string) bool {
	return source.Platform == platform || slices.Contains(source.Platforms, platform)
}

// platformSource expands only platform tokens. Version tokens retain the existing
// release resolver and a single-platform declaration retains its original bytes.
func platformSource(source ToolSource, platform string) ToolSource {
	os, arch, _ := strings.Cut(platform, "/")
	replacer := strings.NewReplacer("{{.OS}}", os, "{{.Arch}}", arch)
	source.Platform, source.Platforms = platform, nil
	source.URL = replacer.Replace(source.URL)
	source.ExecutablePath = replacer.Replace(source.ExecutablePath)
	source.Companions = slices.Clone(source.Companions)
	for i := range source.Companions {
		source.Companions[i] = replacer.Replace(source.Companions[i])
	}
	return source
}

func validatePlatformToolSource(source ToolSource) error {
	if source.Platform != "" || len(source.Platforms) == 0 {
		return e.New(e.CodeInvalidCatalog, "tool requires exactly one of platform or nonempty platforms")
	}
	seen := map[string]bool{}
	for i, platform := range source.Platforms {
		if err := target.ValidateExecutablePlatform(platform); err != nil {
			return err
		}
		if i > 0 && source.Platforms[i-1] >= platform {
			return e.New(e.CodeInvalidCatalog, "tool platforms must be sorted and unique")
		}
		selected := platformSource(source, platform)
		if err := validateToolSource(selected); err != nil {
			return err
		}
		if strings.ContainsAny(selected.ExecutablePath+strings.Join(selected.Companions, ""), "{}") {
			return e.New(e.CodeInvalidCatalog, "unknown tool member template token")
		}
		key := selected.URL + "\x00" + selected.ExecutablePath
		if seen[key] {
			return e.New(e.CodeInvalidCatalog, "tool platforms select the same release asset and executable")
		}
		seen[key] = true
	}
	return nil
}

func validateToolPinsShape(pin Pin) error {
	if pin.Tools == nil {
		return nil
	}
	if pin.Tool != nil || len(pin.Tools) == 0 {
		return e.New(e.CodeInvalidCatalog, "tool and nonempty tools lock forms are exclusive")
	}
	for i, tool := range pin.Tools {
		if i > 0 && pin.Tools[i-1].Platform >= tool.Platform {
			return e.New(e.CodeInvalidCatalog, "tool pins must be sorted by unique platform")
		}
		if err := validateToolPin(tool); err != nil {
			return err
		}
	}
	return nil
}

func checkPlatformToolPins(source *ToolSource, pin Pin) error {
	if source == nil || source.Platforms == nil {
		return e.New(e.CodeInvalidCatalog, "orphan per-platform tool pins")
	}
	if err := validatePlatformToolSource(*source); err != nil {
		return err
	}
	if pin.Tool != nil {
		return e.New(e.CodeInvalidCatalog, "multi-platform tool requires tools lock form")
	}
	if err := validateToolPinsShape(pin); err != nil {
		return err
	}
	for _, tool := range pin.Tools {
		if !slices.Contains(source.Platforms, tool.Platform) {
			return e.New(e.CodeInvalidCatalog, "tool lock has undeclared platform %q", tool.Platform)
		}
	}
	if len(pin.Tools) != len(source.Platforms) {
		return e.New(e.CodeUnpinnedSource, "tool lock does not cover every declared platform")
	}
	for _, tool := range pin.Tools {
		selected := platformSource(*source, tool.Platform)
		if err := checkToolPin(&selected, Pin{Tag: pin.Tag, Commit: pin.Commit, Tool: &tool}); err != nil {
			return err
		}
	}
	return nil
}

func resolveSourceToolPins(ctx context.Context, source ToolSource, pin Pin) (Pin, error) {
	return resolveSourceToolPinsWithClient(ctx, source, pin, toolHTTPClient())
}

func resolveSourceToolPinsWithClient(ctx context.Context, source ToolSource, pin Pin, client *http.Client) (Pin, error) {
	if err := validateToolSource(source); err != nil {
		return Pin{}, err
	}
	if source.Platforms == nil {
		tool, err := resolveToolWithClient(ctx, source, pin, client)
		if err != nil {
			return Pin{}, err
		}
		pin.Tool, pin.Tools = &tool, nil
		return pin, nil
	}
	var pins []ToolPin
	for _, platform := range source.Platforms {
		tool, err := resolveToolWithClient(ctx, platformSource(source, platform), pin, client)
		if err != nil {
			return Pin{}, err
		}
		pins = append(pins, tool)
	}
	pin.Tool, pin.Tools = nil, pins
	return pin, nil
}

func cloneToolPins(pins []ToolPin) []ToolPin {
	copy := slices.Clone(pins)
	for i := range copy {
		copy[i] = copy[i].clone()
	}
	return copy
}

func selectToolPin(pin Pin, platform string) (ToolPin, error) {
	if err := validateToolPinsShape(pin); err != nil {
		return ToolPin{}, err
	}
	if pin.Tool != nil && pin.Tool.Platform == platform {
		return pin.Tool.clone(), nil
	}
	for _, tool := range pin.Tools {
		if tool.Platform == platform {
			return tool.clone(), nil
		}
	}
	return ToolPin{}, e.New(e.CodeEngineCapability, "tool has no pinned executable for %s", platform)
}

// SelectTool selects an exact platform pin from the resolved catalog snapshot.
// The Plan owner authenticates that snapshot; this selection grants no authority.
func (cat *Catalog) SelectTool(name, platform string) (ToolPin, error) {
	if cat == nil {
		return ToolPin{}, e.New(e.CodeInvalidCatalog, "tool selection requires a catalog")
	}
	tool, single := cat.Tools[name]
	pins, multi := cat.ToolPlatforms[name]
	if single && multi {
		return ToolPin{}, e.New(e.CodeInvalidCatalog, "tool %q has competing pin forms", name)
	}
	if !single && !multi {
		return ToolPin{}, e.New(e.CodeUnpinnedSource, "selected catalog tool %q has no pin", name)
	}
	pin := Pin{Tools: pins}
	if single {
		pin.Tool = &tool
	}
	return selectToolPin(pin, platform)
}

func (cat *Catalog) validatePlatformTools() error {
	if cat == nil {
		return e.New(e.CodeInvalidCatalog, "catalog is nil")
	}
	names := make([]string, 0, len(cat.ToolPlatforms))
	for name := range cat.ToolPlatforms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, exists := cat.Tools[name]; exists {
			return e.New(e.CodeInvalidCatalog, "tool %q has competing pin forms", name)
		}
		pins := cat.ToolPlatforms[name]
		source, exists := cat.Sources[name]
		if !exists || source.Tool == nil || len(pins) == 0 {
			return e.New(e.CodeInvalidCatalog, "per-platform tool %q has no source or pins", name)
		}
		if err := validatePlatformToolSource(*source.Tool); err != nil {
			return err
		}
		if err := validateToolPinsShape(Pin{Tools: pins}); err != nil {
			return err
		}
		if len(source.Tool.Platforms) != len(pins) {
			return e.New(e.CodeUnpinnedSource, "per-platform tool %q has incomplete pins", name)
		}
		for i, pin := range pins {
			if source.Tool.Platforms[i] != pin.Platform {
				return e.New(e.CodeUnpinnedSource, "per-platform tool %q changes declared platforms", name)
			}
		}
	}
	return nil
}

// ForPlatform resolves a release executable's exact platform before graph lowering.
// Existing single-platform component values are returned unchanged.
func (c Component) ForPlatform(platform string) (Component, error) {
	if c.Prebuilt == nil || c.Prebuilt.Executables == nil {
		return c, nil
	}
	for _, pin := range c.Prebuilt.Executables {
		if pin.Platform != platform {
			continue
		}
		tool := pin.clone()
		prebuilt := *c.Prebuilt
		prebuilt.Executables, prebuilt.Executable = nil, &tool
		prebuilt.URL = tool.URL
		prebuilt.Digests = map[string]string{target.ExecutableArch(platform): tool.ExecutableDigest}
		c.Prebuilt = &prebuilt
		c.Platforms = []string{platform}
		return c, nil
	}
	return Component{}, e.New(e.CodeEngineCapability, "release executable has no pin for platform %s", platform)
}

func toolSupportsPlatforms(source *ToolSource, platforms []string) bool {
	if source == nil || len(platforms) == 0 {
		return false
	}
	for _, platform := range platforms {
		if !source.supportsPlatform(platform) {
			return false
		}
	}
	return true
}

func validateToolPins(tools []ToolPin) error {
	seen := map[string]bool{}
	for i, tool := range tools {
		if i > 0 && tools[i-1].Platform >= tool.Platform {
			return e.New(e.CodeInvalidCatalog, "tool pins must be sorted by unique platform")
		}
		if err := validateToolPin(tool); err != nil {
			return err
		}
		key := tool.URL + "\x00" + tool.ExecutablePath
		if seen[key] {
			return e.New(e.CodeInvalidCatalog, "tool platforms must pin distinct assets")
		}
		seen[key] = true
	}
	return nil
}
