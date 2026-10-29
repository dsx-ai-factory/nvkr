// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package buildgraph derives a pinned graph from the producers selected by one
// Definition output. Selections are explicit; there is no catalog grouping or
// matching language between the Definition and its component producers.
package buildgraph

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// Selection names exactly one output's ordinary producers.
type Selection struct {
	Components   []string
	Platform     string
	OutputFormat string
}

// Validate admits the explicit request shape before a caller reads catalog data.
// Catalog membership and exact target routes are checked by Resolve.
func (selection Selection) Validate() error {
	validate := target.ValidatePlatform
	if selection.OutputFormat == definition.OutputExecutable {
		validate = target.ValidateExecutablePlatform
	}
	if err := validate(selection.Platform); err != nil {
		return err
	}
	switch selection.OutputFormat {
	case "", definition.OutputExecutable, definition.OutputOCI, definition.OutputAPT,
		definition.OutputHelm, definition.OutputBundle, definition.OutputDiskImage, definition.OutputDebianPackage:
	default:
		return e.New(e.CodeInvalidCatalog, "output format %q is unsupported", selection.OutputFormat)
	}
	if len(selection.Components) == 0 {
		return e.New(e.CodeInvalidCatalog, "output must select at least one component")
	}
	if selection.OutputFormat == definition.OutputDebianPackage && len(selection.Components) != 1 {
		return e.New(e.CodeInvalidCatalog, "Debian output requires exactly one source producer")
	}
	seen := make(map[string]bool, len(selection.Components))
	for _, name := range selection.Components {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return e.New(e.CodeInvalidCatalog, "component selection %q is empty or non-canonical", name)
		}
		if seen[name] {
			return e.New(e.CodeInvalidCatalog, "component %q is selected more than once", name)
		}
		seen[name] = true
	}
	return nil
}

// Resolved is a description of selected, validated producer inputs. It carries no
// execution or evidence authority; Compile validates it again before lowering.
type Resolved struct {
	Platform   string
	Components []catalog.NamedComponent
}

