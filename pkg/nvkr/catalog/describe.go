// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Producer modes reported by a ComponentDescription.
const (
	ProducerModeSource      = "source"
	ProducerModeAcquisition = "acquisition"
	ProducerModePinnedTool  = "pinned-tool"
	ProducerModeExcluded    = "excluded"
)

// ComponentDescription is a detached explanation of one validated component. It
// describes existing declarations only: it selects nothing for a Definition,
// fetches and builds nothing, and carries no build commands, tool arguments or
// environment values. A declared platform is a catalog allowlist entry, not a
// claim that the current host can execute it.
type ComponentDescription struct {
	Catalog         string                     `json:"catalog"`
	Name            string                     `json:"name"`
	Mode            string                     `json:"mode"`
	BuilderType     string                     `json:"builderType,omitempty"`
	OutputType      string                     `json:"outputType,omitempty"`
	Platforms       []string                   `json:"platforms"`
	Source          *DescribedSourcePin        `json:"source,omitempty"`
	Acquisition     *DescribedAcquisition      `json:"acquisition,omitempty"`
	Tool            *DescribedToolPin          `json:"tool,omitempty"`
	ToolchainImage  string                     `json:"toolchainImage,omitempty"`
	ToolchainImages map[string]string          `json:"toolchainImages,omitempty"`
	ExclusionReason string                     `json:"exclusionReason,omitempty"`
	Materials       []DescribedMaterialBinding `json:"materials"`
	Closure         []string                   `json:"closure"`
	Edges           []DescribedMaterialBinding `json:"edges"`
}

// DescribedSourcePin is the exact source revision a from-source component builds.
type DescribedSourcePin struct {
	Repo     string `json:"repo"`
	Tag      string `json:"tag,omitempty"`
	Commit   string `json:"commit"`
	TreeHash string `json:"treeHash"`
}

// DescribedAcquisition is the pinned location and per-architecture digests of an
// acquired component.
type DescribedAcquisition struct {
	URL               string            `json:"url,omitempty"`
	Image             string            `json:"image,omitempty"`
	ImageParentDigest string            `json:"imageParentDigest,omitempty"`
	Digests           map[string]string `json:"digests"`
}

// DescribedToolPin is the catalog source and locked executable a pinned-tool
// producer runs.
type DescribedToolPin struct {
	Source           string `json:"source"`
	Tag              string `json:"tag,omitempty"`
	Format           string `json:"format"`
	URL              string `json:"url"`
	Platform         string `json:"platform"`
	ExecutablePath   string `json:"executablePath"`
	ArchiveDigest    string `json:"archiveDigest"`
	ExecutableDigest string `json:"executableDigest"`
}

// DescribedMaterialBinding is one declared material: Consumer stages the output
// of Component under the name Binding.
type DescribedMaterialBinding struct {
	Consumer  string `json:"consumer"`
	Binding   string `json:"binding"`
	Component string `json:"component"`
}

// Describe validates the catalog and explains the named component. Closure is
// every component reachable through material bindings, sorted and listed once;
// Edges keeps every binding within that closure, including each consumer of a
// shared material, so an author can see why each one is needed.
func (cat *Catalog) Describe(name string) (ComponentDescription, error) {
	if err := cat.Validate(); err != nil {
		return ComponentDescription{}, err
	}
	component, ok := cat.Components[name]
	if !ok {
		return ComponentDescription{}, e.New(e.CodeInvalidCatalog, "catalog %q has no component %q", cat.Metadata.Name, name)
	}
	description := ComponentDescription{
		Catalog:         cat.Metadata.Name,
		Name:            name,
		Mode:            producerMode(component),
		BuilderType:     component.BuilderType,
		OutputType:      component.OutputType,
		Platforms:       slices.Clone(component.Platforms),
		ToolchainImage:  component.ToolchainImage,
		ToolchainImages: maps.Clone(component.ToolchainImages),
		ExclusionReason: component.ExclusionReason,
		Materials:       []DescribedMaterialBinding{},
		Closure:         []string{},
		Edges:           []DescribedMaterialBinding{},
	}
	switch description.Mode {
	case ProducerModeSource:
		description.Source = &DescribedSourcePin{Repo: component.Repo, Tag: component.Tag, Commit: component.Commit, TreeHash: component.TreeHash}
	case ProducerModeAcquisition:
		description.Acquisition = &DescribedAcquisition{
			URL: component.Prebuilt.URL, Image: component.Prebuilt.Image,
			ImageParentDigest: component.Prebuilt.ImageParentDigest, Digests: maps.Clone(component.Prebuilt.Digests),
		}
	case ProducerModePinnedTool:
		executable := component.ToolExecutable
		description.Tool = &DescribedToolPin{
			Source: component.Tool, Tag: component.Tag, Format: executable.Format, URL: executable.URL, Platform: executable.Platform,
			ExecutablePath: executable.ExecutablePath, ArchiveDigest: executable.ArchiveDigest, ExecutableDigest: executable.ExecutableDigest,
		}
	}
	for _, binding := range sortedStringKeys(component.Materials) {
		description.Materials = append(description.Materials, DescribedMaterialBinding{Consumer: name, Binding: binding, Component: component.Materials[binding]})
	}

	// Validate has rejected unknown, excluded and cyclic material references, so
	// this walk visits each reachable component once.
	visited := map[string]bool{name: true}
	pending := []string{name}
	for len(pending) > 0 {
		consumer := pending[0]
		pending = pending[1:]
		materials := cat.Components[consumer].Materials
		for _, binding := range sortedStringKeys(materials) {
			material := materials[binding]
			description.Edges = append(description.Edges, DescribedMaterialBinding{Consumer: consumer, Binding: binding, Component: material})
			if !visited[material] {
				visited[material] = true
				description.Closure = append(description.Closure, material)
				pending = append(pending, material)
			}
		}
	}
	sort.Strings(description.Closure)
	sort.Slice(description.Edges, func(i, j int) bool {
		a, b := description.Edges[i], description.Edges[j]
		if a.Consumer != b.Consumer {
			return a.Consumer < b.Consumer
		}
		return a.Binding < b.Binding
	})
	return description, nil
}

