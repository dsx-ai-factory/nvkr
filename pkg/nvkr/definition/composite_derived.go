// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CompositeDerivedOutput declares a source-less bundle owned by the composite.
// Its materials are named outputs of the admitted member union.
type CompositeDerivedOutput struct {
	Name     string                  `json:"name"`
	Platform string                  `json:"platform"`
	Derived  derivedoutput.Selection `json:"derived"`
}

func (output CompositeDerivedOutput) definitionOutput() DefinitionOutput {
	return DefinitionOutput{Name: output.Name, Platform: output.Platform, Format: OutputBundle, Derived: output.Derived.Clone()}
}

func validateCompositeDerived(outputs []CompositeDerivedOutput) error {
	if len(outputs) > 64 {
		return e.New(e.CodeInvalidCatalog, "composite derivedOutputs exceeds 64 outputs")
	}
	for i, output := range outputs {
		if !namePattern.MatchString(output.Name) || i > 0 && outputs[i-1].Name >= output.Name {
			return e.New(e.CodeInvalidCatalog, "composite derived outputs must have sorted unique canonical names")
		}
		if err := output.definitionOutput().validatePlatforms(); err != nil {
			return err
		}
		if err := output.Derived.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func cloneCompositeDerived(outputs []CompositeDerivedOutput) []CompositeDerivedOutput {
	cloned := slices.Clone(outputs)
	for i := range cloned {
		cloned[i].Derived = *outputs[i].Derived.Clone()
	}
	return cloned
}

func appendCompositeDerived(effective *Definition, attribution *[]OutputAttribution, outputs []CompositeDerivedOutput) error {
	for _, output := range outputs {
		effective.Outputs = append(effective.Outputs, output.definitionOutput())
		// Empty Member identifies only an explicitly declared composite-owned output.
		*attribution = append(*attribution, OutputAttribution{Output: output.Name})
	}
	slices.SortFunc(effective.Outputs, func(a, b DefinitionOutput) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(*attribution, func(a, b OutputAttribution) int { return strings.Compare(a.Output, b.Output) })
	return effective.Validate()
}

func (record CompositionRecord) validateDerivedEffective(effective Definition) error {
	for _, declared := range record.DerivedOutputs {
		index := slices.IndexFunc(effective.Outputs, func(output DefinitionOutput) bool { return output.Name == declared.Name })
		if index < 0 || !reflect.DeepEqual(effective.Outputs[index], declared.definitionOutput()) {
			return e.New(e.CodeUnverifiedRef, "effective Definition substitutes composite-derived output %q", declared.Name)
		}
	}
	return nil
}