// Resolve validates the complete catalog and selects producers without defaults.
// The result owns its data so later catalog mutations cannot alter selected inputs.
func Resolve(cat *catalog.Catalog, selection Selection) (*Resolved, error) {
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if err := cat.Validate(); err != nil {
		return nil, err
	}
	resolved := &Resolved{Platform: selection.Platform}
	names := append([]string(nil), selection.Components...)
	sort.Strings(names)
	for _, name := range names {
		component, ok := cat.Components[name]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "unknown selected component %q", name)
		}
		if selection.OutputFormat == definition.OutputDebianPackage && (!component.FromSource || component.OutputType != "host-package") {
			return nil, e.New(e.CodeInvalidCatalog, "Debian output requires a from-source host-package producer")
		}
		if err := requireComponentRoute(name, component, selection.Platform); err != nil {
			return nil, err
		}
		resolved.Components = append(resolved.Components, catalog.NamedComponent{Name: name, Component: cloneComponent(component)})
	}
	if err := catalog.ValidateMaterialSet(resolved.Components); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "selected output has an incomplete material set")
	}
	if selection.OutputFormat == definition.OutputBundle {
		if len(resolved.Components) != 1 || !resolved.Components[0].FromSource || resolved.Components[0].Prebuilt != nil || resolved.Components[0].OutputType != "tarball" {
			return nil, e.New(e.CodeInvalidCatalog, "bundle output requires exactly one source-built tarball component")
		}
	}
	hasDiskRoot := containsDiskRoot(resolved.Components)
	if hasDiskRoot && selection.OutputFormat != "" && selection.OutputFormat != definition.OutputDiskImage {
		return nil, e.New(e.CodeInvalidCatalog,
			"output format %q cannot select a disk-image component", selection.OutputFormat)
	}
	if selection.OutputFormat == definition.OutputDiskImage || hasDiskRoot {
		if err := validateDiskSelection(resolved.Components); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func containsDiskRoot(components []catalog.NamedComponent) bool {
	for _, component := range components {
		if component.OutputType == definition.OutputDiskImage {
			return true
		}
	}
	return false
}

// validateDiskSelection identifies the disk root from catalog output metadata,
// then requires the authored selection to equal that root's transitive material
// closure. Definition authors select every producer explicitly; the catalog owns
// the dependency edges and no second root selector is necessary.
func validateDiskSelection(components []catalog.NamedComponent) error {
	selected := make(map[string]catalog.Component, len(components))
	for _, component := range components {
		selected[component.Name] = component.Component
	}
	root, err := DiskRootName(components)
	if err != nil {
		return err
	}

	required := make(map[string]bool, len(selected))
	var include func(string)
	include = func(name string) {
		if required[name] {
			return
		}
		required[name] = true
		for _, dependency := range selected[name].Materials {
			include(dependency)
		}
	}
	include(root)
	if len(required) == len(selected) {
		return nil
	}
	extras := make([]string, 0, len(selected)-len(required))
	for name := range selected {
		if !required[name] {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	return e.New(e.CodeInvalidCatalog,
		"disk-image output selects components outside root %q material closure: %s",
		root, strings.Join(extras, ", "))
}

// DiskRootName derives the one top-level QCOW2 producer from an already validated
// selected component set. A disk component consumed by another disk component is
// a material, not a second output root.
func DiskRootName(components []catalog.NamedComponent) (string, error) {
	referenced := make(map[string]bool, len(components))
	for _, component := range components {
		for _, dependency := range component.Materials {
			referenced[dependency] = true
		}
	}

	var roots []string
	for _, component := range components {
		if component.OutputType != definition.OutputDiskImage {
			continue
		}
		// Defensive: Resolve validates the complete catalog before entering this
		// helper. Keep the root check local so future internal callers cannot
		// classify malformed disk metadata as an output root.
		if component.Disk == nil || component.Disk.Format != "qcow2" {
			return "", e.New(e.CodeInvalidCatalog,
				"disk-image output component %q is not a QCOW2 producer", component.Name)
		}
		if !referenced[component.Name] {
			roots = append(roots, component.Name)
		}
	}
	sort.Strings(roots)
	if len(roots) == 0 {
		return "", e.New(e.CodeInvalidCatalog, "disk-image output selects no QCOW2 root component")
	}
	if len(roots) > 1 {
		return "", e.New(e.CodeInvalidCatalog,
			"disk-image output selects multiple disk roots %q and %q", roots[0], roots[1])
	}
	return roots[0], nil
}

// cloneComponent preserves exact strings, including malformed bytes that downstream
// graph validation must reject. A JSON round-trip would repair invalid UTF-8.
func cloneComponent(component catalog.Component) catalog.Component {
	component.Embeds = catalog.CloneEmbeddedComponents(component.Embeds)
	component.Platforms = slices.Clone(component.Platforms)
	if component.Helm != nil {
		copy := *component.Helm
		copy.Images = maps.Clone(copy.Images)
		component.Helm = &copy
	}
	component.ToolchainImages = maps.Clone(component.ToolchainImages)
	component.BuildCommand = slices.Clone(component.BuildCommand)
	component.Materials = maps.Clone(component.Materials)
	component.VendorCommand = slices.Clone(component.VendorCommand)
	component.VersionStamp = maps.Clone(component.VersionStamp)
	component.Patches = slices.Clone(component.Patches)
	component.BuildRequirements = execution.Clone(component.BuildRequirements)
	if component.Flake != nil {
		copy := *component.Flake
		component.Flake = &copy
	}
	if component.Disk != nil {
		copy := *component.Disk
		component.Disk = &copy
	}
	if component.Packer != nil {
		copy := *component.Packer
		copy.Variables = maps.Clone(copy.Variables)
		component.Packer = &copy
	}
	if component.Prebuilt != nil {
		copy := *component.Prebuilt
		copy.Digests = maps.Clone(copy.Digests)
		if copy.Executable != nil {
			executable := *copy.Executable
			copy.Executable = &executable
		}
		if copy.Evidence != nil {
			evidence := *copy.Evidence
			copy.Evidence = &evidence
		}
		component.Prebuilt = &copy
	}
	return component
}

func validateResolved(resolved *Resolved) error {
	if err := target.ValidateExecutablePlatform(resolved.Platform); err != nil {
		return err
	}
	if len(resolved.Components) == 0 {
		return e.New(e.CodeInvalidCatalog, "resolved output must contain at least one component")
	}
	cat := &catalog.Catalog{Components: make(map[string]catalog.Component, len(resolved.Components))}
	previous := ""
	for _, component := range resolved.Components {
		if component.Name <= previous {
			return e.New(e.CodeInvalidCatalog, "resolved components must have sorted unique names")
		}
		previous = component.Name
		cat.Components[component.Name] = component.Component
	}
	if err := cat.Validate(); err != nil {
		return err
	}
	for _, component := range resolved.Components {
		if err := requireComponentRoute(component.Name, component.Component, resolved.Platform); err != nil {
			return err
		}
	}
	if containsDiskRoot(resolved.Components) {
		if err := validateDiskSelection(resolved.Components); err != nil {
			return err
		}
	}
	return nil
}

func requireComponentRoute(name string, component catalog.Component, platform string) error {
	if !slices.Contains(component.Platforms, platform) {
		return e.New(e.CodeEngineCapability, "component %q does not support target %q", name, platform)
	}
	if platform == "darwin/arm64" {
		acquired := !component.FromSource && component.OutputType == "host-package" && component.Prebuilt != nil && component.Prebuilt.Executable != nil
		built := component.FromSource && component.Prebuilt == nil && ((!component.CGO && (component.BuilderType == "" || component.BuilderType == catalog.BuilderOCIHermetic)) || component.BuilderType == catalog.BuilderNativeGo) && (component.OutputType == "host-package" || component.OutputType == "tarball")
		if !acquired && !built {
			return e.New(e.CodeEngineCapability, "component %q requires a pinned executable acquisition or CGO-free OCI build for target %q", name, platform)
		}
	}
	arch := target.ExecutableArch(platform)
	switch {
	case component.Prebuilt != nil:
		if component.Prebuilt.Digests[arch] == "" {
			return e.New(e.CodeUnpinnedSource, "prebuilt component %q has no pinned digest for arch %q", name, arch)
		}
	case !component.FromSource:
		return e.New(e.CodeInvalidCatalog, "excluded component %q cannot produce a required output", name)
	case component.BuilderType == catalog.BuilderVMImage:
		return catalog.ValidateVMImageHostRoute(name, component, arch)
	default:
		if component.ToolchainImages[arch] == "" && component.ToolchainImage == "" {
			return e.New(e.CodeUnpinnedSource, "from-source component %q has no digest-pinned toolchain for arch %q", name, arch)
		}
	}
	return nil
}