// producerMode names how a validated component produces its output. A
// pinned-tool producer builds with a locked tool; it is neither an acquisition
// nor an exclusion, although it sets neither FromSource nor Prebuilt.
func producerMode(component Component) string {
	switch {
	case component.BuilderType == BuilderPinnedTool:
		return ProducerModePinnedTool
	case component.FromSource:
		return ProducerModeSource
	case component.Prebuilt != nil:
		return ProducerModeAcquisition
	default:
		return ProducerModeExcluded
	}
}

// Render encodes the description as JSON or as text. Text quotes control
// characters, since every value comes from catalog bytes.
func (d ComponentDescription) Render(format string) ([]byte, error) {
	switch format {
	case "json":
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "encoding component description")
		}
		return append(raw, '\n'), nil
	case "text":
	default:
		return nil, e.New(e.CodeUsage, "unknown component description format %q (use text or json)", format)
	}
	var out bytes.Buffer
	field := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&out, "%s: %s\n", label, terminalText(value))
		}
	}
	field("catalog", d.Catalog)
	field("component", d.Name)
	field("mode", d.Mode)
	field("builder", d.BuilderType)
	field("output type", d.OutputType)
	field("platforms", strings.Join(d.Platforms, ", "))
	if d.Source != nil {
		field("source repo", d.Source.Repo)
		field("source tag", d.Source.Tag)
		field("source commit", d.Source.Commit)
		field("source tree hash", d.Source.TreeHash)
	}
	if d.Acquisition != nil {
		field("acquired url", d.Acquisition.URL)
		field("acquired image", d.Acquisition.Image)
		field("image parent digest", d.Acquisition.ImageParentDigest)
		for _, arch := range sortedStringKeys(d.Acquisition.Digests) {
			field("digest "+arch, d.Acquisition.Digests[arch])
		}
	}
	if d.Tool != nil {
		field("tool source", d.Tool.Source)
		field("tool tag", d.Tool.Tag)
		field("tool url", d.Tool.URL)
		field("tool platform", d.Tool.Platform)
		field("tool executable", d.Tool.ExecutablePath)
		field("tool archive digest", d.Tool.ArchiveDigest)
		field("tool executable digest", d.Tool.ExecutableDigest)
	}
	field("toolchain image", d.ToolchainImage)
	for _, arch := range sortedStringKeys(d.ToolchainImages) {
		field("toolchain image "+arch, d.ToolchainImages[arch])
	}
	field("exclusion reason", d.ExclusionReason)
	fmt.Fprintf(&out, "materials:%s\n", emptyMarker(len(d.Materials)))
	for _, binding := range d.Materials {
		fmt.Fprintf(&out, "  %s -> %s\n", terminalText(binding.Binding), terminalText(binding.Component))
	}
	fmt.Fprintf(&out, "material closure:%s\n", emptyMarker(len(d.Closure)))
	for _, name := range d.Closure {
		fmt.Fprintf(&out, "  %s\n", terminalText(name))
	}
	fmt.Fprintf(&out, "material edges:%s\n", emptyMarker(len(d.Edges)))
	for _, edge := range d.Edges {
		fmt.Fprintf(&out, "  %s: %s -> %s\n", terminalText(edge.Consumer), terminalText(edge.Binding), terminalText(edge.Component))
	}
	out.WriteString("Declarations only; platforms are not a claim that this host can execute them.\n")
	return out.Bytes(), nil
}

func emptyMarker(count int) string {
	if count == 0 {
		return " none"
	}
	return ""
}

// terminalText escapes control characters as Go quoting does, without the
// surrounding quotes, so catalog bytes cannot move the cursor or rewrite lines.
func terminalText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}
