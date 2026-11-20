// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

type generationSelection struct {
	Bundle              string `json:"bundle"`
	Group               string `json:"group"`
	Definition          string `json:"definition"`
	Environment         string `json:"environment"`
	QualificationPolicy string `json:"qualificationPolicy,omitempty"`
	Runtime             bool   `json:"runtime,omitempty"`
}

// GenerateInventory coordinates the existing maintained producer selections.
// Every Plan and optional qualifier is rendered before any bundle is published.
// Qualification retains its separate manifest-last publication contract.
// Inventory runtime flags select admission for shell-driver checks only. Writes
// regenerate the reviewed baseline without dispatch-time source freezing.
func GenerateInventory(ctx context.Context, filename, group, generatorVersion, root string) error {
	if err := generationContext(ctx); err != nil {
		return err
	}
	if group != "" && group != "main" && group != "nightly" && group != "development" {
		return e.New(e.CodeUsage, "unknown generation group")
	}
	const maxInventory = 64 << 10
	input, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "generation inventory", MaxBytes: maxInventory})
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "opening generation inventory")
	}
	defer func() { _ = input.Close() }()
	raw, err := input.ReadAllContext(ctx, maxInventory)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading generation inventory")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding generation inventory")
	}
	var selections []generationSelection
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selections); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding generation inventory")
	}
	if len(selections) == 0 || len(selections) > 64 {
		return e.New(e.CodePipelineDrift, "generation inventory requires 1–64 selections")
	}
	previous := ""
	for _, selection := range selections {
		if !strings.HasPrefix(selection.Bundle, "nvkr-") || selection.Bundle <= previous || strings.ContainsAny(selection.Bundle, "/\\") {
			return e.New(e.CodePipelineDrift, "generation bundles must be sorted, unique names")
		}
		previous = selection.Bundle
		if selection.Group != "main" && selection.Group != "nightly" && selection.Group != "development" {
			return e.New(e.CodePipelineDrift, "unknown inventory group")
		}
		if !qualificationInputPath(selection.Definition) || !qualificationInputPath(selection.Environment) || (selection.QualificationPolicy != "" && !qualificationInputPath(selection.QualificationPolicy)) {
			return e.New(e.CodePipelineDrift, "generation inventory requires repository-relative input paths")
		}
	}
	var inputs []GenerationInput
	for _, selection := range selections {
		if group != "" && selection.Group != group {
			continue
		}
		// Deliberately omit Runtime: an inventory write establishes the baseline.
		inputs = append(inputs, GenerationInput{Bundle: selection.Bundle, Definition: selection.Definition,
			Environment: selection.Environment, QualificationPolicy: selection.QualificationPolicy})
	}
	_, err = GenerateSelections(ctx, inputs, generatorVersion, root, false)
	return err
}

// GenerationInput describes one producer and its optional separately owned qualifier.
// Runtime requests the existing runtime owner's admission before coordinated publication.
type GenerationInput struct {
	Bundle, Definition, Environment, QualificationPolicy string
	Runtime                                              bool
}

// GenerateSelections renders and admits the complete selected set before writing
// its shared producer records. Qualification retains its separate transaction.
// The returned digests follow the input order.
func GenerateSelections(ctx context.Context, inputs []GenerationInput, generatorVersion, root string, check bool) ([]string, error) {
	if err := generationContext(ctx); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) > 64 {
		return nil, e.New(e.CodeUsage, "coordinated generation requires 1–64 selections")
	}
	var producers []*factory.GeneratedBundle
	var qualifiers []*GeneratedWorkflows
	var digests []string
	for _, selection := range inputs {
		var plan *factory.Plan
		var err error
		if selection.Runtime {
			plan, err = lifecycle.CompileRuntimeGeneration(ctx, selection.Definition, selection.Environment, generatorVersion, root, check)
		} else {
			plan, err = lifecycle.CompileFiles(ctx, selection.Definition, selection.Environment, generatorVersion)
		}
		if err != nil {
			return nil, err
		}
		producer, err := factory.Render(ctx, plan, factory.EngineGitHub)
		if err != nil {
			return nil, err
		}
		if selection.Bundle != "" && path.Base(path.Dir(producer.ManifestPath)) != selection.Bundle {
			return nil, e.New(e.CodePipelineDrift, "inventory bundle differs from its Environment namespace")
		}
		if selection.Runtime {
			if err := factory.PrepareGitHubRuntimeBundle(ctx, plan, root, true); err != nil {
				return nil, err
			}
		} else if check {
			if err := factory.CheckGeneratedBundle(ctx, root, producer); err != nil {
				return nil, err
			}
		}
		producers = append(producers, producer)
		digests = append(digests, producer.PlanDigest)
		if selection.QualificationPolicy == "" {
			continue
		}
		policy, err := lifecycle.LoadQualificationPolicy(ctx, selection.QualificationPolicy)
		if err != nil {
			return nil, err
		}
		qualifier, err := RenderWorkflows(ctx, plan, policy, selection.QualificationPolicy)
		if err != nil {
			return nil, err
		}
		qualifiers = append(qualifiers, qualifier)
	}
	for _, producer := range producers {
		for _, qualifier := range qualifiers {
			if err := checkProducerQualificationOverlap(producer, qualifier); err != nil {
				return nil, err
			}
		}
	}
	for _, qualifier := range qualifiers {
		if check {
			if err := CheckWorkflows(ctx, root, qualifier); err != nil {
				return nil, err
			}
		} else if err := preflightQualificationWrite(ctx, root, qualifier); err != nil {
			return nil, err
		}
	}
	if check {
		return digests, nil
	}
	if err := factory.WriteGeneratedBundles(ctx, root, producers); err != nil {
		return nil, err
	}
	for _, qualifier := range qualifiers {
		if err := WriteWorkflows(ctx, root, qualifier); err != nil {
			return nil, err
		}
	}
	return digests, nil
}
