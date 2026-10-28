// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"slices"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

const OutputDebianPackage = "debian-package"

// DebianSpec declares complete source-owned routes for one final archive. Commands
// and installation behavior remain source-owned; this does not synthesize a DEB.
type DebianSpec struct {
	Routes []DebianRoute `json:"routes"`
}

type DebianRoute struct {
	Target         target.Debian `json:"target"`
	ToolchainImage string        `json:"toolchainImage"`
	BuildCommand   []string      `json:"buildCommand"`
	ArtifactPath   string        `json:"artifactPath"`
	WriteDir       string        `json:"writeDir"`
}

// CloneDebianSpec preserves caller ownership of declaration slices and argv.
func CloneDebianSpec(spec *DebianSpec) *DebianSpec {
	if spec == nil {
		return nil
	}
	copy := &DebianSpec{Routes: slices.Clone(spec.Routes)}
	for i := range copy.Routes {
		copy.Routes[i].BuildCommand = slices.Clone(copy.Routes[i].BuildCommand)
	}
	return copy
}

func validateDebianProducer(name string, component Component) error {
	if component.Debian == nil {
		return e.New(e.CodeInvalidCatalog, "Debian producer %q requires exact source routes", name)
	}
	if component.OutputType != OutputDebianPackage || !component.FromSource || component.Prebuilt != nil ||
		component.BuilderType != BuilderOCIHermetic || component.Helm != nil || component.Disk != nil ||
		component.Packer != nil || component.Flake != nil || len(component.Materials) != 0 {
		return e.New(e.CodeInvalidCatalog, "Debian producer %q requires an OCI-hermetic source build without competing output contracts", name)
	}
	if len(component.BuildCommand) != 0 || len(component.BuildCommandByPlatform) != 0 || component.ArtifactPath != "" || component.WriteDir != "" ||
		component.ToolchainImage != "" || len(component.ToolchainImages) != 0 {
		return e.New(e.CodeInvalidCatalog, "Debian producer %q must declare commands, paths and toolchains only in its exact routes", name)
	}
	if err := validateComponentTargets(name, component.Platforms, false); err != nil {
		return err
	}
	targets := make([]target.Debian, 0, len(component.Debian.Routes))
	covered := make(map[string]bool)
	for _, route := range component.Debian.Routes {
		if len(route.BuildCommand) == 0 || strings.TrimSpace(route.BuildCommand[0]) == "" {
			return e.New(e.CodeInvalidCatalog, "Debian producer %q route requires exact source argv", name)
		}
		for _, argument := range route.BuildCommand {
			if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
				return e.New(e.CodeInvalidCatalog, "Debian producer %q route argv contains invalid text", name)
			}
		}
		if !validSourceRelativePath(route.WriteDir) || route.WriteDir == "." || strings.Contains(route.WriteDir, "{{") ||
			!validSourceRelativePath(route.ArtifactPath) || !strings.HasPrefix(route.ArtifactPath, route.WriteDir+"/") {
			return e.New(e.CodeInvalidCatalog, "Debian producer %q route requires an archive beneath its private write directory", name)
		}
		targets = append(targets, route.Target)
		if !slices.Contains(component.Platforms, route.Target.Platform) {
			return e.New(e.CodeInvalidCatalog, "Debian producer %q route platform is not declared", name)
		}
		covered[route.Target.Platform] = true
		candidate := component
		candidate.Debian, candidate.OutputType = nil, "host-package"
		candidate.Platforms = []string{route.Target.Platform}
		candidate.ToolchainImage = route.ToolchainImage
		candidate.BuildCommand, candidate.ArtifactPath, candidate.WriteDir = route.BuildCommand, route.ArtifactPath, route.WriteDir
		if err := validateComponent(name, candidate); err != nil {
			return err
		}
	}
	if err := target.ValidateDebianRoutes(targets); err != nil {
		return err
	}
	if len(covered) != len(component.Platforms) {
		return e.New(e.CodeInvalidCatalog, "Debian producer %q lacks a route for every declared platform", name)
	}
	return nil
}
