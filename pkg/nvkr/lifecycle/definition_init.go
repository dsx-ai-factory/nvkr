// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"sigs.k8s.io/yaml"
)

// DefinitionInit selects one output and its intended location in the input root.
type DefinitionInit struct {
	Environment     string
	Root            string
	Destination     string
	Format          string
	APTDistribution string
	APTComponent    string
	Name            string
	Catalog         string
	Platform        string
	Components      []string
}

// InitDefinition admits one explicit producer selection and emits catalog
// references relative to Destination. It writes no files. Root is the checkout
// input root used by subsequent Run preparation; omission means the working root.
func InitDefinition(ctx context.Context, request DefinitionInit) (Definition, error) {
	if ctx == nil {
		return Definition{}, e.New(e.CodeUsage, "definition init requires a context")
	}
	if err := ctx.Err(); err != nil {
		return Definition{}, e.Wrap(e.CodeTimeout, err, "definition init canceled")
	}
	if request.Name == "" || request.Catalog == "" || request.Platform == "" || len(request.Components) == 0 || request.Destination == "" {
		return Definition{}, e.New(e.CodeUsage, "definition init requires --name, --catalog, --platform, --destination and at least one --component")
	}
	if request.Format == "" {
		request.Format = definition.OutputExecutable
	}
	components := slices.Clone(request.Components)
	slices.Sort(components)
	for i := 1; i < len(components); i++ {
		if components[i] == components[i-1] {
			return Definition{}, e.New(e.CodeUsage, "component %q is selected more than once", components[i])
		}
	}
	authored := Definition{
		APIVersion: definition.DefinitionAPIVersion,
		Kind:       definition.DefinitionKind,
		Metadata:   definition.Metadata{Name: request.Name},
		Catalog:    definition.Catalog{Base: request.Catalog},
		Outputs: []DefinitionOutput{{
			Name:       request.Name,
			Platform:   request.Platform,
			Components: components,
			Format:     request.Format,
		}},
	}
	if request.APTDistribution != "" || request.APTComponent != "" {
		authored.Outputs[0].APT = &definition.APTOutput{Distribution: request.APTDistribution, Component: request.APTComponent}
	}
	// Shape first, so a bad name or platform never reads the catalog.
	if err := authored.Validate(); err != nil {
		return Definition{}, err
	}
	root := request.Root
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Definition{}, e.Wrap(e.CodeUsage, err, "resolving input root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Definition{}, e.Wrap(e.CodeUsage, err, "opening input root")
	}
	destination := request.Destination
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(root, destination)
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return Definition{}, e.New(e.CodeUsage, "starter destination %q must be inside checkout root %q; choose an in-checkout destination and run from that root", request.Destination, root)
	}
	// Require the destination parent to exist, and reject symlink routes that
	// bounded compiler input capture would refuse later.
	parent := filepath.Dir(destination)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != filepath.Join(resolvedRoot, filepath.Dir(relative)) {
		return Definition{}, e.New(e.CodeUsage, "starter destination parent must exist beneath the input root without symlinks: %q", parent)
	}
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.Mode().IsRegular() {
			return Definition{}, e.New(e.CodeUsage, "starter destination must be a regular file path")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Definition{}, e.Wrap(e.CodeUsage, statErr, "checking starter destination")
	}
	base, err := filepath.Abs(request.Catalog)
	if err != nil {
		return Definition{}, e.Wrap(e.CodeUsage, err, "resolving catalog %q", request.Catalog)
	}
	catalogRelative, err := filepath.Rel(root, base)
	if err != nil || (!filepath.IsLocal(catalogRelative) && catalogRelative != ".") {
		return Definition{}, e.New(e.CodeUsage, "starter catalog must be inside checkout root %q", root)
	}
	reference, err := filepath.Rel(parent, base)
	if err != nil {
		return Definition{}, e.Wrap(e.CodeUsage, err, "resolving destination-relative catalog")
	}
	authored.Catalog.Base = filepath.ToSlash(reference)
	environment, err := factory.DefaultLocalEnvironment()
	if request.Environment != "" {
		environment, err = LoadEnvironment(ctx, request.Environment)
	}
	if err != nil {
		return Definition{}, err
	}
	if err := factory.ValidateStarter(ctx, root, filepath.ToSlash(relative), authored, environment); err != nil {
		return Definition{}, err
	}
	return authored, nil
}

// RenderDefinition returns the authored YAML of a validated Definition. Keys
// are sorted, so equal Definitions always produce identical bytes.
func RenderDefinition(authored Definition) ([]byte, error) {
	if err := authored.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(authored)
}
