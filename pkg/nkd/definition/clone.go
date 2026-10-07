// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"maps"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

// Clone transfers ownership of every mutable authored value to its new owner.
func (input Definition) Clone() Definition {
	input.Catalog.Overlays = append([]string(nil), input.Catalog.Overlays...)
	input.Outputs = append([]DefinitionOutput(nil), input.Outputs...)
	for i := range input.Outputs {
		out := &input.Outputs[i]
		out.Components = append([]string(nil), out.Components...)
		if out.Catalog != nil {
			value := *out.Catalog
			value.Overlays = append([]string(nil), value.Overlays...)
			out.Catalog = &value
		}
		if out.APT != nil {
			value := *out.APT
			out.APT = &value
		}
		if out.Helm != nil {
			value := *out.Helm
			value.Images = maps.Clone(value.Images)
			out.Helm = &value
		}
	}
	return input
}

// Clone transfers ownership without changing the authored environment's identity.
func (input Environment) Clone() Environment {
	input.GitHubDispatchBranches = append([]string(nil), input.GitHubDispatchBranches...)
	input.ExecutionClasses = execution.CloneClasses(input.ExecutionClasses)
	input.DiskInspectionProfiles = maps.Clone(input.DiskInspectionProfiles)
	if input.SourceAccess != nil {
		original := input.SourceAccess
		input.SourceAccess = make(map[string]SourceAccess, len(original))
		for key, value := range original {
			input.SourceAccess[key] = value
		}
	}
	if input.Publication != nil {
		value := *input.Publication
		input.Publication = &value
	}
	if input.Signing != nil {
		value := *input.Signing
		input.Signing = &value
	}
	input.Runtime.Services = append([]string(nil), input.Runtime.Services...)
	input.Runtime.Setup = append([]string(nil), input.Runtime.Setup...)
	if input.Runtime.Variables != nil {
		original := input.Runtime.Variables
		input.Runtime.Variables = make(map[string]string, len(original))
		for key, value := range original {
			input.Runtime.Variables[key] = value
		}
	}
	return input
}
