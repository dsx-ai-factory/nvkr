// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// DefinitionInput is one admitted --definition document of either kind.
type DefinitionInput = definition.Input

// DefinitionPinReport describes one composite pin check or rewrite.
type DefinitionPinReport = definition.PinReport

// LoadDefinitionInput dispatches a --definition path by authored kind. Uses
// that admit only plain Definitions call LoadDefinition, which refuses
// composites with engine-capability.
func LoadDefinitionInput(ctx context.Context, path string) (DefinitionInput, error) {
	return definition.LoadInput(ctx, path)
}

// CompositeView describes an admitted composite and its derived effective
// Definition. It is descriptive output, never an authored input.
type CompositeView struct {
	Kind                      string                         `json:"kind"`
	Name                      string                         `json:"name"`
	Digest                    string                         `json:"digest"`
	Members                   []CompositeMemberView          `json:"members"`
	EffectiveDefinitionDigest string                         `json:"effectiveDefinitionDigest"`
	EffectiveDefinition       Definition                     `json:"effectiveDefinition"`
	Outputs                   []definition.OutputAttribution `json:"outputs"`
}

// CompositeMemberView names one member pin and the file it was admitted from.
type CompositeMemberView struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	Definition string `json:"definition"`
}

// InspectComposite projects an admitted composite input.
func InspectComposite(input DefinitionInput) (CompositeView, error) {
	if input.Composition == nil {
		return CompositeView{}, e.New(e.CodeUsage, "composite inspection requires a composite Definition")
	}
	effective := input.Composition.Effective()
	effectiveDigest, err := effective.Digest()
	if err != nil {
		return CompositeView{}, err
	}
	view := CompositeView{Kind: definition.CompositeKind, Name: effective.Metadata.Name, Digest: input.Composition.Digest(),
		EffectiveDefinitionDigest: effectiveDigest, EffectiveDefinition: effective, Outputs: input.Composition.Attribution()}
	for _, member := range input.Composition.Members() {
		view.Members = append(view.Members, CompositeMemberView{Name: member.Name, Digest: member.Digest, Definition: member.Path})
	}
	return view, nil
}

// PinDefinition recomputes a composite's member pins through its owner.
func PinDefinition(ctx context.Context, path string, check bool) (DefinitionPinReport, error) {
	kind, err := definition.ReadDocumentKind(ctx, path)
	if err != nil {
		return definition.PinReport{}, err
	}
	if kind != definition.CompositeKind {
		return definition.PinReport{}, e.New(e.CodeUsage, "definition pin requires a composite Definition; %s is a plain Definition", path)
	}
	relative, err := definition.WorkingRelativePath(path)
	if err != nil {
		return definition.PinReport{}, err
	}
	return definition.PinComposite(ctx, ".", relative, check)
}

// RefuseComposite fails a deferred ADR-26 §9 use with engine-capability
// before any catalog, network, subprocess or output work. An unreadable or
// unknown document is left to the use's own admission, which fails closed.
func RefuseComposite(ctx context.Context, path, use string) error {
	if kind, err := definition.ReadDocumentKind(ctx, path); err == nil && kind == definition.CompositeKind {
		return e.New(e.CodeEngineCapability, "%s of a composite Definition is deferred (ADR-26 §9)", use)
	}
	return nil
}

func captureComposite(ctx context.Context, definitionPath, environmentPath, generatorVersion string) (*factory.Plan, factory.InputSnapshot, error) {
	compositePath, err := definition.WorkingRelativePath(definitionPath)
	if err != nil {
		return nil, factory.InputSnapshot{}, err
	}
	relativeEnvironment, err := definition.WorkingRelativePath(environmentPath)
	if err != nil {
		return nil, factory.InputSnapshot{}, err
	}
	return factory.CaptureCompileFiles(ctx, ".", compositePath, relativeEnvironment, generatorVersion)
}
