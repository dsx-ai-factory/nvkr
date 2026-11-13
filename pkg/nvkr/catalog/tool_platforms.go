// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"text/template"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func toolPlatforms(source ToolSource) ([]string, error) {
	if source.Platforms == nil {
		if err := target.ValidateExecutablePlatform(source.Platform); err != nil {
			return nil, err
		}
		return []string{source.Platform}, nil
	}
	if source.Platform != "" || len(source.Platforms) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "tool requires exactly one of platform or nonempty platforms")
	}
	for i, platform := range source.Platforms {
		if err := target.ValidateExecutablePlatform(platform); err != nil {
			return nil, err
		}
		if i > 0 && source.Platforms[i-1] >= platform {
			return nil, e.New(e.CodeInvalidCatalog, "tool platforms must be sorted and unique")
		}
	}
	return slices.Clone(source.Platforms), nil
}

func renderPlatformTool(source ToolSource, pin Pin, platform string) (ToolSource, error) {
	osName, arch, _ := strings.Cut(platform, "/")
	major, minor, patch := splitVersion(pin.Tag)
	data := struct {
		refData
		OS string
	}{refData: refData{Tag: pin.Tag, ModuleTag: StripTagPrefix(pin.Tag), Version: NormalizeVersion(pin.Tag), Commit: pin.Commit, Major: major, Minor: minor, Patch: patch, Arch: arch}, OS: osName}
	render := func(value string) (string, error) {
		t, err := template.New("tool asset").Option("missingkey=error").Parse(value)
		if err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "parse platform tool template")
		}
		var out strings.Builder
		if err := t.Execute(&out, data); err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "render platform tool template")
		}
		return out.String(), nil
	}
	source.Platform, source.Platforms = platform, nil
	var err error
	if source.URL, err = render(source.URL); err != nil {
		return ToolSource{}, err
	}
	if source.ExecutablePath, err = render(source.ExecutablePath); err != nil {
		return ToolSource{}, err
	}
	source.Companions = slices.Clone(source.Companions)
	for i, companion := range source.Companions {
		if source.Companions[i], err = render(companion); err != nil {
			return ToolSource{}, err
		}
	}
	return source, nil
}

func validatePlatformToolSource(source ToolSource) error {
	platforms, err := toolPlatforms(source)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, platform := range platforms {
		rendered, err := renderPlatformTool(source, Pin{Tag: "v1.2.3", Commit: strings.Repeat("a", 40)}, platform)
		if err != nil {
			return err
		}
		if err := validateToolSource(rendered); err != nil {
			return err
		}
		key := rendered.URL + "\x00" + rendered.ExecutablePath
		if seen[key] {
			return e.New(e.CodeInvalidCatalog, "tool platforms must select distinct release assets")
		}
		seen[key] = true
	}
	return nil
}

func cloneTools(tools []ToolPin) []ToolPin {
	if tools == nil {
		return nil
	}
	out := make([]ToolPin, len(tools))
	for i, tool := range tools {
		out[i] = tool.clone()
	}
	return out
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

func checkPlatformToolPins(source ToolSource, pin Pin) error {
	if pin.Tool != nil {
		return e.New(e.CodeInvalidCatalog, "multi-platform source cannot carry a single tool pin")
	}
	platforms, err := toolPlatforms(source)
	if err != nil {
		return err
	}
	if err := validateToolPins(pin.Tools); err != nil {
		return err
	}
	for _, tool := range pin.Tools {
		if !slices.Contains(platforms, tool.Platform) {
			return e.New(e.CodeInvalidCatalog, "orphan tool platform %q", tool.Platform)
		}
	}
	if len(pin.Tools) != len(platforms) {
		return e.New(e.CodeUnpinnedSource, "tool source requires every declared platform to be pinned")
	}
	for i, platform := range platforms {
		rendered, err := renderPlatformTool(source, pin, platform)
		if err != nil {
			return err
		}
		if err := checkToolPin(&rendered, Pin{Tag: pin.Tag, Commit: pin.Commit, Tool: &pin.Tools[i]}); err != nil {
			return err
		}
	}
	return nil
}

func resolvePlatformTools(ctx context.Context, source ToolSource, pin Pin, client *http.Client) ([]ToolPin, error) {
	if err := validateToolSource(source); err != nil {
		return nil, err
	}
	platforms, err := toolPlatforms(source)
	if err != nil {
		return nil, err
	}
	tools := make([]ToolPin, 0, len(platforms))
	for _, platform := range platforms {
		rendered, err := renderPlatformTool(source, pin, platform)
		if err != nil {
			return nil, err
		}
		var tool ToolPin
		if client == nil {
			tool, err = resolveToolPin(ctx, rendered, pin)
		} else {
			tool, err = resolveToolWithClient(ctx, rendered, pin, client)
		}
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	if err := validateToolPins(tools); err != nil {
		return nil, err
	}
	return tools, nil
}

// ToolForPlatform selects a detached exact pin; there is no cross-platform fallback.
func (cat *Catalog) ToolForPlatform(name, platform string) (ToolPin, error) {
	if pin, ok := cat.Tools[name]; ok {
		if pin.Platform != platform {
			return ToolPin{}, e.New(e.CodeEngineCapability, "tool %q has no pin for platform %s", name, platform)
		}
		return pin.clone(), nil
	}
	if pins, ok := cat.ToolsByPlatform[name]; ok {
		for _, pin := range pins {
			if pin.Platform == platform {
				return pin.clone(), nil
			}
		}
		return ToolPin{}, e.New(e.CodeEngineCapability, "tool %q has no pin for platform %s", name, platform)
	}
	return ToolPin{}, e.New(e.CodeUnpinnedSource, "selected catalog tool %q has no pin", name)
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
	available, err := toolPlatforms(*source)
	if err != nil {
		return false
	}
	for _, platform := range platforms {
		if !slices.Contains(available, platform) {
			return false
		}
	}
	return true
}
