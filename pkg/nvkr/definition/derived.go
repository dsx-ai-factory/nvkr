// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

func (definition Definition) validateDerivedOutputs() error {
	outputs := make(map[string]DefinitionOutput, len(definition.Outputs))
	for _, output := range definition.Outputs {
		outputs[output.Name] = output
	}
	for _, output := range definition.Outputs {
		if output.Derived == nil {
			continue
		}
		if output.Format != OutputBundle || len(output.Components) != 0 || output.Catalog != nil || output.Helm != nil || output.APT != nil || len(output.Platforms) != 0 {
			return e.New(e.CodeInvalidCatalog, "derived output %q requires bundle format without ordinary producer settings", output.Name)
		}
		if err := output.Derived.Validate(); err != nil {
			return err
		}
		for _, name := range output.Derived.Outputs {
			sibling, exists := outputs[name]
			if !exists || name == output.Name || sibling.Derived != nil || (sibling.Format != OutputHelm && sibling.Format != OutputOCI) {
				return e.New(e.CodeInvalidCatalog, "derived output %q describes missing or incompatible sibling %q", output.Name, name)
			}
		}
	}
	return nil
}
